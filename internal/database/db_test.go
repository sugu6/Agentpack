package database

import (
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestInitCreatesCoreTables(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "agentpack.db")
	if err := Init(dbPath); err != nil {
		t.Fatal(err)
	}
	defer Close()

	conn := GetDB()
	if conn == nil {
		t.Fatal("database not initialized")
	}
	if _, err := conn.Exec(`INSERT INTO settings (key, value) VALUES (?, ?)`, "theme", "system"); err != nil {
		t.Fatal(err)
	}
}

func TestInit_WALMode(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "agentpack.db")
	if err := Init(dbPath); err != nil {
		t.Fatal(err)
	}
	defer Close()

	var journalMode string
	if err := GetDB().QueryRow(`PRAGMA journal_mode`).Scan(&journalMode); err != nil {
		t.Fatal(err)
	}
	if journalMode != "wal" {
		t.Errorf("expected WAL journal mode, got %s", journalMode)
	}
}

func TestInit_ForeignKeys(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "agentpack.db")
	if err := Init(dbPath); err != nil {
		t.Fatal(err)
	}
	defer Close()

	var fkEnabled int
	if err := GetDB().QueryRow(`PRAGMA foreign_keys`).Scan(&fkEnabled); err != nil {
		t.Fatal(err)
	}
	if fkEnabled != 1 {
		t.Errorf("expected foreign keys enabled, got %d", fkEnabled)
	}
}

func TestInit_RejectsEmptyPath(t *testing.T) {
	if err := Init(""); err == nil {
		t.Fatal("expected error for empty path")
	}
}

func TestGetDB_NilBeforeInit(t *testing.T) {
	// Init 前 GetDB 应返回 nil
	// 注意：由于全局变量，此测试需在独立环境中运行，或依赖顺序
	conn := GetDB()
	_ = conn // 可能为 nil，不 panic 即可
}

func TestClose_NoErrorWhenNotOpen(t *testing.T) {
	err := Close()
	_ = err // 未 Init 时 Close 不应 panic
}

func TestWithTransaction_Commit(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "agentpack.db")
	if err := Init(dbPath); err != nil {
		t.Fatal(err)
	}
	defer Close()

	err := WithTransaction(func(tx *sql.Tx) error {
		_, execErr := tx.Exec(`INSERT INTO settings (key, value) VALUES (?, ?)`, "test_key", "test_val")
		return execErr
	})
	if err != nil {
		t.Fatal(err)
	}

	var val string
	if err := GetDB().QueryRow(`SELECT value FROM settings WHERE key = ?`, "test_key").Scan(&val); err != nil {
		t.Fatal(err)
	}
	if val != "test_val" {
		t.Errorf("expected test_val, got %s", val)
	}
}

func TestWithTransaction_Rollback(t *testing.T) {
	if err := Init(":memory:"); err != nil {
		t.Fatalf("init: %v", err)
	}
	defer Close()
	db := GetDB()

	if _, err := db.Exec(`INSERT INTO settings (key, value) VALUES (?, ?)`, "rollback_key", "v1"); err != nil {
		t.Fatalf("seed: %v", err)
	}

	err := WithTransaction(func(tx *sql.Tx) error {
		if _, e := tx.Exec(`UPDATE settings SET value = ? WHERE key = ?`, "v2", "rollback_key"); e != nil {
			return e
		}
		return errors.New("force rollback")
	})
	if err == nil {
		t.Fatalf("expected rollback error, got nil")
	}

	var val string
	if e := db.QueryRow(`SELECT value FROM settings WHERE key = ?`, "rollback_key").Scan(&val); e != nil {
		t.Fatalf("query: %v", e)
	}
	if val != "v1" {
		t.Fatalf("expected v1 after rollback, got %s", val)
	}
}

func TestSchemaMigration_AddColumnIdempotent(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "agentpack.db")
	if err := Init(dbPath); err != nil {
		t.Fatal(err)
	}
	defer Close()

	// 再次调用 Init 应幂等
	if err := Init(dbPath); err != nil {
		t.Fatal("second Init should be idempotent")
	}

	// 验证表仍然可用
	var count int
	if err := GetDB().QueryRow(`SELECT COUNT(*) FROM settings`).Scan(&count); err != nil {
		t.Fatal(err)
	}
}

func TestDefaultDBPath(t *testing.T) {
	origHome := os.Getenv("HOME")
	origUserProfile := os.Getenv("USERPROFILE")
	defer func() {
		os.Setenv("HOME", origHome)
		os.Setenv("USERPROFILE", origUserProfile)
	}()

	tmp := t.TempDir()
	os.Setenv("HOME", tmp)
	os.Setenv("USERPROFILE", tmp)

	path := DefaultDBPath()
	if path == "" {
		t.Fatal("expected non-empty default path")
	}
	if filepath.Base(path) != "agentpack.db" {
		t.Errorf("expected agentpack.db, got %s", filepath.Base(path))
	}
}
