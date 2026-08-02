package main

import (
	"fmt"
	"log"
	"os"
	"os/exec"
	"runtime"
	"strings"

	"agentpack/internal/agents"
	"agentpack/internal/backup"
	"agentpack/internal/config"
	"agentpack/internal/i18n"
	"agentpack/internal/skills"
)

func (a *App) OpenConfigFolder() error {
	dir := config.AgentPackDir()
	if err := os.MkdirAll(dir, 0700); err != nil {
		return err
	}
	switch runtime.GOOS {
	case "windows":
		return exec.Command("explorer.exe", dir).Start()
	case "darwin":
		return exec.Command("open", dir).Start()
	default:
		return exec.Command("xdg-open", dir).Start()
	}
}

func (a *App) GetStartupErrors() []string {
	a.mu.RLock()
	defer a.mu.RUnlock()
	return append([]string{}, a.startupErrors...)
}

func (a *App) GetSettings() (config.Settings, error) {
	a.mu.RLock()
	defer a.mu.RUnlock()
	if a.cfg == nil {
		return config.DefaultSettings(), nil
	}
	return a.cfg.Settings, nil
}

func (a *App) UpdateSettings(s config.Settings) error {
	if err := a.assertInit(); err != nil {
		return err
	}
	if err := a.beginInFlight(); err != nil {
		return err
	}
	defer a.endInFlight()

	// 获取 storeOpMu 以序列化与 RescanAgents、ToggleAgent 等存储操作的并发
	a.storeOpMu.Lock()
	defer a.storeOpMu.Unlock()

	// 收集锁内数据，I/O 操作在锁外执行
	var oldSkillStorage, oldSkillSyncMethod string
	var skillsStore *skills.Store
	var registry *agents.Registry
	var backups *backup.Manager
	var oldLang string

	a.mu.Lock()
	if a.cfg == nil {
		a.cfg = config.Default()
	}
	if s.BackupRetention <= 0 {
		if s.BackupCount > 0 {
			s.BackupRetention = s.BackupCount
		} else {
			s.BackupRetention = config.DefaultSettings().BackupRetention
		}
	}
	if s.BackupCount <= 0 {
		s.BackupCount = s.BackupRetention
	}
	s.LiteAutoDelay = config.ClampLiteDelay(s.LiteAutoDelay)
	newCfg := *a.cfg
	newCfg.Settings = s
	newSettings := newCfg.Settings
	oldLiteEnabled := a.cfg.Settings.LiteAutoEnabled
	oldLiteDelay := a.cfg.Settings.LiteAutoDelay
	oldSkillStorage = a.cfg.Settings.SkillStorage
	oldSkillSyncMethod = a.cfg.Settings.SkillSyncMethod
	oldLang = i18n.ResolveLanguage(a.cfg.Settings.Language)
	skillsStore = a.skillsStore
	registry = a.registry
	backups = a.backups
	a.mu.Unlock()

	// 在释放锁之后执行文件 I/O 操作
	if skillsStore != nil {
		if s.SkillSyncMethod != oldSkillSyncMethod {
			skillsStore.SetSyncMethod(skills.SyncMethod(s.SkillSyncMethod))
			if err := skillsStore.Resync(registry); err != nil {
				skillsStore.SetSyncMethod(skills.SyncMethod(oldSkillSyncMethod))
				return fmt.Errorf("resync skills after method change: %w", err)
			}
		}
		if s.SkillStorage != oldSkillStorage {
			newDir := skills.ResolveSSOTDir(skills.StorageLocation(s.SkillStorage))
			result, err := skillsStore.MigrateStorage(newDir, registry)
			if err != nil {
				if s.SkillSyncMethod != oldSkillSyncMethod {
					skillsStore.SetSyncMethod(skills.SyncMethod(oldSkillSyncMethod))
				}
				return fmt.Errorf("migrate skill storage: %w", err)
			}
			if result.Migrated > 0 {
				log.Printf("migrated %d skills to %s", result.Migrated, newDir)
			}
		}
	}

	if err := config.Save(&newCfg); err != nil {
		// Keep runtime and persisted settings aligned when the final config
		// write fails after a filesystem migration.
		if skillsStore != nil && s.SkillStorage != oldSkillStorage {
			oldDir := skills.ResolveSSOTDir(skills.StorageLocation(oldSkillStorage))
			if _, rollbackErr := skillsStore.MigrateStorage(oldDir, registry); rollbackErr != nil {
				log.Printf("rollback skill storage after settings save failure: %v", rollbackErr)
			}
		}
		if skillsStore != nil && s.SkillSyncMethod != oldSkillSyncMethod {
			skillsStore.SetSyncMethod(skills.SyncMethod(oldSkillSyncMethod))
			if rollbackErr := skillsStore.Resync(registry); rollbackErr != nil {
				log.Printf("rollback skill sync method after settings save failure: %v", rollbackErr)
			}
		}
		return err
	}

	a.mu.Lock()
	a.cfg.Settings = newSettings
	a.refreshBackupHooksLocked()
	a.mu.Unlock()
	if backups != nil {
		if err := backups.SetRetention(s.BackupRetention); err != nil {
			log.Printf("set backup retention: %v", err)
		}
	}

	// 在锁外 emit 事件
	if skillsStore != nil && (s.SkillStorage != oldSkillStorage || s.SkillSyncMethod != oldSkillSyncMethod) {
		a.emit("skills:changed", skillsStore.List())
	}

	// 语言变化时更新托盘菜单文案
	newLang := i18n.ResolveLanguage(s.Language)
	if oldLang != newLang {
		a.mu.RLock()
		tray := a.tray
		a.mu.RUnlock()
		rebuildTrayMenu(tray, newLang)
	}

	if s.LiteAutoEnabled != oldLiteEnabled || s.LiteAutoDelay != oldLiteDelay {
		if s.LiteAutoEnabled {
			a.restartLiteTimer()
		} else {
			a.stopLiteTimer()
		}
	}

	// emit settings:changed 让前端同步 i18n 语言 + 后端托盘重建
	a.emit("settings:changed", newSettings)

	return nil
}

