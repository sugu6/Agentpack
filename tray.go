package main

import (
	_ "embed"
	"log"

	"github.com/energye/systray"
)

//go:embed build/windows/icon.ico
var trayIconData []byte

// setupTray 启动系统托盘（在 goroutine 中运行，三端通用）
func setupTray(app *App) {
	systray.Run(func() {
		systray.SetIcon(trayIconData)
		systray.SetTitle("AgentPack")
		systray.SetTooltip("AgentPack - Agent 管理工具")

		mShow := systray.AddMenuItem("显示窗口", "显示主窗口")
		mQuit := systray.AddMenuItem("退出", "退出 AgentPack")

		mShow.Click(func() { app.ShowWindow() })
		mQuit.Click(func() { app.Quit() })
	}, func() {
		// onExit: systray 已停止，不做额外清理
		log.Println("systray exited")
	})
}

// cleanupTray 停止系统托盘
func cleanupTray() {
	systray.Quit()
}
