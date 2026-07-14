package skills

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io/fs"
	"log"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"agentpack/internal/shared"
)

func ResolveSSOTDir(location StorageLocation) string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	switch location {
	case StorageUnified:
		return filepath.Join(home, ".agents", "skills")
	default:
		return filepath.Join(home, ".agentpack", "skills")
	}
}

func SyncToAgentDir(source, dest string, method SyncMethod) error {
	if !HasSkillManifest(source) {
		return fmt.Errorf("source directory %s does not contain SKILL.md", source)
	}

	parent := filepath.Dir(dest)
	if err := os.MkdirAll(parent, 0755); err != nil {
		return fmt.Errorf("create parent dir: %w", err)
	}

	// 清理 parent 目录下可能残留的 .skill-tmp-* 孤儿目录。
	// 这些是之前 copyDirAtomic 中 Rename 失败回退后遗留的临时目录。
	cleanupStaleTempDirs(parent)

	// Remove existing target if present
	if _, err := os.Lstat(dest); err == nil {
		if err := RemovePath(dest); err != nil {
			return fmt.Errorf("remove existing: %w", err)
		}
	}

	switch method {
	case SyncMethodSymlink:
		return createSymlink(source, dest)
	case SyncMethodCopy:
		return copyDirAtomic(source, dest)
	default:
		return fmt.Errorf("unsupported sync method: %s", method)
	}
}

// cleanupStaleTempDirs 清理目录下所有 .skill-tmp- 前缀的孤儿临时目录。
// 这些目录由 copyDirAtomic 创建，在 Rename 失败回退或进程中断时可能残留。
func cleanupStaleTempDirs(dir string) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	for _, entry := range entries {
		name := entry.Name()
		if !entry.IsDir() || !strings.HasPrefix(name, ".skill-tmp-") {
			continue
		}
		tmpPath := filepath.Join(dir, name)
		if err := removeAllReliable(tmpPath); err != nil {
			log.Printf("cleanup stale temp dir %s: %v", tmpPath, err)
		} else {
			log.Printf("cleanup stale temp dir %s: removed", tmpPath)
		}
	}
}

func createSymlink(source, dest string) error {
	if err := os.Symlink(source, dest); err != nil {
		// Windows: symlink 创建需要管理员权限或开发者模式。
		// 权限不足时自动回退到 copy，避免 sync 全部失败。
		log.Printf("symlink %s -> %s failed (%v), falling back to copy", dest, source, err)
		return copyDirAtomic(source, dest)
	}
	return nil
}

func copyDirAtomic(source, dest string) error {
	parent := filepath.Dir(dest)
	tmpDir, err := os.MkdirTemp(parent, ".skill-tmp-")
	if err != nil {
		return fmt.Errorf("create temp dir: %w", err)
	}
	cleanup := true
	defer func() {
		if cleanup {
			// 用 removeAllReliable 确保 Windows 上临时目录被真正清理
			if rmErr := removeAllReliable(tmpDir); rmErr != nil {
				log.Printf("copyDirAtomic: cleanup tmp dir %s: %v", tmpDir, rmErr)
			}
		}
	}()

	if err := copyDirRecursive(source, tmpDir); err != nil {
		return fmt.Errorf("copy to temp: %w", err)
	}

	if err := os.Rename(tmpDir, dest); err != nil {
		// Rename 失败（可能跨卷或 dest 被占用），回退到直接复制。
		// 注意：tmpDir 仍存在，cleanup 保持 true 以便 defer 清理。
		if copyErr := copyDirRecursive(source, dest); copyErr != nil {
			return fmt.Errorf("atomic rename failed: %w, direct copy also failed: %w", err, copyErr)
		}
		return nil
	}
	// Rename 成功：tmpDir 已变为 dest，无需清理
	cleanup = false
	return nil
}

