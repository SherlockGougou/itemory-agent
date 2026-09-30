// Command itemory-agent is the self-hosted private cloud service for Itemory.
//
// Subcommands:
//
//	serve        run the HTTP API + scanner (default)
//	healthcheck  probe the local HTTP API (used by Docker HEALTHCHECK)
//	benchmark    measure per-file probe/thumbnail cost on a sample directory
//	version      print version
package main

import (
	"context"
	"flag"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/SherlockGougou/itemory-agent/internal/api"
	"github.com/SherlockGougou/itemory-agent/internal/benchmark"
	"github.com/SherlockGougou/itemory-agent/internal/config"
	"github.com/SherlockGougou/itemory-agent/internal/events"
	"github.com/SherlockGougou/itemory-agent/internal/logging"
	"github.com/SherlockGougou/itemory-agent/internal/scan"
	"github.com/SherlockGougou/itemory-agent/internal/store"
	"github.com/SherlockGougou/itemory-agent/internal/thumbs"
)

const (
	apiVersion = 1
)

// version 由构建注入（-ldflags "-X main.version=<tag>"），默认值只用于本地 go run。
// 之前是常量且不注入，导致镜像 tag 与 /api/v1/health 报的版本漂移，App 无法判断线上版本。
var version = "dev"

func main() {
	args := os.Args[1:]
	if len(args) > 0 {
		switch args[0] {
		case "healthcheck":
			os.Exit(runHealthcheck())
		case "benchmark":
			os.Exit(runBenchmark(args[1:]))
		case "version":
			fmt.Println(version)
			return
		case "help", "-h", "--help":
			usage()
			return
		case "serve":
			// fall through to serve
		default:
			fmt.Fprintf(os.Stderr, "unknown command %q\n\n", args[0])
			usage()
			os.Exit(2)
		}
	}
	os.Exit(runServe())
}

func usage() {
	fmt.Fprint(os.Stderr, `itemory-agent - Itemory Private Cloud service

usage:
  itemory-agent [serve]        run HTTP API + scanner (default)
  itemory-agent healthcheck    probe local health endpoint
  itemory-agent benchmark      measure per-file cost (-dir, -sample, -json)
  itemory-agent version        print version

environment:
  ITEMORY_CACHE_DIR   data directory (default ./data)
  ITEMORY_HTTP_ADDR   listen address (default :8787)
  administrator account is configured from the dashboard on first launch
  ITEMORY_PRESET      light | balanced | performance (first run only)
`)
}

func runServe() int {
	dataDir := envOr("ITEMORY_CACHE_DIR", "./data")
	if err := os.MkdirAll(dataDir, 0o755); err != nil {
		fmt.Fprintf(os.Stderr, "cannot create data dir %s: %v\n", dataDir, err)
		return 1
	}

	settings, err := config.Load(dataDir)
	if err != nil {
		fmt.Fprintf(os.Stderr, "cannot load settings: %v\n", err)
		return 1
	}
	ring := logging.NewRing(2000)
	logger := logging.New(settings.Get().LogLevel, ring)
	// 设置里改 logLevel 之后立即生效，不必重启容器。Settings 的字段注释
	// 一直写着「every field is hot-applicable」，但在此之前 logLevel 是例外：
	// logger 只在启动时按当时的取值建过一次。这也是 Manager.Subscribe 的第一次真正使用。
	settings.Subscribe(func(next config.Settings) {
		logger.SetLevel(next.LogLevel)
	})

	index, err := store.Open(filepath.Join(dataDir, "index.sqlite"))
	if err != nil {
		logger.Error("open index failed", "error", err)
		return 1
	}
	defer index.Close()
	// 启动即修复历史索引：0.3.3 之前把动态片段的同内容副本当作独立视频，
	// 一条 UPDATE 收敛，换来不必重扫全库（增量扫描也不会重算未变化文件）。
	if absorbed, err := index.AbsorbMotionClipCopies(); err != nil {
		logger.Warn("absorb motion clip copies failed", "error", err)
	} else if absorbed > 0 {
		logger.Info("motion clip copies absorbed", "count", absorbed)
	}

	hub := events.NewHub()
	thumbMgr := thumbs.New(filepath.Join(dataDir, "thumbs"), settings, logger)
	scanner := scan.New(index, settings, hub, logger)

	tokens, err := api.LoadTokens(dataDir)
	if err != nil {
		logger.Error("load tokens failed", "error", err)
		return 1
	}
	// 服务启动后保持配对窗口关闭，只有管理员登录 Dashboard 并主动点击
	//「开始配对」时才生成一次性二维码，避免部署完成后局域网请求直接抢先配对。
	tokens.ClearClaim()

	server := api.NewServer(api.Deps{
		Version:    version,
		APIVersion: apiVersion,
		DataDir:    dataDir,
		Index:      index,
		Settings:   settings,
		Thumbs:     thumbMgr,
		Scanner:    scanner,
		Hub:        hub,
		Tokens:     tokens,
		Logger:     logger,
		LogRing:    ring,
		StartedAt:  time.Now(),
	})

	addr := envOr("ITEMORY_HTTP_ADDR", ":8787")
	httpServer := &http.Server{
		Addr:              addr,
		Handler:           server.Handler(),
		ReadHeaderTimeout: 10 * time.Second,
	}
	go func() {
		logger.Info("http server listening", "addr", addr, "version", version,
			"dashboard", dashboardURL(addr))
		if err := httpServer.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			logger.Error("http server failed", "error", err)
		}
	}()

	// First run: index empty but libraries configured → start a scan in background.
	if stats, err := index.Stats(); err == nil && stats.Entries == 0 && len(settings.Get().Libraries) > 0 {
		go func() {
			if err := scanner.Scan("incremental"); err != nil {
				logger.Warn("initial scan failed", "error", err)
			}
		}()
	}

	stopSchedule := make(chan struct{})
	go runScheduler(settings, scanner, logger, stopSchedule)

	signals := make(chan os.Signal, 1)
	signal.Notify(signals, syscall.SIGINT, syscall.SIGTERM)
	sig := <-signals
	logger.Info("shutting down", "signal", sig.String())
	close(stopSchedule)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	_ = httpServer.Shutdown(ctx)
	return 0
}

