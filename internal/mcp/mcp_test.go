package mcp

import (
	"agentpack/internal/agents"
	"agentpack/internal/database"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

var testDBDirs []string

func TestMain(m *testing.M) {
	// 允许测试临时目录通过 isSafeAgentConfigPath 验证
	os.Setenv("AGENTPACK_ALLOW_TEMP_DIR", "1")
	if err := initTestDB(); err != nil {
		panic(err)
	}
	code := m.Run()
	_ = database.Close()
	for _, dir := range testDBDirs {
		_ = os.RemoveAll(dir)
	}
	os.Exit(code)
}

func initTestDB() error {
	dir, err := os.MkdirTemp("", "agentpack-mcp-test-*")
	if err != nil {
		return err
	}
	testDBDirs = append(testDBDirs, dir)
	return database.Init(filepath.Join(dir, "test.db"))
}

func resetTestDB(t *testing.T) {
	t.Helper()
	// 关闭旧连接，避免 SQLite 单连接模式下连接泄漏
	_ = database.Close()
	if err := initTestDB(); err != nil {
		t.Fatal(err)
	}
}

func writeFile(t *testing.T, path, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0644); err != nil {
		t.Fatal(err)
	}
}

func TestJsonBackend_ReadWrite(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "mcp.json")

	backend := NewJsonBackend("claude-code")
	servers, err := backend.Read(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(servers) != 0 {
		t.Fatalf("expected empty, got %d", len(servers))
	}

	in := map[string]Server{
		"github": {
			ID:        "test-id",
			Name:      "github",
			Command:   "npx",
			Args:      []string{"-y", "@mcp/server-github"},
			Env:       map[string]string{"GITHUB_TOKEN": "abc"},
			Transport: TransportStdio,
		},
	}
	if err := backend.Write(path, in); err != nil {
		t.Fatal(err)
	}

	out, err := backend.Read(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(out) != 1 {
		t.Fatalf("expected 1, got %d", len(out))
	}
	got := out["github"]
	if got.Command != "npx" {
		t.Errorf("expected npx, got %q", got.Command)
	}
	if got.Env["GITHUB_TOKEN"] != "abc" {
		t.Errorf("expected token abc, got %q", got.Env["GITHUB_TOKEN"])
	}
	if got.ID == "" {
		t.Error("expected stable ID after read")
	}
}

func TestJsonBackend_WriteRejectsInvalidExistingJSON(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "mcp.json")
	original := `{"model":`
	writeFile(t, path, original)

	backend := NewJsonBackend("claude-code")
	err := backend.Write(path, map[string]Server{
		"github": {Name: "github", Command: "npx", Transport: TransportStdio},
	})
	if err == nil {
		t.Fatal("expected invalid JSON error")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != original {
		t.Fatalf("expected original file to remain unchanged, got %q", string(data))
	}
}

func TestTomlBackend_ReadWrite(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.toml")

	backend := NewTomlBackend()
	in := map[string]Server{
		"alpha": {
			ID:        "alpha-id",
			Name:      "alpha",
			Command:   "echo",
			Args:      []string{"hello"},
			Transport: TransportStdio,
		},
	}
	if err := backend.Write(path, in); err != nil {
		t.Fatal(err)
	}

	out, err := backend.Read(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(out) != 1 {
		t.Fatalf("expected 1, got %d", len(out))
	}
	got := out["alpha"]
	if got.Command != "echo" {
		t.Errorf("expected echo, got %q", got.Command)
	}
	if len(got.Args) != 1 || got.Args[0] != "hello" {
		t.Errorf("expected [hello], got %v", got.Args)
	}
}

func TestTomlBackend_WriteRejectsInvalidExistingTOML(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.toml")
	original := "model = \"gpt-4\n"
	writeFile(t, path, original)

	backend := NewTomlBackend()
	err := backend.Write(path, map[string]Server{
		"alpha": {Name: "alpha", Command: "echo", Transport: TransportStdio},
	})
	if err == nil {
		t.Fatal("expected invalid TOML error")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != original {
		t.Fatalf("expected original file to remain unchanged, got %q", string(data))
	}
}

func TestStore_AddRemoveToggle(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("APPDATA", "")

	claudePath := filepath.Join(home, ".claude.json")
	writeFile(t, claudePath, `{}`)

	reg := agents.NewRegistry()
	// 直接注册测试需要的 agent 而不是依赖于 Scan()
	testAgent := agents.Agent{
		ID:           "claude-code",
		Name:         "Claude Code",
		Type:         agents.TypeClaudeCode,
		Status:       agents.StatusEnabled,
		ConfigPath:   claudePath,
		ConfigFormat: agents.FormatJSON,
	}
	reg.Register(testAgent)

	store := NewStore()
	if err := store.Load(reg); err != nil {
		t.Fatal(err)
	}

	server := Server{
		Name:      "github",
		Command:   "npx",
		Args:      []string{"-y", "@mcp/server-github"},
		Transport: TransportStdio,
	}
	if _, err := store.Add(server, []string{"claude-code"}, reg); err != nil {
		t.Fatal(err)
	}

	list := store.List()
	if len(list) != 1 {
		t.Fatalf("expected 1 server, got %d", len(list))
	}
	id := list[0].ID
	if id == "" {
		t.Fatal("expected non-empty ID")
	}

	backend := NewBackend("claude-code")
	disk, err := backend.Read(claudePath)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := disk["github"]; !ok {
		t.Error("expected github on disk")
	}

	if err := store.ToggleAgent(id, "claude-code", false, reg); err != nil {
		t.Fatal(err)
	}
	disk, _ = backend.Read(claudePath)
	if _, ok := disk["github"]; ok {
		t.Error("expected github removed from disk")
	}
	if store.AgentBound(id, "claude-code") {
		t.Error("expected binding cleared")
	}

	if err := store.ToggleAgent(id, "claude-code", true, reg); err != nil {
		t.Fatal(err)
	}
	disk, _ = backend.Read(claudePath)
	if _, ok := disk["github"]; !ok {
		t.Error("expected github re-added to disk")
	}

	if err := store.Remove(id, reg); err != nil {
		t.Fatal(err)
	}
	disk, _ = backend.Read(claudePath)
	if _, ok := disk["github"]; ok {
		t.Error("expected github removed from disk after Remove")
	}
	if _, ok := store.Get(id); ok {
		t.Error("expected server gone from store")
	}
}

func TestStore_AddAllowsHomeConfigWithoutTempOverride(t *testing.T) {
	t.Setenv("AGENTPACK_ALLOW_TEMP_DIR", "")
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("APPDATA", "")

	claudePath := filepath.Join(home, ".claude.json")
	writeFile(t, claudePath, `{}`)

	reg := agents.NewRegistry()
	reg.Register(agents.Agent{
		ID:           "claude-code",
		Name:         "Claude Code",
		Type:         agents.TypeClaudeCode,
		Status:       agents.StatusEnabled,
		ConfigPath:   claudePath,
		ConfigFormat: agents.FormatJSON,
	})

	store := NewStore()
	if err := store.Load(reg); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Add(Server{Name: "github", Command: "npx", Transport: TransportStdio}, []string{"claude-code"}, reg); err != nil {
		t.Fatal(err)
	}
}

func TestStore_AddAllowsRegistryStyleName(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("APPDATA", "")

	claudePath := filepath.Join(home, ".claude.json")
	writeFile(t, claudePath, `{}`)

	reg := agents.NewRegistry()
	reg.Register(agents.Agent{
		ID:           "claude-code",
		Name:         "Claude Code",
		Type:         agents.TypeClaudeCode,
		Status:       agents.StatusEnabled,
		ConfigPath:   claudePath,
		ConfigFormat: agents.FormatJSON,
	})

	store := NewStore()
	if err := store.Load(reg); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Add(Server{Name: "io.example/filesystem", Command: "npx", Transport: TransportStdio}, []string{"claude-code"}, reg); err != nil {
		t.Fatal(err)
	}

	disk, err := NewBackend("claude-code").Read(claudePath)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := disk["io.example/filesystem"]; !ok {
		t.Fatalf("expected registry-style server name on disk, got %v", disk)
	}
}

func TestStore_AddRejectsInvalidPathBeforeWriting(t *testing.T) {
	t.Setenv("AGENTPACK_ALLOW_TEMP_DIR", "")
	base := t.TempDir()
	home := filepath.Join(base, "home")
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("APPDATA", "")

	claudePath := filepath.Join(home, ".claude.json")
	cursorPath := filepath.Join(base, "outside", "mcp.json")
	writeFile(t, claudePath, `{}`)
	writeFile(t, cursorPath, `{}`)

	reg := agents.NewRegistry()
	reg.Register(agents.Agent{ID: "claude-code", Name: "Claude Code", Type: agents.TypeClaudeCode, Status: agents.StatusEnabled, ConfigPath: claudePath, ConfigFormat: agents.FormatJSON})
	reg.Register(agents.Agent{ID: "cursor", Name: "Cursor", Type: agents.TypeCursor, Status: agents.StatusEnabled, ConfigPath: cursorPath, ConfigFormat: agents.FormatJSON})

	store := NewStore()
	if err := store.Load(reg); err != nil {
		t.Fatal(err)
	}

	_, err := store.Add(Server{Name: "github", Command: "npx", Transport: TransportStdio}, []string{"claude-code", "cursor"}, reg)
	if err == nil {
		t.Fatal("expected invalid path error")
	}
	data, err := os.ReadFile(claudePath)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != `{}` {
		t.Fatalf("expected valid config to remain unchanged, got %s", data)
	}
	if got := store.List(); len(got) != 0 {
		t.Fatalf("expected no server recorded after failed add, got %d", len(got))
	}
}

func TestStore_AddRollsBackSuccessfulWritesOnLaterFailure(t *testing.T) {
	t.Setenv("AGENTPACK_ALLOW_TEMP_DIR", "")
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("APPDATA", "")

	claudePath := filepath.Join(home, ".claude.json")
	cursorPath := filepath.Join(home, ".cursor", "mcp.json")
	writeFile(t, claudePath, `{}`)
	writeFile(t, cursorPath, `{}`)

	reg := agents.NewRegistry()
	reg.Register(agents.Agent{ID: "claude-code", Name: "Claude Code", Type: agents.TypeClaudeCode, Status: agents.StatusEnabled, ConfigPath: claudePath, ConfigFormat: agents.FormatJSON})
	reg.Register(agents.Agent{ID: "cursor", Name: "Cursor", Type: agents.TypeCursor, Status: agents.StatusEnabled, ConfigPath: cursorPath, ConfigFormat: agents.FormatJSON})

	store := NewStore()
	if err := store.Load(reg); err != nil {
		t.Fatal(err)
	}
	writeFile(t, cursorPath, `{"mcpServers":`)

	_, err := store.Add(Server{Name: "github", Command: "npx", Transport: TransportStdio}, []string{"claude-code", "cursor"}, reg)
	if err == nil {
		t.Fatal("expected invalid JSON error")
	}
	data, err := os.ReadFile(claudePath)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != `{}` {
		t.Fatalf("expected first config to be rolled back, got %s", data)
	}
	if got := store.List(); len(got) != 0 {
		t.Fatalf("expected no server recorded after failed add, got %d", len(got))
	}
}

func TestStore_UpdateRollsBackOldConfigRemovalFailure(t *testing.T) {
	t.Setenv("AGENTPACK_ALLOW_TEMP_DIR", "")
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("APPDATA", "")

	claudePath := filepath.Join(home, ".claude.json")
	cursorPath := filepath.Join(home, ".cursor", "mcp.json")
	writeFile(t, claudePath, `{"mcpServers":{"github":{"command":"npx","args":["pkg"]}}}`)
	writeFile(t, cursorPath, `{"mcpServers":{"github":{"command":"npx","args":["pkg"]}}}`)

	reg := agents.NewRegistry()
	reg.Register(agents.Agent{ID: "claude-code", Name: "Claude Code", Type: agents.TypeClaudeCode, Status: agents.StatusEnabled, ConfigPath: claudePath, ConfigFormat: agents.FormatJSON})
	reg.Register(agents.Agent{ID: "cursor", Name: "Cursor", Type: agents.TypeCursor, Status: agents.StatusEnabled, ConfigPath: cursorPath, ConfigFormat: agents.FormatJSON})

	store := NewStore()
	if err := store.Load(reg); err != nil {
		t.Fatal(err)
	}
	servers := store.List()
	if len(servers) != 1 {
		t.Fatalf("expected merged server, got %d", len(servers))
	}
	writeFile(t, cursorPath, `{"mcpServers":`)

	err := store.Update(servers[0].ID, Server{Name: "github", Command: "uvx", Transport: TransportStdio}, []string{"claude-code", "cursor"}, reg)
	if err == nil {
		t.Fatal("expected invalid JSON error")
	}
	disk, err := NewBackend("claude-code").Read(claudePath)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := disk["github"]; !ok {
		t.Fatal("expected old server restored to first config")
	}
	if !store.AgentBound(servers[0].ID, "claude-code") || !store.AgentBound(servers[0].ID, "cursor") {
		t.Fatal("expected old bindings preserved after failed update")
	}
}

func TestStore_SyncDBEncryptsSensitiveEnv(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "test.db")
	if err := database.Init(dbPath); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { resetTestDB(t) })

	claudePath := filepath.Join(dir, ".claude.json")
	writeFile(t, claudePath, `{}`)

	reg := agents.NewRegistry()
	reg.Register(agents.Agent{ID: "claude-code", Name: "Claude Code", Type: agents.TypeClaudeCode, Status: agents.StatusEnabled, ConfigPath: claudePath, ConfigFormat: agents.FormatJSON})

	store := NewStore()
	if err := store.Load(reg); err != nil {
		t.Fatal(err)
	}
	created, err := store.Add(Server{
		Name:      "github",
		Command:   "npx",
		Env:       map[string]string{"GITHUB_TOKEN": "secret-token", "DEBUG": "1"},
		Transport: TransportStdio,
	}, []string{"claude-code"}, reg)
	if err != nil {
		t.Fatal(err)
	}

	var rawEnv string
	if err := database.GetDB().QueryRow(`SELECT env FROM mcp_servers WHERE id = ?`, created.ID).Scan(&rawEnv); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(rawEnv, "secret-token") {
		t.Fatalf("expected sensitive env to be encrypted in db, got %s", rawEnv)
	}
	if !strings.Contains(rawEnv, "enc:") {
		t.Fatalf("expected encrypted env marker in db, got %s", rawEnv)
	}
	if !strings.Contains(rawEnv, `"DEBUG":"1"`) {
		t.Fatalf("expected non-sensitive env to remain readable, got %s", rawEnv)
	}
	got, ok := store.Get(created.ID)
	if !ok {
		t.Fatal("expected server in store")
	}
	if got.Env["GITHUB_TOKEN"] != "secret-token" {
		t.Fatalf("expected plaintext env from store, got %q", got.Env["GITHUB_TOKEN"])
	}
}

func TestStore_AddReturnsSyncDBError(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "closed.db")
	if err := database.Init(dbPath); err != nil {
		t.Fatal(err)
	}
	if err := database.Close(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { resetTestDB(t) })

	claudePath := filepath.Join(dir, ".claude.json")
	writeFile(t, claudePath, `{}`)
	reg := agents.NewRegistry()
	reg.Register(agents.Agent{ID: "claude-code", Name: "Claude Code", Type: agents.TypeClaudeCode, Status: agents.StatusEnabled, ConfigPath: claudePath, ConfigFormat: agents.FormatJSON})

	store := NewStore()
	_, err := store.Add(Server{Name: "github", Command: "npx", Transport: TransportStdio}, []string{"claude-code"}, reg)
	if err == nil || !strings.Contains(err.Error(), "sync database after add") {
		t.Fatalf("expected sync database error, got %v", err)
	}
	if got := store.List(); len(got) != 0 {
		t.Fatalf("expected in-memory rollback after db error, got %d server(s)", len(got))
	}
	disk, readErr := NewBackend("claude-code").Read(claudePath)
	if readErr != nil {
		t.Fatal(readErr)
	}
	if len(disk) != 0 {
		t.Fatalf("expected disk rollback after db error, got %#v", disk)
	}
}

