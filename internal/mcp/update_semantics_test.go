package mcp

import (
	"os"
	"path/filepath"
	"testing"

	"agentpack/internal/agents"
)

// 回归测试：Update 整体替换绑定时，被解绑 agent 的配置文件条目必须删除，
// 否则内存已解绑而磁盘未删，重启 Load 扫描后绑定"复活"。
func TestStore_UpdateUnbindRemovesConfigEntry(t *testing.T) {
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

	created, err := store.Add(Server{Name: "github", Command: "npx", Transport: TransportStdio}, []string{"claude-code", "cursor"}, reg)
	if err != nil {
		t.Fatal(err)
	}

	// 更新：仅保留 claude-code，解绑 cursor
	err = store.Update(created.ID, Server{Name: "github", Command: "npx", Transport: TransportStdio}, []string{"claude-code"}, reg)
	if err != nil {
		t.Fatal(err)
	}

	disk, rerr := NewBackend("cursor").Read(cursorPath)
	if rerr != nil {
		t.Fatal(rerr)
	}
	if _, ok := disk["github"]; ok {
		t.Errorf("cursor config still contains 'github' entry after unbind: %v", disk)
	}
	if store.AgentBound(created.ID, "cursor") {
		t.Error("cursor still bound in memory after unbind update")
	}

	claudeDisk, rerr := NewBackend("claude-code").Read(claudePath)
	if rerr != nil {
		t.Fatal(rerr)
	}
	if _, ok := claudeDisk["github"]; !ok {
		t.Error("claude config should still contain 'github' after update")
	}
}

// 回归测试：Update 不得静默丢失"不可操作"（disabled/未安装）agent 的既有绑定。
// 前端选择器只展示 active agent，编辑提交的 agentIDs 不含 disabled 项；
// 若后端按整体替换处理，一次普通编辑会把 disabled agent 的绑定静默解绑。
func TestStore_UpdatePreservesInactiveAgentBindings(t *testing.T) {
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

	created, err := store.Add(Server{Name: "github", Command: "npx", Transport: TransportStdio}, []string{"claude-code", "cursor"}, reg)
	if err != nil {
		t.Fatal(err)
	}

	// cursor 随后被禁用，前端编辑表单只展示 active agent，提交不含 cursor
	reg.Toggle("cursor", false)

	cursorBefore, rerr := os.ReadFile(cursorPath)
	if rerr != nil {
		t.Fatal(rerr)
	}

	err = store.Update(created.ID, Server{Name: "github", Command: "npx", Transport: TransportStdio}, []string{"claude-code"}, reg)
	if err != nil {
		t.Fatal(err)
	}

	if !store.AgentBound(created.ID, "cursor") {
		t.Error("disabled agent's binding was silently dropped by update")
	}

	cursorAfter, rerr := os.ReadFile(cursorPath)
	if rerr != nil {
		t.Fatal(rerr)
	}
	if string(cursorBefore) != string(cursorAfter) {
		t.Errorf("disabled agent's config file was modified by update: before=%s after=%s", cursorBefore, cursorAfter)
	}
}

// 回归测试：rollbackUpdate 只恢复实际被写入的 agent 配置文件（replaceable + new），
// 不得删除 preserved（disabled/未检测到）agent 的整个配置文件。
// 此前实现把 oldAgentIDs（含 preserved）整体传入 restoreOrRemoveAgentConfigs，
// 而 oldConfigs 只备份了 replaceable agent 的文件；preserved 路径在 oldConfigs
// 中缺失，回滚时走 os.Remove 分支，永久删除该 agent 配置。
func TestStore_RollbackUpdateDoesNotDeletePreservedAgentConfig(t *testing.T) {
	t.Setenv("AGENTPACK_ALLOW_TEMP_DIR", "")
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("APPDATA", "")

	claudePath := filepath.Join(home, ".claude.json")
	cursorPath := filepath.Join(home, ".cursor", "mcp.json")
	writeFile(t, claudePath, `{"mcpServers":{"github":{"command":"npx"}}}`)
	writeFile(t, cursorPath, `{"mcpServers":{"github":{"command":"npx"}}}`)

	reg := agents.NewRegistry()
	reg.Register(agents.Agent{ID: "claude-code", Name: "Claude Code", Type: agents.TypeClaudeCode, Status: agents.StatusEnabled, ConfigPath: claudePath, ConfigFormat: agents.FormatJSON})
	// cursor 为 preserved：非 enabled/detected（禁用）
	reg.Register(agents.Agent{ID: "cursor", Name: "Cursor", Type: agents.TypeCursor, Status: agents.StatusDisabled, ConfigPath: cursorPath, ConfigFormat: agents.FormatJSON})

	store := NewStore()
	if err := store.Load(reg); err != nil {
		t.Fatal(err)
	}

	// 模拟 Update 期间的 oldConfigs：只备份 replaceable（claude），不含 preserved cursor
	oldConfigs := map[string]string{
		claudePath: `{"mcpServers":{"github":{"command":"npx"}}}`,
	}
	oldAgentIDs := []string{"claude-code", "cursor"}
	replaceableIDs := []string{"claude-code"}
	newAgentIDs := []string{"claude-code"}

	err := store.rollbackUpdate("srv-1", Server{ID: "srv-1", Name: "github"}, oldAgentIDs, replaceableIDs, newAgentIDs, oldConfigs, reg)
	if err != nil {
		t.Fatal(err)
	}

	// preserved（cursor）的配置文件必须原样保留，不得被删除
	if _, rerr := os.Stat(cursorPath); rerr != nil {
		t.Fatalf("preserved agent's config file was deleted by rollbackUpdate: %v", rerr)
	}
	// replaceable（claude）的配置恢复为备份内容
	claudeDisk, rerr := NewBackend("claude-code").Read(claudePath)
	if rerr != nil {
		t.Fatal(rerr)
	}
	if _, ok := claudeDisk["github"]; !ok {
		t.Errorf("claude config should be restored after rollback, got %v", claudeDisk)
	}
}
