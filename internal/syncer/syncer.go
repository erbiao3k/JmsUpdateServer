package syncer

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode"
	"unicode/utf8"

	"jmsupdateserver/internal/aliyun"
	"jmsupdateserver/internal/config"
	"jmsupdateserver/internal/feishu"
	"jmsupdateserver/internal/inventory"
	"jmsupdateserver/internal/jumpserver"
	"jmsupdateserver/internal/probe"
	"jmsupdateserver/internal/resetpass"
)

type Report struct {
	GeneratedAt time.Time          `json:"generated_at"`
	TagKey      string             `json:"tag_key"`
	Linux       []inventory.Server `json:"linux"`
	Windows     []inventory.Server `json:"windows"`
}

type Runner struct {
	Config config.Config
	DryRun bool
	Now    func() time.Time
}

func (r Runner) Run(ctx context.Context) error {
	if r.Now == nil {
		r.Now = time.Now
	}
	logger, err := newErrorLogger(r.Config.Output.ErrorLog)
	if err != nil {
		return err
	}
	defer logger.Close()
	bot := feishu.New(r.Config.Feishu.Webhook, r.Config.Feishu.Secret)

	accounts := r.Config.Aliyun.ResolvedAccounts()
	clouds := make(map[string]*aliyun.Client, len(accounts))
	var servers []inventory.Server
	var regionErrs []error
	for _, account := range accounts {
		cloud := aliyun.New(account.AccessKeyID, account.AccessKeySecret, account.BootstrapEndpoint)
		clouds[account.Name] = cloud
		found, errs := cloud.ListTagged(ctx, r.Config.Aliyun.TagKey, r.Config.Probe.RegionConcurrency, r.Config.Blacklist)
		for i := range found {
			found[i].Account = account.Name
		}
		servers = append(servers, found...)
		for _, err := range errs {
			regionErrs = append(regionErrs, fmt.Errorf("账号 %s: %w", account.Label(), err))
		}
	}
	for _, err := range regionErrs {
		logger.Printf("拉取 ECS 失败: %v", err)
		r.alert(ctx, bot, logger, "【JumpServer 同步】失败：拉取 ECS\n原因: "+err.Error())
	}
	if len(servers) == 0 && len(regionErrs) > 0 {
		return fmt.Errorf("没有拉到 ECS，且存在地域错误，详见 %s", r.Config.Output.ErrorLog)
	}
	if err := ctx.Err(); err != nil {
		return err
	}

	sortServers(servers)
	probeServers(ctx, servers, probe.TCP(r.Config.ProbeTimeout), r.Config.Probe)
	for _, server := range servers {
		if server.Probe.Error != "" {
			logger.Printf("%s", server.Probe.Error)
			r.alert(ctx, bot, logger, instanceAlert(server, "【JumpServer 同步】失败：端口探测", server.Probe.Error))
		}
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	r.skipNotRunning(ctx, servers, bot, logger)

	report := r.report(servers)
	if err := writeReport(r.Config.Output.Inventory, report); err != nil {
		return err
	}
	fmt.Printf("清单已写入 %s，Linux %d 台，Windows %d 台\n", r.Config.Output.Inventory, len(report.Linux), len(report.Windows))

	if r.DryRun {
		fmt.Println("dry-run：已跳过在线改密和写入 JumpServer")
		return nil
	}
	if r.Config.PasswordReset.On() {
		resetPasswords(ctx, clouds, servers, r.Config.PasswordReset.Password, r.Config.PasswordReset.Concurrency, r.Config.PasswordReset.CommandTimeout, logger, func(server inventory.Server, title, reason string) {
			r.alert(ctx, bot, logger, instanceAlert(server, title, reason))
		})
		if err := writeReport(r.Config.Output.Inventory, r.report(servers)); err != nil {
			return err
		}
		resetOK, resetFailed, skipped := countReset(servers)
		fmt.Printf("在线改密完成：成功 %d，失败 %d，跳过 %d\n", resetOK, resetFailed, skipped)
	} else {
		fmt.Println("已关闭在线改密，只同步主机")
	}

	jms := jumpserver.New(
		r.Config.JumpServer.BaseURL,
		r.Config.JumpServer.Token,
		r.Config.JumpServer.AuthScheme,
		r.Config.JumpServer.OrgID,
	)
	if err := r.syncJumpServer(ctx, jms, servers, logger, bot); err != nil {
		r.alert(ctx, bot, logger, "【JumpServer 同步】失败\n原因: "+err.Error())
		_ = writeReport(r.Config.Output.Inventory, r.report(servers))
		return err
	}
	if err := writeReport(r.Config.Output.Inventory, r.report(servers)); err != nil {
		return err
	}
	created, updated, failed := countSync(servers)
	_, resetFailed, skipped := countReset(servers)
	fmt.Printf("JumpServer 同步完成：新建 %d，更新 %d，失败 %d，未加入 %d\n", created, updated, failed, skipped)
	if resetFailed > 0 || failed > 0 || skipped > 0 || bot.Failures() > 0 {
		return fmt.Errorf("未加入 JumpServer %d 台，在线改密失败 %d 台，资产同步失败 %d 台，飞书通知失败 %d 次，详见 %s", skipped, resetFailed, failed, bot.Failures(), r.Config.Output.ErrorLog)
	}
	return nil
}

func (r Runner) syncJumpServer(ctx context.Context, jms *jumpserver.Client, servers []inventory.Server, logger *errorLogger, bot *feishu.Client) error {
	linux, err := jms.FindPlatform(ctx, r.Config.JumpServer.LinuxPlatformID, r.Config.JumpServer.LinuxPlatformName)
	if err != nil {
		return fmt.Errorf("查找 Linux 平台: %w", err)
	}
	windows, err := jms.FindPlatform(ctx, r.Config.JumpServer.WindowsPlatformID, r.Config.JumpServer.WindowsPlatformName)
	if err != nil {
		return fmt.Errorf("查找 Windows 平台: %w", err)
	}
	hosts, err := jms.ListHosts(ctx)
	if err != nil {
		return fmt.Errorf("读取 JumpServer 主机: %w", err)
	}
	index := newHostIndex(hosts)
	var linuxTemplate, windowsTemplate string
	if r.Config.PasswordReset.On() {
		var err error
		linuxTemplate, err = jms.EnsureAccountTemplate(ctx, "ecs-linux-root", "root", r.Config.PasswordReset.Password)
		if err != nil {
			return fmt.Errorf("准备 Linux 账号模板: %w", err)
		}
		windowsTemplate, err = jms.EnsureAccountTemplate(ctx, "ecs-windows-administrator", "Administrator", r.Config.PasswordReset.Password)
		if err != nil {
			return fmt.Errorf("准备 Windows 账号模板: %w", err)
		}
	}
	for i := range servers {
		server := &servers[i]
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if server.PasswordReset.SkipSync {
			server.JumpServer.Action = "skipped"
			continue
		}
		action, id, err := upsert(ctx, jms, index, *server, linux, windows, r.Config.JumpServer.NodesDisplay)
		server.JumpServer.Action = action
		server.JumpServer.ID = id
		if err != nil {
			server.JumpServer.Error = err.Error()
			logger.Printf("同步 JumpServer 失败 instance=%s name=%s: %v", server.InstanceID, server.DisplayName(), err)
			r.alert(ctx, bot, logger, instanceAlert(*server, "【JumpServer 同步】失败：写入主机", err.Error()))
			continue
		}
		if server.Password == "" {
			continue
		}
		templateID := linuxTemplate
		if server.OS == inventory.OSWindows {
			templateID = windowsTemplate
		}
		account, err := jms.LinkTemplateAccount(ctx, id, templateID, server.LoginUsername())
		server.JumpServer.Account = account
		if err != nil {
			server.JumpServer.Error = err.Error()
			logger.Printf("写入 JumpServer 账号失败 instance=%s name=%s user=%s: %v", server.InstanceID, server.DisplayName(), server.LoginUsername(), err)
			r.alert(ctx, bot, logger, instanceAlert(*server, "【JumpServer 同步】失败：写入账号", err.Error()))
		}
	}
	if err := r.grantAssets(ctx, jms, servers, logger, bot); err != nil {
		return err
	}
	return nil
}

func (r Runner) grantAssets(ctx context.Context, jms *jumpserver.Client, servers []inventory.Server, logger *errorLogger, bot *feishu.Client) error {
	granted := grantedServers(servers)
	if len(granted) == 0 {
		return nil
	}
	var problems []string
	adminIDs := make([]string, 0, len(granted))
	byValue := map[string][]string{}
	warned := map[string]struct{}{}
	for _, server := range granted {
		adminIDs = append(adminIDs, server.JumpServer.ID)
		value := strings.TrimSpace(server.TagValue)
		if value == "" || strings.EqualFold(value, "admin") {
			continue
		}
		if _, _, ok := namesForTagValue(value); !ok {
			if _, seen := warned[value]; seen {
				continue
			}
			warned[value] = struct{}{}
			msg := "标签值 " + value + " 不能作为授权和用户组名称，这些机器只加入管理员组"
			logger.Printf("%s", msg)
			r.alert(ctx, bot, logger, "【JumpServer 同步】标签值未单独授权\n"+msg)
			continue
		}
		byValue[value] = append(byValue[value], server.JumpServer.ID)
	}
	if err := jms.EnsureAssetPermission(ctx, adminPermissionName, adminUserGroup, allPermissionActions, adminIDs); err != nil {
		logger.Printf("资产授权失败 name=%s group=%s: %v", adminPermissionName, adminUserGroup, err)
		r.alert(ctx, bot, logger, "【JumpServer 同步】失败：资产授权 "+adminPermissionName+"\n原因: "+err.Error())
		problems = append(problems, err.Error())
	}
	values := make([]string, 0, len(byValue))
	for value := range byValue {
		values = append(values, value)
	}
	sort.Strings(values)
	for _, value := range values {
		permission, group, _ := namesForTagValue(value)
		err := jms.EnsureAssetPermission(ctx, permission, group, allPermissionActions, byValue[value])
		if err != nil {
			logger.Printf("资产授权失败 name=%s group=%s: %v", permission, group, err)
			r.alert(ctx, bot, logger, "【JumpServer 同步】失败：资产授权 "+permission+"\n原因: "+err.Error())
			problems = append(problems, err.Error())
		}
	}
	if len(problems) > 0 {
		return fmt.Errorf("准备资产授权: %s", strings.Join(problems, "; "))
	}
	return nil
}

const (
	adminPermissionName = "asset_permissions_admin"
	adminUserGroup      = "user_group_admin"
)

var allPermissionActions = []string{"connect", "upload", "download", "copy", "paste", "delete", "share"}

func namesForTagValue(tagValue string) (permission, group string, ok bool) {
	value := strings.TrimSpace(tagValue)
	if value == "" || strings.EqualFold(value, "admin") || !validPermissionValue(value) {
		return "", "", false
	}
	permission = "asset_permissions_" + value
	group = "user_group_" + value
	if utf8.RuneCountInString(permission) > 128 || utf8.RuneCountInString(group) > 128 {
		return "", "", false
	}
	return permission, group, true
}

func validPermissionValue(value string) bool {
	if value == "" {
		return false
	}
	for _, r := range value {
		if r == '_' || r == '-' || unicode.IsLetter(r) || unicode.IsDigit(r) {
			continue
		}
		return false
	}
	return true
}

func grantedServers(servers []inventory.Server) []inventory.Server {
	out := make([]inventory.Server, 0, len(servers))
	for _, server := range servers {
		if server.JumpServer.ID == "" {
			continue
		}
		if server.JumpServer.Action != "created" && server.JumpServer.Action != "updated" {
			continue
		}
		out = append(out, server)
	}
	return out
}

type assetAPI interface {
	CreateHost(context.Context, jumpserver.Payload) (jumpserver.Host, error)
	UpdateHost(context.Context, string, jumpserver.Payload) (jumpserver.Host, error)
}

func upsert(
	ctx context.Context,
	jms assetAPI,
	index *hostIndex,
	server inventory.Server,
	linux, windows jumpserver.Platform,
	nodes []string,
) (string, string, error) {
	address := server.ConnectAddress()
	if address == "" {
		return "skipped", "", fmt.Errorf("没有内网或外网地址，无法写入 JumpServer")
	}
	name := limitName(server.DisplayName())
	platform := linux
	if server.OS == inventory.OSWindows {
		platform = windows
	}
	existing, ok := index.find(server)
	labels := jumpserver.MergeLabels(nil, managedLabels(server))
	comment, writeComment := jumpserver.ManagedComment("", server)
	if ok {
		labels = jumpserver.MergeLabels(existing.Labels, managedLabels(server))
		comment, writeComment = jumpserver.ManagedComment(existing.Comment, server)
	}
	if missed := jumpserver.UnsafeLabelIPs(server); len(missed) > 0 && !writeComment {
		return "skipped", existing.ID, fmt.Errorf("%s 含有 JumpServer 标签不允许的冒号或逗号，且资产备注已有人工内容，无法写入该地址", strings.Join(missed, "、"))
	}

	payload := jumpserverPayload(name, address, platform, server, labels, comment, writeComment)
	if !ok {
		active := true
		payload.IsActive = &active
		if len(nodes) > 0 {
			payload.NodesDisplay = nodes
		}
		if other, used := index.byName[name]; used && other.ID != "" {
			return "skipped", "", fmt.Errorf("JumpServer 已有同名资产 %s，名称在组织内必须唯一", name)
		}
		created, err := jms.CreateHost(ctx, payload)
		if err != nil {
			return "skipped", "", err
		}
		index.add(created)
		return "created", created.ID, nil
	}

	if other, used := index.byName[name]; used && other.ID != existing.ID {
		return "skipped", existing.ID, fmt.Errorf("JumpServer 已有同名资产 %s，无法把 %s 的名称覆盖为 ECS 名称", name, existing.Name)
	}
	updated, err := jms.UpdateHost(ctx, existing.ID, payload)
	if err != nil {
		return "skipped", existing.ID, err
	}
	index.replace(existing, updated)
	return "updated", updated.ID, nil
}

func jumpserverPayload(name, address string, platform jumpserver.Platform, server inventory.Server, labels []jumpserver.Label, comment string, writeComment bool) jumpserver.Payload {
	payload := jumpserver.Payload{
		Name:      name,
		Address:   address,
		Platform:  jumpserver.PlatformID{ID: platform.ID},
		Protocols: jumpserver.BuildProtocols(platform.Protocols, server.OS, server.ProtocolPort()),
		Labels:    labels,
	}
	if writeComment {
		payload.Comment = comment
	}
	return payload
}

func managedLabels(server inventory.Server) map[string]string {
	return map[string]string{
		inventory.LabelInstanceID: server.InstanceID,
		inventory.LabelRegion:     server.RegionID,
		inventory.LabelPrivateIP:  server.PrivateIP,
		inventory.LabelPublicIP:   server.PublicIP,
	}
}

func limitName(name string) string {
	if utf8.RuneCountInString(name) <= 128 {
		return name
	}
	runes := []rune(name)
	return string(runes[:128])
}

type hostIndex struct {
	byInstance map[string]jumpserver.Host
	byAddress  map[string][]jumpserver.Host
	byName     map[string]jumpserver.Host
}

func newHostIndex(hosts []jumpserver.Host) *hostIndex {
	index := &hostIndex{
		byInstance: map[string]jumpserver.Host{},
		byAddress:  map[string][]jumpserver.Host{},
		byName:     map[string]jumpserver.Host{},
	}
	for _, host := range hosts {
		index.add(host)
	}
	return index
}

func (h *hostIndex) add(host jumpserver.Host) {
	if host.Name != "" {
		h.byName[host.Name] = host
	}
	if host.Address != "" {
		h.byAddress[host.Address] = append(h.byAddress[host.Address], host)
	}
	for _, label := range host.Labels {
		if label.Name == inventory.LabelInstanceID && label.Value != "" {
			h.byInstance[label.Value] = host
		}
	}
}

func (h *hostIndex) replace(old, host jumpserver.Host) {
	if old.Name != "" && h.byName[old.Name].ID == old.ID {
		delete(h.byName, old.Name)
	}
	h.removeAddress(old)
	for _, label := range old.Labels {
		if label.Name == inventory.LabelInstanceID && h.byInstance[label.Value].ID == old.ID {
			delete(h.byInstance, label.Value)
		}
	}
	h.add(host)
}

func (h *hostIndex) removeAddress(host jumpserver.Host) {
	list := h.byAddress[host.Address]
	kept := list[:0]
	for _, item := range list {
		if item.ID != host.ID {
			kept = append(kept, item)
		}
	}
	if len(kept) == 0 {
		delete(h.byAddress, host.Address)
		return
	}
	h.byAddress[host.Address] = kept
}

func (h *hostIndex) find(server inventory.Server) (jumpserver.Host, bool) {
	if host, ok := h.byInstance[server.InstanceID]; ok {
		return host, true
	}
	for _, address := range []string{server.PrivateIP, server.PublicIP} {
		if address == "" {
			continue
		}
		var matches []jumpserver.Host
		for _, host := range h.byAddress[address] {
			if labeledInstance(host) != "" && labeledInstance(host) != server.InstanceID {
				continue
			}
			matches = append(matches, host)
		}
		if len(matches) == 1 {
			return matches[0], true
		}
	}
	return jumpserver.Host{}, false
}

func labeledInstance(host jumpserver.Host) string {
	for _, label := range host.Labels {
		if label.Name == inventory.LabelInstanceID {
			return label.Value
		}
	}
	return ""
}

func resetPasswords(ctx context.Context, clouds map[string]*aliyun.Client, servers []inventory.Server, password string, concurrency int, commandTimeout time.Duration, logger *errorLogger, notify func(inventory.Server, string, string)) {
	if concurrency <= 0 {
		concurrency = 8
	}
	sem := make(chan struct{}, concurrency)
	var wg sync.WaitGroup
	for i := range servers {
		if ctx.Err() != nil {
			servers[i].PasswordReset.Error = ctx.Err().Error()
			continue
		}
		if servers[i].PasswordReset.SkipSync {
			continue
		}
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			cloud := clouds[servers[i].Account]
			var err error
			if cloud == nil {
				err = fmt.Errorf("找不到账号 %s 的 AccessKey", servers[i].Account)
			} else {
				err = cloud.ResetLoginPassword(ctx, servers[i], password, commandTimeout)
			}
			if err != nil {
				servers[i].PasswordReset.Error = err.Error()
				logger.Printf("在线改密失败 instance=%s name=%s user=%s: %v", servers[i].InstanceID, servers[i].DisplayName(), servers[i].LoginUsername(), err)
				title := "【JumpServer 同步】失败：在线改密"
				switch aliyun.BlockReason(err) {
				case aliyun.BlockNotRunning:
					servers[i].PasswordReset.SkipSync = true
					title = "【JumpServer 同步】未加入：实例不是 Running"
				case aliyun.BlockAssistantOffline:
					servers[i].PasswordReset.SkipSync = true
					title = "【JumpServer 同步】未加入：云助手不在线"
				}
				notify(servers[i], title, err.Error())
				return
			}
			servers[i].Password = password
			servers[i].PasswordReset.OK = true
			servers[i].PasswordReset.Username = servers[i].LoginUsername()
		}(i)
	}
	wg.Wait()
}

