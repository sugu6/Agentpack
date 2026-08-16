package backup

import (
	"agentpack/internal/agents"
	"agentpack/internal/crypto"
	"agentpack/internal/iowriter"
	"agentpack/internal/mcp"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"strings"
	"time"
)

type Exporter struct {
	mcpStore    *mcp.Store
	registry    *agents.Registry
	baseDir     string
	cfgProvider func() map[string]any // 返回应用设置，用于导出
}

func NewExporter(ms *mcp.Store, reg *agents.Registry) *Exporter {
	return &Exporter{mcpStore: ms, registry: reg}
}

func (e *Exporter) SetBaseDir(dir string) {
	e.baseDir = dir
}

func (e *Exporter) SetSettingsProvider(fn func() map[string]any) {
	e.cfgProvider = fn
}

func (e *Exporter) Export() (Snapshot, error) {
	snap := Snapshot{
		Version:       CurrentVersion,
		SchemaVersion: CurrentSchemaVersion,
		CreatedAt:     formatTime(time.Now().UTC()),
		MCPServers:    SnapshotFromStore(e.mcpStore),
	}
	if err := encryptSnapshotMCPServers(snap.MCPServers); err != nil {
		return Snapshot{}, err
	}
	// 捕获 Agent 状态和应用设置
	if e.registry != nil {
		settings := make(map[string]any)
		agentStatus := make(map[string]string)
		for _, ag := range e.registry.All() {
			agentStatus[ag.ID] = string(ag.Status)
		}
		settings["agentStatus"] = agentStatus
		if e.cfgProvider != nil {
			if appSettings := e.cfgProvider(); appSettings != nil {
				settings["appSettings"] = appSettings
			}
		}
		snap.Settings = settings
	}
	return snap, nil
}

func (e *Exporter) ExportToWriter(w io.Writer) error {
	snap, err := e.Export()
	if err != nil {
		return err
	}
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(snap)
}

func (e *Exporter) ExportToFile(path string) error {
	if path == "" {
		return fmt.Errorf("path required")
	}
	cleanPath := filepath.Clean(path)
	if !filepath.IsAbs(cleanPath) {
		return fmt.Errorf("invalid export path: must be absolute")
	}
	snap, err := e.Export()
	if err != nil {
		return err
	}
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetIndent("", "  ")
	if err := enc.Encode(snap); err != nil {
		return err
	}
	return iowriter.WriteAtomic(cleanPath, buf.Bytes(), 0600)
}

type ImportOptions struct {
	ApplyMCP         bool
	Overwrite        bool
	ApplyAgentStatus bool // 恢复 Agent 启用/禁用状态
	ApplySettings    bool // 恢复应用设置
}

type ImportResult struct {
	MCPApplied         int            `json:"mcpApplied"`
	MCPSkipped         int            `json:"mcpSkipped"`
	AgentStatusApplied int            `json:"agentStatusApplied"`
	ExportedSettings   map[string]any `json:"exportedSettings,omitempty"` // 快照中的应用设置，由上层决定是否应用
}

func (e *Exporter) Import(snap Snapshot, opts ImportOptions) (ImportResult, error) {
	res := ImportResult{}
	// 解密前复制 MCPServers 切片：解密结果写回会污染调用方传入的 Snapshot
	//（切片共享底层数组），调用方复用同一快照二次 Import 时，明文 Env
	// 会被再次解密而失败（旧行为对 ImportFromReader 的每次调用都安全，
	// 因为快照每次重新反序列化；但对 RestoreFromBackup 等复用路径是隐患）。
	servers := append([]SnapshotMCP(nil), snap.MCPServers...)
	// 解密失败的服务器跳过而非中止整个导入：密钥不匹配（跨机器恢复、
	// 密钥文件损坏后重建）时其余可解密的服务器照常恢复，失败的计入
	// MCPSkipped 并在结果中体现，用户可重新导出/修复后重试。
	kept := servers[:0]
	for i := range servers {
		srv := &servers[i]
		if srv.Env != nil {
			decryptedEnv, err := crypto.DecryptEnv(srv.Env)
			if err != nil {
				log.Printf("import: skip server %q: decrypt env: %v", srv.Name, err)
				res.MCPSkipped++
				continue
			}
			srv.Env = decryptedEnv
		}
		kept = append(kept, *srv)
	}
	servers = kept
	for _, srv := range servers {
		if srv.Command != "" {
			if err := mcp.ValidateCommand(srv.Command); err != nil {
				return res, fmt.Errorf("server %q: %w", srv.Name, err)
			}
		}
	}
	if opts.ApplyMCP && e.mcpStore != nil {
		beforeMCP := e.mcpStore.List()
		count, err := e.applyMCP(servers, opts, &res)
		if err != nil {
			if rollbackErr := e.rollbackMCP(beforeMCP); rollbackErr != nil {
				return res, fmt.Errorf("apply mcp: %w; rollback: %v", err, rollbackErr)
			}
			return res, fmt.Errorf("apply mcp: %w", err)
		}
		res.MCPApplied = count
	}
	// 恢复 Agent 启用/禁用状态
	if opts.ApplyAgentStatus && e.registry != nil && snap.Settings != nil {
		if raw, ok := snap.Settings["agentStatus"]; ok {
			if statusMap, ok := raw.(map[string]any); ok {
				applied := e.applyAgentStatus(statusMap)
				res.AgentStatusApplied = applied
			}
		}
	}
	// 提取应用设置，由上层决定是否应用
	if snap.Settings != nil {
		if raw, ok := snap.Settings["appSettings"]; ok {
			if m, ok := raw.(map[string]any); ok {
				res.ExportedSettings = m
			}
		}
	}
	return res, nil
}

