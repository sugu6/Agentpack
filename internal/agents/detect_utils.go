package agents

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"
)

// npmCache 缓存 npm 全局包列表，避免重复调用 npm
// 缓存带 TTL（60 秒），避免频繁的 npm list -g 调用影响性能
var (
	npmCache         map[string]bool
	npmCacheMu       sync.RWMutex
	npmCacheLoadedAt time.Time
)

const cacheTTL = 60 * time.Second

// CheckCommandExists 检查命令是否存在于 PATH 中
func CheckCommandExists(cmd string) bool {
	path := os.Getenv("PATH")
	if path == "" {
		return false
	}

	exts := []string{""}
	if runtime.GOOS == "windows" {
		// .ps1 不能直接执行，不应作为命令存在性判断
		exts = []string{".exe", ".cmd", ".bat"}
	}

	for _, dir := range filepath.SplitList(path) {
		for _, ext := range exts {
			cmdPath := filepath.Join(dir, cmd+ext)
			if fileExists(cmdPath) {
				return true
			}
		}
	}
	return false
}

// CheckNpmPackageInstalled 检查 npm 全局包是否已安装
// 通过执行 npm list -g --depth=0 并解析输出来判断
// 缓存带 TTL，避免频繁调用 npm list -g
func CheckNpmPackageInstalled(pkg string) bool {
	npmCacheMu.RLock()
	cache := npmCache
	loadedAt := npmCacheLoadedAt
	npmCacheMu.RUnlock()

	// 缓存过期时，在锁外触发重新加载，采用双重检查避免重复加载
	if cache == nil || time.Since(loadedAt) > cacheTTL {
		needsLoad := func() bool {
			npmCacheMu.Lock()
			defer npmCacheMu.Unlock()
			if npmCache != nil && time.Since(npmCacheLoadedAt) <= cacheTTL {
				return false
			}
			return true
		}()
		if needsLoad {
			loadNpmCache()
		}
	}

	npmCacheMu.RLock()
	defer npmCacheMu.RUnlock()
	if npmCache != nil {
		return npmCache[pkg]
	}
	// 缓存加载失败时回退到单次检查
	return checkNpmPackageSingle(pkg)
}

// loadNpmCache 一次性加载所有 npm 全局包到缓存
func loadNpmCache() {
	cache := make(map[string]bool)

	// 查找 npm 可执行文件
	npmPath, err := exec.LookPath("npm")
	if err != nil {
		// npm 不在 PATH：明确标记为空缓存（确定无 npm），更新时间戳
		npmCacheMu.Lock()
		npmCache = cache
		npmCacheLoadedAt = time.Now()
		npmCacheMu.Unlock()
		return
	}

	// 使用 context 超时，避免 npm 挂起时永久阻塞
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, npmPath, "list", "-g", "--depth=0", "--json")
	output, err := cmd.Output()
	if err != nil {
		// 区分两种情况：
		// 1. npm list 在有 extraneous 包时返回 exit code 1，但仍有有效输出 → 继续解析
		// 2. 超时或执行失败且无输出 → 不更新缓存时间戳，让下次调用立即重试
		if len(output) == 0 {
			// 执行失败且无输出，可能是超时或 npm 崩溃。
			// 不更新 npmCacheLoadedAt，让下次 CheckNpmPackageInstalled 立即重试。
			return
		}
	}

	// 使用 encoding/json 解析，避免手动字符串匹配的脆弱性
	var result struct {
		Dependencies map[string]struct{} `json:"dependencies"`
	}
	if err := json.Unmarshal(output, &result); err != nil {
		// JSON 解析失败，不更新缓存时间戳，让下次调用立即重试
		return
	}
	for name := range result.Dependencies {
		cache[name] = true
	}
	npmCacheMu.Lock()
	npmCache = cache
	npmCacheLoadedAt = time.Now()
	npmCacheMu.Unlock()
}

// checkNpmPackageSingle 单独检查一个 npm 包（回退方案）
func checkNpmPackageSingle(pkg string) bool {
	npmPath, err := exec.LookPath("npm")
	if err != nil {
		return false
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, npmPath, "list", "-g", pkg, "--depth=0")
	output, _ := cmd.CombinedOutput()
	return strings.Contains(string(output), pkg+"@")
}

// ResetNpmCache 重置 npm 缓存（用于测试或重新扫描时）
func ResetNpmCache() {
	npmCacheMu.Lock()
	defer npmCacheMu.Unlock()
	npmCache = nil
	npmCacheLoadedAt = time.Time{}
}

// regCache 缓存注册表已安装应用列表
var (
	regCache         map[string]bool
	regCacheMu       sync.RWMutex
	regCacheLoadedAt time.Time

	// skipRegistryLookup 用于测试隔离：当为 true 时，注册表检测直接返回 false，
	// 避免开发机/CI 上真实安装的 IDE 干扰单元测试。通过 SetSkipRegistryLookupForTesting
	// 设置，而非环境变量，以防生产环境被外部 env 误改。
	skipRegistryLookup   bool
	skipRegistryLookupMu sync.RWMutex
)

// SetSkipRegistryLookupForTesting 仅供测试使用。返回一个用于恢复原值的清理函数。
func SetSkipRegistryLookupForTesting(skip bool) func() {
	skipRegistryLookupMu.Lock()
	prev := skipRegistryLookup
	skipRegistryLookup = skip
	skipRegistryLookupMu.Unlock()
	return func() {
		skipRegistryLookupMu.Lock()
		skipRegistryLookup = prev
		skipRegistryLookupMu.Unlock()
	}
}

func shouldSkipRegistryLookup() bool {
	skipRegistryLookupMu.RLock()
	defer skipRegistryLookupMu.RUnlock()
	return skipRegistryLookup
}

