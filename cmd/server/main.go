package main

import (
	"flag"
	"fmt"
	"os"
	"os/signal"
	"runtime"
	"syscall"
	"time"

	"edgetunnel-lb/pkg/config"
	"edgetunnel-lb/pkg/health"
	"edgetunnel-lb/pkg/logger"
	"edgetunnel-lb/pkg/model"
	"edgetunnel-lb/pkg/notify"
	"edgetunnel-lb/pkg/prefer"
	"edgetunnel-lb/pkg/proxy"
	"edgetunnel-lb/pkg/quota"
	"edgetunnel-lb/pkg/web"
)

func main() {
	configPath := flag.String("config", "config.json", "配置文件路径")
	flag.Parse()

	metrics := &model.Metrics{
		StartTime: time.Now().Unix(),
	}

	cfgMgr, err := config.InitConfig(*configPath)
	if err != nil {
		fmt.Printf("无法初始化配置: %v\n", err)
		os.Exit(1)
	}

	cfg := cfgMgr.Get()

	// 1. 初始化各基础组件
	_ = notify.InitTelegram(cfgMgr, metrics)
	prefMgr := prefer.InitPreferManager(cfgMgr)
	healthMgr := health.InitHealthManager(cfgMgr, metrics)
	quotaMgr := quota.InitQuotaManager(cfgMgr)

	// 2. 打印启动横幅
	printBanner(cfg)

	// 3. 启动三大服务
	wsGW := proxy.NewWSGateway(cfgMgr, healthMgr, prefMgr, quotaMgr, metrics)
	if err := wsGW.Start(); err != nil {
		logger.Errorf("启动 WebSocket 网关失败 (端口 %d): %v", cfg.HTTPPort, err)
	}

	xhttpGW := proxy.NewXHTTPGateway(cfgMgr, healthMgr, prefMgr, quotaMgr, metrics)
	if err := xhttpGW.Start(); err != nil {
		logger.Errorf("启动 XHTTP 双模网关失败 (端口 %d): %v", cfg.H2CPort, err)
	}

	webServer := web.NewWebServer(cfgMgr, healthMgr, prefMgr, quotaMgr, metrics)
	if err := webServer.Start(); err != nil {
		logger.Errorf("启动 Web 控制台失败 (端口 %d): %v", cfg.WebPort, err)
	}

	if cfg.Telegram.BotToken != "" {
		logger.Infof("📢 Telegram 告警通知: 已启用")
	} else {
		logger.Infof("📢 Telegram 告警通知: 未配置 (已跳过)")
	}

	// 4. 监听退出信号
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, os.Interrupt, syscall.SIGTERM)

	sig := <-sigCh
	logger.Infof("收到退出信号 (%s)，正在安全停止网关...", sig)

	_ = wsGW.Close()
	_ = xhttpGW.Close()
	_ = webServer.Close()

	logger.Infof("EdgeTunnel 分流网关已完全退出。")
}

func printBanner(cfg model.Config) {
	fmt.Println("============================================================")
	fmt.Printf("🚀 EdgeTunnel 本地分流网关 [Go 高性能版] (PID: %d)\n", os.Getpid())
	fmt.Printf("   💻 CPU 核心调度: %d 逻辑核心并发 (原生 Goroutine 并发调度)\n", runtime.NumCPU())
	fmt.Printf("   🌐 8787 端口: WebSocket Upgrade 网关 (Pages 后端: %d 个)\n", len(cfg.PagesBackends))
	fmt.Printf("   ⚡ 8788 端口: XHTTP 嗅探双模网关 (Worker 后端: %d 个)\n", len(cfg.WorkerBackends))
	fmt.Printf("   🖥️ 8789 端口: Web 控制台与 REST API (http://0.0.0.0:%d)\n", cfg.WebPort)
	fmt.Printf("   🎯 默认出站优选: %s\n", cfg.DefaultPrefer)
	fmt.Println("============================================================")
}
