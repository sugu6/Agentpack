package main

import (
	"agentpack/internal/agents"
	"agentpack/internal/config"
	"agentpack/internal/market"
	"agentpack/internal/mcp"
	"agentpack/internal/skills"
	"encoding/json"
	"os"
	"path/filepath"
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

func readAgentsLockForTest(t *testing.T) skills.AgentsLockFile {
	t.Helper()
	home, err := os.UserHomeDir()
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(home, ".agents", ".skill-lock.json"))
	if err != nil {
		t.Fatalf("read lock file: %v", err)
	}
	var lock skills.AgentsLockFile
	if err := json.Unmarshal(data, &lock); err != nil {
		t.Fatalf("unmarshal lock file: %v", err)
	}
	return lock
}

func TestApplyBackfillWithVerification(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	directories := []string{"code-simplifier", "debugger", "ui-ux-pro-max", "shared"}
	matches := map[string][]market.BackfillCandidate{
		"code-simplifier": {{Owner: "simonwong", Repo: "agent-skills", Installs: 1702}},
		"debugger": {
			{Owner: "software-mansion", Repo: "argent", Installs: 9999},
			{Owner: "shubhamsaboo", Repo: "awesome-llm-apps", Installs: 500},
		},
		"ui-ux-pro-max": {{Owner: "nextlevelbuilder", Repo: "ui-ux-pro-max-skill", Installs: 296028}},
	}
	verify := func(dir string, cands []market.BackfillCandidate) (market.BackfillCandidate, string, bool, bool) {
		if dir == "debugger" {
			// 第一个候选（argent）内容不一致，第二个（awesome-llm-apps）内容一致
			return cands[1], "skills/debugger", true, false
		}
		if dir == "ui-ux-pro-max" {
			// 内容不一致：验证过但被拒绝
			return market.BackfillCandidate{}, "", false, false
		}
		return cands[0], "skills/" + dir, true, false
	}
	res := applyBackfillWithVerification(matches, directories, verify)
	if len(res.Matched) != 2 || len(res.Mismatched) != 1 || len(res.Unmatched) != 1 || len(res.Failed) != 0 {
		t.Fatalf("unexpected result: %+v", res)
	}
	if len(res.Unmatched) != 1 || res.Unmatched[0] != "shared" {
		t.Fatalf("expected shared unmatched, got %v", res.Unmatched)
	}
	if len(res.Mismatched) != 1 || res.Mismatched[0] != "ui-ux-pro-max" {
		t.Fatalf("expected ui-ux-pro-max mismatched, got %v", res.Mismatched)
	}
	lock := readAgentsLockForTest(t)
	entry, ok := lock.Skills["code-simplifier"]
	if !ok {
		t.Fatal("expected lock entry for code-simplifier")
	}
	if entry.Source != "simonwong/agent-skills" || entry.SourceType != "github" || entry.Branch != "main" {
		t.Fatalf("unexpected lock entry: %+v", entry)
	}
	if entry.SourceURL != "https://github.com/simonwong/agent-skills" {
		t.Fatalf("unexpected source url: %q", entry.SourceURL)
	}
	if entry.FullPath != "skills/code-simplifier" {
		t.Fatalf("expected verified fullPath written to lock, got %q", entry.FullPath)
	}
	// debugger 应写入内容一致的第二候选（awesome-llm-apps）
	dbgEntry, ok := lock.Skills["debugger"]
	if !ok {
		t.Fatal("expected lock entry for debugger")
	}
	if dbgEntry.Source != "shubhamsaboo/awesome-llm-apps" {
		t.Fatalf("expected content-matched candidate written, got %q", dbgEntry.Source)
	}
	if _, ok := lock.Skills["shared"]; ok {
		t.Fatal("expected no lock entry for unmatched shared")
	}
	if _, ok := lock.Skills["ui-ux-pro-max"]; ok {
		t.Fatal("expected no lock entry for mismatched ui-ux-pro-max")
	}
}
