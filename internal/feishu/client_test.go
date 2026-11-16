package feishu

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"
)

func TestNotifyPostsTextAndSign(t *testing.T) {
	var got map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Content-Type") != "application/json" {
			t.Errorf("content-type = %s", r.Header.Get("Content-Type"))
		}
		raw, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(raw, &got)
		_, _ = w.Write([]byte(`{"code":0,"msg":"success","StatusCode":0}`))
	}))
	defer srv.Close()

	if err := New(srv.URL, "test-secret").Notify(context.Background(), "实例不是 Running"); err != nil {
		t.Fatal(err)
	}
	if got["msg_type"] != "text" {
		t.Fatalf("payload = %#v", got)
	}
	content, _ := got["content"].(map[string]any)
	if content["text"] != "实例不是 Running" || got["sign"] == "" || got["timestamp"] == "" {
		t.Fatalf("payload = %#v", got)
	}
	timestamp, _ := got["timestamp"].(string)
	unix, err := strconv.ParseInt(timestamp, 10, 64)
	if err != nil {
		t.Fatal(err)
	}
	if sign("test-secret", unix) != got["sign"] {
		t.Fatal("签名不一致")
	}
}

func TestNotifyEmptyWebhook(t *testing.T) {
	if err := New("", "").Notify(context.Background(), "hello"); err != nil {
		t.Fatal(err)
	}
}

func TestNotifyRejectsFeishuError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"code":19024,"msg":"key word not found"}`))
	}))
	defer srv.Close()

	bot := New(srv.URL, "")
	if err := bot.Notify(context.Background(), "hello"); err == nil {
		t.Fatal("期望飞书拒绝")
	}
	if bot.Failures() != 1 {
		t.Fatalf("failures = %d", bot.Failures())
	}
}
