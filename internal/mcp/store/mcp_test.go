package store

import (
	"agentpack/internal/agents"
	"agentpack/internal/database"
	"agentpack/internal/mcp/types"
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

	server := types.Server{
		Name:      "github",
		Command:   "npx",
		Args:      []string{"-y", "@mcp/server-github"},
		Transport: types.TransportStdio,
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
	if _, err := store.Add(types.Server{Name: "github", Command: "npx", Transport: types.TransportStdio}, []string{"claude-code"}, reg); err != nil {
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
	if _, err := store.Add(types.Server{Name: "io.example/filesystem", Command: "npx", Transport: types.TransportStdio}, []string{"claude-code"}, reg); err != nil {
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

	_, err := store.Add(types.Server{Name: "github", Command: "npx", Transport: types.TransportStdio}, []string{"claude-code", "cursor"}, reg)
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

	_, err := store.Add(types.Server{Name: "github", Command: "npx", Transport: types.TransportStdio}, []string{"claude-code", "cursor"}, reg)
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
	seedManagedFromDisk(t, reg)

	store := NewStore()
	if err := store.Load(reg); err != nil {
		t.Fatal(err)
	}
	servers := store.List()
	if len(servers) != 1 {
		t.Fatalf("expected merged server, got %d", len(servers))
	}
	writeFile(t, cursorPath, `{"mcpServers":`)

	err := store.Update(servers[0].ID, types.Server{Name: "github", Command: "uvx", Transport: types.TransportStdio}, []string{"claude-code", "cursor"}, reg)
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
	created, err := store.Add(types.Server{
		Name:      "github",
		Command:   "npx",
		Env:       map[string]string{"GITHUB_TOKEN": "secret-token", "DEBUG": "1"},
		Transport: types.TransportStdio,
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
	_, err := store.Add(types.Server{Name: "github", Command: "npx", Transport: types.TransportStdio}, []string{"claude-code"}, reg)
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
	seedManagedFromDisk(t, reg)

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
	seedManagedFromDisk(t, reg)

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
	seedManagedFromDisk(t, reg)

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
	seedManagedFromDisk(t, reg)

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
	seedManagedFromDisk(t, reg)

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

	server := types.Server{
		Name:      "github",
		Command:   "npx",
		Args:      []string{"-y", "@mcp/server-github"},
		Transport: types.TransportStdio,
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

func TestStore_AddRejectsExistingServerNameOnAgent(t *testing.T) {
	resetTestDB(t)
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("APPDATA", "")

	claudePath := filepath.Join(home, ".claude.json")
	writeFile(t, claudePath, `{"mcpServers":{"github":{"command":"old","args":["server"]}}}`)
	reg := agents.NewRegistry()
	reg.Register(agents.Agent{
		ID: "claude-code", Name: "Claude Code", Type: agents.TypeClaudeCode,
		Status: agents.StatusEnabled, ConfigPath: claudePath, ConfigFormat: agents.FormatJSON,
	})
	store := NewStore()
	if err := store.Load(reg); err != nil {
		t.Fatal(err)
	}

	_, err := store.Add(types.Server{
		Name: "github", Command: "new", Args: []string{"server"}, Transport: types.TransportStdio,
	}, []string{"claude-code"}, reg)
	if err == nil {
		t.Fatal("expected same-name add to be rejected")
	}
	disk, err := NewBackend("claude-code").Read(claudePath)
	if err != nil {
		t.Fatal(err)
	}
	if disk["github"].Command != "old" {
		t.Fatalf("existing server was overwritten: %#v", disk["github"])
	}
}

func TestStore_LoadKeepsValidConfigsWhenAnotherConfigIsMalformed(t *testing.T) {
	resetTestDB(t)
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("APPDATA", "")
	validPath := filepath.Join(home, ".claude.json")
	invalidPath := filepath.Join(home, ".cursor", "mcp.json")
	writeFile(t, validPath, `{"mcpServers":{"valid":{"command":"echo"}}}`)
	writeFile(t, invalidPath, `{"mcpServers":`)

	reg := agents.NewRegistry()
	reg.Register(agents.Agent{ID: "claude-code", Name: "Claude Code", Type: agents.TypeClaudeCode, Status: agents.StatusEnabled, ConfigPath: validPath, ConfigFormat: agents.FormatJSON})
	reg.Register(agents.Agent{ID: "cursor", Name: "Cursor", Type: agents.TypeCursor, Status: agents.StatusEnabled, ConfigPath: invalidPath, ConfigFormat: agents.FormatJSON})
	seedManagedFromDisk(t, reg)

	store := NewStore()
	if err := store.Load(reg); err == nil {
		t.Fatal("expected malformed config error")
	}
	if got := store.List(); len(got) != 1 || got[0].Name != "valid" {
		t.Fatalf("valid config was discarded with malformed config: %#v", got)
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
	seedManagedFromDisk(t, reg)

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
	seedManagedFromDisk(t, reg)

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
	seedManagedFromDisk(t, reg)

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

	_, err := store.Add(types.Server{Command: "npx"}, []string{"claude-code"}, reg)
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

	_, err := store.Add(types.Server{Name: "x", Command: "npx"}, []string{}, reg)
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
	seedManagedFromDisk(t, reg)

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
	seedManagedFromDisk(t, reg)

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