// rollbackMCP restores the complete MCP state captured before an import.
// Import currently only adds or updates servers, so restoring the pre-import
// set means removing new IDs and updating existing IDs back to their previous
// definitions and bindings.
func (e *Exporter) rollbackMCP(before []mcp.Server) error {
	if e.mcpStore == nil || e.registry == nil {
		return nil
	}
	beforeByID := make(map[string]mcp.Server, len(before))
	for _, srv := range before {
		beforeByID[srv.ID] = srv
	}

	var errs []string
	for _, current := range e.mcpStore.List() {
		old, existed := beforeByID[current.ID]
		if !existed {
			if err := e.mcpStore.Remove(current.ID, e.registry); err != nil {
				errs = append(errs, fmt.Sprintf("remove %s: %v", current.Name, err))
			}
			continue
		}
		if err := e.mcpStore.Update(old.ID, old, old.BoundAgents, e.registry); err != nil {
			errs = append(errs, fmt.Sprintf("restore %s: %v", old.Name, err))
		}
	}
	if len(errs) > 0 {
		return fmt.Errorf("MCP rollback failed: %s", strings.Join(errs, "; "))
	}
	return nil
}

// applyAgentStatus 根据快照中的 Agent 状态恢复启用/禁用
func (e *Exporter) applyAgentStatus(statusMap map[string]any) int {
	if e.registry == nil {
		return 0
	}
	// 收集需要禁用的 Agent ID（状态为 disabled 的）
	var disabledIDs []string
	for id, status := range statusMap {
		if s, ok := status.(string); ok && s == string(agents.StatusDisabled) {
			disabledIDs = append(disabledIDs, id)
		}
	}
	if len(disabledIDs) == 0 {
		return 0
	}
	e.registry.ApplyDisabled(disabledIDs)
	return len(disabledIDs)
}

func (e *Exporter) applyMCP(items []SnapshotMCP, opts ImportOptions, res *ImportResult) (int, error) {
	if e.registry == nil {
		return 0, fmt.Errorf("registry not available")
	}
	applied := 0
	for _, item := range items {
		if item.Name == "" {
			res.MCPSkipped++
			continue
		}

		// 优先使用快照中保存的 Agent 绑定关系，回退到所有当前启用的 Agent
		agentIDs := item.BoundAgents
		if len(agentIDs) > 0 {
			// 快照绑定可能含本机不存在/已禁用的 agent（跨机器恢复、本机
			// agent 集变化）：直接透传会在 mcpStore.Add/Update 的
			// validateAgentIDs 整体报错导致整个导入失败回滚。先过滤到
			// 本机可用 agent；过滤后为空再回退全部启用 agent。
			valid := make(map[string]bool)
			for _, ag := range e.registry.All() {
				if ag.Status == agents.StatusEnabled || ag.Status == agents.StatusDetected {
					valid[ag.ID] = true
				}
			}
			filtered := make([]string, 0, len(agentIDs))
			for _, id := range agentIDs {
				if valid[id] {
					filtered = append(filtered, id)
				}
			}
			agentIDs = filtered
		}
		if len(agentIDs) == 0 {
			for _, ag := range e.registry.All() {
				if ag.Status == agents.StatusEnabled || ag.Status == agents.StatusDetected {
					agentIDs = append(agentIDs, ag.ID)
				}
			}
		}
		if len(agentIDs) == 0 {
			res.MCPSkipped++
			continue
		}

		// 使用 Store.FindByName 代替全量拷贝的 findMCPByName
		existing, ok := e.mcpStore.FindByName(item.Name)
		if ok && !opts.Overwrite {
			res.MCPSkipped++
			continue
		}
		env := item.Env
		if env == nil {
			env = map[string]string{}
		}
		transport := mcp.Transport(item.Transport)
		if transport == "" {
			transport = mcp.TransportStdio
		}
		if ok {
			if err := e.mcpStore.Update(existing.ID, mcp.Server{
				Name:        item.Name,
				Description: item.Description,
				Command:     item.Command,
				Args:        item.Args,
				Env:         env,
				Transport:   transport,
				URL:         item.URL,
				Source:      item.Source,
				SourceID:    item.SourceID,
			}, agentIDs, e.registry); err != nil {
				return applied, err
			}
		} else {
			if _, err := e.mcpStore.Add(mcp.Server{
				Name:        item.Name,
				Description: item.Description,
				Command:     item.Command,
				Args:        item.Args,
				Env:         env,
				Transport:   transport,
				URL:         item.URL,
				Source:      item.Source,
				SourceID:    item.SourceID,
			}, agentIDs, e.registry); err != nil {
				// 同名不同条目的服务器已存在（同命令/URL）时跳过，不中止整个导入
				if errors.Is(err, mcp.ErrDuplicateServer) {
					res.MCPSkipped++
					continue
				}
				return applied, err
			}
		}
		applied++
	}
	return applied, nil
}

