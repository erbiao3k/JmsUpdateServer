package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"syscall"
	"time"

	"jmsupdateserver/internal/config"
	"jmsupdateserver/internal/syncer"
)

func main() {
	if len(os.Args) < 2 || isHelp(os.Args[1]) {
		usage()
		return
	}
	if os.Args[1] != "start" {
		usage()
		os.Exit(2)
	}

	fs := flag.NewFlagSet("start", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	configPath := fs.String("config", "config.yaml", "配置文件路径")
	dryRun := fs.Bool("dry-run", false, "只扫描、分类、探测并写清单，不写入 JumpServer")
	if err := fs.Parse(os.Args[2:]); err != nil {
		usage()
		os.Exit(2)
	}

	cfg, err := load(*configPath, *dryRun)
	if err != nil {
		fmt.Fprintf(os.Stderr, "%v\n", err)
		os.Exit(1)
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	var announced time.Time
	round := 0
	for {
		if ctx.Err() != nil {
			fmt.Println("已停止")
			return
		}
		now := time.Now()
		if !cfg.InWindow(now) {
			next := cfg.NextStart(now)
			if !next.Equal(announced) {
				fmt.Printf("当前不在执行时间段 %s-%s（本机时间），%s 开始\n", cfg.WindowStart, cfg.WindowEnd, next.Format("2006-01-02 15:04"))
				announced = next
			}
			if !wait(ctx, nap(now, next, cfg.Every)) {
				fmt.Println("已停止")
				return
			}
			cfg = reload(*configPath, *dryRun, cfg)
			continue
		}

		announced = time.Time{}
		round++
		fmt.Printf("第 %d 轮开始，周期 %s，时间段 %s-%s\n", round, cfg.Every, cfg.WindowStart, cfg.WindowEnd)
		err = syncer.Runner{Config: cfg, DryRun: *dryRun}.Run(ctx)
		if ctx.Err() != nil {
			fmt.Println("已停止")
			return
		}
		if err != nil {
			fmt.Fprintf(os.Stderr, "第 %d 轮失败: %v\n", round, err)
		} else {
			fmt.Printf("第 %d 轮完成\n", round)
		}

		wake := time.Now().Add(cfg.Every)
		if !cfg.InWindow(wake) {
			wake = cfg.NextStart(time.Now())
		}
		fmt.Printf("下一轮 %s\n", wake.Format("2006-01-02 15:04"))
		announced = wake
		if !wait(ctx, nap(time.Now(), wake, cfg.Every)) {
			fmt.Println("已停止")
			return
		}
		cfg = reload(*configPath, *dryRun, cfg)
	}
}

func isHelp(arg string) bool {
	return arg == "-h" || arg == "--help" || arg == "help"
}

func usage() {
	fmt.Fprintf(os.Stderr, `把带配置标签的阿里云 ECS 同步到 JumpServer。

用法:
  sync start [-config config.yaml] [-dry-run]

不带参数时只显示本说明。加上 start 才会按配置循环执行。

  -config   配置文件路径，默认 config.yaml
  -dry-run  只扫描、探测并写清单，不改密、不写入 JumpServer
`)
}

func load(path string, dryRun bool) (config.Config, error) {
	cfg, err := config.Load(path)
	if err != nil {
		return config.Config{}, err
	}
	if err := cfg.Validate(dryRun); err != nil {
		return config.Config{}, err
	}
	return cfg, nil
}

func reload(path string, dryRun bool, current config.Config) config.Config {
	next, err := load(path, dryRun)
	if err != nil {
		fmt.Fprintf(os.Stderr, "重新读取配置失败，继续使用上一轮配置: %v\n", err)
		return current
	}
	return next
}

func nap(now, target time.Time, limit time.Duration) time.Duration {
	delay := target.Sub(now)
	if delay > limit {
		delay = limit
	}
	if delay < time.Second {
		return time.Second
	}
	return delay
}

func wait(ctx context.Context, interval time.Duration) bool {
	timer := time.NewTimer(interval)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
		return true
	}
}