func copyDirRecursive(src, dst string) error {
	srcAbs, err := filepath.Abs(src)
	if err != nil {
		return fmt.Errorf("resolve src abs: %w", err)
	}
	return filepath.WalkDir(src, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}

		rel, err := filepath.Rel(src, path)
		if err != nil {
			return err
		}
		target := filepath.Join(dst, rel)

		if d.IsDir() {
			return os.MkdirAll(target, 0755)
		}

		info, err := d.Info()
		if err != nil {
			return err
		}

		// 安全处理符号链接：不跟随符号链接读取目标文件内容，
		// 避免源目录中包含指向 SSOT 外部文件（如 ~/.ssh/id_rsa）的符号链接
		// 导致敏感信息被复制到 SSOT 再同步到各 agent。
		if info.Mode()&os.ModeSymlink != 0 {
			linkTarget, lerr := os.Readlink(path)
			if lerr != nil {
				return fmt.Errorf("read symlink %s: %w", path, lerr)
			}
			// 解析符号链接目标的绝对路径：
			// 绝对路径直接 Clean；相对路径基于符号链接所在目录解析。
			var resolvedTarget string
			if filepath.IsAbs(linkTarget) {
				resolvedTarget = filepath.Clean(linkTarget)
			} else {
				resolvedTarget = filepath.Clean(filepath.Join(filepath.Dir(path), linkTarget))
			}
			// 仅当目标在源目录内时才重建符号链接。
			// 使用 filepath.Rel 校验路径包含关系，防止指向外部敏感文件。
			if !isWithinDir(resolvedTarget, srcAbs) {
				log.Printf("copyDirRecursive: skipping symlink %s -> %s (target outside source dir)", path, linkTarget)
				return nil
			}
			return os.Symlink(linkTarget, target)
		}

		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		return os.WriteFile(target, data, info.Mode().Perm())
	})
}

func RemovePath(path string) error {
	info, err := os.Lstat(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}

	if info.Mode()&os.ModeSymlink != 0 {
		return os.Remove(path)
	}
	if info.IsDir() {
		return removeAllReliable(path)
	}
	return os.Remove(path)
}

// removeAllReliable 在 Windows 上 os.RemoveAll 可能因文件句柄占用（AV 扫描、
// 索引服务等）返回 nil 却未实际删除目录。此函数先尝试 os.RemoveAll，
// 若路径仍然存在则手动遍历目录树：先删除所有文件、再自底向上删除目录。
func removeAllReliable(path string) error {
	if err := os.RemoveAll(path); err != nil {
		return err
	}
	// 快速路径：os.RemoveAll 成功（大多数情况）
	if _, err := os.Stat(path); os.IsNotExist(err) {
		return nil
	}
	// 回退：手动遍历删除
	return removeAllManual(path)
}

// removeAllManual 手动遍历目录树，先删文件再自底向上删目录。
// os.Remove 对单个文件和空目录在 Windows 上可靠工作。
func removeAllManual(root string) error {
	var paths []string
	walkErr := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		paths = append(paths, p)
		return nil
	})
	if walkErr != nil {
		return walkErr
	}

	// 第一轮：删除所有文件（非目录）
	for _, p := range paths {
		info, err := os.Lstat(p)
		if err != nil {
			continue // 已删除
		}
		if !info.IsDir() {
			_ = os.Chmod(p, 0666) // 清除只读属性
			os.Remove(p)
		}
	}

	// 第二轮：自底向上删除目录（逆序，因为 WalkDir 是前序遍历）
	for i := len(paths) - 1; i >= 0; i-- {
		p := paths[i]
		info, err := os.Lstat(p)
		if err != nil {
			continue // 已删除
		}
		if info.IsDir() {
			_ = os.Chmod(p, 0777) // 清除只读属性
			if err := os.Remove(p); err != nil {
				// 最后一个目录删除失败则返回错误
				if i == 0 {
					return fmt.Errorf("remove dir %s: %w", p, err)
				}
			}
		}
	}

	// 验证
	if _, err := os.Stat(root); err == nil {
		return fmt.Errorf("path %s still exists after manual removal", root)
	}
	return nil
}

