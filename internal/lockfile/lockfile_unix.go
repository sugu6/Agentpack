//go:build !windows

package lockfile

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
)

// LockFile 使用 PID 文件防止多个 AgentPack 实例同时运行
type LockFile struct {
	path    string
	file    *os.File
	content string
}

// TryAcquire 尝试获取文件锁
// 使用 O_EXCL 原子创建锁文件，避免 TOCTOU 竞态。
// 已存在的锁文件通过 PID 检验进程存活状态，
// 权限错误（Permission denied）时保守假设进程存活。
func TryAcquire(lockDir string) (*LockFile, error) {
	if lockDir == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return nil, fmt.Errorf("cannot determine home directory for lock file: %w", err)
		}
		if home == "" {
			return nil, fmt.Errorf("home directory is empty, cannot create lock file")
		}
		lockDir = filepath.Join(home, ".agentpack")
	}

	if err := os.MkdirAll(lockDir, 0700); err != nil {
		return nil, fmt.Errorf("create lock dir: %w", err)
	}

	path := filepath.Join(lockDir, "agentpack.lock")

	// 尝试原子创建锁文件。O_EXCL 确保只有一个进程能成功。
	f, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE|os.O_EXCL, 0600)
	if err == nil {
		content := strconv.Itoa(os.Getpid()) + "\n"
		if _, werr := f.WriteString(content); werr != nil {
			f.Close()
			os.Remove(path)
			return nil, fmt.Errorf("write lock: %w", werr)
		}
		return &LockFile{path: path, file: f, content: content}, nil
	}

	if !os.IsExist(err) {
		return nil, fmt.Errorf("acquire lock: %w", err)
	}

	// 锁文件已存在，读取 PID 检查进程是否存活
	data, rerr := os.ReadFile(path)
	if rerr != nil {
		// 无法读取锁文件，视为残留文件，删除并重试
		os.Remove(path)
		return TryAcquire(lockDir)
	}

	pidStr := strings.TrimSpace(string(data))
	pid, perr := strconv.Atoi(pidStr)
	if perr != nil || pid <= 0 {
		os.Remove(path)
		return TryAcquire(lockDir)
	}

	alive, err := isProcessAlive(pid)
	if err != nil || alive {
		// 权限错误或进程存活时，保守认为已有实例运行
		return nil, fmt.Errorf("another AgentPack instance is running (PID %d)", pid)
	}

	// 进程已不存在，删除旧锁文件并重试
	os.Remove(path)
	return TryAcquire(lockDir)
}

// TryAcquireName acquires a lock in the given directory. The name parameter is
// ignored on Unix platforms (file-based locking uses a fixed filename).
func TryAcquireName(lockDir, name string) (*LockFile, error) {
	return TryAcquire(lockDir)
}

// isProcessAlive 检查进程是否存活。
// 使用 signal(0) 检测，权限错误视为进程存活（保守安全）。
func isProcessAlive(pid int) (bool, error) {
	proc, err := os.FindProcess(pid)
	if err != nil {
		// FindProcess 在 Unix 上通常不会失败，除非 pid 无效
		return false, nil
	}
	if err := proc.Signal(syscall.Signal(0)); err != nil {
		// ESRCH = 进程不存在
		if err == syscall.ESRCH {
			return false, nil
		}
		// EPERM = 权限不足，无法确定进程是否存活，保守返回存活
		return true, err
	}
	return true, nil
}

// Release 释放锁
func (l *LockFile) Release() {
	if l == nil || l.file == nil {
		return
	}
	data, err := os.ReadFile(l.path)
	if err == nil && string(data) == l.content {
		os.Remove(l.path)
	}
	l.file.Close()
}
