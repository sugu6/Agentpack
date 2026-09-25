package store

import (
	"agentpack/internal/agents"
	"agentpack/internal/mcp/types"
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

// TestStore_UpdateCarriesOverExtraKeys 验证 Update 整表重写时被更新条目
// 上用户手工添加的未建模键（Extra，如 Claude 的 oauth）不被静默删除。
func TestStore_UpdateCarriesOverExtraKeys(t *testing.T) {
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

	server := types.Server{
		Name:      "extra-carryover",
		Command:   "npx",
		Args:      []string{"-y", "some-server"},
		Transport: types.TransportStdio,
	}
	added, err := store.Add(server, []string{"claude-code"}, reg)
	if err != nil {
		t.Fatal(err)
	}

	// 模拟用户在配置文件手工给条目添加未建模键（oauth）
	var cfg map[string]map[string]any
	data, err := os.ReadFile(claudePath)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(data, &cfg); err != nil {
		t.Fatal(err)
	}
	entry, ok := cfg["mcpServers"]["extra-carryover"].(map[string]any)
	if !ok {
		t.Fatalf("entry not found: %s", data)
	}
	entry["oauth"] = map[string]any{"clientId": "user-added"}
	data, err = json.Marshal(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(claudePath, data, 0644); err != nil {
		t.Fatal(err)
	}

	// Update：改 args，请求不带 oauth → 重写后 oauth 必须保留
	updated := types.Server{
		Name:      "extra-carryover",
		Command:   "npx",
		Args:      []string{"-y", "some-server", "--debug"},
		Transport: types.TransportStdio,
	}
	if err := store.Update(added.ID, updated, []string{"claude-code"}, reg); err != nil {
		t.Fatal(err)
	}

	data, err = os.ReadFile(claudePath)
	if err != nil {
		t.Fatal(err)
	}
	var after map[string]map[string]any
	if err := json.Unmarshal(data, &after); err != nil {
		t.Fatal(err)
	}
	entry, ok = after["mcpServers"]["extra-carryover"].(map[string]any)
	if !ok {
		t.Fatalf("entry missing after update: %s", data)
	}
	if _, ok := entry["oauth"]; !ok {
		t.Errorf("user-added oauth key lost on Update rewrite:\n%s", data)
	}
	args, _ := entry["args"].([]any)
	if len(args) != 3 {
		t.Errorf("expected updated args, got %v", entry["args"])
	}
}

// setEntryExtra 直接在配置文件的 mcpServers 条目上手工添加未建模键，
// 模拟用户手工编辑（与 TestStore_UpdateCarriesOverExtraKeys 相同手法）。
func setEntryExtra(t *testing.T, path, serverName, key string, value any) {
	t.Helper()
	var cfg map[string]map[string]any
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(data, &cfg); err != nil {
		t.Fatal(err)
	}
	entry, ok := cfg["mcpServers"][serverName].(map[string]any)
	if !ok {
		t.Fatalf("entry %q not found in %s: %s", serverName, path, data)
	}
	entry[key] = value
	data, err = json.Marshal(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0644); err != nil {
		t.Fatal(err)
	}
}

// readEntryExtra 读取配置文件中某条目上的一个未建模键，返回 (键存在, 值)。
func readEntryExtra(t *testing.T, path, serverName, key string) (bool, any) {
	t.Helper()
	var cfg map[string]map[string]any
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(data, &cfg); err != nil {
		t.Fatal(err)
	}
	entry, ok := cfg["mcpServers"][serverName].(map[string]any)
	if !ok {
		return false, nil
	}
	v, ok := entry[key]
	return ok, v
}

// registerTwoAgents 注册 claude-code 与 cursor 两个 JSON agent（配置路径在
// 临时 HOME 下），返回 registry 与两个配置路径。
func registerTwoAgents(t *testing.T, home string) (*agents.Registry, string, string) {
	t.Helper()
	claudePath := filepath.Join(home, ".claude.json")
	cursorPath := filepath.Join(home, ".cursor", "mcp.json")
	writeFile(t, claudePath, `{}`)
	writeFile(t, cursorPath, `{}`)
	reg := agents.NewRegistry()
	reg.Register(agents.Agent{
		ID:           "claude-code",
		Name:         "Claude Code",
		Type:         agents.TypeClaudeCode,
		Status:       agents.StatusEnabled,
		ConfigPath:   claudePath,
		ConfigFormat: agents.FormatJSON,
	})
	reg.Register(agents.Agent{
		ID:           "cursor",
		Name:         "Cursor",
		Type:         agents.TypeCursor,
		Status:       agents.StatusEnabled,
		ConfigPath:   cursorPath,
		ConfigFormat: agents.FormatJSON,
	})
	return reg, claudePath, cursorPath
}

func newTestStore(t *testing.T) *Store {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("APPDATA", "")
	return NewStore()
}

// 回归测试（第 6 条并发重构）：配置文件 IO 已移出 s.mu 写锁，读操作不再被
// 长事务阻塞。用有界超时断言读写并发不死锁：出现死锁时快速失败而非挂起。
func TestStore_ConcurrentReadWriteNoDeadlock(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("APPDATA", "")

	claudePath := filepath.Join(home, ".claude.json")
	writeFile(t, claudePath, `{}`)
	reg := agents.NewRegistry()
	reg.Register(agents.Agent{ID: "claude-code", Name: "Claude Code", Type: agents.TypeClaudeCode, Status: agents.StatusEnabled, ConfigPath: claudePath})

	store := NewStore()
	if err := store.Load(reg); err != nil {
		t.Fatal(err)
	}
	created, err := store.Add(types.Server{Name: "seed", Command: "echo", Transport: types.TransportStdio}, []string{"claude-code"}, reg)
	if err != nil {
		t.Fatal(err)
	}

	finished := make(chan struct{})
	go func() {
		defer close(finished)
		var wg sync.WaitGroup
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < 200; i++ {
				_ = store.List()
				_, _ = store.Get(created.ID)
				_ = store.Ready()
				_ = store.AgentBound(created.ID, "claude-code")
				_ = store.AgentMcpCounts()
			}
		}()
		for i := 0; i < 30; i++ {
			if err := store.ToggleAgent(created.ID, "claude-code", i%2 == 1, reg); err != nil {
				t.Errorf("toggle: %v", err)
				return
			}
		}
		wg.Wait()
	}()

	select {
	case <-finished:
	case <-time.After(60 * time.Second):
		t.Fatal("并发读写死锁")
	}
}

// TestStore_MultiAgentUpdateKeepsPerFileExtra 验证绑定多个 agent 的服务器在
// Update 整表重写时，各配置文件保留自己条目上的 Extra 键，不被其他文件的键
// 覆盖、也不丢失（回归：共享 *Server 在路径循环内被改写导致跨文件污染）。
func TestStore_MultiAgentUpdateKeepsPerFileExtra(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("APPDATA", "")
	reg, claudePath, cursorPath := registerTwoAgents(t, home)

	store := NewStore()
	if err := store.Load(reg); err != nil {
		t.Fatal(err)
	}
	added, err := store.Add(types.Server{
		Name:      "multi",
		Command:   "npx",
		Args:      []string{"-y", "some-server"},
		Transport: types.TransportStdio,
	}, []string{"claude-code", "cursor"}, reg)
	if err != nil {
		t.Fatal(err)
	}

	// 各文件的条目分别手工添加不同的未建模键
	setEntryExtra(t, claudePath, "multi", "oauth", map[string]any{"clientId": "claude-user"})
	setEntryExtra(t, cursorPath, "multi", "envFile", ".env.claude-style")

	// 普通编辑（改 args）触发两个文件的整表重写
	if err := store.Update(added.ID, types.Server{
		Name:      "multi",
		Command:   "npx",
		Args:      []string{"-y", "some-server", "--debug"},
		Transport: types.TransportStdio,
	}, []string{"claude-code", "cursor"}, reg); err != nil {
		t.Fatal(err)
	}

	if ok, _ := readEntryExtra(t, claudePath, "multi", "oauth"); !ok {
		t.Errorf("claude file lost its own oauth key after Update:\n%s", mustRead(t, claudePath))
	}
	if ok, _ := readEntryExtra(t, claudePath, "multi", "envFile"); ok {
		t.Errorf("claude file was contaminated with cursor's envFile key:\n%s", mustRead(t, claudePath))
	}
	if ok, _ := readEntryExtra(t, cursorPath, "multi", "envFile"); !ok {
		t.Errorf("cursor file lost its own envFile key after Update:\n%s", mustRead(t, cursorPath))
	}
	if ok, _ := readEntryExtra(t, cursorPath, "multi", "oauth"); ok {
		t.Errorf("cursor file was contaminated with claude's oauth key:\n%s", mustRead(t, cursorPath))
	}
}

// TestStore_RenamePreservesExtra 验证改名后新名条目保留原条目上的未建模键
// （回归：pendingExtra 捕获被新旧同名条件门控，改名必丢 Extra）。
func TestStore_RenamePreservesExtra(t *testing.T) {
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
	added, err := store.Add(types.Server{
		Name:      "orig",
		Command:   "npx",
		Args:      []string{"-y", "some-server"},
		Transport: types.TransportStdio,
	}, []string{"claude-code"}, reg)
	if err != nil {
		t.Fatal(err)
	}

	setEntryExtra(t, claudePath, "orig", "oauth", map[string]any{"clientId": "user-added"})

	// 改名 Update：新名条目必须带上原条目的 oauth
	if err := store.Update(added.ID, types.Server{
		Name:      "renamed",
		Command:   "npx",
		Args:      []string{"-y", "some-server"},
		Transport: types.TransportStdio,
	}, []string{"claude-code"}, reg); err != nil {
		t.Fatal(err)
	}
	if ok, _ := readEntryExtra(t, claudePath, "renamed", "oauth"); !ok {
		t.Errorf("rename dropped user-added oauth key:\n%s", mustRead(t, claudePath))
	}
	if ok, _ := readEntryExtra(t, claudePath, "orig", "oauth"); ok {
		t.Errorf("old-name entry survived rename:\n%s", mustRead(t, claudePath))
	}
}

// TestStore_BindDoesNotInjectForeignExtra 验证把服务器绑定到另一个 agent 时，
// 目标文件的新条目不被来源 agent 的 Extra 键污染（Extra 是每文件条目属性，
// 只能取目标文件自身的既有键）。
func TestStore_BindDoesNotInjectForeignExtra(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("APPDATA", "")
	reg, claudePath, cursorPath := registerTwoAgents(t, home)

	store := NewStore()
	if err := store.Load(reg); err != nil {
		t.Fatal(err)
	}
	added, err := store.Add(types.Server{
		Name:      "bindme",
		Command:   "npx",
		Args:      []string{"-y", "some-server"},
		Transport: types.TransportStdio,
	}, []string{"claude-code"}, reg)
	if err != nil {
		t.Fatal(err)
	}

	// 用户在 claude 文件手工添加 oauth；重新 Load 让内存 Extra 携带它
	setEntryExtra(t, claudePath, "bindme", "oauth", map[string]any{"clientId": "claude-user"})
	if err := store.Load(reg); err != nil {
		t.Fatal(err)
	}
	if srv, ok := store.servers[added.ID]; !ok || srv.Extra == nil {
		t.Fatalf("precondition failed: in-memory Extra not loaded from claude config: %+v", srv)
	}

	// 绑定到 cursor：cursor 文件的新条目不得出现 claude 的 oauth
	if err := store.ToggleAgent(added.ID, "cursor", true, reg); err != nil {
		t.Fatal(err)
	}
	if ok, _ := readEntryExtra(t, cursorPath, "bindme", "oauth"); ok {
		t.Errorf("bind injected foreign claude oauth into cursor file:\n%s", mustRead(t, cursorPath))
	}
}

// mustRead 返回文件内容（测试失败消息用）。
func mustRead(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}