// OpenURL 在系统浏览器中打开指定 URL。

// OpenURL 在系统浏览器中打开指定 URL。
func (a *App) OpenURL(url string) {
	switch runtime.GOOS {
	case "windows":
		cmd := exec.Command("cmd", "/c", "start", "", url)
		hideConsoleWindow(cmd)
		cmd.Start()
	case "darwin":
		exec.Command("open", url).Start()
	default:
		exec.Command("xdg-open", url).Start()
	}
}

// Quit 退出应用程序。设置 allowClose 标志后调用 application.Quit。

// Quit 退出应用程序。设置 allowClose 标志后调用 application.Quit。
func (a *App) Quit() error {
	a.mu.Lock()
	a.allowClose = true
	closed := a.closed
	a.mu.Unlock()
	if closed {
		return nil
	}
	a.wailsApp.Quit()
	return nil
}

// HideWindow 隐藏窗口（最小化到系统托盘）。

// HideWindow 隐藏窗口（最小化到系统托盘）。
func (a *App) HideWindow() {
	a.mu.RLock()
	closed := a.closed
	a.mu.RUnlock()
	if closed {
		return
	}
	a.wailsApp.Window.Current().Hide()
}

// ShowWindow 显示窗口（从系统托盘恢复）。同时退出轻量模式并停用空闲计时器，
// 计时器由前端上报的用户活动（NotifyActivity）重新拉起。

// ShowWindow 显示窗口（从系统托盘恢复）。同时退出轻量模式并停用空闲计时器，
// 计时器由前端上报的用户活动（NotifyActivity）重新拉起。
func (a *App) ShowWindow() {
	a.liteMu.Lock()
	wasLite := a.liteMode
	a.liteMode = false
	a.stopLiteTimerLocked()
	a.liteMu.Unlock()

	a.showWindowRaw()

	if wasLite && onLiteModeChanged != nil {
		onLiteModeChanged(false)
	}
}

// showWindowRaw 仅执行窗口显示，不触碰轻量模式状态。

// showWindowRaw 仅执行窗口显示，不触碰轻量模式状态。
func (a *App) showWindowRaw() {
	a.mu.RLock()
	closed := a.closed
	a.mu.RUnlock()
	if closed {
		return
	}
	a.wailsApp.Window.Current().Show()
}

func (a *App) SetTheme(theme string) {
	a.mu.RLock()
	closed := a.closed
	a.mu.RUnlock()
	if closed {
		return
	}

	// v3 alpha 无运行时 SetTheme 公共 API，通过 WndProcInterceptor 缓存的 HWND
	// 直接调用 DwmSetWindowAttribute(DWMWA_USE_IMMERSIVE_DARK_MODE) 实现。
	hwnd := getMainWindowHWND()
	if hwnd == 0 {
		return
	}
	switch theme {
	case "dark":
		SetDarkMode(hwnd, true)
	case "light":
		SetDarkMode(hwnd, false)
	case "system":
		SetDarkMode(hwnd, isDarkMode())
	}
}

func (a *App) PickDirectory() (string, error) {
	a.mu.RLock()
	closed := a.closed
	a.mu.RUnlock()
	if closed {
		return "", fmt.Errorf("app not ready")
	}
	return a.wailsApp.Dialog.OpenFile().
		SetTitle("选择目录").
		CanChooseFiles(false).
		CanChooseDirectories(true).
		PromptForSingleSelection()
}

func (a *App) PickFile(filters string) (string, error) {
	a.mu.RLock()
	closed := a.closed
	a.mu.RUnlock()
	if closed {
		return "", fmt.Errorf("app not ready")
	}
	dialog := a.wailsApp.Dialog.OpenFile().
		SetTitle("选择文件")
	if filters != "" {
		for _, f := range strings.Split(filters, ",") {
			f = strings.TrimSpace(f)
			if f != "" {
				dialog = dialog.AddFilter(f, f)
			}
		}
	}
	return dialog.PromptForSingleSelection()
}
