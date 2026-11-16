package jumpserver

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"

	"jmsupdateserver/internal/resetpass"
)

type accountDTO struct {
	ID             string          `json:"id"`
	Name           string          `json:"name"`
	Username       string          `json:"username"`
	Asset          json.RawMessage `json:"asset"`
	Source         json.RawMessage `json:"source"`
	SourceID       string          `json:"source_id"`
	FollowTemplate bool            `json:"follow_template"`
}

func (a accountDTO) follows(templateID string) bool {
	return a.FollowTemplate && strings.EqualFold(a.SourceID, templateID) && choiceValue(a.Source) != "local"
}

func (a accountDTO) assetID() string {
	raw := bytes.TrimSpace(a.Asset)
	if len(raw) == 0 || string(raw) == "null" {
		return ""
	}
	var id string
	if err := json.Unmarshal(raw, &id); err == nil {
		return id
	}
	var obj struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(raw, &obj); err == nil {
		return obj.ID
	}
	return ""
}

type templateDTO struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	Username string `json:"username"`
}

type templateWrite struct {
	Name           string `json:"name"`
	Username       string `json:"username"`
	SecretType     string `json:"secret_type"`
	Secret         string `json:"secret"`
	SecretStrategy string `json:"secret_strategy"`
	Privileged     bool   `json:"privileged"`
	AutoPush       bool   `json:"auto_push"`
	IsActive       bool   `json:"is_active"`
}

// EnsureAccountTemplate 创建或更新一套账号模板。密码只保存在模板上。
func (c *Client) EnsureAccountTemplate(ctx context.Context, name, username, secret string) (string, error) {
	id, err := c.ensureAccountTemplate(ctx, name, username, secret)
	if err == nil {
		return id, nil
	}
	return "", fmt.Errorf("%s", resetpass.Redact(err.Error(), secret))
}

func (c *Client) ensureAccountTemplate(ctx context.Context, name, username, secret string) (string, error) {
	if strings.TrimSpace(name) == "" || strings.TrimSpace(username) == "" || secret == "" {
		return "", fmt.Errorf("缺少模板名称、用户名或密码")
	}
	existing, err := c.findTemplate(ctx, name)
	if err != nil {
		return "", err
	}
	payload := templateWrite{
		Name:           name,
		Username:       username,
		SecretType:     "password",
		Secret:         secret,
		SecretStrategy: "specific",
		Privileged:     true,
		AutoPush:       false,
		IsActive:       true,
	}
	if existing != nil {
		body, err := c.do(ctx, http.MethodPatch, "/api/v1/accounts/account-templates/"+url.PathEscape(existing.ID)+"/", payload)
		if err != nil {
			return "", err
		}
		var updated templateDTO
		if err := json.Unmarshal(body, &updated); err == nil && updated.ID != "" {
			return updated.ID, nil
		}
		return existing.ID, nil
	}
	body, err := c.do(ctx, http.MethodPost, "/api/v1/accounts/account-templates/", payload)
	if err != nil {
		return "", err
	}
	var created templateDTO
	if err := json.Unmarshal(body, &created); err != nil {
		return "", err
	}
	if created.ID == "" {
		return "", fmt.Errorf("创建账号模板 %s 没有返回 ID", name)
	}
	return created.ID, nil
}

// LinkTemplateAccount 把主机上的登录账号关联到模板。已关联同一模板时不再改动。
func (c *Client) LinkTemplateAccount(ctx context.Context, assetID, templateID, username string) (string, error) {
	if strings.TrimSpace(assetID) == "" || strings.TrimSpace(templateID) == "" || strings.TrimSpace(username) == "" {
		return "", fmt.Errorf("缺少资产、模板或用户名，无法关联账号")
	}
	existing, err := c.findAccount(ctx, assetID, username)
	if err != nil {
		return "", err
	}
	if existing != nil && existing.follows(templateID) {
		return "linked", nil
	}
	if existing != nil && existing.FollowTemplate && existing.SourceID != "" && !strings.EqualFold(existing.SourceID, templateID) {
		_, err = c.do(ctx, http.MethodPatch, "/api/v1/accounts/accounts/"+url.PathEscape(existing.ID)+"/", map[string]any{
			"follow_template": false,
		})
		if err != nil {
			return "", err
		}
	}
	if existing != nil {
		payload := map[string]any{
			"template":        templateID,
			"follow_template": true,
			"push_now":        false,
		}
		_, err = c.do(ctx, http.MethodPatch, "/api/v1/accounts/accounts/"+url.PathEscape(existing.ID)+"/", payload)
		if err != nil {
			return "", err
		}
		return "updated", nil
	}
	payload := map[string]any{
		"asset":           assetID,
		"template":        templateID,
		"follow_template": true,
		"push_now":        false,
	}
	_, err = c.do(ctx, http.MethodPost, "/api/v1/accounts/accounts/", payload)
	if err != nil {
		return "", err
	}
	return "created", nil
}

func (c *Client) findTemplate(ctx context.Context, name string) (*templateDTO, error) {
	path := "/api/v1/accounts/account-templates/?name=" + url.QueryEscape(name) + "&limit=20"
	body, err := c.do(ctx, http.MethodGet, path, nil)
	if err != nil {
		return nil, err
	}
	templates, err := decodeList[templateDTO](body)
	if err != nil {
		return nil, err
	}
	for i := range templates {
		if templates[i].Name == name && templates[i].ID != "" {
			return &templates[i], nil
		}
	}
	return nil, nil
}

func choiceValue(raw json.RawMessage) string {
	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 || string(raw) == "null" {
		return ""
	}
	var text string
	if err := json.Unmarshal(raw, &text); err == nil {
		return text
	}
	var obj struct {
		Value string `json:"value"`
	}
	if err := json.Unmarshal(raw, &obj); err == nil {
		return obj.Value
	}
	return ""
}

func (c *Client) findAccount(ctx context.Context, assetID, username string) (*accountDTO, error) {
	const limit = 100
	for offset := 0; offset < limit*20; offset += limit {
		path := fmt.Sprintf(
			"/api/v1/accounts/accounts/?asset_id=%s&username=%s&secret_type=password&limit=%d&offset=%d",
			url.QueryEscape(assetID),
			url.QueryEscape(username),
			limit,
			offset,
		)
		body, err := c.do(ctx, http.MethodGet, path, nil)
		if err != nil {
			return nil, err
		}
		accounts, err := decodeList[accountDTO](body)
		if err != nil {
			return nil, err
		}
		for i := range accounts {
			if !strings.EqualFold(accounts[i].Username, username) || accounts[i].ID == "" {
				continue
			}
			if id := accounts[i].assetID(); id != "" && !strings.EqualFold(id, assetID) {
				continue
			}
			return &accounts[i], nil
		}
		if len(accounts) < limit {
			return nil, nil
		}
	}
	return nil, nil
}
