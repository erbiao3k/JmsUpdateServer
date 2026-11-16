package jumpserver

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestEnsureAssetPermissionCreates(t *testing.T) {
	var body map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/api/v1/users/groups/":
			_, _ = w.Write([]byte(`{"results":[{"id":"group-1","name":"user_group_test"}]}`))
		case r.Method == http.MethodGet && r.URL.Path == "/api/v1/perms/asset-permissions/":
			_, _ = w.Write([]byte(`{"results":[]}`))
		default:
			raw, _ := io.ReadAll(r.Body)
			_ = json.Unmarshal(raw, &body)
			w.WriteHeader(http.StatusCreated)
			_, _ = w.Write([]byte(`{"id":"perm-1"}`))
		}
	}))
	defer srv.Close()

	err := New(srv.URL, "tok", "Token", "").EnsureAssetPermission(
		context.Background(),
		"asset_permissions_test",
		"user_group_test",
		[]string{"connect", "upload", "download", "copy", "paste", "delete", "share"},
		[]string{"asset-1"},
	)
	if err != nil {
		t.Fatal(err)
	}
	groups, _ := body["user_groups"].([]any)
	assets, _ := body["assets"].([]any)
	actions, _ := body["actions"].([]any)
	if body["name"] != "asset_permissions_test" || len(groups) != 1 || groups[0] != "group-1" || len(assets) != 1 || assets[0] != "asset-1" || len(actions) != 7 {
		t.Fatalf("payload = %#v", body)
	}
}

func TestEnsureAssetPermissionAppends(t *testing.T) {
	var body map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/api/v1/users/groups/":
			_, _ = w.Write([]byte(`{"results":[{"id":"group-new","name":"user_group_admin"}]}`))
		case r.Method == http.MethodGet && r.URL.Path == "/api/v1/perms/asset-permissions/":
			_, _ = w.Write([]byte(`{"results":[{"id":"perm-1","name":"asset_permissions_admin"}]}`))
		case r.Method == http.MethodGet:
			_, _ = w.Write([]byte(`{"id":"perm-1","user_groups":[{"id":"group-old"}],"assets":[{"id":"asset-old"}]}`))
		default:
			raw, _ := io.ReadAll(r.Body)
			_ = json.Unmarshal(raw, &body)
			_, _ = w.Write([]byte(`{"id":"perm-1"}`))
		}
	}))
	defer srv.Close()

	err := New(srv.URL, "tok", "Token", "").EnsureAssetPermission(context.Background(), "asset_permissions_admin", "user_group_admin", []string{"connect"}, []string{"asset-new"})
	if err != nil {
		t.Fatal(err)
	}
	groups, _ := body["user_groups"].([]any)
	assets, _ := body["assets"].([]any)
	if len(groups) != 2 || len(assets) != 2 {
		t.Fatalf("payload = %#v", body)
	}
}

func TestEnsureAssetPermissionCreatesGroup(t *testing.T) {
	var groupBody map[string]string
	var permBody map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/api/v1/users/groups/":
			_, _ = w.Write([]byte(`{"results":[]}`))
		case r.Method == http.MethodPost && r.URL.Path == "/api/v1/users/groups/":
			raw, _ := io.ReadAll(r.Body)
			_ = json.Unmarshal(raw, &groupBody)
			w.WriteHeader(http.StatusCreated)
			_, _ = w.Write([]byte(`{"id":"group-created","name":"user_group_test"}`))
		case r.Method == http.MethodGet && r.URL.Path == "/api/v1/perms/asset-permissions/":
			_, _ = w.Write([]byte(`{"results":[]}`))
		default:
			raw, _ := io.ReadAll(r.Body)
			_ = json.Unmarshal(raw, &permBody)
			w.WriteHeader(http.StatusCreated)
			_, _ = w.Write([]byte(`{"id":"perm-1"}`))
		}
	}))
	defer srv.Close()

	err := New(srv.URL, "tok", "Token", "").EnsureAssetPermission(context.Background(), "asset_permissions_test", "user_group_test", []string{"connect"}, []string{"asset-1"})
	if err != nil {
		t.Fatal(err)
	}
	groups, _ := permBody["user_groups"].([]any)
	if groupBody["name"] != "user_group_test" || len(groups) != 1 || groups[0] != "group-created" {
		t.Fatalf("group = %#v perm = %#v", groupBody, permBody)
	}
}
