package resetpass

import (
	"encoding/base64"
	"fmt"
	"strings"
	"unicode/utf8"

	"jmsupdateserver/internal/inventory"
)

const allowedSpecial = "()`~!@#$%^&*-_+=|{}[]:;'<>,.?/"

// Check 按阿里云在线改密规则检查配置文件中的密码，错误信息不回显密码。
func Check(password string) error {
	n := utf8.RuneCountInString(password)
	if n < 8 || n > 30 {
		return fmt.Errorf("长度必须是 8 到 30 个字符")
	}
	if strings.HasPrefix(password, "/") {
		return fmt.Errorf("不能以 / 开头")
	}
	classes, ok := charClasses(password)
	if !ok {
		return fmt.Errorf("只能包含字母、数字和这些特殊字符：%s", allowedSpecial)
	}
	if classes < 3 {
		return fmt.Errorf("至少包含大写字母、小写字母、数字、特殊字符中的三类")
	}
	return nil
}

// Command 返回云助手命令类型和脚本。密码只以 Base64 出现在脚本里。
func Command(osName, password string) (string, string, error) {
	if strings.TrimSpace(password) == "" {
		return "", "", fmt.Errorf("密码为空")
	}
	encoded := base64.StdEncoding.EncodeToString([]byte(password))
	var script string
	var kind string
	switch osName {
	case inventory.OSLinux:
		kind = "RunShellScript"
		script = linuxScript(encoded)
	case inventory.OSWindows:
		kind = "RunPowerShellScript"
		script = windowsScript(encoded)
	default:
		return "", "", fmt.Errorf("无法识别操作系统 %s", osName)
	}
	return kind, normalizeNewlines(script), nil
}

func linuxScript(encoded string) string {
	return fmt.Sprintf(`set -eu
b64='%s'
pw=$(printf '%%s' "$b64" | base64 -d 2>/dev/null || printf '%%s' "$b64" | openssl enc -d -base64 -A 2>/dev/null) || exit 1
if [ -z "$pw" ]; then
  exit 1
fi
printf 'root:%%s\n' "$pw" | chpasswd
`, encoded)
}

func windowsScript(encoded string) string {
	return fmt.Sprintf(`$ErrorActionPreference = 'Stop'
$pw = [System.Text.Encoding]::UTF8.GetString([System.Convert]::FromBase64String('%s'))
if ([string]::IsNullOrEmpty($pw)) { exit 1 }
$user = [ADSI]'WinNT://./Administrator,user'
$user.SetPassword($pw)
$user.SetInfo()
`, encoded)
}

func normalizeNewlines(script string) string {
	script = strings.ReplaceAll(script, "\r\n", "\n")
	return strings.ReplaceAll(script, "\r", "\n")
}

// Redact 从错误文本里去掉密码、脚本，以及它们的 Base64。
func Redact(text string, secrets ...string) string {
	for _, secret := range secrets {
		if secret == "" {
			continue
		}
		text = strings.ReplaceAll(text, secret, "***")
		encoded := base64.StdEncoding.EncodeToString([]byte(secret))
		if encoded != "" {
			text = strings.ReplaceAll(text, encoded, "***")
		}
	}
	return text
}

func charClasses(password string) (int, bool) {
	var upper, lower, digit, special bool
	for _, r := range password {
		switch {
		case r >= 'A' && r <= 'Z':
			upper = true
		case r >= 'a' && r <= 'z':
			lower = true
		case r >= '0' && r <= '9':
			digit = true
		case strings.ContainsRune(allowedSpecial, r):
			special = true
		default:
			return 0, false
		}
	}
	n := 0
	for _, ok := range []bool{upper, lower, digit, special} {
		if ok {
			n++
		}
	}
	return n, true
}
