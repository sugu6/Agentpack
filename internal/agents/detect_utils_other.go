//go:build !windows

package agents

// loadRegistryCache 在非 Windows 平台上为空实现，注册表检测不可用。
func loadRegistryCache() {}

// CheckAppxPackageInstalled 在非 Windows 平台上始终返回 false。
func CheckAppxPackageInstalled(_ string) bool { return false }
