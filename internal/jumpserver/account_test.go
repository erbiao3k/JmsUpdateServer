package jumpserver

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestEnsureAccountTemplateCreates(t *testing.T) {
	const secret = "Ab3!secret-value_X"
	var method string
	var body map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			_, _ = w.Write([]byte(`{"results":[]}`))
			return
		}
		method = r.Method
		raw, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(raw, &body)
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"id":"tpl-1","name":"ecs-linux-root"}`))
	}))
	defer srv.Close()

	id, err := New(srv.URL, "tok", "Token", "").EnsureAccountTemplate(context.Background(), "ecs-linux-root", "root", secret)
	if err != nil {
		t.Fatal(err)
	}
	if id != "tpl-1" || method != http.MethodPost {
		t.Fatalf("id=%s method=%s", id, method)
	}
	if body["secret"] != secret || body["secret_strategy"] != "specific" || body["username"] != "root" || body["privileged"] != true || body["auto_push"] != false {
		t.Fatalf("payload = %#v", body)
	}
}

func TestEnsureAccountTemplateUpdatesByName(t *testing.T) {
	var method, path string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			_, _ = w.Write([]byte(`{"results":[{"id":"tpl-9","name":"other"},{"id":"tpl-2","name":"ecs-windows-administrator"}]}`))
			return
		}
		method = r.Method
		path = r.URL.Path
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"id":"tpl-2"}`))
	}))
	defer srv.Close()

	id, err := New(srv.URL, "tok", "Token", "").EnsureAccountTemplate(context.Background(), "ecs-windows-administrator", "Administrator", "Ab3!secret-value_X")
	if err != nil {
		t.Fatal(err)
	}
	if id != "tpl-2" || method != http.MethodPatch || path != "/api/v1/accounts/account-templates/tpl-2/" {
		t.Fatalf("id=%s method=%s path=%s", id, method, path)
	}
}

func TestEnsureAccountTemplateRedactsSecret(t *testing.T) {
	const secret = "Ab3!secret-value_X"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			_, _ = w.Write([]byte(`{"results":[]}`))
			return
		}
		http.Error(w, "secret rejected: "+secret, http.StatusBadRequest)
	}))
	defer srv.Close()

	_, err := New(srv.URL, "tok", "Token", "").EnsureAccountTemplate(context.Background(), "ecs-linux-root", "root", secret)
	if err == nil {
		t.Fatal("期望失败")
	}
	if strings.Contains(err.Error(), secret) {
		t.Fatalf("错误包含密码: %s", err.Error())
	}
}

func TestLinkTemplateAccountCreatesWithoutSecret(t *testing.T) {
	var method string
	var body map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			if !strings.Contains(r.URL.RawQuery, "asset_id=asset-1") || !strings.Contains(r.URL.RawQuery, "secret_type=password") {
				t.Errorf("query = %s", r.URL.RawQuery)
			}
			_, _ = w.Write([]byte(`{"results":[{"id":"wrong","username":"root","asset":{"id":"other-asset"}}]}`))
			return
		}
		method = r.Method
		raw, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(raw, &body)
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"id":"a2"}`))
	}))
	defer srv.Close()

	action, err := New(srv.URL, "tok", "Bearer", "").LinkTemplateAccount(context.Background(), "asset-1", "tpl-1", "root")
	if err != nil {
		t.Fatal(err)
	}
	if action != "created" || method != http.MethodPost {
		t.Fatalf("action=%s method=%s", action, method)
	}
	if body["asset"] != "asset-1" || body["template"] != "tpl-1" || body["follow_template"] != true || body["push_now"] != false {
		t.Fatalf("payload = %#v", body)
	}
	if _, ok := body["secret"]; ok {
		t.Fatal("关联模板时不应写入主机密码")
	}
}

func TestLinkTemplateAccountSkipsSameTemplate(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			t.Fatalf("已关联同一模板时不应再写账号: %s", r.Method)
		}
		_, _ = w.Write([]byte(`{"results":[{"id":"a1","username":"root","asset":{"id":"asset-1"},"source":{"value":"template"},"source_id":"tpl-1","follow_template":true}]}`))
	}))
	defer srv.Close()

	action, err := New(srv.URL, "tok", "Token", "").LinkTemplateAccount(context.Background(), "asset-1", "tpl-1", "root")
	if err != nil {
		t.Fatal(err)
	}
	if action != "linked" {
		t.Fatalf("action=%s", action)
	}
}

func TestLinkTemplateAccountSwitchesLocalAccount(t *testing.T) {
	var body map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			_, _ = w.Write([]byte(`{"results":[{"id":"a1","username":"Administrator","asset":"asset-2","source":"local","follow_template":false}]}`))
			return
		}
		raw, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(raw, &body)
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"id":"a1"}`))
	}))
	defer srv.Close()

	action, err := New(srv.URL, "tok", "Token", "").LinkTemplateAccount(context.Background(), "asset-2", "tpl-2", "Administrator")
	if err != nil {
		t.Fatal(err)
	}
	if action != "updated" || body["template"] != "tpl-2" || body["follow_template"] != true {
		t.Fatalf("action=%s payload=%#v", action, body)
	}
	if _, ok := body["secret"]; ok {
		t.Fatal("切换模板时不应提交密码")
	}
}
