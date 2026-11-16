package inventory

import "strings"

const (
	OSLinux   = "linux"
	OSWindows = "windows"

	LabelInstanceID = "ecs_instance_id"
	LabelPrivateIP  = "private_ip"
	LabelPublicIP   = "public_ip"
	LabelRegion     = "ecs_region"
)

// Server 是一台带配置标签键的 ECS，以及后续端口探测、同步结果。
type Server struct {
	InstanceID string `json:"instance_id"`
	Name       string `json:"name"`
	Account    string `json:"account,omitempty"`
	TagKey     string `json:"tag_key,omitempty"`
	TagValue   string `json:"tag_value,omitempty"`
	RegionID   string `json:"region_id"`
	ZoneID     string `json:"zone_id"`
	Status     string `json:"status"`
	OSType     string `json:"os_type"`
	OSName     string `json:"os_name"`
	OS         string `json:"os"`
	PrivateIP  string `json:"private_ip"`
	PublicIP   string `json:"public_ip"`
	Probe      Probe  `json:"probe"`
	// Endpoint 是该地域的 ECS API 地址，只在改密时使用。
	Endpoint string `json:"-"`
	// Password 是本次在线改密得到的登录密码，只写入 JumpServer 账号。
	Password      string      `json:"-"`
	PasswordReset ResetResult `json:"password_reset,omitempty"`
	JumpServer    Sync        `json:"jumpserver"`
}

type Probe struct {
	OK    bool   `json:"ok"`
	Scope string `json:"scope,omitempty"`
	Port  int    `json:"port,omitempty"`
	Error string `json:"error,omitempty"`
	// Ports 是本次探测使用的端口。探测失败时，协议端口取第一个。
	Ports []int `json:"-"`
}

type Sync struct {
	Action  string `json:"action,omitempty"`
	ID      string `json:"id,omitempty"`
	Account string `json:"account,omitempty"`
	Error   string `json:"error,omitempty"`
}

// ResetResult 只记录改密是否成功。密码本身不进清单。
// SkipSync 为 true 时这台机器不写入 JumpServer。
type ResetResult struct {
	OK       bool   `json:"ok"`
	Username string `json:"username,omitempty"`
	Error    string `json:"error,omitempty"`
	SkipSync bool   `json:"skip_sync,omitempty"`
}

// ClassifyOS 按 ECS 返回的 OSType / OSName 分成 Windows 与 Linux。
// 未标明 Windows 的实例归入 Linux，后续按 SSH 探测。
func ClassifyOS(osType, osName string) string {
	if strings.EqualFold(strings.TrimSpace(osType), OSWindows) ||
		strings.Contains(strings.ToLower(osName), OSWindows) {
		return OSWindows
	}
	return OSLinux
}

// ConnectAddress 是写入 JumpServer address 的连接地址。
// 探测成功时用通的那一侧；都不通时优先内网，便于堡垒机在内网恢复后直接连。
func (s Server) ConnectAddress() string {
	if s.Probe.OK {
		if s.Probe.Scope == "public" {
			return s.PublicIP
		}
		return s.PrivateIP
	}
	if s.PrivateIP != "" {
		return s.PrivateIP
	}
	return s.PublicIP
}

// ProtocolPort 是写入 JumpServer 协议的端口。探测失败时用该系统配置的第一个端口。
func (s Server) ProtocolPort() int {
	if s.Probe.OK && s.Probe.Port > 0 {
		return s.Probe.Port
	}
	if len(s.Probe.Ports) > 0 {
		return s.Probe.Ports[0]
	}
	if s.OS == OSWindows {
		return 3389
	}
	return 22
}

func (s Server) DisplayName() string {
	name := strings.TrimSpace(s.Name)
	if name == "" {
		return s.InstanceID
	}
	return name
}

// LoginUsername 是在线改密和 JumpServer 账号使用的登录名。
func (s Server) LoginUsername() string {
	if s.OS == OSWindows {
		return "Administrator"
	}
	return "root"
}