func probeServers(ctx context.Context, servers []inventory.Server, dial probe.DialFunc, cfg config.Probe) {
	concurrency := cfg.Concurrency
	if concurrency <= 0 {
		concurrency = 32
	}
	sem := make(chan struct{}, concurrency)
	var wg sync.WaitGroup
	for i := range servers {
		if ctx.Err() != nil {
			servers[i].Probe.Error = ctx.Err().Error()
			continue
		}
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			servers[i].Probe = probe.Check(servers[i], cfg.PortsFor(servers[i].OS), dial)
		}(i)
	}
	wg.Wait()
}

func sortServers(servers []inventory.Server) {
	sort.Slice(servers, func(i, j int) bool {
		if servers[i].Account != servers[j].Account {
			return servers[i].Account < servers[j].Account
		}
		if servers[i].RegionID != servers[j].RegionID {
			return servers[i].RegionID < servers[j].RegionID
		}
		if servers[i].Name != servers[j].Name {
			return servers[i].Name < servers[j].Name
		}
		return servers[i].InstanceID < servers[j].InstanceID
	})
}

func (r Runner) report(servers []inventory.Server) Report {
	report := Report{
		GeneratedAt: r.Now(),
		TagKey:      r.Config.Aliyun.TagKey,
		Linux:       []inventory.Server{},
		Windows:     []inventory.Server{},
	}
	for _, server := range servers {
		if server.OS == inventory.OSWindows {
			report.Windows = append(report.Windows, server)
			continue
		}
		report.Linux = append(report.Linux, server)
	}
	return report
}

