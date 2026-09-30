package update

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// isolateCleanupMarker 将清理标记路径隔离到临时目录，返回标记文件路径。
// 供会走到 Install 成功路径（markInstallerForCleanup）及清理逻辑的测试使用，
// 避免读写真实 ~/.agentpack。
func isolateCleanupMarker(t *testing.T) string {
	t.Helper()
	marker := filepath.Join(t.TempDir(), pendingCleanupMarker)
	orig := pendingCleanupMarkerPath
	pendingCleanupMarkerPath = func() string { return marker }
	t.Cleanup(func() { pendingCleanupMarkerPath = orig })
	return marker
}

// writeInstallerFixture 写入一个符合 CI 命名规则的安装包文件。
func writeInstallerFixture(t *testing.T, dir, name string) string {
	t.Helper()
	p := filepath.Join(dir, name)
	if err := os.WriteFile(p, []byte("payload"), 0o600); err != nil {
		t.Fatalf("write installer fixture: %v", err)
	}
	return p
}

// TestCleanupRemovesInstallerAndMarker 正例：下载目录内的安装包被删除，标记一并清除。
func TestCleanupRemovesInstallerAndMarker(t *testing.T) {
	dir := setupDownloadDir(t)
	marker := isolateCleanupMarker(t)
	p := writeInstallerFixture(t, dir, "AgentPack-9.9.9-windows-amd64-installer.exe")
	if err := os.WriteFile(marker, []byte(p), 0o600); err != nil {
		t.Fatalf("write marker: %v", err)
	}

	if !cleanupPendingInstaller() {
		t.Fatal("successful cleanup must not request retry")
	}
	if _, err := os.Stat(p); !os.IsNotExist(err) {
		t.Errorf("installer should be removed, stat err = %v", err)
	}
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Errorf("marker should be removed, stat err = %v", err)
	}
}

// TestCleanupWithoutMarkerIsNoop 无标记（绝大多数启动）：同步返回，不做任何事。
func TestCleanupWithoutMarkerIsNoop(t *testing.T) {
	isolateCleanupMarker(t)
	CleanupInstalledPackage() // 有标记缺失时若误入重试分支会引入 goroutine，此处仅需不阻塞
}

// TestCleanupMissingFileClearsMarker 安装包已被用户手动删除：仅清除标记。
func TestCleanupMissingFileClearsMarker(t *testing.T) {
	dir := setupDownloadDir(t)
	marker := isolateCleanupMarker(t)
	gone := filepath.Join(dir, "AgentPack-9.9.9-windows-amd64-installer.exe")
	if err := os.WriteFile(marker, []byte(gone), 0o600); err != nil {
		t.Fatalf("write marker: %v", err)
	}

	if !cleanupPendingInstaller() {
		t.Fatal("missing file must not request retry")
	}
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Errorf("marker should be cleared, stat err = %v", err)
	}
}

// TestCleanupKeepsMarkerOnRemovalFailure 删除失败（安装器仍占用文件）：
// 保留标记以便重试，且不清除标记。
func TestCleanupKeepsMarkerOnRemovalFailure(t *testing.T) {
	dir := setupDownloadDir(t)
	marker := isolateCleanupMarker(t)
	p := writeInstallerFixture(t, dir, "AgentPack-9.9.9-windows-amd64-installer.exe")
	if err := os.WriteFile(marker, []byte(p), 0o600); err != nil {
		t.Fatalf("write marker: %v", err)
	}

	orig := removeInstallerFile
	removeInstallerFile = func(string) error { return errors.New("file in use") }
	t.Cleanup(func() { removeInstallerFile = orig })

	if cleanupPendingInstaller() {
		t.Fatal("removal failure must request retry")
	}
	if _, err := os.Stat(marker); err != nil {
		t.Errorf("marker must be kept for retry: %v", err)
	}
}

