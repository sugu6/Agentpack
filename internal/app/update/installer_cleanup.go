package update

// 更新完成后删除安装包。
//
// 下载的安装包落在用户下载目录（~/Downloads），由本进程启动安装器：
// 主程序随即退出，而安装包文件此时正被安装器自身占用，无法在退出前删除。
// 故 Install 在启动安装器后仅记录待删路径（标记文件），由新版本首次启动时
// 删除——此时安装器已退出、文件句柄已释放。若首次尝试仍失败（安装器仍在
// 收尾），按退避间隔在本次会话内重试；应用提前退出则标记保留，下次启动继续。

import (
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
	"time"

	"agentpack/internal/config"
)

// pendingCleanupMarker 标记文件名（位于 ~/.agentpack），内容为待删除安装包的绝对路径。
const pendingCleanupMarker = "update-installer-pending"

// cleanupRetryDelays 首次删除失败后的退避重试间隔。安装完成页勾选"启动应用"
// 时安装器仍会存活数秒（正在收尾），Windows 下其对安装包的文件句柄占用会
// 使删除立即失败；退避重试让清理在本次会话内完成，不必拖到下次启动。
// 包级变量便于测试缩短间隔。
var cleanupRetryDelays = []time.Duration{5 * time.Second, 15 * time.Second, 30 * time.Second}

// pendingCleanupMarkerPath 返回标记文件路径。注入点：测试覆写为临时目录，
// 避免 Install 成功路径的测试写入真实 ~/.agentpack。
var pendingCleanupMarkerPath = func() string {
	dir := config.AgentPackDir()
	if dir == "" {
		return ""
	}
	return filepath.Join(dir, pendingCleanupMarker)
}

// removeInstallerFile 删除文件。注入点：测试模拟删除失败（文件被占用）。
var removeInstallerFile = os.Remove

// installerAssetExts 本应用发布资产的扩展名（与 CI 资产命名规则及
// validateUpdateInstaller 允许的类型对应），用于清理时的第二道校验。
var installerAssetExts = []string{".exe", ".msi", ".dmg", ".tar.gz", ".tgz"}

// markInstallerForCleanup 记录待删除的安装包路径，供新版本启动时清理。
// 尽力而为：写入失败仅记日志，不阻断安装流程。
func markInstallerForCleanup(path string) {
	marker := pendingCleanupMarkerPath()
	if marker == "" || path == "" {
		return
	}
	if err := os.MkdirAll(filepath.Dir(marker), 0o755); err != nil {
		log.Printf("update: cannot create cleanup marker dir: %v", err)
		return
	}
	if err := os.WriteFile(marker, []byte(path), 0o600); err != nil {
		log.Printf("update: cannot write cleanup marker: %v", err)
	}
}

// CleanupInstalledPackage 在应用启动时调用一次：删除上一次更新下载的安装包。
// 先同步尝试一次（多数情况一步到位），失败则在后台按退避间隔重试。
func CleanupInstalledPackage() {
	if cleanupPendingInstaller() {
		return
	}
	go func() {
		for _, d := range cleanupRetryDelays {
			time.Sleep(d)
			if cleanupPendingInstaller() {
				return
			}
		}
		log.Printf("update: downloaded installer still in use, will retry on next start")
	}()
}

// cleanupPendingInstaller 执行一次清理尝试。返回 true 表示无需重试
// （无标记、已删除、或校验拒绝后放弃）；false 表示删除失败，应保留标记重试。
func cleanupPendingInstaller() bool {
	marker := pendingCleanupMarkerPath()
	if marker == "" {
		return true
	}
	data, err := os.ReadFile(marker)
	if err != nil {
		return true // 多数启动无标记（ENOENT）；其他读失败亦不值得重试
	}
	path := strings.TrimSpace(string(data))
	if path == "" {
		_ = removeInstallerFile(marker)
		return true
	}
	// 安全校验 fail-closed：只删除"位于下载目录、形如本应用安装包"的常规文件，
	// 防止标记被篡改（~/.agentpack 用户可写）后删除任意路径。不满足即放弃并清标记。
	if err := validateInstallerCleanupPath(path); err != nil {
		log.Printf("update: skip installer cleanup (%v): %s", err, path)
		_ = removeInstallerFile(marker)
		return true
	}
	switch err := removeInstallerFile(path); {
	case err == nil:
		_ = removeInstallerFile(marker)
		log.Printf("update: removed downloaded installer after update: %s", path)
		return true
	case os.IsNotExist(err):
		// 用户已手动删除：清掉标记即可
		_ = removeInstallerFile(marker)
		return true
	default:
		return false
	}
}

// validateInstallerCleanupPath 校验待删除路径（安全兜底，fail-closed）：
// 绝对路径 + 位于下载目录内 + 文件名为本应用安装包 + 常规文件（目录/符号链接拒绝）。
func validateInstallerCleanupPath(path string) error {
	if !filepath.IsAbs(path) {
		return fmt.Errorf("not an absolute path")
	}
	if !isInstallerAssetName(filepath.Base(path)) {
		return fmt.Errorf("not an app installer file name")
	}
	// 目录包含校验用 filepath.Rel 而非字符串前缀比较：避免 /a/bc 被误判为
	// 位于 /a/b 内（项目内同类校验的既有约定）。
	rel, err := filepath.Rel(downloadDir(), filepath.Dir(path))
	if err != nil || rel != "." {
		return fmt.Errorf("outside download directory")
	}
	// Lstat 不跟随符号链接：链接一律拒绝，避免经链接间接删除他处文件；
	// 文件不存在则放行（删除时按 IsNotExist 清标记，语义等价于已完成）。
	fi, err := os.Lstat(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return fmt.Errorf("stat: %w", err)
	}
	if !fi.Mode().IsRegular() {
		return fmt.Errorf("not a regular file")
	}
	return nil
}

// isInstallerAssetName 判断文件名是否符合本应用安装包命名
// （CI 规则：AgentPack-<version>-<os>-<arch>[-installer].<ext>）。
func isInstallerAssetName(name string) bool {
	if !strings.HasPrefix(name, "AgentPack-") {
		return false
	}
	lower := strings.ToLower(name)
	for _, ext := range installerAssetExts {
		if strings.HasSuffix(lower, ext) {
			return true
		}
	}
	return false
}