func TestStore_MergeByCommandArgs(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("APPDATA", "")

	claudePath := filepath.Join(home, ".claude.json")
	cursorPath := filepath.Join(home, ".cursor", "mcp.json")
	writeFile(t, claudePath, `{"mcpServers":{"github":{"command":"npx","args":["-y","x"]}}}`)
	writeFile(t, cursorPath, `{"mcpServers":{"github":{"command":"npx","args":["-y","x"]}}}`)

	reg := agents.NewRegistry()
	reg.Register(agents.Agent{ID: "claude-code", Name: "Claude Code", Type: agents.TypeClaudeCode, Status: agents.StatusEnabled, ConfigPath: claudePath, ConfigFormat: agents.FormatJSON})
	reg.Register(agents.Agent{ID: "cursor", Name: "Cursor", Type: agents.TypeCursor, Status: agents.StatusEnabled, ConfigPath: cursorPath, ConfigFormat: agents.FormatJSON})

	store := NewStore()
	if err := store.Load(reg); err != nil {
		t.Fatal(err)
	}

	servers := store.List()
	if len(servers) != 1 {
		t.Fatalf("expected 1 merged server, got %d", len(servers))
	}
	srv := servers[0]
	if !store.AgentBound(srv.ID, "claude-code") || !store.AgentBound(srv.ID, "cursor") {
		t.Error("expected server bound to both agents")
	}
}

