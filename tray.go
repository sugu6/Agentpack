package main

import (
	_ "embed"

	"agentpack/internal/i18n"

	"github.com/wailsapp/wails/v3/pkg/application"
)

//go:embed build/windows/icon.ico
var trayIconData []byte

// 托盘菜单项引用，用于语言切换时更新文案
var (
	trayShowItem *application.MenuItem
	trayQuitItem *application.MenuItem
)

// setupTray 使用 v3 原生 SystemTray API 创建系统托盘。
func setupTray(wailsApp *application.App, app *App) *application.SystemTray {
	lang := i18n.ResolveLanguage(app.cfg.Settings.Language)

	menu := application.NewMenu()
	trayShowItem = menu.Add(i18n.T(lang, "tray.show"))
	trayShowItem.OnClick(func(ctx *application.Context) {
		app.ShowWindow()
	})
	menu.AddSeparator()
	trayQuitItem = menu.Add(i18n.T(lang, "tray.quit"))
	trayQuitItem.OnClick(func(ctx *application.Context) {
		app.Quit()
	})

	tray := wailsApp.SystemTray.New().
		SetIcon(trayIconData).
		SetMenu(menu)
	tray.SetTooltip(i18n.T(lang, "tray.tooltip"))

	return tray
}

// rebuildTrayMenu 切换语言后更新托盘菜单文案
func rebuildTrayMenu(tray *application.SystemTray, lang string) {
	if tray == nil {
		return
	}
	tray.SetTooltip(i18n.T(lang, "tray.tooltip"))
	if trayShowItem != nil {
		trayShowItem.SetLabel(i18n.T(lang, "tray.show"))
	}
	if trayQuitItem != nil {
		trayQuitItem.SetLabel(i18n.T(lang, "tray.quit"))
	}
}
