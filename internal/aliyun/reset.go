package aliyun

import (
	"context"
	"encoding/base64"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	ecs "github.com/alibabacloud-go/ecs-20140526/v4/client"
	"github.com/alibabacloud-go/tea/tea"

	"jmsupdateserver/internal/inventory"
	"jmsupdateserver/internal/resetpass"
)

// ResetLoginPassword 用云助手在线修改 Linux root 或 Windows Administrator。
// 实例必须是 Running。命令不保存，脚本不输出密码。
func (c *Client) ResetLoginPassword(ctx context.Context, server inventory.Server, password string, commandTimeout time.Duration) error {
	kind, script, err := resetpass.Command(server.OS, password)
	if err != nil {
		return err
	}
	encodedScript := base64.StdEncoding.EncodeToString([]byte(script))
	err = c.resetLoginPassword(ctx, server, kind, encodedScript, commandTimeout)
	if err == nil {
		return nil
	}
	return fmt.Errorf("%s", resetpass.Redact(err.Error(), password, script, encodedScript))
}

func (c *Client) resetLoginPassword(ctx context.Context, server inventory.Server, kind, encodedScript string, commandTimeout time.Duration) error {
	if strings.TrimSpace(server.InstanceID) == "" || strings.TrimSpace(server.RegionID) == "" {
		return fmt.Errorf("缺少实例 ID 或地域，无法在线改密")
	}
	if !strings.EqualFold(server.Status, "Running") {
		status := server.Status
		if status == "" {
			status = "未知"
		}
		return fmt.Errorf("实例状态为 %s，不是 Running", status)
	}
	endpoint := strings.TrimSpace(server.Endpoint)
	if !strings.Contains(endpoint, ".") {
		endpoint = "ecs." + server.RegionID + ".aliyuncs.com"
	}
	client, err := c.newClient(endpoint)
	if err != nil {
		return err
	}
	seconds := int64(commandTimeout / time.Second)
	if seconds < 10 {
		seconds = 60
	}
	req := &ecs.RunCommandRequest{
		RegionId:        tea.String(server.RegionID),
		InstanceId:      []*string{tea.String(server.InstanceID)},
		Type:            tea.String(kind),
		CommandContent:  tea.String(encodedScript),
		ContentEncoding: tea.String("Base64"),
		KeepCommand:     tea.Bool(false),
		Timeout:         tea.Int64(seconds),
	}
	resp, err := call(ctx, func() (*ecs.RunCommandResponse, error) {
		return client.RunCommand(req)
	})
	if err != nil {
		return fmt.Errorf("RunCommand: %w", err)
	}
	invokeID := ""
	if resp != nil && resp.Body != nil {
		invokeID = deref(resp.Body.InvokeId)
	}
	if invokeID == "" {
		return fmt.Errorf("RunCommand 没有返回 InvokeId")
	}
	return waitInvocation(ctx, client, server, invokeID, commandTimeout)
}

func waitInvocation(ctx context.Context, client *ecs.Client, server inventory.Server, invokeID string, commandTimeout time.Duration) error {
	if commandTimeout < 10*time.Second {
		commandTimeout = 60 * time.Second
	}
	deadline := time.Now().Add(commandTimeout + 30*time.Second)
	for {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		resp, err := call(ctx, func() (*ecs.DescribeInvocationResultsResponse, error) {
			return client.DescribeInvocationResults(&ecs.DescribeInvocationResultsRequest{
				RegionId:   tea.String(server.RegionID),
				InvokeId:   tea.String(invokeID),
				InstanceId: tea.String(server.InstanceID),
			})
		})
		if err != nil {
			return fmt.Errorf("DescribeInvocationResults: %w", err)
		}
		result := firstInvocationResult(resp)
		if result != nil {
			status := deref(result.InvocationStatus)
			switch status {
			case "Pending", "Running", "Stopping", "Scheduled":
			case "Success", "Finished":
				if result.ExitCode != nil && *result.ExitCode != 0 {
					return invocationError(invokeID, result)
				}
				return nil
			case "":
			default:
				return invocationError(invokeID, result)
			}
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("云助手改密超时 invoke=%s", invokeID)
		}
		timer := time.NewTimer(2 * time.Second)
		select {
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		case <-timer.C:
		}
	}
}

func firstInvocationResult(resp *ecs.DescribeInvocationResultsResponse) *ecs.DescribeInvocationResultsResponseBodyInvocationInvocationResultsInvocationResult {
	if resp == nil || resp.Body == nil || resp.Body.Invocation == nil || resp.Body.Invocation.InvocationResults == nil {
		return nil
	}
	results := resp.Body.Invocation.InvocationResults.InvocationResult
	if len(results) == 0 {
		return nil
	}
	return results[0]
}

func invocationError(invokeID string, result *ecs.DescribeInvocationResultsResponseBodyInvocationInvocationResultsInvocationResult) error {
	exit := ""
	if result.ExitCode != nil {
		exit = fmt.Sprintf("%d", *result.ExitCode)
	}
	output := decodeOutput(deref(result.Output))
	output = strings.TrimSpace(output)
	if utf8.RuneCountInString(output) > 300 {
		output = string([]rune(output)[:300])
	}
	return fmt.Errorf("云助手改密失败 invoke=%s status=%s exit=%s code=%s info=%s output=%s",
		invokeID, deref(result.InvocationStatus), exit, deref(result.ErrorCode), deref(result.ErrorInfo), output)
}

func decodeOutput(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}
	decoded, err := base64.StdEncoding.DecodeString(raw)
	if err != nil || !utf8.Valid(decoded) {
		return raw
	}
	return string(decoded)
}