func TestStore_MergeByCommandArgsIgnoreOtherFields(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("APPDATA", "")

	claudePath := filepath.Join(home, ".claude.json")
	cursorPath := filepath.Join(home, ".cursor", "mcp.json")
	writeFile(t, claudePath, `{"mcpServers":{"mysql":{"command":"npx","args":["-y","@f4ww4z/mcp-mysql-server"],"env":{"MYSQL_HOST":"3306"}}}}`)
	writeFile(t, cursorPath, `{"mcpServers":{"mysql":{"command":"npx","args":["-y","@f4ww4z/mcp-mysql-server"],"disabled":true,"fromGalleryId":"xxx"}}}`)

	reg := agents.NewRegistry()
	reg.Register(agents.Agent{ID: "claude-code", Name: "Claude Code", Type: agents.TypeClaudeCode, Status: agents.StatusEnabled, ConfigPath: claudePath, ConfigFormat: agents.FormatJSON})
	reg.Register(agents.Agent{ID: "cursor", Name: "Cursor", Type: agents.TypeCursor, Status: agents.StatusEnabled, ConfigPath: cursorPath, ConfigFormat: agents.FormatJSON})

	store := NewStore()
	if err := store.Load(reg); err != nil {
		t.Fatal(err)
	}

	servers := store.List()
	if len(servers) != 1 {
		t.Fatalf("expected 1 merged server (same command+args, ignore env/disabled), got %d", len(servers))
	}
	if !store.AgentBound(servers[0].ID, "claude-code") || !store.AgentBound(servers[0].ID, "cursor") {
		t.Error("expected server bound to both agents")
	}
}

func TestStore_MergeByCommandArgsDifferentName(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("APPDATA", "")

	claudePath := filepath.Join(home, ".claude.json")
	cursorPath := filepath.Join(home, ".cursor", "mcp.json")
	writeFile(t, claudePath, `{"mcpServers":{"web-research":{"command":"npx","args":["-y","@mzxrai/mcp-webresearch@latest"]}}}`)
	writeFile(t, cursorPath, `{"mcpServers":{"web research":{"command":"npx","args":["-y","@mzxrai/mcp-webresearch@latest"]}}}`)

	reg := agents.NewRegistry()
	reg.Register(agents.Agent{ID: "claude-code", Name: "Claude Code", Type: agents.TypeClaudeCode, Status: agents.StatusEnabled, ConfigPath: claudePath, ConfigFormat: agents.FormatJSON})
	reg.Register(agents.Agent{ID: "cursor", Name: "Cursor", Type: agents.TypeCursor, Status: agents.StatusEnabled, ConfigPath: cursorPath, ConfigFormat: agents.FormatJSON})

	store := NewStore()
	if err := store.Load(reg); err != nil {
		t.Fatal(err)
	}

	servers := store.List()
	if len(servers) != 1 {
		t.Fatalf("expected 1 merged server (same command+args, different name), got %d", len(servers))
	}
	if !store.AgentBound(servers[0].ID, "claude-code") || !store.AgentBound(servers[0].ID, "cursor") {
		t.Error("expected server bound to both agents")
	}
}

