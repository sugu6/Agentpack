package backup

import (
	"agentpack/internal/agents"
	"agentpack/internal/crypto"
	"agentpack/internal/database"
	"agentpack/internal/iowriter"
	"agentpack/internal/mcp"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"database/sql"

	"github.com/google/uuid"
)

const (
	defaultRetention = 50  // 默认保留快照数
	defaultListLimit = 100 // List 默认上限
	summaryListLimit = 500 // ListSummaries 硬上限
	summaryOutCap    = 64  // ListSummaries 输出切片初始容量
)

type Manager struct {
	mu          sync.Mutex
	wg          sync.WaitGroup
	registry    *agents.Registry
	mcpStore    *mcp.Store
	baseDir     string
	retention   int
	cfgProvider func() map[string]any // 返回应用设置，用于快照导出
}

func NewManager(baseDir string, retention int, reg *agents.Registry) *Manager {
	if retention <= 0 {
		retention = defaultRetention
	}
	return &Manager{registry: reg, baseDir: baseDir, retention: retention}
}

func (m *Manager) Bind(registry *agents.Registry, mcpStore *mcp.Store) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.registry = registry
	m.mcpStore = mcpStore
}

func (m *Manager) SetSettingsProvider(fn func() map[string]any) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.cfgProvider = fn
}