func (e *Exporter) ImportFromReader(r io.Reader, opts ImportOptions) (ImportResult, error) {
	const maxImportSize = 100 * 1024 * 1024
	data, err := io.ReadAll(io.LimitReader(r, maxImportSize+1))
	if err != nil {
		return ImportResult{}, fmt.Errorf("read import data: %w", err)
	}
	if len(data) > maxImportSize {
		return ImportResult{}, fmt.Errorf("import data exceeds maximum size of %d MB", maxImportSize/(1024*1024))
	}
	snap, err := DecodeSnapshot(data)
	if err != nil {
		return ImportResult{}, err
	}
	return e.Import(snap, opts)
}

// decodeSnapshot 解码导入数据为 Snapshot，兼容两种格式：
//  1. 裸 Snapshot（早期导入文件，或直接由 Import 序列化的快照）
//  2. ExportPayload{"manifest":..., "snapshot":...}（Manager.ExportToFile 导出的文件）
//
// Manager.ExportToFile 以 ExportPayload 包裹写出，而旧的导入路径直接按裸
// Snapshot 反序列化——Go 的 json.Unmarshal 对未知顶层 key 静默忽略，导致
// 导出文件被导入时得到空的 Snapshot（MCP/设置全部丢失但不报错）。此处先
// 检查顶层是否含 "snapshot" 字段，有则取该字段，否则整体作为 Snapshot。
// DecodeSnapshot 解码导入数据为 Snapshot，兼容两种格式：
//  1. 裸 Snapshot（早期导入文件，或直接由 Import 序列化的快照）
//  2. ExportPayload{"manifest":..., "snapshot":...}（Manager.ExportToFile 导出的文件）
//
// Manager.ExportToFile 以 ExportPayload 包裹写出，而旧的导入路径直接按裸
// Snapshot 反序列化——Go 的 json.Unmarshal 对未知顶层 key 静默忽略，导致
// 导出文件被导入时得到空的 Snapshot（MCP/设置全部丢失但不报错）。此处先
// 检查顶层是否含 "snapshot" 字段，有则取该字段，否则整体作为 Snapshot。
// 导出（decodeSnapshot 保持包内私有）供 app 层在应用 MCP 之前预检设置。
func DecodeSnapshot(data []byte) (Snapshot, error) {
	var top map[string]json.RawMessage
	if err := json.Unmarshal(data, &top); err != nil {
		return Snapshot{}, fmt.Errorf("decode: %w", err)
	}
	if raw, ok := top["snapshot"]; ok {
		var snap Snapshot
		if err := json.Unmarshal(raw, &snap); err != nil {
			return Snapshot{}, fmt.Errorf("decode snapshot payload: %w", err)
		}
		return validateSnapshotSchema(snap)
	}
	var snap Snapshot
	if err := json.Unmarshal(data, &snap); err != nil {
		return Snapshot{}, fmt.Errorf("decode: %w", err)
	}
	return validateSnapshotSchema(snap)
}

// validateSnapshotSchema 校验快照 schema 版本：新版本导出的快照包含当前
// 版本不认识的字段/结构，静默导入会丢数据。拒绝并明确报错，用户升级后重试。
func validateSnapshotSchema(snap Snapshot) (Snapshot, error) {
	if snap.SchemaVersion > CurrentSchemaVersion {
		return Snapshot{}, fmt.Errorf(
			"snapshot schema version %d is newer than supported %d; please update the app and try again",
			snap.SchemaVersion, CurrentSchemaVersion)
	}
	return snap, nil
}

func (e *Exporter) ImportFromFile(path string, opts ImportOptions) (ImportResult, error) {
	info, err := os.Stat(path)
	if err != nil {
		return ImportResult{}, fmt.Errorf("stat import file: %w", err)
	}
	const maxImportSize = 100 * 1024 * 1024
	if info.Size() > maxImportSize {
		return ImportResult{}, fmt.Errorf("import file too large: %d bytes (max %d MB)",
			info.Size(), maxImportSize/(1024*1024))
	}
	f, err := os.Open(path)
	if err != nil {
		return ImportResult{}, err
	}
	defer f.Close()
	return e.ImportFromReader(f, opts)
}

func (e *Exporter) RestoreFromBackup(mgr *Manager, id string, opts ImportOptions) (ImportResult, error) {
	snap, err := mgr.GetSnapshot(id)
	if err != nil {
		return ImportResult{}, err
	}
	return e.Import(snap, opts)
}
