package jumpserver

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"jmsupdateserver/internal/inventory"
)

const managedCommentPrefix = "ecs_instance_id="

type Client struct {
	baseURL    string
	token      string
	authScheme string
	orgID      string
	http       *http.Client
}

type Label struct {
	Name  string `json:"name"`
	Value string `json:"value"`
}

type Protocol struct {
	Name string `json:"name"`
	Port int    `json:"port"`
}

type Host struct {
	ID       string  `json:"id"`
	Name     string  `json:"name"`
	Address  string  `json:"address"`
	Comment  string  `json:"comment"`
	Labels   []Label `json:"labels"`
	Platform struct {
		ID int `json:"id"`
	} `json:"platform"`
}

type Platform struct {
	ID        int
	Name      string
	Protocols []ProtocolDef
}

type ProtocolDef struct {
	Name     string
	Port     int
	Required bool
	Primary  bool
	Default  bool
}

func New(baseURL, token, authScheme, orgID string) *Client {
	return &Client{
		baseURL:    strings.TrimRight(strings.TrimSpace(baseURL), "/"),
		token:      strings.TrimSpace(token),
		authScheme: strings.TrimSpace(authScheme),
		orgID:      strings.TrimSpace(orgID),
		http:       &http.Client{Timeout: 60 * time.Second},
	}
}

func (c *Client) ListHosts(ctx context.Context) ([]Host, error) {
	const limit = 500
	hosts := make([]Host, 0)
	for offset := 0; ; offset += limit {
		path := fmt.Sprintf("/api/v1/assets/hosts/?limit=%d&offset=%d", limit, offset)
		body, err := c.do(ctx, http.MethodGet, path, nil)
		if err != nil {
			return nil, err
		}
		page, err := decodeList[Host](body)
		if err != nil {
			return nil, fmt.Errorf("解析主机列表: %w", err)
		}
		hosts = append(hosts, page...)
		if len(page) < limit {
			break
		}
	}
	return hosts, nil
}

func (c *Client) FindPlatform(ctx context.Context, id int, name string) (Platform, error) {
	if id > 0 {
		body, err := c.do(ctx, http.MethodGet, fmt.Sprintf("/api/v1/assets/platforms/%d/", id), nil)
		if err != nil {
			return Platform{}, err
		}
		return decodePlatform(body)
	}
	body, err := c.do(ctx, http.MethodGet, "/api/v1/assets/platforms/?category=host&limit=200", nil)
	if err != nil {
		return Platform{}, err
	}
	raws, err := decodeList[json.RawMessage](body)
	if err != nil {
		return Platform{}, fmt.Errorf("解析平台列表: %w", err)
	}
	for _, raw := range raws {
		platform, err := decodePlatform(raw)
		if err != nil {
			continue
		}
		if strings.EqualFold(platform.Name, name) {
			body, err := c.do(ctx, http.MethodGet, fmt.Sprintf("/api/v1/assets/platforms/%d/", platform.ID), nil)
			if err != nil {
				return Platform{}, err
			}
			return decodePlatform(body)
		}
	}
	return Platform{}, fmt.Errorf("未找到类别为 host 的平台 %q", name)
}

type Payload struct {
	Name         string     `json:"name"`
	Address      string     `json:"address"`
	Platform     PlatformID `json:"platform"`
	Protocols    []Protocol `json:"protocols"`
	Labels       []Label    `json:"labels"`
	Comment      string     `json:"comment,omitempty"`
	NodesDisplay []string   `json:"nodes_display,omitempty"`
	IsActive     *bool      `json:"is_active,omitempty"`
}

type PlatformID struct {
	ID int `json:"id"`
}

func (c *Client) CreateHost(ctx context.Context, payload Payload) (Host, error) {
	body, err := c.do(ctx, http.MethodPost, "/api/v1/assets/hosts/", payload)
	if err != nil {
		return Host{}, err
	}
	var host Host
	if err := json.Unmarshal(body, &host); err != nil {
		return Host{}, fmt.Errorf("解析新建主机: %w", err)
	}
	return host, nil
}

