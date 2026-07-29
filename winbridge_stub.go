//go:build !windows

package main

// Windows 原生桥接的桩实现 — 在非 Windows 平台上返回空值/空操作，
// 确保 app.go 和 main.go 中引用的 Windows 专有符号可编译。

// getMainWindowHWND 在非 Windows 平台始终返回 0。
func getMainWindowHWND() uintptr {
	return 0
}

// SetDarkMode 在非 Windows 平台为空操作。
func SetDarkMode(hwnd uintptr, dark bool) {}

// isDarkMode 在非 Windows 平台始终返回 false。
func isDarkMode() bool {
	return false
}

// WndProcHook 在非 Windows 平台为空操作。
func WndProcHook(hwnd uintptr, msg uint32, wParam, lParam uintptr) (uintptr, bool) {
	return 0, false
}

// TrimWorkingSet 在非 Windows 平台为空操作。
func TrimWorkingSet() {}
