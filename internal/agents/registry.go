package agents

import (
	"os"
	"runtime"
	"sort"
	"sync"

	"agentpack/internal/shared"
)

type Registry struct {
	mu      sync.RWMutex
	agents  map[string]*Agent
	enabled map[string]bool
}

func NewRegistry() *Registry {
	return &Registry{
		agents:  make(map[string]*Agent),
		enabled: make(map[string]bool),
	}
}

func (r *Registry) Scan() {
	// 重置检测缓存，确保每次扫描获取最新数据
	ResetNpmCache()
	ResetRegistryCache()

	adapters := []Adapter{
		NewClaudeCodeAdapter(),
		NewOpenCodeAdapter(),
		NewCursorAdapter(),
		NewCodexAdapter(),
		NewTraeAdapter(),
		NewTraeCNAdapter(),
	}

	// Desktop 端适配器仅在 Windows 上通过注册表检测已安装应用。
	// macOS/Linux 上跳过这些适配器，避免不必要的注册表/目录枚举。
	if runtime.GOOS == "windows" {
		adapters = append(adapters,
			NewClaudeCodeDesktopAdapter(),
			NewOpenCodeDesktopAdapter(),
			NewCodexDesktopAdapter(),
		)
	}

	// 在获取写锁之前执行所有 Detect() 调用（涉及慢速 I/O：exec.Command、
	// 注册表枚举等），避免阻塞并发读操作。adapters 在此处构造且从未被
	// 其他 goroutine 共享，因此无需加锁即可安全访问。
	type detectResult struct {
		adapter Adapter
		info    *DetectInfo
	}
	results := make([]detectResult, 0, len(adapters))
	for _, a := range adapters {
		info := a.Detect()
		results = append(results, detectResult{adapter: a, info: info})
	}

	r.mu.Lock()
	defer r.mu.Unlock()

	now := shared.NowRFC3339()
	detectedIDs := make(map[string]bool)

	for _, res := range results {
		a := res.adapter
		info := res.info
		id := a.ID()

		existing, existed := r.agents[id]
		agent := existing
		if !existed {
			agent = newAgent()
			agent.ID = id
			agent.Name = a.Name()
			agent.Type = a.Type()
			agent.ConfigFormat = a.ConfigFormat()
			agent.DetectedAt = now
		}
		agent.ConfigPath = info.ConfigPath
		agent.Error = info.Error
		agent.Variant = info.Variant
		agent.LastScannedAt = now

		switch info.Status {
		case StatusNotFound:
			if agent.Status != StatusDisabled {
				agent.Status = StatusNotFound
			}
		case StatusDetected:
			// 保留用户手动 disabled 的状态，不要被 Scan 覆盖
			if !existed || agent.Status != StatusDisabled {
				agent.Status = StatusEnabled
			}
			r.enabled[id] = agent.Status == StatusEnabled
			detectedIDs[id] = true
		default:
			agent.Status = info.Status
		}

		r.agents[id] = agent
	}

	// 清理：移除不再被任何适配器管理的旧条目
	for id := range r.agents {
		if !detectedIDs[id] {
			// 用 LastScannedAt 区分两种情况：
			// 1. LastScannedAt == now：本轮被适配器处理过（如仅有配置文件的 agent），
			//    即使状态是 NotFound 也应保留——这是有意注册的条目
			// 2. LastScannedAt != now：本轮完全未被任何适配器处理，
			//    说明适配器已不再管理该 ID
			if r.agents[id].LastScannedAt == now {
				continue
			}
			prevStatus := r.agents[id].Status
			// 保留用户手动 disabled 的状态，只标记非 disabled 的为 not_found
			if prevStatus != StatusNotFound && prevStatus != StatusDisabled {
				r.agents[id].Status = StatusNotFound
			}
			// 仅删除上一轮扫描时已经是 not_found 的条目（prevStatus == NotFound）。
			// 本轮新变为 not_found 的条目（prevStatus 是 Enabled/Detected 等）保留一轮，
			// 让用户能看到"曾经可用但现已卸载"的提示。
			// 修复原 bug：原逻辑在本轮将 Status 设为 NotFound 后立即检查 == NotFound，
			// 导致曾经 enabled 的 Agent 在第一次未检测到时就被删除。
			if prevStatus == StatusNotFound {
				delete(r.agents, id)
				delete(r.enabled, id)
			}
		}
	}
}

func (r *Registry) LoadDisabled(ids []string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, id := range ids {
		delete(r.enabled, id)
		if a, ok := r.agents[id]; ok {
			a.Status = StatusDisabled
		}
	}
}

func (r *Registry) ApplyDisabled(ids []string) {
	r.mu.Lock()
	defer r.mu.Unlock()

	disabled := make(map[string]bool, len(ids))
	for _, id := range ids {
		disabled[id] = true
	}

	r.enabled = make(map[string]bool)
	for id, a := range r.agents {
		if a.Status == StatusNotFound {
			continue
		}
		if disabled[id] {
			a.Status = StatusDisabled
			continue
		}
		a.Status = StatusEnabled
		r.enabled[id] = true
	}
}

