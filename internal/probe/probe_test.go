package probe

import (
	"fmt"
	"strings"
	"testing"

	"jmsupdateserver/internal/inventory"
)

func TestLinuxUsesPrivateBeforePublic(t *testing.T) {
	open := map[string]bool{
		"10.0.0.8:2221": true,
		"47.0.0.8:22":   true,
	}
	result := Check(inventory.Server{
		OS:         inventory.OSLinux,
		InstanceID: "i-linux",
		Name:       "web",
		PrivateIP:  "10.0.0.8",
		PublicIP:   "47.0.0.8",
	}, []int{22, 2221}, func(ip string, port int) bool {
		return open[fmt.Sprintf("%s:%d", ip, port)]
	})
	if !result.OK || result.Scope != "private" || result.Port != 2221 {
		t.Fatalf("probe = %+v", result)
	}
}

func TestLinuxFallsBackToPublic(t *testing.T) {
	result := Check(inventory.Server{
		OS:        inventory.OSLinux,
		PrivateIP: "10.0.0.8",
		PublicIP:  "47.0.0.8",
	}, []int{22, 2221}, func(ip string, port int) bool {
		return ip == "47.0.0.8" && port == 22
	})
	if !result.OK || result.Scope != "public" || result.Port != 22 {
		t.Fatalf("probe = %+v", result)
	}
}

func TestWindowsChecks3389Only(t *testing.T) {
	var seen []string
	result := Check(inventory.Server{
		OS:         inventory.OSWindows,
		InstanceID: "i-win",
		Name:       "rdp",
		RegionID:   "cn-hangzhou",
		PrivateIP:  "10.1.0.3",
		PublicIP:   "47.1.0.3",
	}, []int{3389}, func(ip string, port int) bool {
		seen = append(seen, fmt.Sprintf("%s:%d", ip, port))
		return false
	})
	if result.OK || !strings.Contains(result.Error, "3389") {
		t.Fatalf("probe = %+v", result)
	}
	if strings.Join(seen, ",") != "10.1.0.3:3389,47.1.0.3:3389" {
		t.Fatalf("dial order = %v", seen)
	}
}
