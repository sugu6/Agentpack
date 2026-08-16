package main

import (
	"embed"
	"log"
	"os"

	"agentpack/internal/config"

	"github.com/wailsapp/wails/v3/pkg/application"
	"github.com/wailsapp/wails/v3/pkg/events"
)

//go:embed all:frontend/dist
var assets embed.FS

func main() {
	cfg := config.Load()

	app := NewApp(cfg)

	// beta 仍无公开运行时 SetTheme API，标题栏跟随系统主题（SystemDefault），
	// 应用内主题切换（light/dark）由 winbridge.SetDarkMode + SystemThemeChanged 事件驱动。
	winTheme := application.SystemDefault
	macAppearance := application.DefaultAppearance

	// 生产模式启用官方单实例（v3 beta）：第二实例会通知首实例后以 ExitCode 退出。
	// dev 模式跳过，避免 wails3 dev 热重启被单实例锁拦截。
	var singleInstance *application.SingleInstanceOptions
	if !isDevMode() {
		singleInstance = &application.SingleInstanceOptions{
			UniqueID: "com.sugu6.agentpack",
			ExitCode: 1,
			OnSecondInstanceLaunch: func(data application.SecondInstanceData) {
				// 启动完成后二次启动应用时，唤醒主窗口（从托盘恢复）
				if app.ready() {
					app.ShowWindow()
				}
			},
		}
	}

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
		SingleInstance: singleInstance,
	})

	app.setWailsApp(wailsApp)

	// 系统主题切换 → 同步原生标题栏暗色（替代 winbridge 手动解析 WM_SETTINGCHANGE）。
	// themeGetter 读取当前主题配置：仅 system 主题下跟随系统切换；固定主题
	// （light/dark）时系统切换不改变标题栏，避免与前端固定 UI 视觉撕裂。
	registerSystemThemeHook(wailsApp, func() string {
		return app.getTheme()
	})

	// 创建主窗口 — 与 v2 完全对齐的 Mica + 透明背景配置
	mainWindow := wailsApp.Window.NewWithOptions(application.WebviewWindowOptions{
		Title:                      "AgentPack",
		Width:                      960,
		Height:                     640,
		MinWidth:                   800,
		MinHeight:                  500,
		URL:                        "/",
		BackgroundType:             application.BackgroundTypeTranslucent,
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
	// 注入主窗口引用：Window.Current() 依赖窗口激活状态，未激活时返回 nil
	// 会导致 HideWindow/showWindowRaw nil 解引用 panic，故保存创建时的引用
	app.setMainWindow(mainWindow)

	// 创建 v3 原生系统托盘
	tray := setupTray(wailsApp, app)
	app.setTray(tray)

	err := wailsApp.Run()
	if err != nil {
		log.Printf("AgentPack: %v", err)
		os.Exit(1)
	}
}