// CheckAppInstalledViaRegistry 通过 Windows 注册表检查应用是否已安装
// 在非 Windows 平台上回退到目录检测
func CheckAppInstalledViaRegistry(displayNames []string) bool {
	return CheckAppInstalledViaRegistryExclude(displayNames, nil)
}

// CheckAppInstalledViaRegistryExclude 通过 Windows 注册表检查应用是否已安装
// 排除包含 excludeNames 中任意子串的条目。缓存带 TTL，避免频繁注册表枚举。
func CheckAppInstalledViaRegistryExclude(displayNames []string, excludeNames []string) bool {
	if shouldSkipRegistryLookup() {
		return false
	}
	if runtime.GOOS != "windows" {
		return false
	}

	// TTL 检查：缓存超过 60 秒时触发重新加载
	regCacheMu.RLock()
	cache := regCache
	loadedAt := regCacheLoadedAt
	regCacheMu.RUnlock()
	if cache == nil || time.Since(loadedAt) > cacheTTL {
		needsLoad := func() bool {
			regCacheMu.Lock()
			defer regCacheMu.Unlock()
			if regCache != nil && time.Since(regCacheLoadedAt) <= cacheTTL {
				return false
			}
			return true
		}()
		if needsLoad {
			loadRegistryCache()
		}
	}

	regCacheMu.RLock()
	defer regCacheMu.RUnlock()
	if regCache != nil {
		for _, name := range displayNames {
			key := strings.ToLower(name)
			for regName := range regCache {
				if !strings.Contains(regName, key) {
					continue
				}
				excluded := false
				for _, excl := range excludeNames {
					if strings.Contains(regName, strings.ToLower(excl)) {
						excluded = true
						break
					}
				}
				if !excluded {
					return true
				}
			}
		}
	}
	return false
}

// ResetRegistryCache 重置注册表缓存
func ResetRegistryCache() {
	regCacheMu.Lock()
	defer regCacheMu.Unlock()
	regCache = nil
	regCacheLoadedAt = time.Time{}
}

// GetAppDataPath 获取应用数据目录
func GetAppDataPath(appName string, platformPaths map[string][]string) []string {
	h := homeDir()
	if h == "" {
		return nil
	}

	paths := make([]string, 0)
	switch runtime.GOOS {
	case "windows":
		appData := os.Getenv("APPDATA")
		localAppData := os.Getenv("LOCALAPPDATA")
		for _, subPath := range platformPaths["windows"] {
			if appData != "" {
				paths = append(paths, filepath.Join(appData, subPath))
			}
			if localAppData != "" {
				paths = append(paths, filepath.Join(localAppData, subPath))
			}
			paths = append(paths, filepath.Join(h, "AppData", "Roaming", subPath))
			paths = append(paths, filepath.Join(h, "AppData", "Local", subPath))
		}
	case "darwin":
		for _, subPath := range platformPaths["darwin"] {
			paths = append(paths, filepath.Join(h, "Library", "Application Support", subPath))
		}
	default:
		for _, subPath := range platformPaths["linux"] {
			paths = append(paths, filepath.Join(h, ".config", subPath))
			paths = append(paths, filepath.Join(h, "."+strings.ToLower(appName)))
		}
	}
	return paths
}

// DetectAppDir 检测应用数据目录是否存在
func DetectAppDir(appName string, platformPaths map[string][]string) (string, bool) {
	paths := GetAppDataPath(appName, platformPaths)
	for _, path := range paths {
		if dirExists(path) {
			return path, true
		}
	}
	return "", false
}

// FirstExistingFile 返回第一个存在的文件路径
func FirstExistingFile(candidates []string) string {
	for _, c := range candidates {
		if fileExists(c) {
			return c
		}
	}
	return ""
}

// FirstExistingDir 返回第一个存在的目录路径
func FirstExistingDir(candidates []string) string {
	for _, c := range candidates {
		if dirExists(c) {
			return c
		}
	}
	return ""
}

// DetectIDE 通用的 IDE 检测函数
// 先通过注册表检测，失败后回退到目录检测
// appName 用于目录检测时作为应用名称，来自 registryNames[0]
// excludeNames 可选，用于排除注册表中包含特定子串的条目（如 trae.go 排除 "cn"）
func DetectIDE(registryNames []string, appPaths map[string][]string, excludeNames ...string) bool {
	if len(registryNames) == 0 {
		return false
	}
	hasIDE := CheckAppInstalledViaRegistryExclude(registryNames, excludeNames)
	if hasIDE {
		return true
	}
	appName := registryNames[0]
	if _, ok := DetectAppDir(appName, appPaths); ok {
		return true
	}
	return false
}

// DetectCLI 通用的 CLI 检测函数
// 先通过 npm 包检测，失败后回退到命令检测
func DetectCLI(npmPackage string, commands ...string) bool {
	if CheckNpmPackageInstalled(npmPackage) {
		return true
	}
	for _, cmd := range commands {
		if CheckCommandExists(cmd) {
			return true
		}
	}
	return false
}

// BuildDetectInfo 根据检测结果构建 DetectInfo
func BuildDetectInfo(hasIDE, hasCLI, hasDesktop, hasConfig bool, variant AgentVariant, configPath string) *DetectInfo {
	hasAnyAgent := hasIDE || hasCLI || hasDesktop

	if !hasAnyAgent && !hasConfig {
		return &DetectInfo{Status: StatusNotFound, ConfigPath: configPath}
	}

	if !hasAnyAgent {
		return &DetectInfo{Status: StatusNotFound, Variant: VariantConfig, ConfigPath: configPath}
	}

	return &DetectInfo{
		Status:     StatusDetected,
		Variant:    variant,
		ConfigPath: configPath,
	}
}
