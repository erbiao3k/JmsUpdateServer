package probe

import (
	"fmt"
	"net"
	"strconv"
	"strings"
	"time"

	"jmsupdateserver/internal/inventory"
)

// DialFunc 报告 ip:port 的 TCP 是否连通。
type DialFunc func(ip string, port int) bool

// Check 按给定端口探测管理端口。先内网，再外网，同一侧按端口列表顺序尝试。
func Check(server inventory.Server, ports []int, dial DialFunc) inventory.Probe {
	protocol := "SSH"
	if server.OS == inventory.OSWindows {
		protocol = "RDP"
	}
	if len(ports) == 0 {
		return inventory.Probe{Error: protocol + " 没有配置探测端口"}
	}

	if result, ok := firstOpen(server.PrivateIP, "private", ports, dial); ok {
		result.Ports = ports
		return result
	}
	if result, ok := firstOpen(server.PublicIP, "public", ports, dial); ok {
		result.Ports = ports
		return result
	}
	return inventory.Probe{
		Ports: ports,
		Error: fmt.Sprintf(
			"%s 端口不可达 instance=%s name=%s region=%s private=%s public=%s 已检测 %s",
			protocol,
			server.InstanceID,
			server.DisplayName(),
			server.RegionID,
			emptyAs(server.PrivateIP, "空"),
			emptyAs(server.PublicIP, "空"),
			describeChecks(server, ports),
		),
	}
}

func firstOpen(ip, scope string, ports []int, dial DialFunc) (inventory.Probe, bool) {
	ip = strings.TrimSpace(ip)
	if ip == "" {
		return inventory.Probe{}, false
	}
	for _, port := range ports {
		if dial(ip, port) {
			return inventory.Probe{OK: true, Scope: scope, Port: port}, true
		}
	}
	return inventory.Probe{}, false
}

func describeChecks(server inventory.Server, ports []int) string {
	parts := make([]string, 0, 2)
	if server.PrivateIP != "" {
		parts = append(parts, "内网 "+joinPorts(ports))
	} else {
		parts = append(parts, "内网地址为空")
	}
	if server.PublicIP != "" {
		parts = append(parts, "外网 "+joinPorts(ports))
	} else {
		parts = append(parts, "外网地址为空")
	}
	return strings.Join(parts, " 与 ")
}

func joinPorts(ports []int) string {
	text := make([]string, len(ports))
	for i, port := range ports {
		text[i] = strconv.Itoa(port)
	}
	return strings.Join(text, "/")
}

func emptyAs(value, fallback string) string {
	if strings.TrimSpace(value) == "" {
		return fallback
	}
	return value
}

// TCP 用超时建立 TCP 连接，连上即关闭。
func TCP(timeout time.Duration) DialFunc {
	if timeout <= 0 {
		timeout = 3 * time.Second
	}
	return func(ip string, port int) bool {
		conn, err := net.DialTimeout("tcp", net.JoinHostPort(ip, strconv.Itoa(port)), timeout)
		if err != nil {
			return false
		}
		_ = conn.Close()
		return true
	}
}
