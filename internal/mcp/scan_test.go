package mcp

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"agentpack/internal/agents"
)

// 核心场景：扫描发现 agent 配置里已存在但未被管理的服务器，
// "加入管理"（Add 到来源 agent）应成功，且不改写用户原有配置文件。
func TestScanThenAddAdoptsExistingEntry(t *testing.T) {
	resetTestDB(t)
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("APPDATA", "")

	claudePath := filepath.Join(home, ".claude.json")
	original := `{"mcpServers":{"context7":{"command":"npx","args":["-y","@upstash/context7-mcp"]}}}`
	writeFile(t, claudePath, original)

	reg := agents.NewRegistry()
	reg.Register(agents.Agent{
		ID: "claude-code", Name: "Claude Code", Type: agents.TypeClaudeCode,
		Status: agents.StatusEnabled, ConfigPath: claudePath, ConfigFormat: agents.FormatJSON,
	})

	store := NewStore() // 未 Load：什么都不管理
	scan := store.Scan(reg)
	if scan.Total != 1 || scan.Managed != 0 {
		t.Fatalf("expected 1 unmanaged item, got %#v", scan)
	}
	item := scan.Items[0]

	server := item.Server
	server.ID = "" // 前端复位 id
	if _, err := store.Add(server, []string{"claude-code"}, reg); err != nil {
		t.Fatalf("adopt scanned server for source agent should succeed: %v", err)
	}
	// 采纳已存在条目不应改写用户配置
	data, err := os.ReadFile(claudePath)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != original {
		t.Errorf("config was rewritten on adopt:\n%s", data)
	}
	if len(store.ByAgent("claude-code")) != 1 {
		t.Fatal("expected server bound to claude-code")
	}
	// 二次扫描应标记为已管理
	again := store.Scan(reg)
	if again.Managed != 1 {
		t.Fatalf("expected managed=1 after adopt, got %#v", again)
	}
	// 重复 Add 同一 key 应被拒绝（复用已有条目）
	server.ID = ""
	if _, err := store.Add(server, []string{"claude-code"}, reg); !errors.Is(err, ErrDuplicateServer) {
		t.Fatalf("expected duplicate add rejected with ErrDuplicateServer, got %v", err)
	}
}

// 同一 key 已管理时，Add 应拒绝，避免 store 内出现同 key 双份。
func TestStore_AddRejectsDuplicateKey(t *testing.T) {
	resetTestDB(t)
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("APPDATA", "")

	claudePath := filepath.Join(home, ".claude.json")
	writeFile(t, claudePath, `{}`)
	reg := agents.NewRegistry()
	reg.Register(agents.Agent{
		ID: "claude-code", Name: "Claude Code", Type: agents.TypeClaudeCode,
		Status: agents.StatusEnabled, ConfigPath: claudePath, ConfigFormat: agents.FormatJSON,
	})

	store := NewStore()
	if err := store.Load(reg); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Add(Server{
		Name: "github", Command: "npx", Args: []string{"-y", "@mcp/server-github"}, Transport: TransportStdio,
	}, []string{"claude-code"}, reg); err != nil {
		t.Fatal(err)
	}
	// 同名同命令再次 Add：拒绝
	if _, err := store.Add(Server{
		Name: "github2", Command: "npx", Args: []string{"-y", "@mcp/server-github"}, Transport: TransportStdio,
	}, []string{"claude-code"}, reg); err == nil {
		t.Fatal("expected duplicate key add rejected")
	}
	if len(store.List()) != 1 {
		t.Fatalf("expected 1 server, got %d", len(store.List()))
	}
}

