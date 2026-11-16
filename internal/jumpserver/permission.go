package jumpserver

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
)

// EnsureAssetPermission 按名称创建或更新资产授权，并绑定用户组。
// 用户组不存在时按名称创建，已有同名用户组直接使用，不改组内成员。
// 已有授权只追加用户组和资产，不移除原来的成员。账号使用全部账号。
func (c *Client) EnsureAssetPermission(ctx context.Context, name, userGroup string, actions, assetIDs []string) error {
	name = strings.TrimSpace(name)
	userGroup = strings.TrimSpace(userGroup)
	assetIDs = uniqueIDs(assetIDs)
	if name == "" || userGroup == "" {
		return fmt.Errorf("缺少资产授权名称或用户组")
	}
	if len(assetIDs) == 0 {
		return nil
	}
	groupID, err := c.ensureUserGroupID(ctx, userGroup)
	if err != nil {
		return err
	}
	existing, err := c.findAssetPermission(ctx, name)
	if err != nil {
		return err
	}
	if existing == nil {
		payload := map[string]any{
			"name":        name,
			"user_groups": []string{groupID},
			"assets":      assetIDs,
			"accounts":    []string{"@ALL"},
			"protocols":   []string{"all"},
			"actions":     actions,
			"is_active":   true,
		}
		_, err = c.do(ctx, http.MethodPost, "/api/v1/perms/asset-permissions/", payload)
		if err != nil {
			return fmt.Errorf("创建资产授权 %s: %w", name, err)
		}
		return nil
	}
	detail, err := c.assetPermissionDetail(ctx, existing.ID)
	if err != nil {
		return err
	}
	groups := appendID(detail.groupIDs(), groupID)
	assets := appendIDs(detail.assetIDs(), assetIDs)
	if len(groups) == len(detail.groupIDs()) && len(assets) == len(detail.assetIDs()) {
		return nil
	}
	payload := map[string]any{
		"user_groups": groups,
		"assets":      assets,
		"accounts":    []string{"@ALL"},
		"protocols":   []string{"all"},
		"actions":     actions,
	}
	_, err = c.do(ctx, http.MethodPatch, "/api/v1/perms/asset-permissions/"+url.PathEscape(existing.ID)+"/", payload)
	if err != nil {
		return fmt.Errorf("更新资产授权 %s: %w", name, err)
	}
	return nil
}

func (c *Client) ensureUserGroupID(ctx context.Context, name string) (string, error) {
	id, err := c.findUserGroupID(ctx, name)
	if err != nil || id != "" {
		return id, err
	}
	body, err := c.do(ctx, http.MethodPost, "/api/v1/users/groups/", map[string]string{"name": name})
	if err != nil {
		if id, findErr := c.findUserGroupID(ctx, name); findErr == nil && id != "" {
			return id, nil
		}
		return "", fmt.Errorf("创建用户组 %s: %w", name, err)
	}
	var created permissionDTO
	if err := json.Unmarshal(body, &created); err != nil {
		return "", fmt.Errorf("解析用户组 %s: %w", name, err)
	}
	if created.ID == "" {
		return "", fmt.Errorf("创建用户组 %s 没有返回 ID", name)
	}
	return created.ID, nil
}

func (c *Client) findUserGroupID(ctx context.Context, name string) (string, error) {
	path := "/api/v1/users/groups/?name=" + url.QueryEscape(name) + "&limit=20"
	body, err := c.do(ctx, http.MethodGet, path, nil)
	if err != nil {
		return "", fmt.Errorf("查找用户组 %s: %w", name, err)
	}
	groups, err := decodeList[permissionDTO](body)
	if err != nil {
		return "", fmt.Errorf("解析用户组 %s: %w", name, err)
	}
	for _, group := range groups {
		if group.Name == name && group.ID != "" {
			return group.ID, nil
		}
	}
	return "", nil
}

func (c *Client) findAssetPermission(ctx context.Context, name string) (*permissionDTO, error) {
	path := "/api/v1/perms/asset-permissions/?name=" + url.QueryEscape(name) + "&limit=20"
	body, err := c.do(ctx, http.MethodGet, path, nil)
	if err != nil {
		return nil, err
	}
	items, err := decodeList[permissionDTO](body)
	if err != nil {
		return nil, err
	}
	for i := range items {
		if items[i].Name == name && items[i].ID != "" {
			return &items[i], nil
		}
	}
	return nil, nil
}

func (c *Client) assetPermissionDetail(ctx context.Context, id string) (permissionDTO, error) {
	body, err := c.do(ctx, http.MethodGet, "/api/v1/perms/asset-permissions/"+url.PathEscape(id)+"/", nil)
	if err != nil {
		return permissionDTO{}, err
	}
	var item permissionDTO
	if err := json.Unmarshal(body, &item); err != nil {
		return permissionDTO{}, err
	}
	return item, nil
}

type permissionDTO struct {
	ID         string          `json:"id"`
	Name       string          `json:"name"`
	UserGroups json.RawMessage `json:"user_groups"`
	Assets     json.RawMessage `json:"assets"`
}

func (p permissionDTO) groupIDs() []string {
	return relatedIDs(p.UserGroups)
}

func (p permissionDTO) assetIDs() []string {
	return relatedIDs(p.Assets)
}

func relatedIDs(raw json.RawMessage) []string {
	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 || bytes.Equal(raw, []byte("null")) {
		return nil
	}
	var texts []string
	if err := json.Unmarshal(raw, &texts); err == nil {
		return uniqueIDs(texts)
	}
	var objects []struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(raw, &objects); err != nil {
		return nil
	}
	ids := make([]string, 0, len(objects))
	for _, item := range objects {
		ids = append(ids, item.ID)
	}
	return uniqueIDs(ids)
}

func appendID(ids []string, id string) []string {
	return appendIDs(ids, []string{id})
}

func appendIDs(base, extra []string) []string {
	seen := map[string]struct{}{}
	out := make([]string, 0, len(base)+len(extra))
	for _, id := range append(append([]string{}, base...), extra...) {
		id = strings.TrimSpace(id)
		if id == "" {
			continue
		}
		if _, ok := seen[id]; ok {
			continue
		}
		seen[id] = struct{}{}
		out = append(out, id)
	}
	return out
}

func uniqueIDs(ids []string) []string {
	return appendIDs(nil, ids)
}
