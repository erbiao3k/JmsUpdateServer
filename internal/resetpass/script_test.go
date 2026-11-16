package resetpass

import (
	"encoding/base64"
	"strings"
	"testing"

	"jmsupdateserver/internal/inventory"
)

func TestCheck(t *testing.T) {
	if err := Check("Ab3!secret"); err != nil {
		t.Fatal(err)
	}
	if err := Check("short1A"); err == nil {
		t.Fatal("过短密码应被拒绝")
	}
	if err := Check("/Ab3!secret"); err == nil {
		t.Fatal("以 / 开头应被拒绝")
	}
	if err := Check("abcdefgh"); err == nil {
		t.Fatal("只有一类字符应被拒绝")
	}
	if err := Check("Ab3 secret!"); err == nil {
		t.Fatal("空格应被拒绝")
	}
	if err := Check("Ab3!secret"); err != nil {
		t.Fatal(err)
	}
}

func TestCommandHidesPassword(t *testing.T) {
	const password = "Ab3!secret-value_X"
	kind, script, err := Command(inventory.OSLinux, password)
	if err != nil {
		t.Fatal(err)
	}
	if kind != "RunShellScript" {
		t.Fatalf("kind = %s", kind)
	}
	if strings.Contains(script, password) {
		t.Fatal("Linux 脚本包含明文密码")
	}
	if !strings.Contains(script, "chpasswd") || !strings.Contains(script, "root:") || !strings.Contains(script, "openssl enc -d -base64") {
		t.Fatalf("Linux 脚本不完整: %s", script)
	}
	if strings.Contains(script, "\r") {
		t.Fatal("Linux 脚本含有回车符")
	}
	if !strings.Contains(script, base64.StdEncoding.EncodeToString([]byte(password))) {
		t.Fatal("Linux 脚本缺少密码的 Base64")
	}

	kind, script, err = Command(inventory.OSWindows, password)
	if err != nil {
		t.Fatal(err)
	}
	if kind != "RunPowerShellScript" {
		t.Fatalf("kind = %s", kind)
	}
	if strings.Contains(script, password) {
		t.Fatal("Windows 脚本包含明文密码")
	}
	if !strings.Contains(script, "SetPassword") || !strings.Contains(script, "Administrator") || strings.Contains(script, "Set-LocalUser") {
		t.Fatalf("Windows 脚本不完整: %s", script)
	}
}

func TestRedactRemovesScriptAndPassword(t *testing.T) {
	const password = "Ab3!secret-value_X"
	_, script, err := Command(inventory.OSLinux, password)
	if err != nil {
		t.Fatal(err)
	}
	encodedScript := base64.StdEncoding.EncodeToString([]byte(script))
	text := "failed " + password + " " + script + " " + encodedScript
	got := Redact(text, password, script)
	if strings.Contains(got, password) || strings.Contains(got, script) || strings.Contains(got, encodedScript) {
		t.Fatalf("脱敏不完整: %s", got)
	}
}
