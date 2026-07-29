package main

import (
	_ "embed"

	"agentpack/internal/i18n"

	"github.com/wailsapp/wails/v3/pkg/application"
)

//go:embed build/windows/icon.ico
var trayIconData []byte

// 托盘菜单项引用，用于语言切换与轻量模式状态同步时更新菜单项
//
// ⚠️ 禁止在这些菜单项上调用 Menu.Update()：v3 alpha 的 Menu.Update() 会走
// windowsMenu（菜单栏实现）重建路径，processMenu 内部把每个 MenuItem.impl
// 改指向一个与托盘无关的新 HMENU，导致此后所有 SetChecked/SetLabel 都写不进
// 托盘真正显示的菜单。SetChecked/SetLabel 自身已经通过 impl.update() 直接
// 调用 SetMenuItemInfo 写入原生菜单，无需额外刷新。
var (
	trayShowItem *application.MenuItem
	trayLiteItem *application.MenuItem
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
	trayLiteItem = menu.AddCheckbox(i18n.T(lang, "tray.lite"), false)
	trayLiteItem.OnClick(func(ctx *application.Context) {
		// v3 在触发回调前已翻转 checked，翻转后的值即用户期望的目标状态
		app.SetLiteMode(trayLiteItem.Checked())
		// 目标状态与实际状态不一致时（例如已处于该状态导致 SetLiteMode 空转）
		// 把勾选态拉回真实状态，避免菜单显示与后端状态漂移
		syncTrayLiteState(app.IsLiteMode())
	})
	menu.AddSeparator()
	trayQuitItem = menu.Add(i18n.T(lang, "tray.quit"))
	trayQuitItem.OnClick(func(ctx *application.Context) {
		app.Quit()
	})

	// 后端自动进入/退出轻量模式时，回写复选框状态
	onLiteModeChanged = syncTrayLiteState

	tray := wailsApp.SystemTray.New().
		SetIcon(trayIconData).
		SetMenu(menu)
	tray.SetTooltip(i18n.T(lang, "tray.tooltip"))

	return tray
}

// syncTrayLiteState 将轻量模式状态同步到托盘复选框。
// SetChecked 内部会调用 SetMenuItemInfo 直接写入原生菜单，不需要 Menu.Update()。
func syncTrayLiteState(on bool) {
	if trayLiteItem == nil {
		return
	}
	if trayLiteItem.Checked() == on {
		return
	}
	application.InvokeAsync(func() {
		trayLiteItem.SetChecked(on)
	})
}

// rebuildTrayMenu 切换语言后更新托盘菜单文案
func rebuildTrayMenu(tray *application.SystemTray, lang string) {
	if tray == nil {
		return
	}
	application.InvokeAsync(func() {
		tray.SetTooltip(i18n.T(lang, "tray.tooltip"))
		if trayShowItem != nil {
			trayShowItem.SetLabel(i18n.T(lang, "tray.show"))
		}
		if trayLiteItem != nil {
			trayLiteItem.SetLabel(i18n.T(lang, "tray.lite"))
		}
		if trayQuitItem != nil {
			trayQuitItem.SetLabel(i18n.T(lang, "tray.quit"))
		}
	})
}