func TestStore_MergeByCommandArgsAcrossFormats(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("APPDATA", "")

	claudePath := filepath.Join(home, ".claude.json")
	codexPath := filepath.Join(home, ".codex", "config.toml")
	writeFile(t, claudePath, `{"mcpServers":{"git":{"command":"uvx","args":["mcp-server-git"]}}}`)
	writeFile(t, codexPath, `[[mcp_servers]]
name = "git"
command = "uvx"
args = ["mcp-server-git"]`)

	reg := agents.NewRegistry()
	reg.Register(agents.Agent{ID: "claude-code", Name: "Claude Code", Type: agents.TypeClaudeCode, Status: agents.StatusEnabled, ConfigPath: claudePath, ConfigFormat: agents.FormatJSON})
	reg.Register(agents.Agent{ID: "codex", Name: "Codex", Type: agents.TypeCodex, Status: agents.StatusEnabled, ConfigPath: codexPath, ConfigFormat: agents.FormatTOML})

	store := NewStore()
	if err := store.Load(reg); err != nil {
		t.Fatal(err)
	}

	servers := store.List()
	if len(servers) != 1 {
		t.Fatalf("expected 1 merged server (same command+args across formats), got %d", len(servers))
	}
	if !store.AgentBound(servers[0].ID, "claude-code") || !store.AgentBound(servers[0].ID, "codex") {
		t.Error("expected server bound to both agents")
	}
}

func TestStore_SharedConfigPathBindsBothAgents(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("APPDATA", "")

	// OpenCode CLI 和 Desktop 共享同一 ConfigPath
	opencodePath := filepath.Join(home, ".config", "opencode", "opencode.json")
	writeFile(t, opencodePath, `{"mcp":{"servers":{"github":{"type":"local","command":["npx","-y","@mcp/server-github"]}}}}`)

	reg := agents.NewRegistry()
	reg.Register(agents.Agent{ID: "opencode", Name: "OpenCode", Type: agents.TypeOpenCode, Status: agents.StatusEnabled, ConfigPath: opencodePath, ConfigFormat: agents.FormatJSON})
	reg.Register(agents.Agent{ID: "opencode-desktop", Name: "OpenCode", Type: agents.TypeOpenCode, Status: agents.StatusEnabled, ConfigPath: opencodePath, ConfigFormat: agents.FormatJSON})

	store := NewStore()
	if err := store.Load(reg); err != nil {
		t.Fatal(err)
	}

	servers := store.List()
	if len(servers) != 1 {
		t.Fatalf("expected 1 server (shared config read once), got %d", len(servers))
	}
	srv := servers[0]
	if !store.AgentBound(srv.ID, "opencode") {
		t.Error("expected server bound to opencode CLI")
	}
	if !store.AgentBound(srv.ID, "opencode-desktop") {
		t.Error("expected server bound to opencode-desktop")
	}
}

func TestStore_SharedConfigPathWriteOnce(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("APPDATA", "")

	opencodePath := filepath.Join(home, ".config", "opencode", "opencode.json")
	writeFile(t, opencodePath, `{"mcp":{"servers":{}}}`)

	reg := agents.NewRegistry()
	reg.Register(agents.Agent{ID: "opencode", Name: "OpenCode", Type: agents.TypeOpenCode, Status: agents.StatusEnabled, ConfigPath: opencodePath, ConfigFormat: agents.FormatJSON})
	reg.Register(agents.Agent{ID: "opencode-desktop", Name: "OpenCode", Type: agents.TypeOpenCode, Status: agents.StatusEnabled, ConfigPath: opencodePath, ConfigFormat: agents.FormatJSON})

	store := NewStore()
	store.Load(reg)

	server := Server{
		Name:      "github",
		Command:   "npx",
		Args:      []string{"-y", "@mcp/server-github"},
		Transport: TransportStdio,
	}
	// Add 同时绑定 opencode 和 opencode-desktop，应只写一次文件
	if _, err := store.Add(server, []string{"opencode", "opencode-desktop"}, reg); err != nil {
		t.Fatal(err)
	}

	backend := NewBackend("opencode")
	disk, err := backend.Read(opencodePath)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := disk["github"]; !ok {
		t.Error("expected github on disk")
	}

	// 验证 store 中只有一个 server 条目
	servers := store.List()
	if len(servers) != 1 {
		t.Fatalf("expected 1 server, got %d", len(servers))
	}
	srv := servers[0]
	if !store.AgentBound(srv.ID, "opencode") || !store.AgentBound(srv.ID, "opencode-desktop") {
		t.Error("expected server bound to both opencode agents")
	}
}

func TestStore_SharedConfigPathRemoveOnce(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("APPDATA", "")

	opencodePath := filepath.Join(home, ".config", "opencode", "opencode.json")
	writeFile(t, opencodePath, `{"mcp":{"servers":{"github":{"type":"local","command":["npx","-y","@mcp/server-github"]}}}}`)

	reg := agents.NewRegistry()
	reg.Register(agents.Agent{ID: "opencode", Name: "OpenCode", Type: agents.TypeOpenCode, Status: agents.StatusEnabled, ConfigPath: opencodePath, ConfigFormat: agents.FormatJSON})
	reg.Register(agents.Agent{ID: "opencode-desktop", Name: "OpenCode", Type: agents.TypeOpenCode, Status: agents.StatusEnabled, ConfigPath: opencodePath, ConfigFormat: agents.FormatJSON})

	store := NewStore()
	store.Load(reg)

	servers := store.List()
	if len(servers) != 1 {
		t.Fatalf("expected 1 server, got %d", len(servers))
	}
	srvID := servers[0].ID

	if err := store.Remove(srvID, reg); err != nil {
		t.Fatal(err)
	}

	backend := NewBackend("opencode")
	disk, err := backend.Read(opencodePath)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := disk["github"]; ok {
		t.Error("expected github removed from disk after Remove")
	}
}

func TestStore_MergeWithCmdCWrapper(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("APPDATA", "")

	claudePath := filepath.Join(home, ".claude.json")
	cursorPath := filepath.Join(home, ".cursor", "mcp.json")
	// Claude Code 在 Windows 上把 command 包装为 cmd /c npx，且无 @latest 后缀
	writeFile(t, claudePath, `{"mcpServers":{"context7":{"command":"cmd","args":["/c","npx","-y","@upstash/context7-mcp"]}}}`)
	// Cursor 使用原始 npx，带 @latest 后缀
	writeFile(t, cursorPath, `{"mcpServers":{"context7":{"command":"npx","args":["-y","@upstash/context7-mcp@latest"]}}}`)

	reg := agents.NewRegistry()
	reg.Register(agents.Agent{ID: "claude-code", Name: "Claude Code", Type: agents.TypeClaudeCode, Status: agents.StatusEnabled, ConfigPath: claudePath, ConfigFormat: agents.FormatJSON})
	reg.Register(agents.Agent{ID: "cursor", Name: "Cursor", Type: agents.TypeCursor, Status: agents.StatusEnabled, ConfigPath: cursorPath, ConfigFormat: agents.FormatJSON})

	store := NewStore()
	if err := store.Load(reg); err != nil {
		t.Fatal(err)
	}

	servers := store.List()
	if len(servers) != 1 {
		t.Fatalf("expected 1 merged server (cmd /c wrapper normalized), got %d", len(servers))
	}
	if !store.AgentBound(servers[0].ID, "claude-code") || !store.AgentBound(servers[0].ID, "cursor") {
		t.Error("expected server bound to both agents")
	}
}

