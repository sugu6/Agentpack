package backup

import (
	"agentpack/internal/agents"
	"agentpack/internal/crypto"
	"agentpack/internal/iowriter"
	"agentpack/internal/mcp"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"
)

type Exporter struct {
	mcpStore   *mcp.Store
	registry   *agents.Registry
	baseDir    string
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
	MCPApplied       int            `json:"mcpApplied"`
	MCPSkipped       int            `json:"mcpSkipped"`
	AgentStatusApplied int          `json:"agentStatusApplied"`
	ExportedSettings   map[string]any `json:"exportedSettings,omitempty"` // 快照中的应用设置，由上层决定是否应用
}

func (e *Exporter) Import(snap Snapshot, opts ImportOptions) (ImportResult, error) {
	res := ImportResult{}
	for i, srv := range snap.MCPServers {
		if srv.Env != nil {
			decryptedEnv, err := crypto.DecryptEnv(srv.Env)
			if err != nil {
				return res, fmt.Errorf("decrypt env for server %q: %w", srv.Name, err)
			}
			snap.MCPServers[i].Env = decryptedEnv
		}
	}
	for _, srv := range snap.MCPServers {
		if srv.Command != "" {
			if err := mcp.ValidateCommand(srv.Command); err != nil {
				return res, fmt.Errorf("server %q: %w", srv.Name, err)
			}
		}
	}
	if opts.ApplyMCP && e.mcpStore != nil {
		count, err := e.applyMCP(snap.MCPServers, opts, &res)
		if err != nil {
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
	var snap Snapshot
	if err := json.Unmarshal(data, &snap); err != nil {
		return ImportResult{}, fmt.Errorf("decode: %w", err)
	}
	return e.Import(snap, opts)
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
