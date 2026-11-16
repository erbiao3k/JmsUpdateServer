package aliyun

import (
	"errors"
	"testing"
)

func TestBlockReason(t *testing.T) {
	cases := []struct {
		err  error
		want string
	}{
		{errors.New("实例状态为 Stopped，不是 Running"), BlockNotRunning},
		{errors.New("The specified instance was not running during task execution"), BlockNotRunning},
		{errors.New("云助手改密失败 code=ClientNotRunning info=The aliyun service is not running on the instance"), BlockAssistantOffline},
		{errors.New("code=ClientNotResponse info=The aliyun service in the instance does not response"), BlockAssistantOffline},
		{errors.New("code=DeliveryTimeout info=The command delivery has been timeout"), BlockAssistantOffline},
		{errors.New("code=ExitCodeNonzero output=chpasswd failed"), ""},
		{nil, ""},
	}
	for _, tc := range cases {
		if got := BlockReason(tc.err); got != tc.want {
			t.Fatalf("BlockReason(%v) = %q, want %q", tc.err, got, tc.want)
		}
	}
}
