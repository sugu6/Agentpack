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
			info, err := os.Stat(cmdPath)
			if err != nil {
				continue
			}
			// Windows 不校验可执行权限（PATHEXT 已限定扩展名）；
			// Unix 额外校验任意可执行位，避免把不可执行的普通文件误判为命令。
			if runtime.GOOS == "windows" || info.Mode().Perm()&0111 != 0 {
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

// markNpmCacheFailed 在 npm list -g 超时/输出不可解析时记录失败结果：
// 写入空缓存并更新时间戳，避免 60 秒内每次 CheckNpmPackageInstalled 都重新
// 触发一次带 30s 超时的 npm 子进程（多适配器扫描时会串行重试多次）。
// 该窗口内 npm 包检测返回"未安装"，CLI 检测会回退到 PATH 命令检查；
// 下次 Scan 时 ResetNpmCache 会强制重新探测。
func markNpmCacheFailed() {
	npmCacheMu.Lock()
	defer npmCacheMu.Unlock()
	if npmCache == nil {
		npmCache = make(map[string]bool)
	}
	npmCacheLoadedAt = time.Now()
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
	hideConsoleWindow(cmd)
	output, err := cmd.Output()
	if err != nil {
		// 区分两种情况：
		// 1. npm list 在有 extraneous 包时返回 exit code 1，但仍有有效输出 → 继续解析
		// 2. 超时或执行失败且无输出 → 记失败缓存，避免后续调用反复触发 30s 超时
		if len(output) == 0 {
			markNpmCacheFailed()
			return
		}
	}

	// 使用 encoding/json 解析，避免手动字符串匹配的脆弱性
	var result struct {
		Dependencies map[string]struct{} `json:"dependencies"`
	}
	if err := json.Unmarshal(output, &result); err != nil {
		// JSON 解析失败：记失败，避免每次调用都重试 npm list
		markNpmCacheFailed()
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
	hideConsoleWindow(cmd)
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

// FirstExistingFile 返回第一个存在的文件路径
func FirstExistingFile(candidates []string) string {
	for _, c := range candidates {
		if fileExists(c) {
			return c
		}
	}
	return ""
}

// pathExists 判断路径是否存在（文件或目录均可）
func pathExists(p string) bool {
	if p == "" {
		return false
	}
	_, err := os.Stat(p)
	return err == nil
}

// expandEnvPath 展开路径中的环境变量与用户主目录标记：
// 支持 %VAR%（Windows 风格）、$VAR/${VAR} 以及前导 ~。
func expandEnvPath(p string) string {
	if p == "" {
		return ""
	}
	if p == "~" || strings.HasPrefix(p, "~/") || strings.HasPrefix(p, `~\`) {
		if h := homeDir(); h != "" {
			p = filepath.Join(h, strings.TrimLeft(p, `~/\`))
		}
	}
	return os.ExpandEnv(percentToDollar(p))
}

// percentToDollar 把 %VAR% 转为 $VAR，以便 os.ExpandEnv 展开
func percentToDollar(s string) string {
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		if s[i] == '%' {
			if j := strings.IndexByte(s[i+1:], '%'); j > 0 {
				b.WriteByte('$')
				b.WriteString(s[i+1 : i+1+j])
				i += j + 1
				continue
			}
		}
		b.WriteByte(s[i])
	}
	return b.String()
}

// isRegistryEntryLive 判断 Uninstall 注册表条目是否对应磁盘上真实存在的安装。
// 卸载器常残留 Uninstall 条目：InstallLocation/UninstallString 指向已被删除的路径。
// 只有安装目录或卸载程序仍存在时才视为真实安装，否则判定为残留条目。
// 独立实现（不依赖 registry 包），便于在任意平台写单元测试。
func isRegistryEntryLive(installLocation, uninstallString string) bool {
	if installLocation != "" && dirExists(installLocation) {
		return true
	}
	// MSI 安装条目：UninstallString 形如 "MsiExec.exe /X{GUID}"（相对或绝对路径）。
	// msiexec.exe 是常驻系统组件，其存在不能证明目标应用仍在，但 MSI 卸载器
	// 会同步清理自身注册表条目（残留概率极低）；若据此过滤会把 winget/MSI
	// 安装的应用误判为未安装，故按已安装处理。
	if strings.Contains(strings.ToLower(uninstallString), "msiexec") {
		return true
	}
	p := extractUninstallPath(uninstallString)
	// 既无 InstallLocation 又无可用卸载路径的条目（仅剩 DisplayName）按残留过滤
	if p == "" {
		return false
	}
	return fileExists(p)
}

// extractUninstallPath 从 UninstallString 中提取卸载程序路径。
// 支持引号包裹（"C:\...\uninstall.exe" /S）与裸路径两种形式；
// 形如 MsiExec.exe /X{GUID} 的无绝对路径条目返回空串。
func extractUninstallPath(uninstallString string) string {
	s := strings.TrimSpace(uninstallString)
	if s == "" {
		return ""
	}
	if s[0] == '"' {
		if i := strings.IndexByte(s[1:], '"'); i >= 0 {
			p := s[1 : 1+i]
			if filepath.IsAbs(p) {
				return p
			}
		}
		return ""
	}
	// 无引号：优先尝试截取到 .exe 结尾（兼容含空格但未加引号的路径，
	// 例如 C:\Program Files\App\uninstall.exe /S）；再取第一个绝对路径 token
	if i := strings.Index(strings.ToLower(s), ".exe"); i > 0 {
		p := s[:i+len(".exe")]
		if filepath.IsAbs(p) {
			return p
		}
	}
	fields := strings.Fields(s)
	if len(fields) > 0 && filepath.IsAbs(fields[0]) {
		return fields[0]
	}
	return ""
}

// DetectIDE 通用的 IDE 检测函数
// 先通过注册表检测（Windows），失败后检查平台安装位置候选文件
// （可执行文件 / .app / desktop 文件）。
// 注意：不再用用户配置目录（%APPDATA%、~/.cursor 等）充当"已安装"证据——
// 卸载器不会删除这些目录，残留目录会把已卸载的 IDE 误判为已安装。
// excludeNames 可选，用于排除注册表中包含特定子串的条目（如 trae.go 排除 "cn"）
func DetectIDE(registryNames []string, installPaths map[string][]string, excludeNames ...string) bool {
	if len(registryNames) == 0 {
		return false
	}
	if CheckAppInstalledViaRegistryExclude(registryNames, excludeNames) {
		return true
	}
	for _, p := range installPaths[runtime.GOOS] {
		if pathExists(expandEnvPath(p)) {
			return true
		}
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
		// 全部未命中时仍保留传入的 variant，避免条目以空 variant 注册
		// （如 Desktop 适配器未检测到时的 VariantDesktop）。
		return &DetectInfo{Status: StatusNotFound, Variant: variant, ConfigPath: configPath}
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