func TestStore_NotMergeDifferentCommand(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("APPDATA", "")

	claudePath := filepath.Join(home, ".claude.json")
	cursorPath := filepath.Join(home, ".cursor", "mcp.json")
	writeFile(t, claudePath, `{"mcpServers":{"tool":{"command":"python","args":["srv.py"]}}}`)
	writeFile(t, cursorPath, `{"mcpServers":{"tool":{"command":"node","args":["srv.js"]}}}`)

	reg := agents.NewRegistry()
	reg.Register(agents.Agent{ID: "claude-code", Name: "Claude Code", Type: agents.TypeClaudeCode, Status: agents.StatusEnabled, ConfigPath: claudePath, ConfigFormat: agents.FormatJSON})
	reg.Register(agents.Agent{ID: "cursor", Name: "Cursor", Type: agents.TypeCursor, Status: agents.StatusEnabled, ConfigPath: cursorPath, ConfigFormat: agents.FormatJSON})

	store := NewStore()
	if err := store.Load(reg); err != nil {
		t.Fatal(err)
	}

	servers := store.List()
	if len(servers) != 2 {
		t.Fatalf("expected 2 servers (different command), got %d", len(servers))
	}
}

func TestStore_NotMergeDifferentArgs(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("APPDATA", "")

	claudePath := filepath.Join(home, ".claude.json")
	cursorPath := filepath.Join(home, ".cursor", "mcp.json")
	writeFile(t, claudePath, `{"mcpServers":{"tool":{"command":"npx","args":["pkg-a"]}}}`)
	writeFile(t, cursorPath, `{"mcpServers":{"tool":{"command":"npx","args":["pkg-b"]}}}`)

	reg := agents.NewRegistry()
	reg.Register(agents.Agent{ID: "claude-code", Name: "Claude Code", Type: agents.TypeClaudeCode, Status: agents.StatusEnabled, ConfigPath: claudePath, ConfigFormat: agents.FormatJSON})
	reg.Register(agents.Agent{ID: "cursor", Name: "Cursor", Type: agents.TypeCursor, Status: agents.StatusEnabled, ConfigPath: cursorPath, ConfigFormat: agents.FormatJSON})

	store := NewStore()
	if err := store.Load(reg); err != nil {
		t.Fatal(err)
	}

	servers := store.List()
	if len(servers) != 2 {
		t.Fatalf("expected 2 servers (different args), got %d", len(servers))
	}
}

func TestStore_RejectsEmptyName(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("APPDATA", "")

	claudePath := filepath.Join(home, ".claude.json")
	writeFile(t, claudePath, `{}`)

	reg := agents.NewRegistry()
	testAgent := agents.Agent{
		ID:           "claude-code",
		Name:         "Claude Code",
		Type:         agents.TypeClaudeCode,
		Status:       agents.StatusEnabled,
		ConfigPath:   claudePath,
		ConfigFormat: agents.FormatJSON,
	}
	reg.Register(testAgent)
	store := NewStore()
	store.Load(reg)

	_, err := store.Add(Server{Command: "npx"}, []string{"claude-code"}, reg)
	if err == nil {
		t.Fatal("expected error for empty name")
	}
}

func TestStore_RejectsNoAgents(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("APPDATA", "")

	reg := agents.NewRegistry()
	store := NewStore()
	store.Load(reg)

	_, err := store.Add(Server{Name: "x", Command: "npx"}, []string{}, reg)
	if err == nil {
		t.Fatal("expected error for no agents")
	}
}

func TestStore_AtomicWriteBackup(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("APPDATA", "")

	claudePath := filepath.Join(home, ".claude.json")
	writeFile(t, claudePath, `{"mcpServers":{"existing":{"command":"echo","args":["hi"]}}}`)

	if _, err := BackupConfig("claude-code", claudePath); err != nil {
		t.Fatal(err)
	}
	backupDir := filepath.Join(home, ".agentpack", "backups", "mcp")
	entries, err := os.ReadDir(backupDir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) == 0 {
		t.Error("expected backup file")
	}
}

func TestJsonBackend_ReadOpenCodeFlatFormat(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "opencode.json")

	// OpenCode flat format: servers directly under "mcp", command is an array
	writeFile(t, path, `{
		"$schema": "https://opencode.ai/config.json",
		"mcp": {
			"context7": {
				"command": ["cmd", "/c", "npx", "-y", "@upstash/context7-mcp"],
				"enabled": true,
				"type": "local"
			},
			"fetch": {
				"command": ["uvx", "mcp-server-fetch"],
				"enabled": true,
				"type": "local"
			},
			"memory": {
				"command": ["cmd", "/c", "npx", "-y", "@modelcontextprotocol/server-memory"],
				"enabled": true,
				"type": "local"
			}
		}
	}`)

	backend := NewJsonBackend("opencode")
	servers, err := backend.Read(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(servers) != 3 {
		t.Fatalf("expected 3 servers, got %d", len(servers))
	}

	// context7: cmd /c npx → should be normalized to cmd + ["/c", "npx", "-y", "@upstash/context7-mcp"]
	c7, ok := servers["context7"]
	if !ok {
		t.Fatal("expected context7 server")
	}
	if c7.Command != "cmd" {
		t.Errorf("context7: expected command=cmd, got %q", c7.Command)
	}
	if len(c7.Args) != 4 || c7.Args[0] != "/c" {
		t.Errorf("context7: expected args=[/c, npx, -y, @upstash/context7-mcp], got %v", c7.Args)
	}

	// fetch: uvx (no wrapper)
	fetch, ok := servers["fetch"]
	if !ok {
		t.Fatal("expected fetch server")
	}
	if fetch.Command != "uvx" {
		t.Errorf("fetch: expected command=uvx, got %q", fetch.Command)
	}
	if len(fetch.Args) != 1 || fetch.Args[0] != "mcp-server-fetch" {
		t.Errorf("fetch: expected args=[mcp-server-fetch], got %v", fetch.Args)
	}

	// memory: cmd /c npx
	mem, ok := servers["memory"]
	if !ok {
		t.Fatal("expected memory server")
	}
	if mem.Command != "cmd" {
		t.Errorf("memory: expected command=cmd, got %q", mem.Command)
	}
	if len(mem.Args) != 4 || mem.Args[1] != "npx" {
		t.Errorf("memory: unexpected args: %v", mem.Args)
	}
}

