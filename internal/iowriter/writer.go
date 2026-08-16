package iowriter

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// maxBackupsPerBase 控制同一源文件在备份目录中保留的最大版本数。
// BackupFile 按内容 hash 命名（<base>.<hash>.bak），每次内容变化都产生
// 新文件且永不删除，长期使用（如 mcp 配置备份目录 ~/.agentpack/backups/mcp/）
// 会导致磁盘无限增长。
const maxBackupsPerBase = 20

func WriteAtomic(path string, data []byte, perm os.FileMode) error {
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return fmt.Errorf("mkdir: %w", err)
	}
	f, err := os.CreateTemp(filepath.Dir(path), filepath.Base(path)+".tmp.*")
	if err != nil {
		return fmt.Errorf("create tmp: %w", err)
	}
	tmp := f.Name()
	if _, err := f.Write(data); err != nil {
		f.Close()
		_ = os.Remove(tmp)
		return fmt.Errorf("write tmp: %w", err)
	}
	if err := f.Sync(); err != nil {
		f.Close()
		_ = os.Remove(tmp)
		return fmt.Errorf("fsync tmp: %w", err)
	}
	if err := f.Close(); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("close tmp: %w", err)
	}
	if err := os.Chmod(tmp, perm); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("chmod tmp: %w", err)
	}
	if err := os.Rename(tmp, path); err != nil {
		// 跨设备 rename 失败时，回退到目标文件系统创建临时文件并 rename
		_ = os.Remove(tmp)
		tmp2, err2 := os.CreateTemp(filepath.Dir(path), filepath.Base(path)+".fallback.*")
		if err2 != nil {
			return fmt.Errorf("rename failed (%v) and fallback tmp also failed: %w", err, err2)
		}
		tmp2Name := tmp2.Name()
		if _, err2 := tmp2.Write(data); err2 != nil {
			tmp2.Close()
			_ = os.Remove(tmp2Name)
			return fmt.Errorf("rename failed (%v) and fallback write also failed: %w", err, err2)
		}
		if err2 := tmp2.Sync(); err2 != nil {
			tmp2.Close()
			_ = os.Remove(tmp2Name)
			return fmt.Errorf("rename failed (%v) and fallback sync also failed: %w", err, err2)
		}
		if err2 := tmp2.Close(); err2 != nil {
			_ = os.Remove(tmp2Name)
			return fmt.Errorf("rename failed (%v) and fallback close also failed: %w", err, err2)
		}
		if err2 := os.Chmod(tmp2Name, perm); err2 != nil {
			_ = os.Remove(tmp2Name)
			return fmt.Errorf("rename failed (%v) and fallback chmod also failed: %w", err, err2)
		}
		if err2 := os.Rename(tmp2Name, path); err2 != nil {
			_ = os.Remove(tmp2Name)
			return fmt.Errorf("rename failed (%v) and fallback rename also failed: %w", err, err2)
		}
	}
	return nil
}

func HashFile(path string) (string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:]), nil
}

func BackupFile(path, backupDir string) (string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return "", nil
		}
		return "", err
	}
	if err := os.MkdirAll(backupDir, 0700); err != nil {
		return "", err
	}
	hash := sha256.Sum256(data)
	shortHash := hex.EncodeToString(hash[:16])
	dst := filepath.Join(backupDir, fmt.Sprintf("%s.%s.bak", filepath.Base(path), shortHash))
	if _, err := os.Stat(dst); err == nil {
		return dst, nil
	}
	if err := os.WriteFile(dst, data, 0600); err != nil {
		return "", err
	}
	pruneBackupDir(backupDir, filepath.Base(path))
	return dst, nil
}

// pruneBackupDir 保留同一源文件的最新 maxBackupsPerBase 份备份，删除更旧的。
// 仅删除匹配 <base>.<...>.bak 模式的文件，不影响备份目录中的其他内容。
func pruneBackupDir(backupDir, base string) {
	entries, err := os.ReadDir(backupDir)
	if err != nil {
		return
	}
	prefix := base + "."
	var names []string
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		if strings.HasPrefix(e.Name(), prefix) && strings.HasSuffix(e.Name(), ".bak") {
			names = append(names, e.Name())
		}
	}
	if len(names) <= maxBackupsPerBase {
		return
	}
	modTime := func(n string) (time.Time, bool) {
		fi, err := os.Stat(filepath.Join(backupDir, n))
		if err != nil {
			return time.Time{}, false
		}
		return fi.ModTime(), true
	}
	sort.SliceStable(names, func(i, j int) bool {
		ti, oki := modTime(names[i])
		tj, okj := modTime(names[j])
		if oki && okj {
			return ti.Before(tj)
		}
		return oki && !okj
	})
	for _, n := range names[:len(names)-maxBackupsPerBase] {
		if err := os.Remove(filepath.Join(backupDir, n)); err != nil {
			log.Printf("iowriter: prune backup %s: %v", n, err)
		}
	}
}