func (c *Client) UpdateHost(ctx context.Context, id string, payload Payload) (Host, error) {
	body, err := c.do(ctx, http.MethodPatch, "/api/v1/assets/hosts/"+url.PathEscape(id)+"/", payload)
	if err != nil {
		return Host{}, err
	}
	var host Host
	if err := json.Unmarshal(body, &host); err != nil {
		return Host{}, fmt.Errorf("解析更新主机: %w", err)
	}
	return host, nil
}

func (h *Host) UnmarshalJSON(data []byte) error {
	var raw struct {
		ID       string            `json:"id"`
		Name     string            `json:"name"`
		Address  string            `json:"address"`
		Comment  json.RawMessage   `json:"comment"`
		Labels   []json.RawMessage `json:"labels"`
		Platform struct {
			ID int `json:"id"`
		} `json:"platform"`
	}
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}
	h.ID = raw.ID
	h.Name = raw.Name
	h.Address = raw.Address
	h.Comment = jsonString(raw.Comment)
	h.Platform.ID = raw.Platform.ID
	labels := make([]Label, 0, len(raw.Labels))
	for _, item := range raw.Labels {
		item = bytes.TrimSpace(item)
		if len(item) == 0 || bytes.Equal(item, []byte("null")) {
			continue
		}
		var label Label
		if err := json.Unmarshal(item, &label); err != nil {
			return err
		}
		if strings.TrimSpace(label.Name) == "" {
			continue
		}
		labels = append(labels, label)
	}
	h.Labels = labels
	return nil
}

func jsonString(raw json.RawMessage) string {
	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 || bytes.Equal(raw, []byte("null")) {
		return ""
	}
	var text string
	if err := json.Unmarshal(raw, &text); err != nil {
		return ""
	}
	return text
}

func (c *Client) do(ctx context.Context, method, path string, payload any) ([]byte, error) {
	var reader io.Reader
	if payload != nil {
		raw, err := json.Marshal(payload)
		if err != nil {
			return nil, err
		}
		reader = bytes.NewReader(raw)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.baseURL+path, reader)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/json")
	if payload != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if c.token != "" {
		req.Header.Set("Authorization", c.authScheme+" "+c.token)
	}
	if c.orgID != "" {
		req.Header.Set("X-JMS-ORG", c.orgID)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return nil, err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("%s %s -> %s: %s", method, path, resp.Status, trimBody(body))
	}
	return body, nil
}

func trimBody(body []byte) string {
	text := strings.TrimSpace(string(body))
	if len(text) > 800 {
		return text[:800]
	}
	return text
}

func decodeList[T any](body []byte) ([]T, error) {
	body = bytes.TrimSpace(body)
	if len(body) == 0 {
		return nil, nil
	}
	if body[0] == '[' {
		var items []T
		if err := json.Unmarshal(body, &items); err != nil {
			return nil, err
		}
		return items, nil
	}
	var page struct {
		Results []T `json:"results"`
	}
	if err := json.Unmarshal(body, &page); err != nil {
		return nil, err
	}
	return page.Results, nil
}

func decodePlatform(body []byte) (Platform, error) {
	var raw map[string]any
	if err := json.Unmarshal(body, &raw); err != nil {
		return Platform{}, err
	}
	platform := Platform{
		ID:   asInt(raw["id"]),
		Name: asString(raw["name"]),
	}
	items, _ := raw["protocols"].([]any)
	for _, item := range items {
		obj, _ := item.(map[string]any)
		if obj == nil {
			continue
		}
		platform.Protocols = append(platform.Protocols, ProtocolDef{
			Name:     asString(obj["name"]),
			Port:     asInt(obj["port"]),
			Required: asBool(obj["required"]),
			Primary:  asBool(obj["primary"]),
			Default:  asBool(obj["default"]),
		})
	}
	if platform.ID == 0 || platform.Name == "" {
		return Platform{}, fmt.Errorf("平台数据缺少 id 或 name")
	}
	return platform, nil
}

