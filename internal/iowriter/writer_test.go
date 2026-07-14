package iowriter

import (
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

func TestWriteAtomic(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "sub", "file.txt")

	if err := WriteAtomic(path, []byte("hello"), 0644); err != nil {
		t.Fatal(err)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "hello" {
		t.Errorf("expected hello, got %s", string(data))
	}
}

func TestWriteAtomic_Overwrite(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "file.txt")

	if err := WriteAtomic(path, []byte("v1"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := WriteAtomic(path, []byte("v2"), 0644); err != nil {
		t.Fatal(err)
	}

	data, _ := os.ReadFile(path)
	if string(data) != "v2" {
		t.Errorf("expected v2, got %s", string(data))
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
	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")
	backupDir := filepath.Join(dir, "backups")

	if err := os.WriteFile(path, []byte("v1"), 0644); err != nil {
		t.Fatal(err)
	}

	if _, err := BackupFile(path, backupDir); err != nil {
		t.Fatal(err)
	}
	if _, err := BackupFile(path, backupDir); err != nil {
		t.Fatal(err)
	}

	entries, _ := os.ReadDir(backupDir)
	if len(entries) != 1 {
		t.Errorf("expected dedup to keep 1 backup, got %d", len(entries))
	}
}

func TestBackupFile_DifferentContent(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")
	backupDir := filepath.Join(dir, "backups")

	if err := os.WriteFile(path, []byte("v1"), 0644); err != nil {
		t.Fatal(err)
	}
	if _, err := BackupFile(path, backupDir); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("v2-different"), 0644); err != nil {
		t.Fatal(err)
	}
	if _, err := BackupFile(path, backupDir); err != nil {
		t.Fatal(err)
	}

	entries, _ := os.ReadDir(backupDir)
	if len(entries) != 2 {
		t.Errorf("expected 2 backups for different content, got %d", len(entries))
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
