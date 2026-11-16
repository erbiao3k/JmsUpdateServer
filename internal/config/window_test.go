package config

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestInWindowAndNextStart(t *testing.T) {
	cfg := Config{
		WindowStart: Clock{Hour: 9, set: true},
		WindowEnd:   Clock{Hour: 20, set: true},
	}
	day := time.Date(2026, 11, 12, 0, 0, 0, 0, time.Local)
	at := func(hour, minute int) time.Time {
		return time.Date(day.Year(), day.Month(), day.Day(), hour, minute, 0, 0, time.Local)
	}
	if !cfg.InWindow(at(9, 0)) || !cfg.InWindow(at(19, 59)) {
		t.Fatal("9:00 和 19:59 应在时间段内")
	}
	if cfg.InWindow(at(8, 59)) || cfg.InWindow(at(20, 0)) {
		t.Fatal("8:59 和 20:00 应在时间段外")
	}
	if next := cfg.NextStart(at(8, 0)); next.Day() != 12 || next.Hour() != 9 {
		t.Fatalf("上午应等到当天 09:00，得到 %s", next)
	}
	if next := cfg.NextStart(at(20, 0)); next.Day() != 13 || next.Hour() != 9 {
		t.Fatalf("20:00 应等到次日 09:00，得到 %s", next)
	}
}

func TestLoadWindowFromYAML(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	raw := []byte("interval: 5m\nwindow_start: 09:00\nwindow_end: \"20:00\"\n")
	if err := os.WriteFile(path, raw, 0o644); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.WindowStart.String() != "09:00" || cfg.WindowEnd.String() != "20:00" || cfg.Every != 5*time.Minute {
		t.Fatalf("start=%s end=%s every=%s", cfg.WindowStart, cfg.WindowEnd, cfg.Every)
	}
}

func TestProbePortsDefaultAndReject(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	raw := []byte("aliyun:\n  access_key_id: id\n  access_key_secret: secret\n")
	if err := os.WriteFile(path, raw, 0o644); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Trim(fmt.Sprint(cfg.Probe.PortsFor("linux")), "[]") != "22 2221" {
		t.Fatalf("linux ports = %v", cfg.Probe.LinuxPorts)
	}
	if strings.Trim(fmt.Sprint(cfg.Probe.PortsFor("windows")), "[]") != "3389" {
		t.Fatalf("windows ports = %v", cfg.Probe.WindowsPorts)
	}

	raw = []byte("probe:\n  linux_ports: [0]\n")
	if err := os.WriteFile(path, raw, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(path); err == nil {
		t.Fatal("无效端口应拒绝")
	}
}

func TestBlacklistDropsBlankIDs(t *testing.T) {
	cfg := Config{Blacklist: []string{"", " i-1 ", "i-1", "i-2"}}
	cfg.applyDefaults()
	if len(cfg.Blacklist) != 2 || cfg.Blacklist[0] != "i-1" || cfg.Blacklist[1] != "i-2" {
		t.Fatalf("blacklist = %#v", cfg.Blacklist)
	}
}

func TestAliyunAccounts(t *testing.T) {
	single := Config{Aliyun: Aliyun{AccessKeyID: "id", AccessKeySecret: "secret"}}
	single.applyDefaults()
	if accounts := single.Aliyun.ResolvedAccounts(); len(accounts) != 1 || accounts[0].Name != "" || accounts[0].BootstrapEndpoint == "" {
		t.Fatalf("single = %#v", accounts)
	}
	if err := single.checkAliyunAccounts(); err != nil {
		t.Fatal(err)
	}

	many := Config{Aliyun: Aliyun{Accounts: []AliyunAccount{
		{Name: "a", AccessKeyID: "id-a", AccessKeySecret: "secret-a"},
		{Name: "b", AccessKeyID: "id-b", AccessKeySecret: "secret-b"},
	}}}
	many.applyDefaults()
	if err := many.checkAliyunAccounts(); err != nil {
		t.Fatal(err)
	}
	many.Aliyun.Accounts[1].Name = "a"
	if err := many.checkAliyunAccounts(); err == nil {
		t.Fatal("重复账号名称应拒绝")
	}
}
