//go:build !windows

// Package winbridge 的桩实现 — 在非 Windows 平台上返回空值/空操作，
// 确保根目录 app.go 和 main.go 中引用的 Windows 专有符号可编译。
package winbridge

import "github.com/wailsapp/wails/v3/pkg/application"

// GetMainWindowHWND 在非 Windows 平台始终返回 0。
func GetMainWindowHWND() uintptr {
	return 0
}

// SetDarkMode 在非 Windows 平台为空操作。
func SetDarkMode(hwnd uintptr, dark bool) {}

// IsDarkMode 在非 Windows 平台始终返回 false。
func IsDarkMode() bool {
	return false
}

// WndProcHook 在非 Windows 平台为空操作。
func WndProcHook(hwnd uintptr, msg uint32, wParam, lParam uintptr) (uintptr, bool) {
	return 0, false
}

// RegisterSystemThemeHook 在非 Windows 平台为空操作（SystemThemeChanged 仅 Windows 触发）。
// 签名与 winbridge.go 的 Windows 实现保持一致，main.go 调用点在所有平台传两个参数。
func RegisterSystemThemeHook(*application.App, func() string) {}

// TrimWorkingSet 在非 Windows 平台为空操作。
func TrimWorkingSet() {}