func HashDir(dir string) (string, bool) {
	h := sha256.New()
	var skipped int
	// 捕获 WalkDir 的返回错误。
	// 如果根目录不可访问，WalkDir 会立即返回错误，此时应标记为不完整。
	walkErr := filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			log.Printf("hash: skipping inaccessible path %s: %v", path, err)
			skipped++
			return nil
		}
		if d.IsDir() {
			return nil
		}
		rel, rerr := filepath.Rel(dir, path)
		if rerr != nil {
			return nil
		}
		data, rerr := os.ReadFile(path)
		if rerr != nil {
			log.Printf("hash: skipping unreadable file %s: %v", rel, rerr)
			skipped++
			return nil
		}
		h.Write([]byte(rel))
		h.Write([]byte{0})
		h.Write(data)
		h.Write([]byte{0})
		return nil
	})
	if walkErr != nil {
		// WalkDir 在根目录不可访问时返回错误，标记为不完整
		log.Printf("hash: walkdir error for %s: %v", dir, walkErr)
		skipped++
	}
	if skipped > 0 {
		log.Printf("hash: %d files skipped (incomplete hash) for %s", skipped, dir)
	}
	return hex.EncodeToString(h.Sum(nil)), skipped == 0
}

func MigrateSSOTDir(oldDir, newDir string) (int, []string) {
	if oldDir == newDir {
		return 0, nil
	}

	entries, err := os.ReadDir(oldDir)
	if err != nil {
		if os.IsNotExist(err) {
			return 0, nil
		}
		return 0, []string{fmt.Sprintf("read old dir: %v", err)}
	}

	if err := os.MkdirAll(newDir, 0755); err != nil {
		return 0, []string{fmt.Sprintf("create new dir: %v", err)}
	}

	var errs []string
	migrated := 0
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		oldPath := filepath.Join(oldDir, e.Name())
		newPath := filepath.Join(newDir, e.Name())

		// Try rename first (atomic, same filesystem)
		if err := os.Rename(oldPath, newPath); err != nil {
			// Cross-device fallback: copy + delete
			if copyErr := copyDirRecursive(oldPath, newPath); copyErr != nil {
				errs = append(errs, fmt.Sprintf("migrate %s: %v", e.Name(), copyErr))
				continue
			}
			os.RemoveAll(oldPath)
		}
		migrated++
	}

	// Try to remove old dir if empty
	os.Remove(oldDir)

	return migrated, errs
}

func BackupSkillDir(ssotDir, backupDir, skillDir string) (string, error) {
	src := filepath.Join(ssotDir, skillDir)
	if !HasSkillManifest(src) {
		return "", fmt.Errorf("skill directory %s does not contain SKILL.md", skillDir)
	}

	if err := os.MkdirAll(backupDir, 0755); err != nil {
		return "", fmt.Errorf("create backup dir: %w", err)
	}

	// Clean old backups (keep last 20)
	cleanOldBackups(backupDir, 20)

	timestamp := strings.NewReplacer(":", "", "-", "", "T", "", "Z", "").Replace(shared.NowRFC3339())
	backupName := fmt.Sprintf("%s_%s", timestamp, skillDir)
	backupPath := filepath.Join(backupDir, backupName)

	if err := copyDirRecursive(src, backupPath); err != nil {
		return "", fmt.Errorf("backup skill: %w", err)
	}

	return backupPath, nil
}

func cleanOldBackups(backupDir string, keep int) {
	entries, err := os.ReadDir(backupDir)
	if err != nil || len(entries) <= keep {
		return
	}

	// Sort by name (timestamp prefix ensures chronological order)
	sort.Slice(entries, func(i, j int) bool {
		return entries[i].Name() < entries[j].Name()
	})

	toRemove := len(entries) - keep
	for i := 0; i < toRemove; i++ {
		os.RemoveAll(filepath.Join(backupDir, entries[i].Name()))
	}
}