// 用户手动把被管理条目替换成同名不同命令后，Remove 不应误删用户条目。
func TestStore_RemoveSkipsUserModifiedEntry(t *testing.T) {
	resetTestDB(t)
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("APPDATA", "")

	claudePath := filepath.Join(home, ".claude.json")
	writeFile(t, claudePath, `{}`)
	reg := agents.NewRegistry()
	reg.Register(agents.Agent{
		ID: "claude-code", Name: "Claude Code", Type: agents.TypeClaudeCode,
		Status: agents.StatusEnabled, ConfigPath: claudePath, ConfigFormat: agents.FormatJSON,
	})
	store := NewStore()
	if err := store.Load(reg); err != nil {
		t.Fatal(err)
	}
	srv, err := store.Add(Server{
		Name: "github", Command: "npx", Args: []string{"-y", "@mcp/server-github"}, Transport: TransportStdio,
	}, []string{"claude-code"}, reg)
	if err != nil {
		t.Fatal(err)
	}
	// 用户手动替换为同名不同命令的服务器
	writeFile(t, claudePath, `{"mcpServers":{"github":{"command":"python","args":["my_server.py"]}}}`)

	if err := store.Remove(srv.ID, reg); err != nil {
		t.Fatal(err)
	}
	disk, err := NewBackend("claude-code").Read(claudePath)
	if err != nil {
		t.Fatal(err)
	}
	got, ok := disk["github"]
	if !ok {
		t.Fatal("user-modified entry should survive Remove")
	}
	if got.Command != "python" {
		t.Fatalf("user-modified entry was altered: %#v", got)
	}
	if _, ok := store.Get(srv.ID); ok {
		t.Fatal("expected server gone from store")
	}
}

// 单个坏 entry（危险命令）只跳过该条，不影响同文件其它服务器；返回 ErrPartialRead。
func TestJsonBackend_ReadSkipsInvalidEntries(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "mcp.json")
	writeFile(t, path, `{"mcpServers":{"bad":{"command":"echo $HOME"},"good":{"command":"npx","args":["-y","pkg"]}}}`)
	backend := NewJsonBackend("claude-code")
	out, err := backend.Read(path)
	if !errors.Is(err, ErrPartialRead) {
		t.Fatalf("expected ErrPartialRead, got %v", err)
	}
	if _, ok := out["bad"]; ok {
		t.Error("bad entry should be skipped")
	}
	if _, ok := out["good"]; !ok {
		t.Error("good entry should be kept")
	}
}

// 部分加载后 store 仍 Ready，正常配置保留（App 层依赖该语义决定是否锁死）。
func TestStore_ReadyAfterPartialLoad(t *testing.T) {
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
	if !store.Ready() {
		t.Fatal("expected store Ready() after partial load")
	}
	if got := store.List(); len(got) != 1 || got[0].Name != "valid" {
		t.Fatalf("valid config was discarded with malformed config: %#v", got)
	}
}

// 共享同一配置文件的多个 agent 都应记录为来源。
func TestStore_ScanRecordsAllSharedPathAgents(t *testing.T) {
	resetTestDB(t)
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("APPDATA", "")

	sharedPath := filepath.Join(home, "shared.json")
	writeFile(t, sharedPath, `{"mcpServers":{"context7":{"command":"npx","args":["-y","@upstash/context7-mcp"]}}}`)

	reg := agents.NewRegistry()
	reg.Register(agents.Agent{ID: "a", Name: "Agent A", Type: agents.TypeClaudeCode, Status: agents.StatusEnabled, ConfigPath: sharedPath, ConfigFormat: agents.FormatJSON})
	reg.Register(agents.Agent{ID: "b", Name: "Agent B", Type: agents.TypeClaudeCode, Status: agents.StatusEnabled, ConfigPath: sharedPath, ConfigFormat: agents.FormatJSON})

	store := NewStore()
	scan := store.Scan(reg)
	if scan.Total != 1 {
		t.Fatalf("expected 1 item, got %d", scan.Total)
	}
	item := scan.Items[0]
	if len(item.Sources) != 2 {
		t.Fatalf("expected 2 sources for shared path, got %v", item.Sources)
	}
	names := map[string]bool{}
	for _, s := range item.Sources {
		names[s.AgentName] = true
	}
	if !names["Agent A"] || !names["Agent B"] {
		t.Fatalf("expected both shared-path agents in sources: %v", item.Sources)
	}
}