func (m *Manager) SetRetention(retention int) error {
	if retention < 0 {
		return fmt.Errorf("retention must be non-negative")
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.retention = retention
	return m.applySnapshotRetentionLocked()
}

func (m *Manager) Wait() {
	m.wg.Wait()
}

func (m *Manager) runAsync(fn func()) {
	m.wg.Add(1)
	go func() {
		defer m.wg.Done()
		defer func() {
			if r := recover(); r != nil {
				log.Printf("backup hook panic: %v", r)
			}
		}()
		fn()
	}()
}

type Summary struct {
	ID          string `json:"id"`
	CreatedAt   string `json:"createdAt"`
	Description string `json:"description"`
	Action      string `json:"action"`
	AgentID     string `json:"agentId"`
	AgentPath   string `json:"agentPath"`
	MCPCount    int    `json:"mcpCount"`
	Version     string `json:"version"`
}

func formatTime(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return t.UTC().Format(time.RFC3339)
}

func parseTime(value string) time.Time {
	if value == "" {
		return time.Time{}
	}
	t, err := time.Parse(time.RFC3339, value)
	if err != nil {
		return time.Time{}
	}
	return t.UTC()
}

func (m *Manager) Create(description, action, agentID, agentPath, data string) (Backup, error) {
	if data == "" {
		return Backup{}, fmt.Errorf("empty backup data")
	}
	now := time.Now().UTC()
	b := Backup{
		ID:          uuid.NewString(),
		CreatedAt:   now,
		Description: description,
		Action:      action,
		AgentID:     agentID,
		AgentPath:   agentPath,
		Data:        data,
		Size:        len(data),
	}
	if err := m.persist(b); err != nil {
		return Backup{}, err
	}
	return b, nil
}

func (m *Manager) List(limit int) ([]Backup, error) {
	if limit <= 0 {
		limit = defaultListLimit
	}
	db := database.GetDB()
	if db == nil {
		return []Backup{}, nil
	}
	rows, err := db.Query(`SELECT id, created_at, description, action, agent_id, agent_path, data FROM backups ORDER BY created_at DESC LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]Backup, 0, limit)
	for rows.Next() {
		var b Backup
		var created int64
		if err := rows.Scan(&b.ID, &created, &b.Description, &b.Action, &b.AgentID, &b.AgentPath, &b.Data); err != nil {
			return nil, err
		}
		b.CreatedAt = time.Unix(created, 0).UTC()
		b.Size = len(b.Data)
		out = append(out, b)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate backups: %w", err)
	}
	return out, nil
}

func (m *Manager) Get(id string) (Backup, error) {
	db := database.GetDB()
	if db == nil {
		return Backup{}, fmt.Errorf("database not initialized")
	}
	row := db.QueryRow(`SELECT id, created_at, description, action, agent_id, agent_path, data FROM backups WHERE id = ?`, id)
	var b Backup
	var created int64
	if err := row.Scan(&b.ID, &created, &b.Description, &b.Action, &b.AgentID, &b.AgentPath, &b.Data); err != nil {
		return Backup{}, fmt.Errorf("backup %s: %w", id, err)
	}
	b.CreatedAt = time.Unix(created, 0).UTC()
	b.Size = len(b.Data)
	return b, nil
}

func (m *Manager) DeleteBackup(id string) error {
	db := database.GetDB()
	if db == nil {
		return nil
	}
	_, err := db.Exec(`DELETE FROM backups WHERE id = ?`, id)
	return err
}

func (m *Manager) persist(b Backup) error {
	db := database.GetDB()
	if db == nil {
		return fmt.Errorf("database not initialized")
	}
	_, err := db.Exec(
		`INSERT INTO backups (id, created_at, description, action, agent_id, agent_path, data) VALUES (?,?,?,?,?,?,?)`,
		b.ID, b.CreatedAt.Unix(), b.Description, b.Action, b.AgentID, b.AgentPath, b.Data,
	)
	return err
}

func (m *Manager) Truncate(keep int) error {
	if keep <= 0 {
		return nil
	}
	db := database.GetDB()
	if db == nil {
		return nil
	}
	_, err := db.Exec(`
		DELETE FROM backups WHERE id NOT IN (
			SELECT id FROM backups ORDER BY created_at DESC LIMIT ?
		)
	`, keep)
	return err
}

func (m *Manager) Count() (int, error) {
	db := database.GetDB()
	if db == nil {
		return 0, nil
	}
	var n int
	if err := db.QueryRow(`SELECT COUNT(*) FROM backups`).Scan(&n); err != nil {
		return 0, err
	}
	return n, nil
}

func (m *Manager) OnMutation(action string, detail mcp.MutationDetail) {
	m.mu.Lock()
	registry := m.registry
	m.mu.Unlock()
	if registry == nil {
		return
	}
	if len(detail.Agents) == 0 {
		return
	}
	desc := fmt.Sprintf("%s server %q", action, detail.ServerName)
	// 记录所有关联的 Agent，用逗号拼接
	agentIDs := strings.Join(detail.Agents, ",")
	if _, err := m.Capture(action, agentIDs, "", desc); err != nil {
		log.Printf("auto backup on %s server %q: %v", action, detail.ServerName, err)
	}
}

func SnapshotFromStore(s *mcp.Store) []SnapshotMCP {
	if s == nil {
		return nil
	}
	servers := s.List()
	out := make([]SnapshotMCP, 0, len(servers))
	for _, srv := range servers {
		out = append(out, SnapshotMCP{
			Name:        srv.Name,
			Description: srv.Description,
			Command:     srv.Command,
			Args:        srv.Args,
			Env:         srv.Env,
			Transport:   string(srv.Transport),
			URL:         srv.URL,
			Source:      srv.Source,
			SourceID:    srv.SourceID,
			BoundAgents: srv.BoundAgents,
		})
	}
	return out
}

func (m *Manager) CreateSnapshot(snap Snapshot) (string, error) {
	snap.Version = CurrentVersion
	snap.SchemaVersion = CurrentSchemaVersion
	createdAt := parseTime(snap.CreatedAt)
	if createdAt.IsZero() {
		createdAt = time.Now().UTC()
		snap.CreatedAt = formatTime(createdAt)
	}
	if err := encryptSnapshotMCPServers(snap.MCPServers); err != nil {
		return "", err
	}
	data, err := json.Marshal(snap)
	if err != nil {
		return "", err
	}
	db := database.GetDB()
	if db == nil {
		return "", fmt.Errorf("database not initialized")
	}
	id := uuid.NewString()
	_, err = db.Exec(
		`INSERT INTO export_snapshots (id, name, version, schema_version, mcp_count, data, created_at) VALUES (?,?,?,?,?,?,?)`,
		id, snap.Description, snap.Version, snap.SchemaVersion, len(snap.MCPServers), string(data), createdAt.Unix(),
	)
	if err != nil {
		return "", err
	}
	return id, nil
}

func (m *Manager) ListSnapshots(limit int) ([]Snapshot, error) {
	if limit <= 0 {
		limit = 50
	}
	db := database.GetDB()
	if db == nil {
		return []Snapshot{}, nil
	}
	rows, err := db.Query(`SELECT id, data FROM export_snapshots ORDER BY created_at DESC LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]Snapshot, 0, limit)
	var corruptCount int
	for rows.Next() {
		var id, data string
		if err := rows.Scan(&id, &data); err != nil {
			return nil, err
		}
		var snap Snapshot
		if err := json.Unmarshal([]byte(data), &snap); err != nil {
			log.Printf("backup list: skip corrupt snapshot %s: %v", id, err)
			corruptCount++
			continue
		}
		out = append(out, snap)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate snapshots: %w", err)
	}
	if corruptCount > 0 {
		log.Printf("backup list: %d corrupt snapshot(s) skipped", corruptCount)
	}
	return out, nil
}