func TestJsonBackend_OpenCodeFlatFormatWriteRoundTrip(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "opencode.json")

	// Write initial config in flat format
	writeFile(t, path, `{
		"$schema": "https://opencode.ai/config.json",
		"mcp": {
			"context7": {
				"command": ["npx", "-y", "@upstash/context7-mcp"],
				"enabled": true,
				"type": "local"
			}
		}
	}`)

	backend := NewJsonBackend("opencode")

	// Read should detect flat format
	servers, err := backend.Read(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(servers) != 1 {
		t.Fatalf("expected 1 server, got %d", len(servers))
	}

	// Write back a new server
	in := map[string]Server{
		"fetch": {Name: "fetch", Command: "uvx", Args: []string{"mcp-server-fetch"}, Transport: TransportStdio},
	}
	if err := backend.Write(path, in); err != nil {
		t.Fatal(err)
	}

	// Verify file still uses flat mcp format (not mcp.servers)
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	body := string(data)
	if containsStr(body, `"mcp"`) && containsStr(body, `"servers"`) {
		// Make sure it's NOT using nested mcp.servers format for the servers themselves
		// The flat format has "mcp": { "fetch": {...} } not "mcp": { "servers": { "fetch": {...} } }
		var cfg map[string]json.RawMessage
		if err := json.Unmarshal(data, &cfg); err != nil {
			t.Fatal(err)
		}
		mcpRaw, ok := cfg["mcp"]
		if !ok {
			t.Fatal("expected mcp key")
		}
		var mcp map[string]json.RawMessage
		if err := json.Unmarshal(mcpRaw, &mcp); err != nil {
			t.Fatal(err)
		}
		if _, hasServers := mcp["servers"]; hasServers {
			t.Error("expected flat mcp format, got nested mcp.servers format")
		}
		if _, hasFetch := mcp["fetch"]; !hasFetch {
			t.Error("expected fetch key directly under mcp")
		}
	}
	// Also verify $schema preserved
	if !containsStr(body, `"$schema"`) {
		t.Error("expected $schema preserved")
	}
}

func TestStore_OpenCodeFlatFormatMergeWithClaude(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("APPDATA", "")

	// OpenCode 使用扁平 mcp 格式，command 为数组，带 cmd /c 包装
	opencodePath := filepath.Join(home, ".config", "opencode", "opencode.json")
	writeFile(t, opencodePath, `{
		"$schema": "https://opencode.ai/config.json",
		"mcp": {
			"context7": {
				"command": ["cmd", "/c", "npx", "-y", "@upstash/context7-mcp"],
				"enabled": true,
				"type": "local"
			}
		}
	}`)

	// Claude Code 使用标准 mcpServers 格式
	claudePath := filepath.Join(home, ".claude.json")
	writeFile(t, claudePath, `{"mcpServers":{"context7":{"command":"npx","args":["-y","@upstash/context7-mcp@latest"]}}}`)

	reg := agents.NewRegistry()
	reg.Register(agents.Agent{ID: "opencode", Name: "OpenCode", Type: agents.TypeOpenCode, Status: agents.StatusEnabled, ConfigPath: opencodePath, ConfigFormat: agents.FormatJSON})
	reg.Register(agents.Agent{ID: "claude-code", Name: "Claude Code", Type: agents.TypeClaudeCode, Status: agents.StatusEnabled, ConfigPath: claudePath, ConfigFormat: agents.FormatJSON})

	store := NewStore()
	if err := store.Load(reg); err != nil {
		t.Fatal(err)
	}

	servers := store.List()
	if len(servers) != 1 {
		t.Fatalf("expected 1 merged server (OpenCode flat + Claude Code), got %d", len(servers))
	}
	srv := servers[0]
	if !store.AgentBound(srv.ID, "opencode") {
		t.Error("expected server bound to opencode")
	}
	if !store.AgentBound(srv.ID, "claude-code") {
		t.Error("expected server bound to claude-code")
	}
}

func TestJsonBackend_ReadOpenCodeNestedFormat(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "opencode.json")

	// OpenCode uses nested "mcp" → "servers" format with command as array
	writeFile(t, path, `{
		"mcp": {
			"servers": {
				"github": {
					"type": "local",
					"command": ["npx", "-y", "@mcp/server-github"],
					"environment": {"TOKEN": "xyz"}
				},
				"filesystem": {
					"type": "local",
					"command": ["npx", "-y", "@mcp/server-fs"]
				}
			}
		}
	}`)

	backend := NewJsonBackend("opencode")
	servers, err := backend.Read(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(servers) != 2 {
		t.Fatalf("expected 2 servers, got %d", len(servers))
	}
	got, ok := servers["github"]
	if !ok {
		t.Fatal("expected github server")
	}
	if got.Command != "npx" {
		t.Errorf("expected npx, got %q", got.Command)
	}
	if len(got.Args) != 2 || got.Args[0] != "-y" {
		t.Errorf("expected args [-y, @mcp/server-github], got %v", got.Args)
	}
	if got.Env["TOKEN"] != "xyz" {
		t.Errorf("expected token xyz, got %q", got.Env["TOKEN"])
	}
	if _, ok := servers["filesystem"]; !ok {
		t.Error("expected filesystem server")
	}
}

func TestJsonBackend_ReadTopLevelMcpServers(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "claude.json")

	// Claude Code / Cursor use top-level "mcpServers"
	writeFile(t, path, `{
		"mcpServers": {
			"github": {
				"command": "npx",
				"args": ["-y", "@mcp/server-github"]
			}
		}
	}`)

	backend := NewJsonBackend("claude-code")
	servers, err := backend.Read(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(servers) != 1 {
		t.Fatalf("expected 1 server, got %d", len(servers))
	}
	if _, ok := servers["github"]; !ok {
		t.Error("expected github server")
	}
}

func TestJsonBackend_ReadEmptyConfig(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "empty.json")
	writeFile(t, path, `{}`)

	backend := NewJsonBackend("claude-code")
	servers, err := backend.Read(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(servers) != 0 {
		t.Errorf("expected 0 servers, got %d", len(servers))
	}
}