// 写路径拒绝含未解析条目的配置：否则整表重写会把用户条目静默删除。
// 只读路径（Load/Scan）仍保留可解析条目。
func TestStore_MutationRefusedOnPartialConfig(t *testing.T) {
	resetTestDB(t)
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("APPDATA", "")

	claudePath := filepath.Join(home, ".claude.json")
	original := `{"mcpServers":{"bad":{"command":"echo $HOME"},"good":{"command":"npx","args":["-y","pkg"]}}}`
	writeFile(t, claudePath, original)

	reg := agents.NewRegistry()
	reg.Register(agents.Agent{
		ID: "claude-code", Name: "Claude Code", Type: agents.TypeClaudeCode,
		Status: agents.StatusEnabled, ConfigPath: claudePath, ConfigFormat: agents.FormatJSON,
	})
	seedManagedFromDisk(t, reg)

	store := NewStore()
	if err := store.Load(reg); err != nil {
		t.Fatal(err)
	}
	// Load 保留可解析条目
	if got := store.List(); len(got) != 1 || got[0].Name != "good" {
		t.Fatalf("expected good entry loaded, got %#v", got)
	}
	// 向该 agent 添加新服务器必须被拒绝（否则重写会删除 bad 条目）
	if _, err := store.Add(Server{
		Name: "github", Command: "npx", Args: []string{"-y", "@mcp/server-github"}, Transport: TransportStdio,
	}, []string{"claude-code"}, reg); err == nil {
		t.Fatal("expected add refused on partial config")
	}
	data, _ := os.ReadFile(claudePath)
	if string(data) != original {
		t.Fatalf("config was rewritten despite unparsable entries:\n%s", data)
	}
}

// Update 改为与其它服务器同 key 时应拒绝，避免 store 内同 key 双份。
func TestStore_UpdateRejectsKeyCollision(t *testing.T) {
	resetTestDB(t)
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("APPDATA", "")

	claudePath := filepath.Join(home, ".claude.json")
	writeFile(t, claudePath, `{}`)
	reg := agents.NewRegistry()
	reg.Register(agents.Agent{
		ID: "claude-code", Name: "Claude Code", Type: agents.TypeClaudeCode,
		Status: agents.StatusEnabled, ConfigPath: claudePath, ConfigFormat: agents.FormatJSON,
	})
	store := NewStore()
	if err := store.Load(reg); err != nil {
		t.Fatal(err)
	}
	a, err := store.Add(Server{Name: "a", Command: "npx", Args: []string{"-y", "pkg-a"}, Transport: TransportStdio}, []string{"claude-code"}, reg)
	if err != nil {
		t.Fatal(err)
	}
	b, err := store.Add(Server{Name: "b", Command: "uvx", Args: []string{"pkg-b"}, Transport: TransportStdio}, []string{"claude-code"}, reg)
	if err != nil {
		t.Fatal(err)
	}
	// 把 b 改成与 a 同 key → 拒绝
	err = store.Update(b.ID, Server{Name: "b", Command: "npx", Args: []string{"-y", "pkg-a"}, Transport: TransportStdio}, []string{"claude-code"}, reg)
	if !errors.Is(err, ErrDuplicateServer) {
		t.Fatalf("expected ErrDuplicateServer, got %v", err)
	}
	if len(store.List()) != 2 {
		t.Fatalf("expected still 2 servers, got %d", len(store.List()))
	}
	// 只改名称（key 不变）→ 允许
	if err := store.Update(a.ID, Server{Name: "a2", Command: "npx", Args: []string{"-y", "pkg-a"}, Transport: TransportStdio}, []string{"claude-code"}, reg); err != nil {
		t.Fatalf("name-only update should succeed: %v", err)
	}
}

