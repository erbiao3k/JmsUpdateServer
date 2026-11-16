package feishu

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"
)

type Client struct {
	webhook string
	secret  string
	http    *http.Client

	mu       sync.Mutex
	failures int
}

func New(webhook, secret string) *Client {
	return &Client{
		webhook: strings.TrimSpace(webhook),
		secret:  strings.TrimSpace(secret),
		http:    &http.Client{Timeout: 10 * time.Second},
	}
}

func (c *Client) Failures() int {
	if c == nil {
		return 0
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.failures
}

// Notify 向飞书自定义机器人发送一条文本。未配置 webhook 时直接返回。
func (c *Client) Notify(ctx context.Context, text string) error {
	if c == nil || c.webhook == "" || strings.TrimSpace(text) == "" {
		return nil
	}
	payload := map[string]any{
		"msg_type": "text",
		"content":  map[string]string{"text": text},
	}
	if c.secret != "" {
		timestamp := time.Now().Unix()
		payload["timestamp"] = strconv.FormatInt(timestamp, 10)
		payload["sign"] = sign(c.secret, timestamp)
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		return c.fail(err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.webhook, bytes.NewReader(raw))
	if err != nil {
		return c.fail(err)
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.http.Do(req)
	if err != nil {
		return c.fail(err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return c.fail(err)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return c.fail(fmt.Errorf("飞书返回 %s: %s", resp.Status, clip(body)))
	}
	var result struct {
		Code          int    `json:"code"`
		Msg           string `json:"msg"`
		StatusCode    int    `json:"StatusCode"`
		StatusMessage string `json:"StatusMessage"`
	}
	if len(bytes.TrimSpace(body)) > 0 {
		if err := json.Unmarshal(body, &result); err != nil {
			return c.fail(fmt.Errorf("解析飞书响应: %w", err))
		}
	}
	if result.Code != 0 || result.StatusCode != 0 {
		msg := result.Msg
		if msg == "" {
			msg = result.StatusMessage
		}
		return c.fail(fmt.Errorf("飞书拒绝发送: %s", msg))
	}
	return nil
}

func (c *Client) fail(err error) error {
	c.mu.Lock()
	c.failures++
	c.mu.Unlock()
	return err
}

func sign(secret string, timestamp int64) string {
	mac := hmac.New(sha256.New, []byte(strconv.FormatInt(timestamp, 10)+"\n"+secret))
	return base64.StdEncoding.EncodeToString(mac.Sum(nil))
}

func clip(body []byte) string {
	text := strings.TrimSpace(string(body))
	if len(text) > 300 {
		return text[:300]
	}
	return text
}