func (m *Manager) DeleteSnapshot(id string) error {
	db := database.GetDB()
	if db == nil {
		return nil
	}
	_, err := db.Exec(`DELETE FROM export_snapshots WHERE id = ?`, id)
	return err
}

func (m *Manager) Delete(id string) error {
	db := database.GetDB()
	if db == nil {
		return nil
	}
	return database.WithTransaction(func(tx *sql.Tx) error {
		if _, err := tx.Exec(`DELETE FROM export_snapshots WHERE id = ?`, id); err != nil {
			return err
		}
		if _, err := tx.Exec(`DELETE FROM backups WHERE id = ?`, id); err != nil {
			return err
		}
		return nil
	})
}

func (m *Manager) Capture(action, agentID, agentPath, description string) (Summary, error) {
	// 在锁内构建快照，锁外执行事务
	m.mu.Lock()
	snap, data, retention, err := m.buildSnapshotLocked(action, agentID, agentPath, description)
	m.mu.Unlock()
	if err != nil {
		return Summary{}, err
	}
	// 在不持有锁的情况下执行事务
	return m.captureWithTransaction(snap, data, retention)
}

func (m *Manager) buildSnapshotLocked(action, agentID, agentPath, description string) (Snapshot, []byte, int, error) {
	now := time.Now().UTC()
	snap := Snapshot{
		Action:        action,
		AgentID:       agentID,
		AgentPath:     agentPath,
		Description:   description,
		CreatedAt:     formatTime(now),
		Version:       CurrentVersion,
		SchemaVersion: CurrentSchemaVersion,
	}
	if m.mcpStore != nil {
		snap.MCPServers = SnapshotFromStore(m.mcpStore)
	}
	if err := encryptSnapshotMCPServers(snap.MCPServers); err != nil {
		return Snapshot{}, nil, 0, err
	}

	// 捕获当前应用的设置快照
	if m.registry != nil {
		settings := make(map[string]any)
		// 记录当前已启用/禁用的 Agent 状态
		agentStatus := make(map[string]string)
		for _, ag := range m.registry.All() {
			agentStatus[ag.ID] = string(ag.Status)
		}
		settings["agentStatus"] = agentStatus
		// 记录应用设置（主题、备份配置、技能仓库等）
		if m.cfgProvider != nil {
			if appSettings := m.cfgProvider(); appSettings != nil {
				settings["appSettings"] = appSettings
			}
		}
		snap.Settings = settings
	}

	data, err := json.Marshal(snap)
	if err != nil {
		return Snapshot{}, nil, 0, err
	}
	return snap, data, m.retention, nil
}

// 使用事务确保 INSERT 和 retention DELETE 的原子性
func (m *Manager) captureWithTransaction(snap Snapshot, data []byte, retention int) (Summary, error) {
	var savedID string
	if err := database.WithTransaction(func(tx *sql.Tx) error {
		savedID = uuid.NewString()
		_, err := tx.Exec(
			`INSERT INTO export_snapshots (id, name, version, schema_version, mcp_count, data, created_at) VALUES (?,?,?,?,?,?,?)`,
			savedID, snap.Description, snap.Version, snap.SchemaVersion, len(snap.MCPServers), string(data), parseTime(snap.CreatedAt).Unix(),
		)
		if err != nil {
			return err
		}
		// 在同一事务中应用 retention
		if retention > 0 {
			_, err := tx.Exec(`
				DELETE FROM export_snapshots WHERE id NOT IN (
					SELECT id FROM export_snapshots ORDER BY created_at DESC LIMIT ?
				)
			`, retention)
			if err != nil {
				return fmt.Errorf("apply retention: %w", err)
			}
		}
		return nil
	}); err != nil {
		return Summary{}, err
	}

	return Summary{
		ID:          savedID,
		CreatedAt:   snap.CreatedAt,
		Description: snap.Description,
		Action:      snap.Action,
		AgentID:     snap.AgentID,
		AgentPath:   snap.AgentPath,
		MCPCount:    len(snap.MCPServers),
		Version:     fmt.Sprintf("%d", snap.Version),
	}, nil
}

