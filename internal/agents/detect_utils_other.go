//go:build !windows

package agents

import "os/exec"

// hideConsoleWindow 在非 Windows 平台上为空实现，无需抑制控制台窗口。
func hideConsoleWindow(_ *exec.Cmd) {}

// loadRegistryCache 在非 Windows 平台上为空实现，注册表检测不可用。
func loadRegistryCache() {}

// CheckAppxPackageInstalled 在非 Windows 平台上始终返回 false。
func CheckAppxPackageInstalled(_ string) bool { return false }
