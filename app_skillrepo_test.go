package main

import (
	"agentpack/internal/agents"
	"agentpack/internal/config"
	"agentpack/internal/market"
	"agentpack/internal/mcp"
	"sync"
	"testing"
)

// newAppWithRepos 构造一个带初始 SkillRepos 的 App,用于测试 UpdateSkillRepo。
// 不依赖磁盘配置文件,cfg 直接构造。
// 通过将 HOME/USERPROFILE 指向临时目录,隔离 config.Save 对真实配置的写入;
// 同时提供非nil的 registry/mcpStore/marketStore 以满足 assertInit 的前置条件。
func newAppWithRepos(t *testing.T, repos []config.SkillRepo) *App {
	t.Helper()
	tmp := t.TempDir()
	t.Setenv("HOME", tmp)
	t.Setenv("USERPROFILE", tmp)
	return &App{
		mu:          sync.RWMutex{},
		cfg:         &config.AppConfig{Settings: config.Settings{SkillRepos: repos}},
		registry:    agents.NewRegistry(),
		mcpStore:    mcp.NewStore(),
		marketStore: market.NewStore(""),
	}
}

func TestUpdateSkillRepo_ChangeBranchOnly(t *testing.T) {
	a := newAppWithRepos(t, []config.SkillRepo{
		{Owner: "anthropics", Name: "skills", Branch: "main"},
	})
	original := config.SkillRepo{Owner: "anthropics", Name: "skills", Branch: "main"}
	updated := config.SkillRepo{Owner: "anthropics", Name: "skills", Branch: "dev"}
	if err := a.UpdateSkillRepo(original, updated); err != nil {
		t.Fatalf("UpdateSkillRepo returned error: %v", err)
	}
	got := a.cfg.Settings.SkillRepos[0]
	if got.Branch != "dev" {
		t.Errorf("expected Branch=dev, got %q", got.Branch)
	}
	if len(a.cfg.Settings.SkillRepos) != 1 {
		t.Errorf("expected 1 repo, got %d", len(a.cfg.Settings.SkillRepos))
	}
}

func TestUpdateSkillRepo_ChangeOwnerName(t *testing.T) {
	a := newAppWithRepos(t, []config.SkillRepo{
		{Owner: "anthropics", Name: "skills", Branch: "main"},
	})
	original := config.SkillRepo{Owner: "anthropics", Name: "skills", Branch: "main"}
	updated := config.SkillRepo{Owner: "myfork", Name: "skills", Branch: "main"}
	if err := a.UpdateSkillRepo(original, updated); err != nil {
		t.Fatalf("UpdateSkillRepo returned error: %v", err)
	}
	got := a.cfg.Settings.SkillRepos[0]
	if got.Owner != "myfork" || got.Name != "skills" {
		t.Errorf("expected myfork/skills, got %s/%s", got.Owner, got.Name)
	}
}

func TestUpdateSkillRepo_DuplicateReturnsError(t *testing.T) {
	a := newAppWithRepos(t, []config.SkillRepo{
		{Owner: "anthropics", Name: "skills", Branch: "main"},
		{Owner: "ComposioHQ", Name: "awesome-claude-skills", Branch: "main"},
	})
	original := config.SkillRepo{Owner: "anthropics", Name: "skills", Branch: "main"}
	updated := config.SkillRepo{Owner: "ComposioHQ", Name: "awesome-claude-skills", Branch: "dev"}
	err := a.UpdateSkillRepo(original, updated)
	if err == nil {
		t.Fatal("expected error for duplicate repo, got nil")
	}
}

func TestUpdateSkillRepo_NotFound(t *testing.T) {
	a := newAppWithRepos(t, []config.SkillRepo{
		{Owner: "anthropics", Name: "skills", Branch: "main"},
	})
	original := config.SkillRepo{Owner: "nonexistent", Name: "repo", Branch: "main"}
	updated := config.SkillRepo{Owner: "myfork", Name: "skills", Branch: "main"}
	err := a.UpdateSkillRepo(original, updated)
	if err == nil {
		t.Fatal("expected error for not found, got nil")
	}
}

func TestUpdateSkillRepo_EmptyBranchDefaultsToMain(t *testing.T) {
	a := newAppWithRepos(t, []config.SkillRepo{
		{Owner: "anthropics", Name: "skills", Branch: "main"},
	})
	original := config.SkillRepo{Owner: "anthropics", Name: "skills", Branch: "main"}
	updated := config.SkillRepo{Owner: "anthropics", Name: "skills", Branch: ""}
	if err := a.UpdateSkillRepo(original, updated); err != nil {
		t.Fatalf("UpdateSkillRepo returned error: %v", err)
	}
	got := a.cfg.Settings.SkillRepos[0]
	if got.Branch != "main" {
		t.Errorf("expected Branch=main (default), got %q", got.Branch)
	}
}

func TestUpdateSkillRepo_EmptyOriginalReturnsError(t *testing.T) {
	a := newAppWithRepos(t, nil)
	original := config.SkillRepo{Owner: "", Name: "skills"}
	updated := config.SkillRepo{Owner: "anthropics", Name: "skills"}
	if err := a.UpdateSkillRepo(original, updated); err == nil {
		t.Fatal("expected error for empty original owner, got nil")
	}
}

func TestUpdateSkillRepo_EmptyUpdatedReturnsError(t *testing.T) {
	a := newAppWithRepos(t, []config.SkillRepo{
		{Owner: "anthropics", Name: "skills", Branch: "main"},
	})
	original := config.SkillRepo{Owner: "anthropics", Name: "skills", Branch: "main"}
	updated := config.SkillRepo{Owner: "", Name: "skills"}
	if err := a.UpdateSkillRepo(original, updated); err == nil {
		t.Fatal("expected error for empty updated owner, got nil")
	}
}
