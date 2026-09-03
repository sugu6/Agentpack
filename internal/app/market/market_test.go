package market

import (
	"testing"

	"agentpack/internal/agents"
	"agentpack/internal/database"
	"agentpack/internal/market"
	"agentpack/internal/mcp"
)

// TestInstallServer_Validation 验证入参校验在触碰 store 前即失败。
func TestInstallServer_Validation(t *testing.T) {
	reg := agents.NewRegistry()
	ms := mcp.NewStore()

	if _, err := InstallServer(ms, reg, market.MarketServer{}, nil); err == nil {
		t.Fatal("expected error for empty server name")
	}
	if _, err := InstallServer(ms, reg, market.MarketServer{Name: "x"}, nil); err == nil {
		t.Fatal("expected error for missing command/url")
	}
}

// TestInstallServer_NormalizesTransport 验证 transport 缺省为 stdio 并写入 store。
func TestInstallServer_NormalizesTransport(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	if err := database.Init(":memory:"); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.Close() })

	reg := agents.NewRegistry()
	reg.Register(agents.Agent{
		ID:         "claude-code",
		Name:       "Claude Code",
		Type:       agents.TypeClaudeCode,
		Status:     agents.StatusEnabled,
		ConfigPath: home + "/.claude.json",
	})
	ms := mcp.NewStore()

	srv, err := InstallServer(ms, reg, market.MarketServer{
		Name:    "demo",
		Command: "npx demo",
		Source:  "official",
	}, []string{"claude-code"})
	if err != nil {
		t.Fatalf("InstallServer returned error: %v", err)
	}
	if srv.Transport != mcp.TransportStdio {
		t.Errorf("expected default transport stdio, got %q", srv.Transport)
	}
	if srv.Source != "official" {
		t.Errorf("expected source official, got %q", srv.Source)
	}
	if got := ms.List(); len(got) != 1 {
		t.Fatalf("expected 1 server in store, got %d", len(got))
	}
}
