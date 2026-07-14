package main

import (
	"embed"

	"agentpack/internal/config"
	"agentpack/internal/lockfile"
	"log"
	"os"

	"github.com/wailsapp/wails/v2"
	"github.com/wailsapp/wails/v2/pkg/options"
	"github.com/wailsapp/wails/v2/pkg/options/assetserver"
	"github.com/wailsapp/wails/v2/pkg/options/linux"
	"github.com/wailsapp/wails/v2/pkg/options/mac"
	"github.com/wailsapp/wails/v2/pkg/options/windows"
)

//go:embed all:frontend/dist
var assets embed.FS

func main() {
	cfg := config.Load()

	releaseLock := func() {}
	if !isDevMode() {
		// 单实例锁：Windows 用 Named Mutex（进程退出自动释放），Unix 用 PID 文件
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

	winTheme := windows.Light
	macAppearance := mac.NSAppearanceNameAqua

	switch cfg.Settings.Theme {
	case "dark":
		winTheme = windows.Dark
		macAppearance = mac.NSAppearanceNameDarkAqua
	case "system":
		winTheme = windows.SystemDefault
		macAppearance = mac.DefaultAppearance
	}

	err := wails.Run(&options.App{
		Title:            "AgentPack",
		Width:            960,
		Height:           640,
		BackgroundColour: nil,
		// dev 模式保留右键菜单便于调试；生产构建禁用以防止用户通过右键"检查"打开 DevTools
		EnableDefaultContextMenu: isDevMode(),
		AssetServer: &assetserver.Options{
			Assets: assets,
		},
		OnStartup:     app.startup,
		OnShutdown:    app.shutdown,
		OnBeforeClose: app.beforeClose,
		Bind: []interface{}{
			app,
		},
		Windows: &windows.Options{
			Theme:                winTheme,
			BackdropType:         windows.Mica,
			WebviewIsTransparent: true,
			WindowIsTranslucent:  true,
		},
		Mac: &mac.Options{
			Appearance:           macAppearance,
			TitleBar:             mac.TitleBarDefault(),
			WebviewIsTransparent: true,
			WindowIsTranslucent:  false,
		},
		Linux: &linux.Options{
			WebviewGpuPolicy: linux.WebviewGpuPolicyAlways,
			ProgramName:      "AgentPack",
		},
	})

	if err != nil {
		log.Printf("AgentPack: %v", err)
		releaseLock()
		os.Exit(1)
	}
}
