package iowriter

import (
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

func TestWriteAtomic(t *testing.T) {
	tests := []struct {
		name   string
		rel    string   // 相对 dir 的目标路径
		writes []string // 按序写入的内容
		want   string   // 最终文件内容
	}{
		{name: "creates nested dirs", rel: filepath.Join("sub", "file.txt"), writes: []string{"hello"}, want: "hello"},
		{name: "overwrites existing", rel: "file.txt", writes: []string{"v1", "v2"}, want: "v2"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			path := filepath.Join(dir, tt.rel)
			for _, w := range tt.writes {
				if err := WriteAtomic(path, []byte(w), 0644); err != nil {
					t.Fatal(err)
				}
			}
			data, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			if string(data) != tt.want {
				t.Errorf("expected %s, got %s", tt.want, string(data))
			}
		})
	}
}

func TestWriteAtomic_RemovesTempFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "file.txt")

	if err := WriteAtomic(path, []byte("v1"), 0644); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path + ".tmp"); !os.IsNotExist(err) {
		t.Fatalf("expected tmp file removed, got err=%v", err)
	}
}

func TestBackupFile_Dedup(t *testing.T) {
	tests := []struct {
		name        string
		contents    []string // 每次写入后立即备份
		wantBackups int
	}{
		{name: "same content dedups", contents: []string{"v1", "v1"}, wantBackups: 1},
		{name: "different content keeps both", contents: []string{"v1", "v2-different"}, wantBackups: 2},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			path := filepath.Join(dir, "config.json")
			backupDir := filepath.Join(dir, "backups")
			for _, content := range tt.contents {
				if err := os.WriteFile(path, []byte(content), 0644); err != nil {
					t.Fatal(err)
				}
				if _, err := BackupFile(path, backupDir); err != nil {
					t.Fatal(err)
				}
			}
			entries, _ := os.ReadDir(backupDir)
			if len(entries) != tt.wantBackups {
				t.Errorf("expected %d backups, got %d", tt.wantBackups, len(entries))
			}
		})
	}
}

func TestBackupFile_UsesLongContentHash(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")
	backupDir := filepath.Join(dir, "backups")

	if err := os.WriteFile(path, []byte("v1"), 0644); err != nil {
		t.Fatal(err)
	}

	backupPath, err := BackupFile(path, backupDir)
	if err != nil {
		t.Fatal(err)
	}

	name := filepath.Base(backupPath)
	const suffix = ".bak"
	const basePrefix = "config.json."
	if !strings.HasPrefix(name, basePrefix) || !strings.HasSuffix(name, suffix) {
		t.Fatalf("unexpected backup filename format: %s", name)
	}
	hash := name[len(basePrefix) : len(name)-len(suffix)]
	if len(hash) < 32 {
		t.Fatalf("expected at least 128-bit hex hash, got %q", hash)
	}
}

func TestHashFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "f.txt")
	if err := os.WriteFile(path, []byte("hello"), 0644); err != nil {
		t.Fatal(err)
	}
	hash, err := HashFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(hash) != 64 {
		t.Errorf("expected 64-char sha256, got %d", len(hash))
	}
}

func TestWriteAtomic_ConcurrentSafe(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "concurrent.txt")

	// 并发写入同一文件，验证 WriteAtomic 不会导致数据损坏
	var wg sync.WaitGroup
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func(n int) {
			defer wg.Done()
			data := []byte{byte('A' + n)}
			// WriteAtomic 是原子的：要么完全写入新内容，要么保留旧内容
			_ = WriteAtomic(path, data, 0644)
		}(i)
	}
	wg.Wait()

	// 无论哪个 goroutine 最后成功，文件内容应恰好为 1 字节（非零）
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(data) != 1 {
		t.Errorf("expected 1 byte after concurrent writes, got %d", len(data))
	}
	if data[0] < 'A' || data[0] > 'J' {
		t.Errorf("expected valid content A-J, got %q", data)
	}
}
