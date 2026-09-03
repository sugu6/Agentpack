package skillbackfill

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"agentpack/internal/market"
	"agentpack/internal/skills"
)

// 简化版验证：不读真实 lock 文件，只测 ApplyWithVerification 的统计逻辑。
func TestApplyWithVerification_Stats(t *testing.T) {
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
	verify := func(dir string, cands []market.BackfillCandidate) (market.BackfillCandidate, string, string, bool, bool) {
		if dir == "debugger" {
			// 第一个候选内容不一致，第二个内容一致
			return cands[1], "skills/debugger", "main", true, false
		}
		if dir == "ui-ux-pro-max" {
			// 内容不一致：验证过但被拒绝
			return market.BackfillCandidate{}, "", "", false, false
		}
		return cands[0], "skills/" + dir, "main", true, false
	}
	res := ApplyWithVerification(matches, directories, verify, func(dir string) bool { return true }, nil)
	if len(res.Matched) != 2 || len(res.Mismatched) != 1 || len(res.Unmatched) != 1 || len(res.Failed) != 0 {
		t.Fatalf("unexpected result: %+v", res)
	}
	if len(res.Unmatched) != 1 || res.Unmatched[0] != "shared" {
		t.Fatalf("expected shared unmatched, got %v", res.Unmatched)
	}
	if len(res.Mismatched) != 1 || res.Mismatched[0] != "ui-ux-pro-max" {
		t.Fatalf("expected ui-ux-pro-max mismatched, got %v", res.Mismatched)
	}

	// 验证 lock 文件写入：code-simplifier 应为 simonwong/agent-skills
	lockData, err := os.ReadFile(filepath.Join(home, ".agents", ".skill-lock.json"))
	if err != nil {
		t.Fatalf("read lock file: %v", err)
	}
	var lock skills.AgentsLockFile
	if err := json.Unmarshal(lockData, &lock); err != nil {
		t.Fatalf("unmarshal lock: %v", err)
	}
	entry, ok := lock.Skills["code-simplifier"]
	if !ok {
		t.Fatal("expected lock entry for code-simplifier")
	}
	if entry.Source != "simonwong/agent-skills" || entry.Branch != "main" {
		t.Fatalf("unexpected lock entry: %+v", entry)
	}
	// debugger 应写入内容一致的第二候选
	dbg, ok := lock.Skills["debugger"]
	if !ok {
		t.Fatal("expected lock entry for debugger")
	}
	if dbg.Source != "shubhamsaboo/awesome-llm-apps" {
		t.Fatalf("expected content-matched candidate written, got %q", dbg.Source)
	}
	// shared / ui-ux-pro-max 不应写入
	if _, ok := lock.Skills["shared"]; ok {
		t.Fatal("expected no lock entry for unmatched shared")
	}
	if _, ok := lock.Skills["ui-ux-pro-max"]; ok {
		t.Fatal("expected no lock entry for mismatched ui-ux-pro-max")
	}
}

// TestApplyWithVerification_SkillUninstalledDuringVerify 验证验证期间技能被卸载
// 时跳过写 lock（stillExists=false）。
func TestApplyWithVerification_SkillUninstalledDuringVerify(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	matches := map[string][]market.BackfillCandidate{
		"gone": {{Owner: "owner", Repo: "repo", Installs: 1}},
	}
	verify := func(dir string, cands []market.BackfillCandidate) (market.BackfillCandidate, string, string, bool, bool) {
		return cands[0], "skills/gone", "main", true, false
	}
	res := ApplyWithVerification(matches, []string{"gone"}, verify, func(dir string) bool { return false }, nil)
	if len(res.Matched) != 0 {
		t.Fatalf("expected no matched when skill uninstalled, got %+v", res)
	}
	// 不应写入 lock
	if _, err := os.Stat(filepath.Join(home, ".agents", ".skill-lock.json")); err == nil {
		t.Fatal("expected no lock file when all skills uninstalled during verify")
	}
}
