package main

import (
	"embed"

	"agentpack/internal/config"
	"agentpack/internal/lockfile"
	"log"
	"os"

	"github.com/wailsapp/wails/v3/pkg/application"
	"github.com/wailsapp/wails/v3/pkg/events"
)

//go:embed all:frontend/dist
var assets embed.FS

func main() {
	cfg := config.Load()

	releaseLock := func() {}
	if !isDevMode() {
		lock, lockErr := lockfile.TryAcquire(config.AgentPackDir())
		if lockErr != nil {
			log.Printf("AgentPack: %v", lockErr)
			os.Exit(1)
		}
		released := false
		releaseLock = func() {
			if released {
				return
			}
			lock.Release()
			released = true
		}
		defer releaseLock()
	}

	app := NewApp(cfg)

	// v3 alpha 无运行时 SetTheme API，使用 SystemDefault 让标题栏跟随系统主题。
	// 应用内主题切换（light/dark）仅影响 CSS，不改变原生标题栏。
	winTheme := application.SystemDefault
	macAppearance := application.DefaultAppearance

	wailsApp := application.New(application.Options{
		Name:        "AgentPack",
		Description: "Unified MCP / Skills / Agent management for AI coding tools",
		Services: []application.Service{
			application.NewService(app),
		},
		Assets: application.AssetOptions{
			Handler: application.AssetFileServerFS(assets),
		},
		Windows: application.WindowsOptions{
			WndProcInterceptor: WndProcHook,
		},
		Mac: application.MacOptions{
			ApplicationShouldTerminateAfterLastWindowClosed: true,
		},
	})

	app.setWailsApp(wailsApp)

	// 创建主窗口 — 与 v2 完全对齐的 Mica + 透明背景配置
	mainWindow := wailsApp.Window.NewWithOptions(application.WebviewWindowOptions{
		Title:     "AgentPack",
		Width:     960,
		Height:    640,
		MinWidth:  800,
		MinHeight: 500,
		URL:       "/",
		BackgroundType: application.BackgroundTypeTranslucent,
		DefaultContextMenuDisabled: !isDevMode(),
		Windows: application.WindowsWindow{
			Theme:        winTheme,
			BackdropType: application.Mica,
		},
		Mac: application.MacWindow{
			Appearance: macAppearance,
		},
	})

	// 监听窗口关闭事件（v3 RegisterHook 同步拦截，e.Cancel() 可阻止关闭）
	mainWindow.RegisterHook(events.Common.WindowClosing, func(e *application.WindowEvent) {
		app.beforeClose(e)
	})

	// 创建 v3 原生系统托盘
	tray := setupTray(wailsApp, app)
	app.setTray(tray)

	err := wailsApp.Run()
	if err != nil {
		log.Printf("AgentPack: %v", err)
		releaseLock()
		os.Exit(1)
	}
}