func writeReport(path string, report Report) error {
	if dir := filepath.Dir(path); dir != "." && dir != "" {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return err
		}
	}
	raw, err := json.MarshalIndent(report, "", "  ")
	if err != nil {
		return err
	}
	raw = append(raw, '\n')
	return os.WriteFile(path, raw, 0o644)
}

func countSync(servers []inventory.Server) (created, updated, failed int) {
	for _, server := range servers {
		switch server.JumpServer.Action {
		case "created":
			created++
		case "updated":
			updated++
		}
		if server.JumpServer.Error != "" {
			failed++
		}
	}
	return created, updated, failed
}

func countReset(servers []inventory.Server) (ok, failed, skipped int) {
	for _, server := range servers {
		switch {
		case server.PasswordReset.SkipSync:
			skipped++
		case server.PasswordReset.OK:
			ok++
		case server.PasswordReset.Error != "":
			failed++
		}
	}
	return ok, failed, skipped
}

func (r Runner) skipNotRunning(ctx context.Context, servers []inventory.Server, bot *feishu.Client, logger *errorLogger) {
	for i := range servers {
		if servers[i].PasswordReset.SkipSync || strings.EqualFold(servers[i].Status, "Running") {
			continue
		}
		status := servers[i].Status
		if status == "" {
			status = "未知"
		}
		reason := fmt.Sprintf("实例状态为 %s，不是 Running", status)
		servers[i].PasswordReset.SkipSync = true
		if servers[i].PasswordReset.Error == "" {
			servers[i].PasswordReset.Error = reason
		}
		r.alert(ctx, bot, logger, instanceAlert(servers[i], "【JumpServer 同步】未加入：实例不是 Running", reason))
	}
}