func (m *Manager) ListSummaries() ([]Summary, error) {
	db := database.GetDB()
	if db == nil {
		return []Summary{}, nil
	}
	rows, err := db.Query(`SELECT id, name, version, mcp_count, created_at FROM export_snapshots ORDER BY created_at DESC LIMIT ?`, summaryListLimit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]Summary, 0, summaryOutCap)
	for rows.Next() {
		var s Summary
		var ver string
		var created int64
		if err := rows.Scan(&s.ID, &s.Description, &ver, &s.MCPCount, &created); err != nil {
			return nil, err
		}
		s.Version = ver
		s.CreatedAt = formatTime(time.Unix(created, 0).UTC())
		out = append(out, s)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate summaries: %w", err)
	}
	return out, nil
}

func (m *Manager) GetSnapshot(id string) (Snapshot, error) {
	db := database.GetDB()
	if db == nil {
		return Snapshot{}, fmt.Errorf("database not initialized")
	}
	var data string
	err := db.QueryRow(`SELECT data FROM export_snapshots WHERE id = ?`, id).Scan(&data)
	if err != nil {
		return Snapshot{}, fmt.Errorf("snapshot %s: %w", id, err)
	}
	var snap Snapshot
	if err := json.Unmarshal([]byte(data), &snap); err != nil {
		return Snapshot{}, fmt.Errorf("decode snapshot: %w", err)
	}
	return snap, nil
}

func (m *Manager) ExportToFile(id, dest string) (string, error) {
	snap, err := m.GetSnapshot(id)
	if err != nil {
		return "", err
	}
	if dest == "" {
		createdAt := parseTime(snap.CreatedAt)
		if createdAt.IsZero() {
			createdAt = time.Now().UTC()
		}
		stamp := createdAt.UTC().Format("20060102-150405")
		dest = filepath.Join(m.baseDir, "backups", fmt.Sprintf("agentpack-%s.json", stamp))
	} else {
		cleanDest := filepath.Clean(dest)
		if !filepath.IsAbs(cleanDest) {
			return "", fmt.Errorf("invalid export path: must be absolute")
		}
		dest = cleanDest
	}
	if err := os.MkdirAll(filepath.Dir(dest), 0700); err != nil {
		return "", err
	}
	payload := ExportPayload{Manifest: ManifestFromSnapshot(snap), Snapshot: snap}
	data, err := json.MarshalIndent(payload, "", "  ")
	if err != nil {
		return "", err
	}
	if err := iowriter.WriteAtomic(dest, data, 0600); err != nil {
		return "", err
	}
	return dest, nil
}

func (m *Manager) applySnapshotRetentionLocked() error {
	retention := m.retention
	if retention <= 0 {
		return nil
	}
	db := database.GetDB()
	if db == nil {
		return nil
	}
	_, err := db.Exec(`
		DELETE FROM export_snapshots WHERE id NOT IN (
			SELECT id FROM export_snapshots ORDER BY created_at DESC LIMIT ?
		)
	`, retention)
	return err
}

func MCPMutationHook(mgr *Manager) mcp.MutationHandler {
	return mcp.MutationFunc(func(action string, detail mcp.MutationDetail) {
		mgr.runAsync(func() {
			mgr.OnMutation(action, detail)
		})
	})
}

func ManifestFromSnapshot(snap Snapshot) Manifest {
	return Manifest{
		Version:       snap.Version,
		SchemaVersion: snap.SchemaVersion,
		AppName:       AppName,
		ExportedAt:    snap.CreatedAt,
		MCPCount:      len(snap.MCPServers),
	}
}

// encryptSnapshotMCPServers 对快照中所有 MCP 服务器的敏感环境变量进行加密。
// 已加密的值会被跳过（EncryptEnv 内部防双重加密）。
// 作为公共辅助函数，用于 Capture、CreateSnapshot 和 Export 路径，
// 确保所有持久化路径的加密行为一致。
func encryptSnapshotMCPServers(servers []SnapshotMCP) error {
	for i, srv := range servers {
		if srv.Env != nil {
			encryptedEnv, err := crypto.EncryptEnv(srv.Env)
			if err != nil {
				return fmt.Errorf("encrypt env for server %q: %w", srv.Name, err)
			}
			servers[i].Env = encryptedEnv
		}
	}
	return nil
}
