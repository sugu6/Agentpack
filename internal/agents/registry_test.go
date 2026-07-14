package agents

import (
	"os"
	"path/filepath"
	"testing"
)

func TestMain(m *testing.M) {
	restore := SetSkipRegistryLookupForTesting(true)
	defer restore()
	os.Exit(m.Run())
}

func writeJSON(t *testing.T, path, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0644); err != nil {
		t.Fatal(err)
	}
}

func TestRegistryScan_NoConfig(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("HOME", tmp)
	t.Setenv("USERPROFILE", tmp)
	t.Setenv("APPDATA", "")

	r := NewRegistry()
	r.Scan()
	// 应该能正常 Scan 而没有 panic
}

func TestRegistryScan_DetectsClaude(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("HOME", tmp)
	t.Setenv("USERPROFILE", tmp)
	t.Setenv("APPDATA", "")
	t.Setenv("PATH", tmp) // 清空 PATH 避免 CheckCommandExists 误检
	ResetNpmCache()
	ResetRegistryCache()

	writeJSON(t, filepath.Join(tmp, ".claude.json"), `{"mcpServers":{"a":{},"b":{}}}`)

	r := NewRegistry()
	r.Scan()

	got := r.Get("claude-code")
	if got == nil {
		t.Fatal("claude-code agent not found")
	}
	// 仅配置文件存在但 CLI 未安装，应为 not_found + VariantConfig
	if got.Status != StatusNotFound {
		t.Errorf("expected not_found, got %s", got.Status)
	}
	if got.Variant != VariantConfig {
		t.Errorf("expected config variant, got %s", got.Variant)
	}
}

func TestRegistryToggle_PersistsState(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("HOME", tmp)
	t.Setenv("USERPROFILE", tmp)
	t.Setenv("APPDATA", "")
	t.Setenv("PATH", tmp)
	ResetNpmCache()
	ResetRegistryCache()

	writeJSON(t, filepath.Join(tmp, ".claude.json"), `{}`)

	r := NewRegistry()
	r.Scan()

	got := r.Get("claude-code")
	if got == nil {
		t.Fatal("claude-code agent not found")
	}

	// not_found agent 不应被 toggle 为 enabled
	r.Toggle("claude-code", true)
	got = r.Get("claude-code")
	if got.Status != StatusNotFound {
		t.Errorf("expected not_found (cannot enable uninstalled agent), got %s", got.Status)
	}
}

func TestRegistryToggle_Disabled(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("HOME", tmp)
	t.Setenv("USERPROFILE", tmp)
	t.Setenv("APPDATA", "")
	t.Setenv("PATH", tmp)
	ResetNpmCache()
	ResetRegistryCache()

	writeJSON(t, filepath.Join(tmp, ".claude.json"), `{}`)

	r := NewRegistry()
	r.Scan()

	// not_found agent 不应被 toggle（无论 true 还是 false）
	r.Toggle("claude-code", true)
	r.Toggle("claude-code", false)
	got := r.Get("claude-code")
	if got == nil {
		t.Fatal("agent not found")
	}
	// 状态应保持 not_found
	if got.Status != StatusNotFound {
		t.Errorf("expected not_found (cannot toggle uninstalled agent), got %s", got.Status)
	}
}

func TestRegistryApplyDisabledRestoresState(t *testing.T) {
	r := NewRegistry()
	r.Register(Agent{ID: "a", Name: "A", Status: StatusEnabled})
	r.Register(Agent{ID: "b", Name: "B", Status: StatusDisabled})
	r.Register(Agent{ID: "missing", Name: "Missing", Status: StatusNotFound})

	r.Toggle("a", false)
	r.ApplyDisabled([]string{"b"})

	if got := r.Get("a"); got == nil || got.Status != StatusEnabled {
		t.Fatalf("expected a restored to enabled, got %#v", got)
	}
	if got := r.Get("b"); got == nil || got.Status != StatusDisabled {
		t.Fatalf("expected b disabled, got %#v", got)
	}
	if got := r.Get("missing"); got == nil || got.Status != StatusNotFound {
		t.Fatalf("expected missing to remain not_found, got %#v", got)
	}
}

func TestRegistryToggle_NotFound(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("HOME", tmp)
	t.Setenv("USERPROFILE", tmp)
	t.Setenv("APPDATA", "")

	r := NewRegistry()
	r.Scan()

	// 如果系统上安装了 claude，这个测试可能检测到它
	// 所以我们只验证 Toggle 不会 panic
	got := r.Get("claude-code")
	if got != nil && got.Status == StatusNotFound {
		r.Toggle("claude-code", true)
		got = r.Get("claude-code")
		if got.Status != StatusNotFound {
			t.Errorf("expected not_found after toggle on not_found agent, got %s", got.Status)
		}
	}
}

