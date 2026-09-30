package update

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
)

// TestInstallVerifyMismatchFailsClosed 落盘后摘要不符（文件被替换）：
// Install 必须拒绝执行，exec seam 零触达。
func TestInstallVerifyMismatchFailsClosed(t *testing.T) {
	var calls int32
	orig := execInstaller
	execInstaller = func(path string) error {
		atomic.AddInt32(&calls, 1)
		return nil
	}
	t.Cleanup(func() { execInstaller = orig })

	svc := newTestService(t, &eventLog{})
	dir := t.TempDir()
	p := filepath.Join(dir, "app"+installerExtFor())
	if err := os.WriteFile(p, []byte("payload"), 0o600); err != nil {
		t.Fatalf("write fixture: %v", err)
	}
	svc.mu.Lock()
	svc.file = p
	svc.expectedDigest = "0000000000000000000000000000000000000000000000000000000000000000"
	svc.mu.Unlock()

	err := svc.Install()
	if err == nil {
		t.Fatal("Install must fail when sha256 mismatches expected digest")
	}
	if !strings.Contains(err.Error(), "sha256 mismatch") {
		t.Errorf("err = %q, want sha256 mismatch message", err)
	}
	if atomic.LoadInt32(&calls) != 0 {
		t.Errorf("exec seam must not run on digest mismatch, calls=%d", calls)
	}
}

// TestInstallVerifyMatchAllowsExec 摘要一致才放行第二道校验后的 exec。
func TestInstallVerifyMatchAllowsExec(t *testing.T) {
	isolateCleanupMarker(t)
	var calls int32
	orig := execInstaller
	execInstaller = func(path string) error {
		atomic.AddInt32(&calls, 1)
		return nil
	}
	t.Cleanup(func() { execInstaller = orig })

	svc := newTestService(t, &eventLog{})
	dir := t.TempDir()
	p := filepath.Join(dir, "app"+installerExtFor())
	content := []byte("payload")
	if err := os.WriteFile(p, content, 0o600); err != nil {
		t.Fatalf("write fixture: %v", err)
	}
	sum := sha256.Sum256(content)
	svc.mu.Lock()
	svc.file = p
	svc.expectedDigest = hex.EncodeToString(sum[:])
	svc.mu.Unlock()

	if err := svc.Install(); err != nil {
		t.Fatalf("Install with matching digest: %v", err)
	}
	if atomic.LoadInt32(&calls) != 1 {
		t.Errorf("exec seam calls = %d, want 1", calls)
	}
}

// TestInstallEmptyDigestFailsClosed expectedDigest 为空（历史下载/字段缺失）
// 也必须拒绝执行：exec 前复核是无条件的，绝不放行未校验安装器。
// 本测钉住"无条件"这一性质——若被改成 expected != "" 才校验，此测即红。
func TestInstallEmptyDigestFailsClosed(t *testing.T) {
	var calls int32
	orig := execInstaller
	execInstaller = func(path string) error {
		atomic.AddInt32(&calls, 1)
		return nil
	}
	t.Cleanup(func() { execInstaller = orig })

	svc := newTestService(t, &eventLog{})
	dir := t.TempDir()
	p := filepath.Join(dir, "app"+installerExtFor())
	if err := os.WriteFile(p, []byte("payload"), 0o600); err != nil {
		t.Fatalf("write fixture: %v", err)
	}
	svc.mu.Lock()
	svc.file = p
	svc.expectedDigest = ""
	svc.mu.Unlock()

	err := svc.Install()
	if err == nil {
		t.Fatal("Install must fail when expectedDigest is empty (unconditional fail-closed)")
	}
	if !strings.Contains(err.Error(), "sha256 mismatch") {
		t.Errorf("err = %q, want sha256 mismatch", err)
	}
	if atomic.LoadInt32(&calls) != 0 {
		t.Errorf("exec seam must not run with empty digest, calls=%d", calls)
	}
}