// ToggleAgent 绑定到"配置里已有同 key 条目"的 agent（部分管理场景）时采纳该条目，不改写配置。
func TestStore_ToggleAgentAdoptsExistingEntry(t *testing.T) {
	resetTestDB(t)
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("APPDATA", "")

	claudePath := filepath.Join(home, ".claude.json")
	cursorPath := filepath.Join(home, ".cursor", "mcp.json")
	writeFile(t, cursorPath, `{"mcpServers":{"context7":{"command":"npx","args":["-y","@upstash/context7-mcp"]}}}`)

	reg := agents.NewRegistry()
	reg.Register(agents.Agent{ID: "claude-code", Name: "Claude Code", Type: agents.TypeClaudeCode, Status: agents.StatusEnabled, ConfigPath: claudePath, ConfigFormat: agents.FormatJSON})
	reg.Register(agents.Agent{ID: "cursor", Name: "Cursor", Type: agents.TypeCursor, Status: agents.StatusEnabled, ConfigPath: cursorPath, ConfigFormat: agents.FormatJSON})
	seedManagedFromDisk(t, reg)

	store := NewStore()
	if err := store.Load(reg); err != nil {
		t.Fatal(err)
	}
	ids := store.List()
	if len(ids) != 1 {
		t.Fatalf("expected 1 server from cursor config, got %d", len(ids))
	}
	id := ids[0].ID

	// claude 配置里手动装了同一条（部分管理场景）
	origClaude := `{"mcpServers":{"context7":{"command":"npx","args":["-y","@upstash/context7-mcp"]}}}`
	writeFile(t, claudePath, origClaude)

	if err := store.ToggleAgent(id, "claude-code", true, reg); err != nil {
		t.Fatalf("toggle should adopt existing entry: %v", err)
	}
	data, _ := os.ReadFile(claudePath)
	if string(data) != origClaude {
		t.Fatalf("claude config rewritten on adopt:\n%s", data)
	}
	if !store.AgentBound(id, "claude-code") {
		t.Fatal("expected binding to claude-code")
	}
}

// URL 服务器按 URL 合并：不同 URL 不再全部塌缩为一条（修复 mergeDuplicatesLocked 的 cmdKey 塌缩）。
func TestStore_LoadKeepsDistinctURLServers(t *testing.T) {
	resetTestDB(t)
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("APPDATA", "")

	claudePath := filepath.Join(home, ".claude.json")
	writeFile(t, claudePath, `{"mcpServers":{"sse-a":{"type":"sse","url":"https://a.example.com/mcp"},"sse-b":{"type":"sse","url":"https://b.example.com/mcp"}}}`)

	reg := agents.NewRegistry()
	reg.Register(agents.Agent{ID: "claude-code", Name: "Claude Code", Type: agents.TypeClaudeCode, Status: agents.StatusEnabled, ConfigPath: claudePath, ConfigFormat: agents.FormatJSON})
	seedManagedFromDisk(t, reg)

	store := NewStore()
	if err := store.Load(reg); err != nil {
		t.Fatal(err)
	}
	servers := store.List()
	if len(servers) != 2 {
		t.Fatalf("expected 2 distinct URL servers, got %d", len(servers))
	}
}

// 同一 URL 的服务器跨 agent 合并为一条并绑定所有来源。
func TestStore_LoadMergesSameURLServer(t *testing.T) {
	resetTestDB(t)
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("APPDATA", "")

	claudePath := filepath.Join(home, ".claude.json")
	writeFile(t, claudePath, `{"mcpServers":{"rmcp":{"type":"sse","url":"https://a.example.com/mcp"}}}`)
	cursorPath := filepath.Join(home, ".cursor", "mcp.json")
	writeFile(t, cursorPath, `{"mcpServers":{"rmcp2":{"type":"sse","url":"https://a.example.com/mcp"}}}`)

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
		t.Fatalf("expected 1 merged URL server, got %d", len(servers))
	}
	if !store.AgentBound(servers[0].ID, "claude-code") || !store.AgentBound(servers[0].ID, "cursor") {
		t.Error("expected merged server bound to both agents")
	}
}

