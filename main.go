// 坚祥数字助理（软著版）—— 单进程本地智能助手服务。
//
// 架构：Go 后端（LLM 主循环 + 工具集 + 会话存储）+ 原生 JS 前端，
// 前后端仅通过 HTTP / SSE 通信，不共享任何运行时模块。
package main

import (
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/exec"
	"time"

	"jxassistant/sandboxhelper"
)

func main() {
	// 本地 Linux 沙箱的自重入口：主进程以隐藏参数重启自身施加 Landlock
	// 限制，随后 exec 目标命令（Go 无法 fork，这是最干净的进程级限制路径）。
	if len(os.Args) > 1 && os.Args[1] == "__jx-sandbox-restrict" {
		os.Exit(sandboxhelper.Main(os.Args[2:]))
	}

	cfgPath := flag.String("config", "config.json", "配置文件路径")
	port := flag.Int("port", 0, "覆盖配置中的监听端口")
	noOpen := flag.Bool("noopen", false, "启动后不自动打开浏览器")
	webDir := flag.String("webdir", "", "开发模式：前端从该磁盘目录提供（默认用内嵌副本）")
	flag.Parse()

	cfg, err := LoadConfig(*cfgPath)
	if err != nil {
		log.Fatalf("加载配置失败: %v", err)
	}
	sandboxIntegrityLow = cfg.SandboxIntegrityLevel != "off"
	if *port > 0 {
		cfg.Port = *port
	}

	app, err := NewApp(cfg, *cfgPath, *webDir)
	if err != nil {
		log.Fatalf("初始化失败: %v", err)
	}

	host := cfg.BindHost
	if host == "" {
		host = "127.0.0.1"
	}
	url := fmt.Sprintf("http://%s:%d", host, cfg.Port)
	log.Printf("坚祥数字助理（软著版）已启动: %s", url)
	log.Printf("模型: %s @ %s（baseURL 填 mock 时为离线演示模式）", cfg.Model, cfg.BaseURL)
	if *webDir != "" {
		log.Printf("开发模式：前端从 %s 读取，修改后刷新浏览器即生效", *webDir)
	}

	if !*noOpen && (host == "127.0.0.1" || host == "localhost") {
		go func() {
			time.Sleep(400 * time.Millisecond)
			_ = exec.Command("rundll32", "url.dll,FileProtocolHandler", url).Start()
		}()
	}

	srv := &http.Server{Addr: fmt.Sprintf("%s:%d", host, cfg.Port), Handler: withCORS(app.Routes())}
	if err := srv.ListenAndServe(); err != nil {
		log.Fatalf("服务退出: %v", err)
	}
}
