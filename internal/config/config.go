package config

import (
	"fmt"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"

	"gopkg.in/yaml.v3"

	"jmsupdateserver/internal/inventory"
	"jmsupdateserver/internal/resetpass"
)

type Config struct {
	Interval      string        `yaml:"interval"`
	WindowStart   Clock         `yaml:"window_start"`
	WindowEnd     Clock         `yaml:"window_end"`
	Aliyun        Aliyun        `yaml:"aliyun"`
	Blacklist     []string      `yaml:"blacklist"`
	JumpServer    JumpServer    `yaml:"jumpserver"`
	Probe         Probe         `yaml:"probe"`
	PasswordReset PasswordReset `yaml:"password_reset"`
	Feishu        Feishu        `yaml:"feishu"`
	Output        Output        `yaml:"output"`

	Every        time.Duration `yaml:"-"`
	ProbeTimeout time.Duration `yaml:"-"`
}

type Aliyun struct {
	AccessKeyID       string          `yaml:"access_key_id"`
	AccessKeySecret   string          `yaml:"access_key_secret"`
	BootstrapEndpoint string          `yaml:"bootstrap_endpoint"`
	TagKey            string          `yaml:"tag_key"`
	Accounts          []AliyunAccount `yaml:"accounts"`
}

// AliyunAccount 是一个独立的阿里云账号。填写 accounts 后不再使用外层的 AccessKey。
type AliyunAccount struct {
	Name              string `yaml:"name"`
	AccessKeyID       string `yaml:"access_key_id"`
	AccessKeySecret   string `yaml:"access_key_secret"`
	BootstrapEndpoint string `yaml:"bootstrap_endpoint"`
}

// Label 是日志里显示的账号名。
func (a AliyunAccount) Label() string {
	if strings.TrimSpace(a.Name) == "" {
		return "默认账号"
	}
	return a.Name
}

func cleanIDs(ids []string) []string {
	seen := map[string]struct{}{}
	out := make([]string, 0, len(ids))
	for _, id := range ids {
		id = strings.TrimSpace(id)
		if id == "" {
			continue
		}
		if _, ok := seen[id]; ok {
			continue
		}
		seen[id] = struct{}{}
		out = append(out, id)
	}
	return out
}

// ResolvedAccounts 返回要扫描的账号。未配置 accounts 时，使用外层的单个 AccessKey。
func (a Aliyun) ResolvedAccounts() []AliyunAccount {
	if len(a.Accounts) > 0 {
		return a.Accounts
	}
	return []AliyunAccount{{
		AccessKeyID:       a.AccessKeyID,
		AccessKeySecret:   a.AccessKeySecret,
		BootstrapEndpoint: a.BootstrapEndpoint,
	}}
}

type JumpServer struct {
	BaseURL             string   `yaml:"base_url"`
	Token               string   `yaml:"token"`
	AuthScheme          string   `yaml:"auth_scheme"`
	OrgID               string   `yaml:"org_id"`
	LinuxPlatformID     int      `yaml:"linux_platform_id"`
	WindowsPlatformID   int      `yaml:"windows_platform_id"`
	LinuxPlatformName   string   `yaml:"linux_platform_name"`
	WindowsPlatformName string   `yaml:"windows_platform_name"`
	NodesDisplay        []string `yaml:"nodes_display"`
}

type Probe struct {
	Timeout           string `yaml:"timeout"`
	Concurrency       int    `yaml:"concurrency"`
	RegionConcurrency int    `yaml:"region_concurrency"`
	LinuxPorts        []int  `yaml:"linux_ports"`
	WindowsPorts      []int  `yaml:"windows_ports"`
}

// PortsFor 返回该系统要探测的端口，顺序与配置一致。
func (p Probe) PortsFor(osName string) []int {
	if osName == inventory.OSWindows {
		return p.WindowsPorts
	}
	return p.LinuxPorts
}

type Output struct {
	Inventory string `yaml:"inventory"`
	ErrorLog  string `yaml:"error_log"`
}

// Feishu 是自定义机器人。Secret 只在开启签名校验时填写。
type Feishu struct {
	Webhook string `yaml:"webhook"`
	Secret  string `yaml:"secret"`
}

// PasswordReset 控制云助手在线改密。Enabled 省略时默认打开；dry-run 仍然不会改密。
type PasswordReset struct {
	Enabled        *bool         `yaml:"enabled"`
	Password       string        `yaml:"password"`
	Timeout        string        `yaml:"timeout"`
	Concurrency    int           `yaml:"concurrency"`
	CommandTimeout time.Duration `yaml:"-"`
}

func (p PasswordReset) On() bool {
	if p.Enabled == nil {
		return true
	}
	return *p.Enabled
}