func (r Runner) alert(ctx context.Context, bot *feishu.Client, logger *errorLogger, text string) {
	text = resetpass.Redact(text, r.Config.PasswordReset.Password)
	if err := bot.Notify(ctx, text); err != nil {
		logger.Printf("飞书通知失败: %v", err)
	}
}

func instanceAlert(server inventory.Server, title, reason string) string {
	account := server.Account
	if account == "" {
		account = "默认账号"
	}
	return fmt.Sprintf("%s\n账号: %s\n实例: %s\n名称: %s\n地域: %s\n系统: %s\n状态: %s\n原因: %s",
		title,
		account,
		server.InstanceID,
		server.DisplayName(),
		server.RegionID,
		server.OS,
		server.Status,
		reason,
	)
}

type errorLogger struct {
	file *os.File
	log  *log.Logger
}

func newErrorLogger(path string) (*errorLogger, error) {
	if dir := filepath.Dir(path); dir != "." && dir != "" {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return nil, err
		}
	}
	file, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		return nil, fmt.Errorf("打开错误日志 %s: %w", path, err)
	}
	return &errorLogger{
		file: file,
		log:  log.New(file, "", log.LstdFlags),
	}, nil
}

func (l *errorLogger) Printf(format string, args ...any) {
	l.log.Printf(format, args...)
}

func (l *errorLogger) Close() {
	_ = l.file.Close()
}