// 无法整体读取的配置文件计入 ScanResult.Failed，其余配置的服务器仍正常展示。
func TestStore_ScanCountsFailedConfigs(t *testing.T) {
	resetTestDB(t)
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("APPDATA", "")

	validPath := filepath.Join(home, ".claude.json")
	invalidPath := filepath.Join(home, ".cursor", "mcp.json")
	writeFile(t, validPath, `{"mcpServers":{"valid":{"command":"echo"}}}`)
	writeFile(t, invalidPath, `{"mcpServers":`) // 整体 JSON 语法错误

	reg := agents.NewRegistry()
	reg.Register(agents.Agent{ID: "claude-code", Name: "Claude Code", Type: agents.TypeClaudeCode, Status: agents.StatusEnabled, ConfigPath: validPath, ConfigFormat: agents.FormatJSON})
	reg.Register(agents.Agent{ID: "cursor", Name: "Cursor", Type: agents.TypeCursor, Status: agents.StatusEnabled, ConfigPath: invalidPath, ConfigFormat: agents.FormatJSON})

	store := NewStore()
	scan := store.Scan(reg)
	if scan.Failed != 1 {
		t.Fatalf("expected 1 failed config, got %d", scan.Failed)
	}
	if len(scan.Items) != 1 || scan.Items[0].Server.Name != "valid" {
		t.Fatalf("expected valid entry scanned, got %#v", scan.Items)
	}
}

func dirFileCount(t *testing.T, dir string) int {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return 0
		}
		t.Fatal(err)
	}
	return len(entries)
}

// 采纳已存在条目（写入被跳过）时不生成无意义的备份快照；正常新增仍生成。
func TestStore_AdoptSkipsBackupSnapshot(t *testing.T) {
	resetTestDB(t)
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("APPDATA", "")

	claudePath := filepath.Join(home, ".claude.json")
	writeFile(t, claudePath, `{"mcpServers":{"context7":{"command":"npx","args":["-y","@upstash/context7-mcp"]}}}`)
	reg := agents.NewRegistry()
	reg.Register(agents.Agent{ID: "claude-code", Name: "Claude Code", Type: agents.TypeClaudeCode, Status: agents.StatusEnabled, ConfigPath: claudePath, ConfigFormat: agents.FormatJSON})

	backupDir := filepath.Join(home, ".agentpack", "backups", "mcp")
	before := dirFileCount(t, backupDir)

	store := NewStore() // 未 Load：context7 未管理
	item := store.Scan(reg).Items[0]
	server := item.Server
	server.ID = ""
	if _, err := store.Add(server, []string{"claude-code"}, reg); err != nil {
		t.Fatal(err)
	}
	if after := dirFileCount(t, backupDir); after != before {
		t.Fatalf("adopt should not create backup snapshot: %d -> %d", before, after)
	}

	// 对照：向空配置新增（真实写入）应生成快照
	cursorPath := filepath.Join(home, ".cursor", "mcp.json")
	writeFile(t, cursorPath, `{}`)
	reg.Register(agents.Agent{ID: "cursor", Name: "Cursor", Type: agents.TypeCursor, Status: agents.StatusEnabled, ConfigPath: cursorPath, ConfigFormat: agents.FormatJSON})
	store2 := NewStore()
	before2 := dirFileCount(t, backupDir)
	if _, err := store2.Add(Server{Name: "github", Command: "npx", Args: []string{"-y", "@mcp/server-github"}, Transport: TransportStdio}, []string{"cursor"}, reg); err != nil {
		t.Fatal(err)
	}
	if after2 := dirFileCount(t, backupDir); after2 != before2+1 {
		t.Fatalf("real add should create 1 backup snapshot: %d -> %d", before2, after2)
	}
}