func Load(path string) (Config, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return Config{}, fmt.Errorf("读取配置 %s: %w", path, err)
	}
	var cfg Config
	if err := yaml.Unmarshal(raw, &cfg); err != nil {
		return Config{}, fmt.Errorf("解析配置 %s: %w", path, err)
	}
	cfg.applyEnv()
	cfg.applyDefaults()
	if err := cfg.Probe.checkPorts(); err != nil {
		return Config{}, err
	}
	timeout, err := time.ParseDuration(cfg.Probe.Timeout)
	if err != nil {
		return Config{}, fmt.Errorf("probe.timeout 无效: %w", err)
	}
	cfg.ProbeTimeout = timeout
	commandTimeout, err := time.ParseDuration(cfg.PasswordReset.Timeout)
	if err != nil {
		return Config{}, fmt.Errorf("password_reset.timeout 无效: %w", err)
	}
	cfg.PasswordReset.CommandTimeout = commandTimeout
	every, err := time.ParseDuration(cfg.Interval)
	if err != nil || every <= 0 {
		return Config{}, fmt.Errorf("interval 无效，请使用类似 5m、300s 的时长")
	}
	cfg.Every = every
	if cfg.WindowEnd.minutes() <= cfg.WindowStart.minutes() {
		return Config{}, fmt.Errorf("window_end 必须晚于 window_start")
	}
	return cfg, nil
}

// Clock 是一天中的 HH:MM。直接从 YAML 原文解析，避免 09:00 被当成数字。
type Clock struct {
	Hour   int
	Minute int
	set    bool
}

func (c Clock) String() string {
	return fmt.Sprintf("%02d:%02d", c.Hour, c.Minute)
}

func (c Clock) minutes() int {
	return c.Hour*60 + c.Minute
}

func (c *Clock) UnmarshalYAML(node *yaml.Node) error {
	if node.Kind != yaml.ScalarNode {
		return fmt.Errorf("时间必须是 HH:MM")
	}
	parsed, err := parseClock(node.Value)
	if err != nil {
		return err
	}
	*c = parsed
	return nil
}

func parseClock(text string) (Clock, error) {
	parts := strings.Split(strings.TrimSpace(text), ":")
	if len(parts) != 2 {
		return Clock{}, fmt.Errorf("时间必须是 HH:MM，例如 09:00")
	}
	hour, errH := strconv.Atoi(parts[0])
	minute, errM := strconv.Atoi(parts[1])
	if errH != nil || errM != nil || hour < 0 || hour > 23 || minute < 0 || minute > 59 {
		return Clock{}, fmt.Errorf("时间必须是 HH:MM，例如 09:00")
	}
	return Clock{Hour: hour, Minute: minute, set: true}, nil
}

// InWindow 判断 t 是否落在当天的执行时间段内。开始时刻包含在内，结束时刻不包含。
func (c Config) InWindow(t time.Time) bool {
	now := t.Hour()*60 + t.Minute()
	return now >= c.WindowStart.minutes() && now < c.WindowEnd.minutes()
}

// NextStart 返回下一次进入执行时间段的本地时间。
func (c Config) NextStart(now time.Time) time.Time {
	start := time.Date(now.Year(), now.Month(), now.Day(), c.WindowStart.Hour, c.WindowStart.Minute, 0, 0, now.Location())
	if now.Before(start) {
		return start
	}
	return start.Add(24 * time.Hour)
}

func (c *Config) applyEnv() {
	set := func(dst *string, key string) {
		if value := strings.TrimSpace(os.Getenv(key)); value != "" {
			*dst = value
		}
	}
	set(&c.Aliyun.AccessKeyID, "ALIYUN_ACCESS_KEY_ID")
	set(&c.Aliyun.AccessKeySecret, "ALIYUN_ACCESS_KEY_SECRET")
	set(&c.JumpServer.BaseURL, "JUMPSERVER_BASE_URL")
	set(&c.JumpServer.Token, "JUMPSERVER_TOKEN")
	set(&c.JumpServer.OrgID, "JUMPSERVER_ORG_ID")
	set(&c.Feishu.Webhook, "FEISHU_WEBHOOK")
	set(&c.Feishu.Secret, "FEISHU_SECRET")
}

