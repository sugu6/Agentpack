package database

import (
	"agentpack/internal/dbutil"
	"database/sql"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"sync"

	_ "modernc.org/sqlite"
)

var (
	db   *sql.DB
	mu   sync.RWMutex
	path string
)

// DB 返回当前数据库连接。调用方不应修改返回的连接配置。
// 如需执行数据库操作，推荐使用 WithTransaction 或在锁内使用。
func GetDB() *sql.DB {
	mu.RLock()
	defer mu.RUnlock()
	return db
}

// MustGetDB 返回当前数据库连接，如果未初始化则 panic。
// 仅在确定数据库已初始化时使用。
func MustGetDB() *sql.DB {
	db := GetDB()
	if db == nil {
		panic("database not initialized")
	}
	return db
}

const schema = `
CREATE TABLE IF NOT EXISTS mcp_servers (
  id TEXT PRIMARY KEY,
  name TEXT NOT NULL,
  description TEXT,
  command TEXT NOT NULL,
  args TEXT,
  env TEXT,
  transport TEXT DEFAULT 'stdio',
  config_type TEXT,
  url TEXT,
  timeout INTEGER,
  source TEXT,
  source_id TEXT,
  installed_at INTEGER NOT NULL,
  updated_at INTEGER NOT NULL
);

CREATE TABLE IF NOT EXISTS mcp_agent_bindings (
  mcp_id TEXT NOT NULL,
  agent_id TEXT NOT NULL,
  enabled INTEGER NOT NULL,
  synced_at INTEGER,
  PRIMARY KEY (mcp_id, agent_id)
);

CREATE TABLE IF NOT EXISTS settings (
  key TEXT PRIMARY KEY,
  value TEXT
);

CREATE TABLE IF NOT EXISTS backups (
  id TEXT PRIMARY KEY,
  created_at INTEGER NOT NULL,
  description TEXT,
  action TEXT,
  agent_id TEXT,
  agent_path TEXT,
  data TEXT NOT NULL
);

CREATE TABLE IF NOT EXISTS activity_log (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  action TEXT NOT NULL,
  target_type TEXT,
  target_id TEXT,
  agent_id TEXT,
  result TEXT,
  message TEXT,
  created_at INTEGER NOT NULL
);

CREATE TABLE IF NOT EXISTS export_snapshots (
  id TEXT PRIMARY KEY,
  name TEXT,
  version TEXT NOT NULL,
  schema_version INTEGER NOT NULL,
  mcp_count INTEGER,
  data TEXT NOT NULL,
  created_at INTEGER NOT NULL
);

CREATE INDEX IF NOT EXISTS idx_mcp_agent_bindings_agent ON mcp_agent_bindings(agent_id);
CREATE INDEX IF NOT EXISTS idx_activity_log_created ON activity_log(created_at);
CREATE INDEX IF NOT EXISTS idx_export_snapshots_created ON export_snapshots(created_at);
CREATE INDEX IF NOT EXISTS idx_mcp_servers_name ON mcp_servers(name);

`

func Init(dbPath string) error {
	if dbPath == "" {
		return fmt.Errorf("empty database path")
	}
	mu.Lock()
	defer mu.Unlock()
	if db != nil && path == dbPath {
		return nil
	}
	if db != nil {
		_ = db.Close()
		db = nil
	}
	dir := filepath.Dir(dbPath)
	if err := os.MkdirAll(dir, 0700); err != nil {
		return err
	}
	conn, err := sql.Open("sqlite", dbPath+"?_pragma=journal_mode(WAL)&_pragma=foreign_keys(1)")
	if err != nil {
		return err
	}
	conn.SetMaxOpenConns(1)
	if err := conn.Ping(); err != nil {
		_ = conn.Close()
		return err
	}
	if _, err := conn.Exec(schema); err != nil {
		_ = conn.Close()
		return err
	}
	// Drop legacy skills tables (migrated to filesystem-only storage)
	if _, err := conn.Exec(`DROP TABLE IF EXISTS skill_agent_bindings; DROP TABLE IF EXISTS skills;`); err != nil {
		log.Printf("drop legacy skills tables: %v", err)
	}
	if err := addColumnIfMissing(conn, "mcp_servers", "config_type TEXT"); err != nil {
		_ = conn.Close()
		return err
	}
	db = conn
	path = dbPath
	return nil
}

func addColumnIfMissing(conn *sql.DB, table, column string) error {
	if _, err := conn.Exec(fmt.Sprintf("ALTER TABLE %s ADD COLUMN %s", table, column)); err != nil {
		if !dbutil.IsDuplicateColumnErr(err) {
			return fmt.Errorf("migration add %s.%s: %w", table, column, err)
		}
	}
	return nil
}

func Close() error {
	mu.Lock()
	defer mu.Unlock()
	if db != nil {
		err := db.Close()
		db = nil
		path = ""
		return err
	}
	return nil
}

func DefaultDBPath() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	dir := filepath.Join(home, ".agentpack")
	if err := os.MkdirAll(dir, 0700); err != nil {
		return ""
	}
	return filepath.Join(dir, "agentpack.db")
}

func WithTransaction(fn func(tx *sql.Tx) error) error {
	db := GetDB()
	if db == nil {
		return fmt.Errorf("database not initialized")
	}
	tx, err := db.Begin()
	if err != nil {
		return err
	}
	committed := false
	defer func() {
		if !committed {
			_ = tx.Rollback()
		}
	}()
	if err := fn(tx); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	committed = true
	return nil
}
