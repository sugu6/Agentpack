package update

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"
)

// installerExtFor 返回当前 GOOS 下合法的安装包扩展名（未知平台返回 ""）。
func installerExtFor() string {
	switch runtime.GOOS {
	case "windows":
		return ".exe"
	case "darwin":
		return ".dmg"
	case "linux":
		return ".tar.gz"
	}
	return ""
}

// wrongInstallerExt 返回当前 GOOS 下不合法的安装包扩展名。
func wrongInstallerExt() string {
	for _, e := range []string{".exe", ".dmg", ".tar.gz"} {
		if e != installerExtFor() {
			return e
		}
	}
	return ".txt"
}

// TestInstallExecutesDownloadedInstaller 已下载且摘要校验通过的安装包：
// 调用 exec seam 且参数为最终文件路径（不真实启动进程）；同时写入清理标记
// （新版本启动时据此删除安装包）。
func TestInstallExecutesDownloadedInstaller(t *testing.T) {
	marker := isolateCleanupMarker(t)
	var gotPath string
	var calls int32
	orig := execInstaller
	execInstaller = func(path string) error {
		atomic.AddInt32(&calls, 1)
		gotPath = path
		return nil
	}
	t.Cleanup(func() { execInstaller = orig })

	svc := newTestService(t, &eventLog{})
	dir := t.TempDir()
	p := filepath.Join(dir, "installed"+installerExtFor())
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
		t.Fatalf("Install: %v", err)
	}
	if atomic.LoadInt32(&calls) != 1 {
		t.Fatalf("exec seam called %d times, want 1", calls)
	}
	if gotPath != p {
		t.Errorf("exec seam path = %q, want %q", gotPath, p)
	}
	data, err := os.ReadFile(marker)
	if err != nil {
		t.Fatalf("cleanup marker should be written after successful exec: %v", err)
	}
	if got := strings.TrimSpace(string(data)); got != p {
		t.Errorf("marker content = %q, want installer path %q", got, p)
	}
}

// TestInstallRejectsInvalidInstallerWithoutExec 各种非法态：报错且绝不触达 exec。
// （非法态全部先于摘要复核被拒绝，故无需设置 expectedDigest。）
func TestInstallRejectsInvalidInstallerWithoutExec(t *testing.T) {
	dir := t.TempDir()
	write := func(name, content string) string {
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
			t.Fatalf("write fixture: %v", err)
		}
		return p
	}

	cases := []struct {
		name string
		file string // "" 表示未下载
	}{
		{"no downloaded installer", ""},
		{"missing file", filepath.Join(dir, "nope"+installerExtFor())},
		{"empty file", write("empty"+installerExtFor(), "")},
	}
	if ext := installerExtFor(); ext != "" {
		cases = append(cases, struct {
			name string
			file string
		}{"wrong extension", write("app"+wrongInstallerExt(), "payload")})
	}

	for _, c := range cases {
		var calls int32
		orig := execInstaller
		execInstaller = func(path string) error {
			atomic.AddInt32(&calls, 1)
			return nil
		}
		svc := newTestService(t, &eventLog{})
		if c.file != "" {
			svc.mu.Lock()
			svc.file = c.file
			svc.mu.Unlock()
		}
		err := svc.Install()
		execInstaller = orig
		if err == nil {
			t.Errorf("%s: want error, got nil", c.name)
		}
		if atomic.LoadInt32(&calls) != 0 {
			t.Errorf("%s: exec seam must not be called, got %d calls", c.name, calls)
		}
	}
}