func BuildProtocols(defs []ProtocolDef, os string, port int) []Protocol {
	connect := "ssh"
	if os == inventory.OSWindows {
		connect = "rdp"
	}
	out := make([]Protocol, 0, len(defs)+1)
	seen := map[string]struct{}{}
	for _, def := range defs {
		name := strings.ToLower(strings.TrimSpace(def.Name))
		if name == "" {
			continue
		}
		include := def.Required || def.Primary || def.Default || name == connect
		if !include {
			continue
		}
		item := Protocol{Name: def.Name, Port: def.Port}
		if port > 0 && (name == connect || sameConnectPort(connect, name)) {
			item.Port = port
		}
		if item.Port == 0 {
			item.Port = port
		}
		out = append(out, item)
		seen[name] = struct{}{}
	}
	if _, ok := seen[connect]; !ok {
		out = append(out, Protocol{Name: connect, Port: port})
	}
	return out
}

// sameConnectPort 表示这个协议和连接协议共用端口。
// JumpServer 的 SCP 走 SSH，没有单独协议；SFTP 有独立端口，需要和 SSH 保持一致。
func sameConnectPort(connect, name string) bool {
	return connect == "ssh" && (name == "sftp" || name == "scp")
}

// MergeLabels 保留资产上的其他标签，并覆盖本次托管的键。
// JumpServer 标签的 name 和 value 都不能包含冒号或逗号，IPv6 因此不能写入标签。
func MergeLabels(existing []Label, managed map[string]string) []Label {
	out := make([]Label, 0, len(existing)+len(managed))
	for _, label := range existing {
		if _, ok := managed[label.Name]; ok || strings.TrimSpace(label.Name) == "" {
			continue
		}
		out = append(out, Label{Name: label.Name, Value: label.Value})
	}
	order := []string{
		inventory.LabelInstanceID,
		inventory.LabelRegion,
		inventory.LabelPrivateIP,
		inventory.LabelPublicIP,
	}
	seen := map[string]struct{}{}
	for _, key := range order {
		seen[key] = struct{}{}
		if value, ok := labelValue(managed[key]); ok {
			out = append(out, Label{Name: key, Value: value})
		}
	}
	for key, value := range managed {
		if _, ok := seen[key]; ok {
			continue
		}
		if safe, ok := labelValue(value); ok {
			out = append(out, Label{Name: key, Value: safe})
		}
	}
	return out
}

func labelValue(value string) (string, bool) {
	value = strings.TrimSpace(value)
	if value == "" || strings.ContainsAny(value, ":,") {
		return "", false
	}
	return value, true
}

// ManagedComment 在备注为空或仍是本程序写入的备注时刷新地址。
// 已有人工备注时保持不动。
func ManagedComment(existing string, server inventory.Server) (string, bool) {
	existing = strings.TrimSpace(existing)
	block := fmt.Sprintf("%s%s\nregion=%s\nprivate_ip=%s\npublic_ip=%s",
		managedCommentPrefix,
		server.InstanceID,
		server.RegionID,
		server.PrivateIP,
		server.PublicIP,
	)
	if existing == "" || strings.HasPrefix(existing, managedCommentPrefix) {
		return block, true
	}
	return existing, false
}

func UnsafeLabelIPs(server inventory.Server) []string {
	var missed []string
	if _, ok := labelValue(server.PrivateIP); !ok && server.PrivateIP != "" {
		missed = append(missed, "内网 "+server.PrivateIP)
	}
	if _, ok := labelValue(server.PublicIP); !ok && server.PublicIP != "" {
		missed = append(missed, "外网 "+server.PublicIP)
	}
	return missed
}

func asString(value any) string {
	switch typed := value.(type) {
	case string:
		return typed
	case map[string]any:
		if text, ok := typed["label"].(string); ok && text != "" {
			return text
		}
		if text, ok := typed["value"].(string); ok {
			return text
		}
	}
	return ""
}

func asInt(value any) int {
	switch typed := value.(type) {
	case float64:
		return int(typed)
	case int:
		return typed
	case json.Number:
		n, _ := typed.Int64()
		return int(n)
	case map[string]any:
		return asInt(typed["value"])
	default:
		return 0
	}
}

func asBool(value any) bool {
	switch typed := value.(type) {
	case bool:
		return typed
	case string:
		return strings.EqualFold(typed, "true") || typed == "1"
	case float64:
		return typed != 0
	case map[string]any:
		return asBool(typed["value"])
	default:
		return false
	}
}