func (c *Config) applyDefaults() {
	c.Aliyun.AccessKeyID = strings.TrimSpace(c.Aliyun.AccessKeyID)
	c.Aliyun.AccessKeySecret = strings.TrimSpace(c.Aliyun.AccessKeySecret)
	if c.Aliyun.BootstrapEndpoint == "" {
		c.Aliyun.BootstrapEndpoint = "ecs.cn-hangzhou.aliyuncs.com"
	}
	if c.Aliyun.TagKey == "" {
		c.Aliyun.TagKey = "jms-manage"
	}
	c.Blacklist = cleanIDs(c.Blacklist)
	for i := range c.Aliyun.Accounts {
		account := &c.Aliyun.Accounts[i]
		account.Name = strings.TrimSpace(account.Name)
		account.AccessKeyID = strings.TrimSpace(account.AccessKeyID)
		account.AccessKeySecret = strings.TrimSpace(account.AccessKeySecret)
		account.BootstrapEndpoint = strings.TrimSpace(account.BootstrapEndpoint)
		if account.BootstrapEndpoint == "" {
			account.BootstrapEndpoint = c.Aliyun.BootstrapEndpoint
		}
	}
	if c.JumpServer.AuthScheme == "" {
		c.JumpServer.AuthScheme = "Bearer"
	}
	if c.JumpServer.LinuxPlatformName == "" {
		c.JumpServer.LinuxPlatformName = "Linux"
	}
	if c.JumpServer.WindowsPlatformName == "" {
		c.JumpServer.WindowsPlatformName = "Windows"
	}
	if c.Probe.Timeout == "" {
		c.Probe.Timeout = "3s"
	}
	if c.Probe.Concurrency <= 0 {
		c.Probe.Concurrency = 32
	}
	if c.Probe.RegionConcurrency <= 0 {
		c.Probe.RegionConcurrency = 5
	}
	if len(c.Probe.LinuxPorts) == 0 {
		c.Probe.LinuxPorts = []int{22, 2221}
	}
	if len(c.Probe.WindowsPorts) == 0 {
		c.Probe.WindowsPorts = []int{3389}
	}
	if c.Output.Inventory == "" {
		c.Output.Inventory = "inventory.json"
	}
	if c.Output.ErrorLog == "" {
		c.Output.ErrorLog = "logs/sync-error.log"
	}
	if c.PasswordReset.Timeout == "" {
		c.PasswordReset.Timeout = "60s"
	}
	if c.PasswordReset.Concurrency <= 0 {
		c.PasswordReset.Concurrency = 8
	}
	if strings.TrimSpace(c.Interval) == "" {
		c.Interval = "5m"
	}
	if !c.WindowStart.set {
		c.WindowStart = Clock{Hour: 9, set: true}
	}
	if !c.WindowEnd.set {
		c.WindowEnd = Clock{Hour: 20, set: true}
	}
}

func (p Probe) checkPorts() error {
	if err := checkPorts("probe.linux_ports", p.LinuxPorts); err != nil {
		return err
	}
	return checkPorts("probe.windows_ports", p.WindowsPorts)
}

func checkPorts(name string, ports []int) error {
	if len(ports) == 0 {
		return fmt.Errorf("%s 至少要有一个端口", name)
	}
	for _, port := range ports {
		if port < 1 || port > 65535 {
			return fmt.Errorf("%s 含有无效端口 %d", name, port)
		}
	}
	return nil
}

func (c Config) checkAliyunAccounts() error {
	accounts := c.Aliyun.ResolvedAccounts()
	if len(accounts) == 0 {
		return fmt.Errorf("缺少阿里云 AccessKey，请配置 aliyun.accounts，或 aliyun.access_key_id / access_key_secret")
	}
	names := map[string]struct{}{}
	for _, account := range accounts {
		if account.AccessKeyID == "" || account.AccessKeySecret == "" {
			if len(c.Aliyun.Accounts) == 0 {
				return fmt.Errorf("缺少阿里云 AccessKey，请配置 aliyun.access_key_id / access_key_secret 或环境变量 ALIYUN_ACCESS_KEY_ID / ALIYUN_ACCESS_KEY_SECRET，也可以改用 aliyun.accounts")
			}
			return fmt.Errorf("阿里云账号 %s 缺少 AccessKey", account.Label())
		}
		if len(accounts) > 1 && account.Name == "" {
			return fmt.Errorf("配置了多个阿里云账号时，每个账号都要填写 name")
		}
		if _, ok := names[account.Name]; ok {
			return fmt.Errorf("阿里云账号名称重复: %s", account.Label())
		}
		names[account.Name] = struct{}{}
	}
	return nil
}

func (c Config) Validate(dryRun bool) error {
	if err := c.checkAliyunAccounts(); err != nil {
		return err
	}
	if dryRun {
		return nil
	}
	if c.PasswordReset.On() {
		if err := resetpass.Check(c.PasswordReset.Password); err != nil {
			return fmt.Errorf("password_reset.password 无效: %w", err)
		}
	}
	if strings.TrimSpace(c.JumpServer.BaseURL) == "" || strings.TrimSpace(c.JumpServer.Token) == "" {
		return fmt.Errorf("缺少 JumpServer 地址或 Token，请配置 jumpserver.base_url / token，或改用 -dry-run 只生成清单")
	}
	if strings.TrimSpace(c.Feishu.Webhook) == "" {
		return fmt.Errorf("缺少飞书机器人 webhook，请配置 feishu.webhook 或环境变量 FEISHU_WEBHOOK")
	}
	parsed, err := url.Parse(c.Feishu.Webhook)
	if err != nil || (parsed.Scheme != "https" && parsed.Scheme != "http") || parsed.Host == "" {
		return fmt.Errorf("feishu.webhook 不是有效的 http(s) 地址")
	}
	return nil
}
