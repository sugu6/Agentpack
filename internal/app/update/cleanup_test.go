package update

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestCleanStaleDownloads_RemovesOldFiles(t *testing.T) {
	tmpDir := t.TempDir()

	files := []struct {
		name string
		age  time.Duration
	}{
		{"old1.downloading", 1 * time.Hour},   // 新鲜，应保留（<24h）
		{"old2.downloading", 30 * time.Hour},  // 陈旧，应删除（>24h）
		{"fresh.downloading", 10 * time.Minute}, // 新鲜，应保留
		{"notDownloading.txt", 1 * time.Hour},   // 非 .downloading，应保留
	}

	for _, f := range files {
		path := filepath.Join(tmpDir, f.name)
		fh, err := os.Create(path)
		if err != nil {
			t.Fatal(err)
		}
		fh.Close()
		modTime := time.Now().Add(-f.age)
		if err := os.Chtimes(path, modTime, modTime); err != nil {
			t.Fatal(err)
		}
	}

	cleanStaleDownloads(tmpDir)

	remaining, err := os.ReadDir(tmpDir)
	if err != nil {
		t.Fatal(err)
	}
	if len(remaining) != 3 {
		t.Fatalf("Expected 3 files, got %d", len(remaining))
	}
	for _, f := range remaining {
		switch f.Name() {
		case "old1.downloading", "fresh.downloading", "notDownloading.txt":
			// 预期保留
		default:
			t.Errorf("Unexpected file: %s", f.Name())
		}
	}
}

func TestCleanStaleDownloads_EmptyDir(t *testing.T) {
	tmpDir := t.TempDir()
	// 空目录不应 panic
	cleanStaleDownloads(tmpDir)
}

func TestCleanStaleDownloads_NonExistentDir(t *testing.T) {
	// 不存在的目录应静默跳过
	cleanStaleDownloads("/nonexistent/path/that/does/not/exist")
}

func TestCleanStaleDownloads_KeepsNearBoundary(t *testing.T) {
	tmpDir := t.TempDir()

	// 设置 23h59m，确保略小于阈值（考虑测试执行时间不会超过 1 分钟）
	path := filepath.Join(tmpDir, "nearBoundary.downloading")
	fh, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	fh.Close()
	modTime := time.Now().Add(-(23*time.Hour + 59*time.Minute))
	if err := os.Chtimes(path, modTime, modTime); err != nil {
		t.Fatal(err)
	}

	cleanStaleDownloads(tmpDir)

	remaining, err := os.ReadDir(tmpDir)
	if err != nil {
		t.Fatal(err)
	}
	if len(remaining) != 1 {
		t.Fatalf("Expected 1 file, got %d", len(remaining))
	}
	if remaining[0].Name() != "nearBoundary.downloading" {
		t.Errorf("Expected nearBoundary.downloading to be kept, got %s", remaining[0].Name())
	}
}