func (r *Registry) DisabledIDs() []string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]string, 0)
	for id, a := range r.agents {
		if a.Status == StatusDisabled {
			out = append(out, id)
		}
	}
	return out
}

// DetectedAgentIDs 返回所有已启用 Agent 的 ID 列表（排除 disabled 和 not_found）
func (r *Registry) DetectedAgentIDs() []string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]string, 0, len(r.agents))
	for id, a := range r.agents {
		if a.Status == StatusEnabled || a.Status == StatusDetected {
			out = append(out, id)
		}
	}
	return out
}

// AllAgentIDs 返回所有 Agent 的 ID 列表（包括 disabled 和 not_found）
func (r *Registry) AllAgentIDs() []string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]string, 0, len(r.agents))
	for id := range r.agents {
		out = append(out, id)
	}
	return out
}

// DetectedAgents 返回所有已启用的 Agent（状态为 enabled 或 detected）
func (r *Registry) DetectedAgents() []*Agent {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]*Agent, 0, len(r.agents))
	for _, a := range r.agents {
		if a.Status != StatusEnabled && a.Status != StatusDetected {
			continue
		}
		c := *a
		out = append(out, &c)
	}
	return out
}

func (r *Registry) All() []*Agent {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]*Agent, 0, len(r.agents))
	for _, a := range r.agents {
		c := *a
		out = append(out, &c)
	}
	return out
}

func (r *Registry) Get(id string) *Agent {
	r.mu.RLock()
	defer r.mu.RUnlock()
	if a, ok := r.agents[id]; ok {
		c := *a
		return &c
	}
	return nil
}

func (r *Registry) Toggle(id string, enabled bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	// 先检查 agent 是否存在，避免对不存在的 ID 在 enabled map 中留下孤儿条目
	a, ok := r.agents[id]
	if !ok {
		return
	}
	if a.Status == StatusNotFound {
		return
	}
	if enabled {
		r.enabled[id] = true
		a.Status = StatusEnabled
	} else {
		delete(r.enabled, id)
		a.Status = StatusDisabled
	}
}

// UpdateCounts 从 MCP Store 更新每个 Agent 的计数
func (r *Registry) UpdateCounts(mcpCounts map[string]int) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for id, a := range r.agents {
		a.McpCount = 0
		if c, ok := mcpCounts[id]; ok {
			a.McpCount = c
		}
	}
}

func (r *Registry) Register(agent Agent) {
	r.mu.Lock()
	defer r.mu.Unlock()
	c := agent
	r.agents[agent.ID] = &c
}

func newAgent() *Agent {
	now := shared.NowRFC3339()
	return &Agent{
		Status:        StatusNotFound,
		ConfigFormat:  FormatJSON,
		DetectedAt:    now,
		LastScannedAt: now,
	}
}

func fileExists(p string) bool {
	if p == "" {
		return false
	}
	_, err := os.Stat(p)
	return err == nil
}

func dirExists(p string) bool {
	if p == "" {
		return false
	}
	info, err := os.Stat(p)
	return err == nil && info.IsDir()
}

func homeDir() string {
	h, _ := os.UserHomeDir()
	return h
}

// SkillCapableAgentIDs returns IDs of agents that support skills and are enabled/detected
func (r *Registry) SkillCapableAgentIDs() []string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]string, 0)
	for id, a := range r.agents {
		if a.Status != StatusEnabled && a.Status != StatusDetected {
			continue
		}
		// Check if the adapter supports skills by looking at the agent's type
		dir := agentSkillsDir(id)
		if dir != "" {
			out = append(out, id)
		}
	}
	sort.Strings(out)
	return out
}

// AgentSkillsDir returns the skills directory for a given agent ID, or empty string if not supported
func (r *Registry) AgentSkillsDir(agentID string) string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return agentSkillsDir(agentID)
}

// skillDirCache caches SkillsDir per agent ID at package init to avoid
// repeated Adapter allocation.
var skillDirCache = computeSkillDirCache()

func computeSkillDirCache() map[string]string {
	m := make(map[string]string, 8)
	for id, ad := range map[string]Adapter{
		"claude-code":         NewClaudeCodeAdapter(),
		"claude-code-desktop": NewClaudeCodeDesktopAdapter(),
		"opencode":            NewOpenCodeAdapter(),
		"opencode-desktop":    NewOpenCodeDesktopAdapter(),
		"codex":               NewCodexAdapter(),
		"codex-desktop":       NewCodexDesktopAdapter(),
		"cursor":              NewCursorAdapter(),
		"trae":                NewTraeAdapter(),
		"trae-cn":             NewTraeCNAdapter(),
	} {
		m[id] = ad.SkillsDir()
	}
	return m
}

// ResetSkillDirCacheForTesting 重新计算 skillDirCache，仅用于测试。
// 调用前应先通过 t.Setenv 设置 HOME/USERPROFILE 等环境变量，
// 使 SkillsDir() 基于临时 HOME 重新计算，实现测试隔离。
func ResetSkillDirCacheForTesting() {
	skillDirCache = computeSkillDirCache()
}

// agentSkillsDir returns the skills directory for an agent ID without locking
func agentSkillsDir(agentID string) string {
	return skillDirCache[agentID]
}