// TestCleanupRetriesAfterTransientFailure 首次删除失败（安装器尚未退出）时
// 按退避间隔重试，成功后在本次会话内完成清理。
func TestCleanupRetriesAfterTransientFailure(t *testing.T) {
	dir := setupDownloadDir(t)
	marker := isolateCleanupMarker(t)
	p := writeInstallerFixture(t, dir, "AgentPack-9.9.9-windows-amd64-installer.exe")

	origRemove := removeInstallerFile
	var calls int32
	removeInstallerFile = func(path string) error {
		// 仅首次（同步尝试）模拟文件被占用，重试即成功（cleanupRetryDelays 在
		// 本测中设为单个 1ms 间隔，无法承受多次失败）
		if atomic.AddInt32(&calls, 1) <= 1 {
			return errors.New("file in use")
		}
		return origRemove(path)
	}
	t.Cleanup(func() { removeInstallerFile = origRemove })

	origDelays := cleanupRetryDelays
	cleanupRetryDelays = []time.Duration{time.Millisecond}
	t.Cleanup(func() { cleanupRetryDelays = origDelays })

	if err := os.WriteFile(marker, []byte(p), 0o600); err != nil {
		t.Fatalf("write marker: %v", err)
	}

	CleanupInstalledPackage()

	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(marker); os.IsNotExist(err) {
			if _, err := os.Stat(p); !os.IsNotExist(err) {
				t.Errorf("installer should be removed after retry, stat err = %v", err)
			}
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("marker not cleaned within deadline")
}

// TestCleanupRefusesUnsafePaths 安全兜底（fail-closed）：仅当路径为绝对路径、
// 位于下载目录直接之下、文件名为本应用安装包、且为常规文件时才删除。
func TestCleanupRefusesUnsafePaths(t *testing.T) {
	dir := setupDownloadDir(t)
	marker := isolateCleanupMarker(t)

	outsideDir := t.TempDir()
	outside := writeInstallerFixture(t, outsideDir, "AgentPack-9.9.9-windows-amd64-installer.exe")
	notes := filepath.Join(dir, "notes.txt")
	if err := os.WriteFile(notes, []byte("x"), 0o600); err != nil {
		t.Fatalf("write notes: %v", err)
	}
	sub := filepath.Join(dir, "sub")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatalf("mkdir sub: %v", err)
	}
	inSub := writeInstallerFixture(t, sub, "AgentPack-9.9.9-windows-amd64-installer.exe")
	asDir := filepath.Join(dir, "AgentPack-9.9.9-linux-amd64.tar.gz")
	if err := os.MkdirAll(asDir, 0o755); err != nil {
		t.Fatalf("mkdir fake installer dir: %v", err)
	}
	// 目录内留一个文件，确保 os.Remove 无法把它当空目录删掉（校验必须独立生效）
	if err := os.WriteFile(filepath.Join(asDir, "keep.txt"), []byte("x"), 0o600); err != nil {
		t.Fatalf("write keep: %v", err)
	}

	cases := []struct {
		name string
		path string
		// keepTarget 该路径是否对应真实存在、必须保留的文件/目录。
		// 相对路径用例本就不指向磁盘上的文件（校验须先于任何文件操作拒绝）。
		keepTarget bool
	}{
		{"relative path", "AgentPack-9.9.9-windows-amd64-installer.exe", false},
		{"outside download dir", outside, true},
		{"non-installer name", notes, true},
		{"subdirectory", inSub, true},
		{"directory named as installer", asDir, true},
	}
	for _, c := range cases {
		if err := os.WriteFile(marker, []byte(c.path), 0o600); err != nil {
			t.Fatalf("%s: write marker: %v", c.name, err)
		}
		if !cleanupPendingInstaller() {
			t.Errorf("%s: refusal must not request retry", c.name)
		}
		if c.keepTarget {
			if _, err := os.Stat(c.path); err != nil {
				t.Errorf("%s: target must be kept, stat err = %v", c.name, err)
			}
		}
		if _, err := os.Stat(marker); !os.IsNotExist(err) {
			t.Errorf("%s: marker should be cleared after refusal, stat err = %v", c.name, err)
		}
	}
}

// TestMarkInstallerForCleanupWritesMarker 标记写入：内容为安装包绝对路径。
func TestMarkInstallerForCleanupWritesMarker(t *testing.T) {
	marker := isolateCleanupMarker(t)
	p := filepath.Join(t.TempDir(), "AgentPack-9.9.9-windows-amd64-installer.exe")

	markInstallerForCleanup(p)

	data, err := os.ReadFile(marker)
	if err != nil {
		t.Fatalf("read marker: %v", err)
	}
	if got := strings.TrimSpace(string(data)); got != p {
		t.Errorf("marker content = %q, want %q", got, p)
	}
}
