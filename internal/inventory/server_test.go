package inventory

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestPasswordStaysOutOfInventory(t *testing.T) {
	server := Server{
		InstanceID: "i-1",
		OS:         OSLinux,
		Password:   "Ab3!secret-value_X",
		Endpoint:   "ecs.cn-hangzhou.aliyuncs.com",
		PasswordReset: ResetResult{
			OK:       true,
			Username: "root",
		},
	}
	raw, err := json.Marshal(server)
	if err != nil {
		t.Fatal(err)
	}
	text := string(raw)
	if strings.Contains(text, server.Password) || strings.Contains(text, server.Endpoint) {
		t.Fatalf("清单包含密码或接口地址: %s", text)
	}
	if !strings.Contains(text, `"password_reset"`) || !strings.Contains(text, `"username":"root"`) {
		t.Fatalf("改密结果应保留: %s", text)
	}
	if server.LoginUsername() != "root" {
		t.Fatal("Linux 登录名应为 root")
	}
	server.OS = OSWindows
	if server.LoginUsername() != "Administrator" {
		t.Fatal("Windows 登录名应为 Administrator")
	}
}
