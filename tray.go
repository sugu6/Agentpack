package main

import (
	_ "embed"
	"log"

	"agentpack/internal/i18n"

	"github.com/energye/systray"
)

//go:embed build/windows/icon.ico
var trayIconData []byte

// trayMenuRefs 保存托盘菜单项引用,用于 rebuild
var trayMenuRefs struct {
	show  *systray.MenuItem
	quit  *systray.MenuItem
}

// setupTray 启动系统托盘（在 goroutine 中运行，三端通用）
func setupTray(app *App) {
	systray.Run(func() {
		lang := i18n.ResolveLanguage(app.cfg.Settings.Language)
		buildTrayMenu(app, lang)
	}, func() {
		// onExit: systray 已停止，不做额外清理
		log.Println("systray exited")
	})
}

// buildTrayMenu 构建托盘菜单（支持语言切换时重建）
func buildTrayMenu(app *App, lang string) {
	systray.SetIcon(trayIconData)
	systray.SetTitle("AgentPack")
	systray.SetTooltip(i18n.T(lang, "tray.tooltip"))

	trayMenuRefs.show = systray.AddMenuItem(i18n.T(lang, "tray.show"), i18n.T(lang, "tray.show"))
	trayMenuRefs.quit = systray.AddMenuItem(i18n.T(lang, "tray.quit"), i18n.T(lang, "tray.quit"))

	trayMenuRefs.show.Click(func() { app.ShowWindow() })
	trayMenuRefs.quit.Click(func() { app.Quit() })
}

// rebuildTrayMenu 切换语言后重建托盘菜单文案
// 注意: systray 库限制,仅更新 tooltip 与菜单项标题,不重新绑定 click（避免重复绑定）
func rebuildTrayMenu(lang string) {
	if trayMenuRefs.show == nil {
		return
	}
	systray.SetTooltip(i18n.T(lang, "tray.tooltip"))
	trayMenuRefs.show.SetTitle(i18n.T(lang, "tray.show"))
	trayMenuRefs.quit.SetTitle(i18n.T(lang, "tray.quit"))
}

// cleanupTray 停止系统托盘
func cleanupTray() {
	systray.Quit()
}