func TestJsonBackend_WritePreservesNonMcpFields(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")

	// Write config with extra fields
	writeFile(t, path, `{
		"theme": "dark",
		"mcpServers": {
			"old": {"command": "echo"}
		},
		"version": 2
	}`)

	backend := NewJsonBackend("claude-code")
	servers := map[string]Server{
		"new": {Name: "new", Command: "npx", Args: []string{"-y", "pkg"}, Transport: TransportStdio},
	}
	if err := backend.Write(path, servers); err != nil {
		t.Fatal(err)
	}

	// Read back and verify non-mcpServers fields preserved
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	body := string(data)
	if !containsStr(body, `"theme"`) {
		t.Error("expected theme field preserved")
	}
	if !containsStr(body, `"version"`) {
		t.Error("expected version field preserved")
	}

	// Verify MCP servers updated
	out, err := backend.Read(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := out["new"]; !ok {
		t.Error("expected new server")
	}
	if _, ok := out["old"]; ok {
		t.Error("expected old server removed")
	}
}

func TestTomlBackend_ReadWriteWithEnv(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.toml")

	backend := NewTomlBackend()
	in := map[string]Server{
		"myserver": {
			ID:        "myserver-id",
			Name:      "myserver",
			Command:   "npx",
			Args:      []string{"-y", "pkg"},
			Env:       map[string]string{"API_KEY": "secret123"},
			Transport: TransportStdio,
		},
	}
	if err := backend.Write(path, in); err != nil {
		t.Fatal(err)
	}

	out, err := backend.Read(path)
	if err != nil {
		t.Fatal(err)
	}
	got, ok := out["myserver"]
	if !ok {
		t.Fatal("expected myserver")
	}
	if got.Env["API_KEY"] != "secret123" {
		t.Errorf("expected API_KEY=secret123, got %q", got.Env["API_KEY"])
	}
}

func TestJsonBackend_OpenCodeWriteRead(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "opencode.json")

	writeFile(t, path, `{"$schema":"https://opencode.ai/config.json"}`)

	backend := NewJsonBackend("opencode")
	in := map[string]Server{
		"github": {
			Name:      "github",
			Command:   "npx",
			Args:      []string{"-y", "@mcp/server-github"},
			Env:       map[string]string{"TOKEN": "abc"},
			Transport: TransportStdio,
		},
	}
	if err := backend.Write(path, in); err != nil {
		t.Fatal(err)
	}

	// Verify the file has mcp.servers nested structure
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	body := string(data)
	if !containsStr(body, `"mcp"`) {
		t.Error("expected mcp key in output")
	}
	if !containsStr(body, `"servers"`) {
		t.Error("expected servers key in output")
	}
	if !containsStr(body, `"$schema"`) {
		t.Error("expected $schema preserved")
	}

	// Read back and verify
	out, err := backend.Read(path)
	if err != nil {
		t.Fatal(err)
	}
	got, ok := out["github"]
	if !ok {
		t.Fatal("expected github server")
	}
	if got.Command != "npx" {
		t.Errorf("expected npx, got %q", got.Command)
	}
	if got.Env["TOKEN"] != "abc" {
		t.Errorf("expected TOKEN=abc, got %q", got.Env["TOKEN"])
	}
}

