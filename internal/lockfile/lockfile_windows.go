//go:build windows

package lockfile

import (
	"fmt"

	"golang.org/x/sys/windows"
)

// LockFile 使用 Windows Named Mutex 实现单实例锁
type LockFile struct {
	handle windows.Handle
	name   string
}

// TryAcquire 尝试获取单实例锁
// 使用 Windows Named Mutex，进程退出时系统自动释放，无需手动清理
func TryAcquire(lockDir string) (*LockFile, error) {
	return TryAcquireName(lockDir, "Global\\AgentPackSingleInstance")
}

// TryAcquireName 尝试获取指定名称的单实例锁
func TryAcquireName(lockDir string, mutexName string) (*LockFile, error) {
	name, err := windows.UTF16PtrFromString(mutexName)
	if err != nil {
		return nil, fmt.Errorf("convert mutex name: %w", err)
	}

	handle, err := windows.CreateMutex(nil, false, name)
	if err != nil {
		return nil, fmt.Errorf("create mutex: %w", err)
	}
	// CreateMutex returns a valid handle even when ERROR_ALREADY_EXISTS occurs.
	// We must check GetLastError immediately to detect the existing instance.
	if windows.GetLastError() == windows.ERROR_ALREADY_EXISTS {
		windows.CloseHandle(handle)
		return nil, fmt.Errorf("another AgentPack instance is running")
	}

	return &LockFile{handle: handle, name: mutexName}, nil
}

// Release 释放锁（关闭 Mutex handle）
func (l *LockFile) Release() {
	if l == nil || l.handle == 0 {
		return
	}
	windows.CloseHandle(l.handle)
	l.handle = 0
}
