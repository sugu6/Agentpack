package lockfile

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestTryAcquire_RemovesInvalidLockAndAcquires(t *testing.T) {
	dir := t.TempDir()
	// 使用测试专用 Mutex 名称，避免与运行中的 AgentPack 实例冲突
	lock, err := TryAcquireName(dir, "Local\\AgentPackTest_Acquire_"+fmt.Sprintf("%d", os.Getpid()))
	if err != nil {
		t.Fatal(err)
	}
	defer lock.Release()

	if runtime.GOOS != "windows" {
		// Unix 实现使用 PID 文件，验证文件内容
		path := filepath.Join(dir, "agentpack.lock")
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		if !strings.HasPrefix(string(data), fmt.Sprintf("%d\n", os.Getpid())) {
			t.Fatalf("expected current pid in lock, got %q", string(data))
		}
	}
}

func TestRelease_DoesNotRemoveReplacedLock(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "agentpack.lock")
	mutexName := "Local\\AgentPackTest_Release_" + fmt.Sprintf("%d", os.Getpid())
	lock, err := TryAcquireName(dir, mutexName)
	if err != nil {
		t.Fatal(err)
	}

	if runtime.GOOS != "windows" {
		// Unix 实现使用 PID 文件，验证 Release 不删除被第三方篡改的锁文件
		if err := os.WriteFile(path, []byte("999999"), 0600); err != nil {
			t.Fatal(err)
		}
		lock.Release()

		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		if string(data) != "999999" {
			t.Fatalf("expected replaced lock to remain, got %q", string(data))
		}
	} else {
		// Windows 使用 Named Mutex，验证 Release 后可以再次获取同名锁
		lock.Release()
		lock2, err := TryAcquireName(dir, mutexName)
		if err != nil {
			t.Fatalf("expected to re-acquire after release, got: %v", err)
		}
		lock2.Release()
	}
}

func TestTryAcquire_SecondAcquireFails(t *testing.T) {
	dir := t.TempDir()
	mutexName := "Local\\AgentPackTest_Second_" + fmt.Sprintf("%d", os.Getpid())
	lock, err := TryAcquireName(dir, mutexName)
	if err != nil {
		t.Fatal(err)
	}
	defer lock.Release()

	// 同名 Mutex 第二次获取应失败
	_, err = TryAcquireName(dir, mutexName)
	if err == nil {
		t.Fatal("expected second acquire to fail")
	}
}

func TestUnix_StalePidFileGetsCleaned(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Unix-only test")
	}
	dir := t.TempDir()
	path := filepath.Join(dir, "agentpack.lock")

	// 写入一个不存在的 PID（99999999 基本不可达），模拟死进程残留
	if err := os.WriteFile(path, []byte("99999999\n"), 0600); err != nil {
		t.Fatal(err)
	}

	// TryAcquire 应检测到死进程并清理
	mutexName := "Local\\AgentPackTest_Stale_" + fmt.Sprintf("%d", os.Getpid())
	lock, err := TryAcquireName(dir, mutexName)
	if err != nil {
		t.Fatalf("expected to clean stale pid and acquire, got: %v", err)
	}
	defer lock.Release()

	// 验证旧文件被删除且新锁文件包含当前 PID
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(string(data), fmt.Sprintf("%d\n", os.Getpid())) {
		t.Fatalf("expected current pid in lock, got %q", string(data))
	}
}
