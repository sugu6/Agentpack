package mcp

import (
	"agentpack/internal/agents"
	"path/filepath"
	"testing"
)

// 计数语义：AgentMcpCounts 反映"该 agent 配置文件里检测到多少个 MCP 服务器"，
// 未纳入管理的条目也计入（与扫描对话框的条目数一致），并按归一化 key 去重。
func TestAgentMcpCountsIncludesUnmanaged(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "mcp.json")
	writeFile(t, cfgPath, `{
		"mcpServers": {
			"git":     {"command": "npx", "args": ["-y", "@git/mcp"]},
			"mysql":   {"command": "npx", "args": ["-y", "@mysql/mcp"]},
			"play":    {"command": "npx", "args": ["-y", "@play/mcp"]}
		}
	}`)

	reg := agents.NewRegistry()
	reg.Register(agents.Agent{
		ID:         "trae-cn",
		Name:       "TraeCode CN",
		Type:       agents.TypeTraeCN,
		ConfigPath: cfgPath,
		Status:     agents.StatusDetected,
	})

	store := NewStore()
	if err := store.Load(reg); err != nil {
		t.Fatal(err)
	}

	// 空基线：三个服务器都未纳入管理，列表应为空
	if got := len(store.List()); got != 0 {
		t.Fatalf("expected no managed servers, got %d", got)
	}
	// 但计数应等于配置里检测到的数量
	counts := store.AgentMcpCounts()
	if got := counts["trae-cn"]; got != 3 {
		t.Fatalf("expected trae-cn count 3, got %d (counts=%v)", got, counts)
	}
}

func TestAgentMcpCountsDedupsByKey(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "mcp.json")
	// "seq" 与 "seq2" 归一化后同 key（同一命令+参数），只应计一次
	writeFile(t, cfgPath, `{
		"mcpServers": {
			"seq":  {"command": "npx", "args": ["-y", "@seq/mcp"]},
			"seq2": {"command": "npx", "args": ["-y", "@seq/mcp"]},
			"web":  {"command": "npx", "args": ["-y", "@web/mcp"]}
		}
	}`)

	reg := agents.NewRegistry()
	reg.Register(agents.Agent{
		ID:         "trae-cn",
		Name:       "TraeCode CN",
		Type:       agents.TypeTraeCN,
		ConfigPath: cfgPath,
		Status:     agents.StatusDetected,
	})

	store := NewStore()
	if err := store.Load(reg); err != nil {
		t.Fatal(err)
	}
	counts := store.AgentMcpCounts()
	if got := counts["trae-cn"]; got != 2 {
		t.Fatalf("expected deduped count 2, got %d (counts=%v)", got, counts)
	}
}

func TestAgentMcpCountsFallbackToBindings(t *testing.T) {
	// 未 Load（configCounts 为空）时，回退为绑定计数
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "settings.json")
	reg := agents.NewRegistry()
	reg.Register(agents.Agent{
		ID:         "claude-code",
		Name:       "Claude Code",
		Type:       agents.TypeClaudeCode,
		ConfigPath: cfgPath,
		Status:     agents.StatusDetected,
	})
	store := NewStore()
	if _, err := store.Add(Server{Name: "github", Command: "npx", Args: []string{"-y", "@gh/mcp"}, Transport: TransportStdio}, []string{"claude-code"}, reg); err != nil {
		t.Fatal(err)
	}
	counts := store.AgentMcpCounts()
	if got := counts["claude-code"]; got != 1 {
		t.Fatalf("expected fallback count 1, got %d (counts=%v)", got, counts)
	}
}
