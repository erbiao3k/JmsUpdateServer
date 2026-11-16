package aliyun

import "strings"

const (
	// BlockNotRunning 表示实例不是 Running，不能加入 JumpServer。
	BlockNotRunning = "not_running"
	// BlockAssistantOffline 表示云助手没有在线，不能加入 JumpServer。
	BlockAssistantOffline = "assistant_offline"
)

// BlockReason 判断这次改密失败是否应跳过 JumpServer。
// 空字符串表示其他失败，主机仍然写入 JumpServer。
func BlockReason(err error) string {
	if err == nil {
		return ""
	}
	text := err.Error()
	if instanceNotRunning(text) {
		return BlockNotRunning
	}
	if assistantOffline(text) {
		return BlockAssistantOffline
	}
	return ""
}

func instanceNotRunning(text string) bool {
	return strings.Contains(text, "不是 Running") ||
		strings.Contains(text, "was not running") ||
		strings.Contains(text, "InstanceNotRunning")
}

func assistantOffline(text string) bool {
	markers := []string{
		"ClientNotRunning",
		"ClientNotResponse",
		"ClientNotOnline",
		"DeliveryTimeout",
		"aliyun service is not running",
		"does not response",
		"does not respond",
	}
	for _, marker := range markers {
		if strings.Contains(text, marker) {
			return true
		}
	}
	return false
}
