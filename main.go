package main

import (
	"embed"
	"os"
	"path/filepath"
	"runtime/debug"

	"agentpack/internal/app/winbridge"
	"agentpack/internal/appmeta"
	"agentpack/internal/config"
	"agentpack/internal/logging"

	"github.com/wailsapp/wails/v3/pkg/application"
	"github.com/wailsapp/wails/v3/pkg/events"
)

//go:embed all:frontend/dist
var assets embed.FS

// 托盘图标用 PNG 而非 icon.ico：Wails 的 CreateSmallHIconFromImage 按
// 裸 PNG 解码，传入多尺寸 .ico 容器必然解码失败（启动日志出现
// "failed to create systray icon"），虽会回退到 exe 资源图标，但
// 直接喂 PNG 可走自定义图标路径，避免该警告。
//
//go:embed build/appicon.png
var trayIconData []byte

func main() {
	cfg := config.Load()

	// 日志必须最先初始化：DB / 配置 / WebView2 的任何启动失败都要有落盘记录。
	// 生产构建（windowsgui）stderr 被丢弃，因此文件日志是唯一可排查的通道。
	// 已知局限：closeLog() 之后仍在运行的后台 goroutine 触发的 log.Printf 会被
	// 静默丢弃（rotator 已关闭）；影响仅限进程退出瞬间的极短窗口，可接受。
	logDir := config.AgentPackDir()
	if logDir != "" {
		logDir = filepath.Join(logDir, "logs")
	}
	closeLog := logging.Init(logging.Options{
		Dir:        logDir,
		Level:      cfg.Settings.LogLevel,
		AlsoStderr: true,
	})
	defer closeLog()

	// panic 捕获：写 crash-*.log（诊断包会收集），非零退出码保留崩溃语义。
	defer func() {
		if r := recover(); r != nil {
			logging.WriteCrash(r, debug.Stack())
			closeLog()
			os.Exit(2)
		}
	}()

	logging.L().Info("AgentPack starting",
		"version", appmeta.Version,
		"logLevel", logging.LevelName(),
		"logDir", logging.Dir(),
		"dev", isDevMode(),
	)

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
				if app.Ready() {
					app.ShowWindow()
				}
			},
		}
	}

	wailsApp := application.New(application.Options{
		Name:        "AgentPack",
		Description: "Unified MCP / Skills / Agent management for AI coding tools",
		// 注入统一 logger：Wails 内部日志（Platform Info / WebView2 报错 / AssetServer）
		// 进入 webview 通道，与应用日志统一落盘，排障时无需再开控制台拼接两路输出。
		Logger:   logging.Cat("webview"),
		LogLevel: logging.Level(),
		Services: []application.Service{
			application.NewService(app),
		},
		Assets: application.AssetOptions{
			Handler: application.AssetFileServerFS(assets),
		},
		Windows: application.WindowsOptions{
			WndProcInterceptor: winbridge.WndProcHook,
		},
		Mac: application.MacOptions{
			ApplicationShouldTerminateAfterLastWindowClosed: true,
		},
		SingleInstance: singleInstance,
	})

	app.SetWailsApp(wailsApp)

	// 系统主题切换 → 同步原生标题栏暗色（替代 winbridge 手动解析 WM_SETTINGCHANGE）。
	// themeGetter 读取当前主题配置：仅 system 主题下跟随系统切换；固定主题
	//（light/dark）时系统切换不改变标题栏，避免与前端固定 UI 视觉撕裂。
	winbridge.RegisterSystemThemeHook(wailsApp, func() string {
		return app.GetTheme()
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
		app.BeforeClose(e)
	})
	// 注入主窗口引用：Window.Current() 依赖窗口激活状态，未激活时返回 nil
	// 会导致 HideWindow/showWindowRaw nil 解引用 panic，故保存创建时的引用
	app.SetMainWindow(mainWindow)

	// 创建 v3 原生系统托盘
	tray := SetupTray(wailsApp, app, trayIconData)
	app.SetTray(tray)

	err := wailsApp.Run()
	if err != nil {
		logging.L().Error("wails run failed", "error", err)
		closeLog()
		os.Exit(1)
	}
}