// runScheduler triggers one incremental scan per day at the configured HH:MM.
func runScheduler(settings *config.Manager, scanner *scan.Scanner, logger *logging.Logger, stop <-chan struct{}) {
	ticker := time.NewTicker(time.Minute)
	defer ticker.Stop()
	lastRun := ""
	for {
		select {
		case <-stop:
			return
		case now := <-ticker.C:
			schedule := settings.Get().ScanSchedule
			if schedule == "" {
				continue
			}
			if now.Format("15:04") != schedule {
				continue
			}
			today := now.Format("2006-01-02")
			if lastRun == today {
				continue
			}
			lastRun = today
			logger.Info("scheduled scan starting")
			go func() {
				if err := scanner.Scan("incremental"); err != nil {
					logger.Warn("scheduled scan failed", "error", err)
				}
			}()
		}
	}
}

func runHealthcheck() int {
	addr := envOr("ITEMORY_HTTP_ADDR", ":8787")
	if strings.HasPrefix(addr, ":") {
		addr = "127.0.0.1" + addr
	}
	client := &http.Client{Timeout: 3 * time.Second}
	resp, err := client.Get("http://" + addr + "/api/v1/health")
	if err != nil {
		fmt.Fprintf(os.Stderr, "healthcheck failed: %v\n", err)
		return 1
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		fmt.Fprintf(os.Stderr, "healthcheck status %d\n", resp.StatusCode)
		return 1
	}
	return 0
}

func runBenchmark(args []string) int {
	fs := flag.NewFlagSet("benchmark", flag.ExitOnError)
	dir := fs.String("dir", "", "directory to sample (required)")
	sample := fs.Int("sample", 200, "number of media files to sample")
	asJSON := fs.Bool("json", false, "print JSON report")
	_ = fs.Parse(args)
	if *dir == "" {
		fmt.Fprintln(os.Stderr, "benchmark: -dir is required")
		return 2
	}
	report, err := benchmark.Run(*dir, *sample)
	if err != nil {
		fmt.Fprintf(os.Stderr, "benchmark failed: %v\n", err)
		return 1
	}
	if *asJSON {
		fmt.Println(report.JSON())
	} else {
		fmt.Println(report.Text())
	}
	return 0
}

func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

// dashboardURL turns a listen address into a local URL for the startup log.
func dashboardURL(addr string) string {
	if strings.HasPrefix(addr, ":") {
		return "http://localhost" + addr + "/"
	}
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		return ""
	}
	if host == "" || host == "0.0.0.0" || host == "::" {
		host = "localhost"
	}
	return "http://" + net.JoinHostPort(host, port) + "/"
}