func TestTomlBackend_CodexArrayFormat(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.toml")

	// Codex uses [[mcp_servers]] array format
	writeFile(t, path, `
model = "gpt-4"

[[mcp_servers]]
name = "my-server"
command = "npx"
args = ["-y", "@mcp/server-fs", "/tmp"]

[[mcp_servers]]
name = "another"
command = "python"
args = ["server.py"]
`)

	backend := NewTomlBackend()
	out, err := backend.Read(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(out) != 2 {
		t.Fatalf("expected 2 servers, got %d", len(out))
	}
	got, ok := out["my-server"]
	if !ok {
		t.Fatal("expected my-server")
	}
	if got.Command != "npx" {
		t.Errorf("expected npx, got %q", got.Command)
	}
	if len(got.Args) != 3 || got.Args[0] != "-y" {
		t.Errorf("expected [-y, @mcp/server-fs, /tmp], got %v", got.Args)
	}
	if _, ok := out["another"]; !ok {
		t.Error("expected another server")
	}
}

func TestTomlBackend_CodexWriteRead(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.toml")

	writeFile(t, path, `model = "gpt-4"`)

	backend := NewTomlBackend()
	in := map[string]Server{
		"my-server": {
			Name:      "my-server",
			Command:   "npx",
			Args:      []string{"-y", "@mcp/server-fs"},
			Env:       map[string]string{"KEY": "val"},
			Transport: TransportStdio,
		},
	}
	if err := backend.Write(path, in); err != nil {
		t.Fatal(err)
	}

	// Verify model field preserved
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	body := string(data)
	if !containsStr(body, `model`) {
		t.Error("expected model field preserved")
	}

	// Read back
	out, err := backend.Read(path)
	if err != nil {
		t.Fatal(err)
	}
	got, ok := out["my-server"]
	if !ok {
		t.Fatal("expected my-server")
	}
	if got.Command != "npx" {
		t.Errorf("expected npx, got %q", got.Command)
	}
	if got.Env["KEY"] != "val" {
		t.Errorf("expected KEY=val, got %q", got.Env["KEY"])
	}
}

func TestTomlBackend_CodexTableFormatReadWrite(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.toml")

	// Codex [mcp_servers.NAME] table format with type field
	writeFile(t, path, `
model = "gpt-4"

[mcp_servers]

[mcp_servers.fetch]
type = "stdio"
command = "uvx"
args = ["mcp-server-fetch"]

[mcp_servers.memory]
type = "stdio"
command = "cmd"
args = ["/c", "npx", "-y", "@modelcontextprotocol/server-memory"]

[mcp_servers.sequential-thinking]
type = "stdio"
command = "cmd"
args = ["/c", "npx", "-y", "@modelcontextprotocol/server-sequential-thinking"]

[mcp_servers.context7]
type = "stdio"
command = "cmd"
args = ["/c", "npx", "-y", "@upstash/context7-mcp"]
`)

	backend := NewTomlBackend()
	out, err := backend.Read(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(out) != 4 {
		t.Fatalf("expected 4 servers, got %d", len(out))
	}

	// 验证 fetch (无 cmd /c 包装)
	fetch, ok := out["fetch"]
	if !ok {
		t.Fatal("expected fetch server")
	}
	if fetch.Command != "uvx" {
		t.Errorf("fetch: expected command=uvx, got %q", fetch.Command)
	}
	if fetch.ConfigType != "stdio" {
		t.Errorf("fetch: expected configType=stdio, got %q", fetch.ConfigType)
	}

	// 验证 context7 (cmd /c 包装)
	c7, ok := out["context7"]
	if !ok {
		t.Fatal("expected context7 server")
	}
	if c7.Command != "cmd" {
		t.Errorf("context7: expected command=cmd, got %q", c7.Command)
	}
	if len(c7.Args) != 4 || c7.Args[0] != "/c" {
		t.Errorf("context7: expected args=[/c, npx, -y, @upstash/context7-mcp], got %v", c7.Args)
	}
	if c7.ConfigType != "stdio" {
		t.Errorf("context7: expected configType=stdio, got %q", c7.ConfigType)
	}

	// 写回并验证保留 type 字段和 table 格式
	if err := backend.Write(path, out); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	body := string(data)
	if !containsStr(body, `type = "stdio"`) {
		t.Error("expected type = \"stdio\" in output")
	}
	if !containsStr(body, `[mcp_servers.fetch]`) {
		t.Error("expected [mcp_servers.fetch] table format")
	}
	if !containsStr(body, `model`) {
		t.Error("expected model field preserved")
	}
	// 确认不是数组格式
	if containsStr(body, `[[mcp_servers]]`) {
		t.Error("expected table format, not array format")
	}

	// 重新读取验证往返一致
	out2, err := backend.Read(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(out2) != 4 {
		t.Fatalf("round-trip: expected 4 servers, got %d", len(out2))
	}
	if out2["context7"].ConfigType != "stdio" {
		t.Errorf("round-trip: expected configType=stdio, got %q", out2["context7"].ConfigType)
	}
}

func TestTomlBackend_TableFormatQuotesRegistryStyleName(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.toml")
	writeFile(t, path, `
model = "gpt-4"

[mcp_servers]

[mcp_servers.existing]
type = "stdio"
command = "echo"
`)

	backend := NewTomlBackend()
	in := map[string]Server{
		"io.example/filesystem": {
			Name:      "io.example/filesystem",
			Command:   "npx",
			Args:      []string{"-y", "@example/filesystem"},
			Transport: TransportStdio,
		},
	}
	if err := backend.Write(path, in); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	body := string(data)
	if !containsStr(body, `[mcp_servers."io.example/filesystem"]`) {
		t.Fatalf("expected quoted table key for registry-style name, got:\n%s", body)
	}
	out, err := backend.Read(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := out["io.example/filesystem"]; !ok {
		t.Fatalf("expected registry-style name after round trip, got %v", out)
	}
}

func TestTomlBackend_CodexArrayFormatWithTypeField(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.toml")

	// [[mcp_servers]] array format also uses type field
	writeFile(t, path, `
model = "gpt-4"

[[mcp_servers]]
name = "my-server"
type = "stdio"
command = "npx"
args = ["-y", "@mcp/server-fs"]
`)
	backend := NewTomlBackend()
	out, err := backend.Read(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(out) != 1 {
		t.Fatalf("expected 1 server, got %d", len(out))
	}
	got, ok := out["my-server"]
	if !ok {
		t.Fatal("expected my-server")
	}
	if got.ConfigType != "stdio" {
		t.Errorf("expected configType=stdio, got %q", got.ConfigType)
	}

	// 写回保留 type 字段
	if err := backend.Write(path, out); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	body := string(data)
	if !containsStr(body, `type = "stdio"`) {
		t.Error("expected type = \"stdio\" in array format output")
	}
}

func TestStore_MergeCodexTableFormatWithClaude(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("APPDATA", "")

	// Codex 使用 [mcp_servers.NAME] table 格式，cmd /c 包装，type = "stdio"
	codexPath := filepath.Join(home, ".codex", "config.toml")
	writeFile(t, codexPath, `
[mcp_servers]

[mcp_servers.context7]
type = "stdio"
command = "cmd"
args = ["/c", "npx", "-y", "@upstash/context7-mcp"]
`)

	// Claude Code 用标准 JSON，npx + @latest
	claudePath := filepath.Join(home, ".claude.json")
	writeFile(t, claudePath, `{"mcpServers":{"context7":{"type":"stdio","command":"npx","args":["-y","@upstash/context7-mcp@latest"]}}}`)

	reg := agents.NewRegistry()
	reg.Register(agents.Agent{ID: "codex", Name: "Codex", Type: agents.TypeCodex, Status: agents.StatusEnabled, ConfigPath: codexPath, ConfigFormat: agents.FormatTOML})
	reg.Register(agents.Agent{ID: "claude-code", Name: "Claude Code", Type: agents.TypeClaudeCode, Status: agents.StatusEnabled, ConfigPath: claudePath, ConfigFormat: agents.FormatJSON})

	store := NewStore()
	if err := store.Load(reg); err != nil {
		t.Fatal(err)
	}

	servers := store.List()
	if len(servers) != 1 {
		t.Fatalf("expected 1 merged server (Codex table + Claude Code), got %d", len(servers))
	}
	srv := servers[0]
	if !store.AgentBound(srv.ID, "codex") {
		t.Error("expected server bound to codex")
	}
	if !store.AgentBound(srv.ID, "claude-code") {
		t.Error("expected server bound to claude-code")
	}
}

func containsStr(s, sub string) bool {
	return strings.Contains(s, sub)
}

func TestJsonBackend_ClaudeCodeTypeFieldPreserved(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "claude.json")

	// Claude Code 使用 "type": "stdio" 而非 "transport"
	writeFile(t, path, `{
		"mcpServers": {
			"context7": {
				"type": "stdio",
				"command": "cmd",
				"args": ["/c", "npx", "-y", "@upstash/context7-mcp"]
			},
			"fetch": {
				"type": "stdio",
				"command": "uvx",
				"args": ["mcp-server-fetch"]
			}
		}
	}`)

	backend := NewJsonBackend("claude-code")
	servers, err := backend.Read(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(servers) != 2 {
		t.Fatalf("expected 2 servers, got %d", len(servers))
	}

	// 验证 ConfigType 被保留
	c7, ok := servers["context7"]
	if !ok {
		t.Fatal("expected context7 server")
	}
	if c7.ConfigType != "stdio" {
		t.Errorf("context7: expected configType=stdio, got %q", c7.ConfigType)
	}
	if c7.Transport != TransportStdio {
		t.Errorf("context7: expected transport=stdio, got %q", c7.Transport)
	}

	// 写回并验证 type 字段保留
	if err := backend.Write(path, servers); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	body := string(data)
	if !containsStr(body, `"type"`) {
		t.Error("expected 'type' field in output")
	}
	if containsStr(body, `"transport"`) {
		t.Error("expected no 'transport' field in output (Claude Code uses 'type')")
	}

	// 重新读取验证往返一致
	out, err := backend.Read(path)
	if err != nil {
		t.Fatal(err)
	}
	if out["context7"].ConfigType != "stdio" {
		t.Errorf("round-trip: expected configType=stdio, got %q", out["context7"].ConfigType)
	}
}

func TestJsonBackend_CursorNoTypeField(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "cursor.json")

	// Cursor 不使用 type 字段
	writeFile(t, path, `{
		"mcpServers": {
			"github": {
				"command": "npx",
				"args": ["-y", "@mcp/server-github"]
			}
		}
	}`)

	backend := NewJsonBackend("cursor")
	servers, err := backend.Read(path)
	if err != nil {
		t.Fatal(err)
	}
	gh, ok := servers["github"]
	if !ok {
		t.Fatal("expected github server")
	}
	if gh.ConfigType != "" {
		t.Errorf("cursor: expected empty configType, got %q", gh.ConfigType)
	}

	// 写回时应自动推导 type 字段
	if err := backend.Write(path, servers); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	body := string(data)
	if !containsStr(body, `"type"`) {
		t.Error("expected 'type' field to be derived from transport for Cursor write-back")
	}
}
