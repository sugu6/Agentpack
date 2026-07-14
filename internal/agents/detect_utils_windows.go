//go:build windows

package agents

import (
	"strings"
	"time"

	"golang.org/x/sys/windows/registry"
)

// loadRegistryCache 从 Windows 注册表加载已安装应用列表
func loadRegistryCache() {
	cache := make(map[string]bool)

	roots := []string{
		`SOFTWARE\Microsoft\Windows\CurrentVersion\Uninstall`,
		`SOFTWARE\WOW6432Node\Microsoft\Windows\CurrentVersion\Uninstall`,
	}

	hives := []registry.Key{registry.LOCAL_MACHINE, registry.CURRENT_USER}

	for _, hive := range hives {
		for _, root := range roots {
			readUninstallSubkeys(hive, root, cache)
		}
	}
	regCacheMu.Lock()
	regCache = cache
	regCacheLoadedAt = time.Now()
	regCacheMu.Unlock()
}

// readUninstallSubkeys 读取注册表 Uninstall 下的所有子键的 DisplayName
func readUninstallSubkeys(hive registry.Key, subkeyPath string, cache map[string]bool) {
	k, err := registry.OpenKey(hive, subkeyPath, registry.ENUMERATE_SUB_KEYS|registry.QUERY_VALUE)
	if err != nil {
		return
	}
	defer k.Close()

	names, err := k.ReadSubKeyNames(-1)
	if err != nil {
		return
	}
	for _, name := range names {
		subKey, err := registry.OpenKey(k, name, registry.QUERY_VALUE)
		if err != nil {
			continue
		}
		displayName, _, err := subKey.GetStringValue("DisplayName")
		subKey.Close()
		if err != nil {
			continue
		}
		if displayName != "" {
			cache[strings.ToLower(displayName)] = true
		}
	}
}

// CheckAppxPackageInstalled 通过 UWP 包注册表检测微软商店应用是否已安装。
// familyName 为包家族名（如 "OpenAI.Codex"），注册表子键格式为 "<familyName>_<version>_<arch>_<pubid>"。
// 非平台直接返回 false。
func CheckAppxPackageInstalled(familyName string) bool {
	// UWP 包注册表键位于 HKCU 下，Local Settings 为虚拟键名，可直接 OpenKey 访问
	const appxRoot = `Software\Classes\Local Settings\Software\Microsoft\Windows\CurrentVersion\AppModel\Repository\Packages`
	k, err := registry.OpenKey(registry.CURRENT_USER, appxRoot, registry.ENUMERATE_SUB_KEYS)
	if err != nil {
		return false
	}
	defer k.Close()

	names, err := k.ReadSubKeyNames(-1)
	if err != nil {
		return false
	}
	prefix := familyName + "_"
	for _, name := range names {
		if strings.HasPrefix(name, prefix) {
			return true
		}
	}
	return false
}