func TestCodexAdapter_Toml(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("HOME", tmp)
	t.Setenv("USERPROFILE", tmp)
	t.Setenv("APPDATA", "")
	t.Setenv("PATH", tmp)
	ResetNpmCache()
	ResetRegistryCache()

	// Codex 配置文件路径为 .codex/config.toml
	configDir := filepath.Join(tmp, ".codex")
	if err := os.MkdirAll(configDir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(configDir, "config.toml"), []byte("model = \"gpt-4\"\n"), 0644); err != nil {
		t.Fatal(err)
	}

	a := NewCodexAdapter()
	info := a.Detect()

	// 仅配置文件存在但 CLI 未安装，应为 not_found + VariantConfig
	if info.Status != StatusNotFound {
		t.Errorf("expected not_found, got %s", info.Status)
	}
	if info.Variant != VariantConfig {
		t.Errorf("expected config variant, got %s", info.Variant)
	}
}

func TestCursorAdapter_Detected(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("HOME", tmp)
	t.Setenv("USERPROFILE", tmp)
	t.Setenv("APPDATA", "")
	t.Setenv("PATH", tmp)
	ResetNpmCache()
	ResetRegistryCache()

	writeJSON(t, filepath.Join(tmp, ".cursor", "mcp.json"), `{"mcpServers":{}}`)

	a := NewCursorAdapter()
	info := a.Detect()
	// 仅配置文件存在但 IDE 未安装，应为 not_found + VariantConfig
	if info.Status != StatusNotFound {
		t.Errorf("expected not_found, got %s", info.Status)
	}
	if info.Variant != VariantConfig {
		t.Errorf("expected config variant, got %s", info.Variant)
	}
}

func TestOpenCodeAdapter_NestedMcpFormat(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("HOME", tmp)
	t.Setenv("USERPROFILE", tmp)
	t.Setenv("APPDATA", "")
	t.Setenv("PATH", tmp)
	ResetNpmCache()
	ResetRegistryCache()

	writeJSON(t, filepath.Join(tmp, ".config", "opencode", "opencode.json"), `{
		"mcp": {
			"servers": {
				"github": {"command": "npx", "args": ["-y", "pkg"]},
				"fs": {"command": "npx", "args": ["-y", "fs-pkg"]}
			}
		}
	}`)

	a := NewOpenCodeAdapter()
	info := a.Detect()
	// 仅配置文件存在但 CLI 未安装，应为 not_found + VariantConfig
	if info.Status != StatusNotFound {
		t.Errorf("expected not_found, got %s", info.Status)
	}
	if info.Variant != VariantConfig {
		t.Errorf("expected config variant, got %s", info.Variant)
	}
}

func TestClaudeCodeVariant(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("HOME", tmp)
	t.Setenv("USERPROFILE", tmp)
	t.Setenv("APPDATA", "")
	t.Setenv("PATH", tmp)
	ResetNpmCache()
	ResetRegistryCache()

	// 仅配置文件存在
	writeJSON(t, filepath.Join(tmp, ".claude.json"), `{}`)

	a := NewClaudeCodeAdapter()
	info := a.Detect()
	// 仅配置文件存在但 CLI/Desktop 均未安装，应为 not_found + VariantConfig
	if info.Status != StatusNotFound {
		t.Errorf("expected not_found, got %s", info.Status)
	}
	if info.Variant != VariantConfig {
		t.Errorf("expected config variant, got %s", info.Variant)
	}
}

func TestUpdateCounts(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("HOME", tmp)
	t.Setenv("USERPROFILE", tmp)
	t.Setenv("APPDATA", "")
	t.Setenv("PATH", tmp)
	ResetNpmCache()
	ResetRegistryCache()

	writeJSON(t, filepath.Join(tmp, ".claude.json"), `{}`)

	r := NewRegistry()
	r.Scan()

	got := r.Get("claude-code")
	if got == nil {
		t.Fatal("claude-code agent not found")
	}
	// 初始计数应为 0
	if got.McpCount != 0 {
		t.Errorf("expected 0, got %d", got.McpCount)
	}

	// UpdateCounts 应该能更新 not_found agent 的计数
	r.UpdateCounts(map[string]int{"claude-code": 3})

	got = r.Get("claude-code")
	if got.McpCount != 3 {
		t.Errorf("expected 3 mcp, got %d", got.McpCount)
	}
}

func TestDetectedAgentIDs(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("HOME", tmp)
	t.Setenv("USERPROFILE", tmp)
	t.Setenv("APPDATA", "")
	t.Setenv("PATH", tmp)
	ResetNpmCache()
	ResetRegistryCache()

	writeJSON(t, filepath.Join(tmp, ".claude.json"), `{}`)

	r := NewRegistry()
	r.Scan()

	// 仅配置文件存在，agent 为 not_found，不应出现在 DetectedAgentIDs 中
	ids := r.DetectedAgentIDs()
	for _, id := range ids {
		if id == "claude-code" {
			t.Errorf("claude-code should not be in detected IDs when only config exists")
		}
	}
}
