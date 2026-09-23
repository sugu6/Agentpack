package skills

import (
	"os"
	"path/filepath"
	"testing"

	"agentpack/internal/agents"
)

func mustSymlink(t *testing.T, oldname, newname string) {
	t.Helper()
	if err := os.Symlink(oldname, newname); err != nil {
		t.Skipf("symlink unavailable (need privilege/dev mode): %v", err)
	}
}

func TestClassifyCopyEntry(t *testing.T) {
	ssotPath := filepath.Join(t.TempDir(), "foo")
	if err := os.MkdirAll(ssotPath, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(ssotPath, "SKILL.md"), []byte("---\nname: foo\n---\n"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(ssotPath, "extra.sh"), []byte("echo hi\n"), 0644); err != nil {
		t.Fatal(err)
	}
	ssotHash, ssotOK := HashDir(ssotPath)
	if !ssotOK || ssotHash == "" {
		t.Fatalf("ssot hash setup failed: %q %v", ssotHash, ssotOK)
	}
	target := filepath.Join(t.TempDir(), "foo")

	t.Run("absent entry yields no conflict", func(t *testing.T) {
		if cl := classifyCopyEntry(SyncMethodSymlink, ssotPath, target, ssotHash, ssotOK); cl.kind != "" {
			t.Errorf("expected no conflict, got %q", cl.kind)
		}
	})

	t.Run("identical plain copy in symlink mode is plain_same", func(t *testing.T) {
		if err := copyDirRecursive(ssotPath, target); err != nil {
			t.Fatal(err)
		}
		defer os.RemoveAll(target)
		cl := classifyCopyEntry(SyncMethodSymlink, ssotPath, target, ssotHash, ssotOK)
		if cl.kind != ConflictPlainSame || cl.localHash != ssotHash {
			t.Errorf("got %+v, want plain_same with equal hash", cl)
		}
	})

	t.Run("same SKILL.md but different sidecar is plain_diff", func(t *testing.T) {
		if err := copyDirRecursive(ssotPath, target); err != nil {
			t.Fatal(err)
		}
		defer os.RemoveAll(target)
		if err := os.WriteFile(filepath.Join(target, "extra.sh"), []byte("echo BYE\n"), 0644); err != nil {
			t.Fatal(err)
		}
		cl := classifyCopyEntry(SyncMethodSymlink, ssotPath, target, ssotHash, ssotOK)
		if cl.kind != ConflictPlainDiff {
			t.Errorf("got %q, want plain_diff (D1: full-tree guard)", cl.kind)
		}
	})

	t.Run("identical plain copy in copy mode yields no conflict", func(t *testing.T) {
		if err := copyDirRecursive(ssotPath, target); err != nil {
			t.Fatal(err)
		}
		defer os.RemoveAll(target)
		cl := classifyCopyEntry(SyncMethodCopy, ssotPath, target, ssotHash, ssotOK)
		if cl.kind != "" {
			t.Errorf("copy mode projection must not be a conflict, got %q", cl.kind)
		}
	})

	t.Run("incomplete ssot hash forces plain_diff", func(t *testing.T) {
		if err := copyDirRecursive(ssotPath, target); err != nil {
			t.Fatal(err)
		}
		defer os.RemoveAll(target)
		cl := classifyCopyEntry(SyncMethodSymlink, ssotPath, target, ssotHash, false)
		if cl.kind != ConflictPlainDiff {
			t.Errorf("got %q, want plain_diff when ssot hash incomplete", cl.kind)
		}
	})

	t.Run("live correct link yields no conflict", func(t *testing.T) {
		mustSymlink(t, ssotPath, target)
		defer os.RemoveAll(target)
		if cl := classifyCopyEntry(SyncMethodSymlink, ssotPath, target, ssotHash, ssotOK); cl.kind != "" {
			t.Errorf("correct projection must not be a conflict, got %q", cl.kind)
		}
	})

	t.Run("dead link is broken_link", func(t *testing.T) {
		mustSymlink(t, filepath.Join(filepath.Dir(target), "no-such-target"), target)
		defer os.RemoveAll(target)
		cl := classifyCopyEntry(SyncMethodSymlink, ssotPath, target, ssotHash, ssotOK)
		if cl.kind != ConflictBrokenLink {
			t.Errorf("got %q, want broken_link", cl.kind)
		}
	})

	t.Run("live link to wrong target is wrong_target", func(t *testing.T) {
		other := filepath.Join(t.TempDir(), "other")
		if err := os.MkdirAll(other, 0755); err != nil {
			t.Fatal(err)
		}
		mustSymlink(t, other, target)
		defer os.RemoveAll(target)
		cl := classifyCopyEntry(SyncMethodSymlink, ssotPath, target, ssotHash, ssotOK)
		if cl.kind != ConflictWrongTarget {
			t.Errorf("got %q, want wrong_target", cl.kind)
		}
	})
}

type reconcileEnv struct {
	store     *Store
	reg       *agents.Registry
	home      string
	ssotDir   string
	claudeDir string
	codexDir  string
}

func newReconcileEnv(t *testing.T) *reconcileEnv {
	t.Helper()
	setupSkillCapableAgentHome(t)
	home, err := os.UserHomeDir()
	if err != nil {
		t.Fatal(err)
	}
	reg := agents.NewRegistry()
	reg.Register(agents.Agent{ID: "claude-code", Name: "Claude Code", Status: agents.StatusEnabled})
	reg.Register(agents.Agent{ID: "claude-code-desktop", Name: "Claude Code Desktop", Status: agents.StatusEnabled})
	reg.Register(agents.Agent{ID: "codex", Name: "Codex", Status: agents.StatusEnabled})
	claudeDir := reg.AgentSkillsDir("claude-code")
	codexDir := reg.AgentSkillsDir("codex")
	if claudeDir == "" || codexDir == "" {
		t.Fatalf("adapter IDs not skill-capable (claudeDir=%q codexDir=%q) — verify computeSkillDirCache keys", claudeDir, codexDir)
	}
	if reg.AgentSkillsDir("claude-code-desktop") != claudeDir {
		t.Fatal("claude-code-desktop must share claude skills dir (dedup fixture broken)")
	}
	ssotDir := ResolveSSOTDir(StorageUnified)
	// 目标 agent 目录预先建出：os.Symlink 的父目录必须存在，
	// 否则 mustSymlink 会因 ENOENT 跳过整测（假绿）。
	if err := os.MkdirAll(claudeDir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(codexDir, 0755); err != nil {
		t.Fatal(err)
	}
	return &reconcileEnv{
		store:     NewStore(ssotDir, SyncMethodSymlink),
		reg:       reg,
		home:      home,
		ssotDir:   ssotDir,
		claudeDir: claudeDir,
		codexDir:  codexDir,
	}
}

func writeReconcileSkill(t *testing.T, dir, sidecar string) {
	t.Helper()
	if err := os.MkdirAll(dir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "SKILL.md"), []byte("---\nname: test\n---\nbody\n"), 0644); err != nil {
		t.Fatal(err)
	}
	if sidecar != "" {
		if err := os.WriteFile(filepath.Join(dir, "extra.sh"), []byte(sidecar), 0644); err != nil {
			t.Fatal(err)
		}
	}
}

func findReconcileItem(items []ReconcileItem, kind ConflictKind, path string) (ReconcileItem, bool) {
	for _, it := range items {
		if it.Kind == kind && it.Path == path {
			return it, true
		}
	}
	return ReconcileItem{}, false
}

func TestScanConflicts_ClassifiesAllKinds(t *testing.T) {
	// 拆为两个子测（Ruling 4）：本机常无 SeCreateSymbolicLinkPrivilege，
	// monolithic 版本会在首个 mustSymlink 处整测 Skip，连不需要 symlink 的
	// 散装分类/去重/排除断言也一并丢失（证据假跳）。链接用例整体 Skip 时，
	// 散装用例仍须真实执行。
	t.Run("plain entries dedup and stray exclusion", func(t *testing.T) {
		env := newReconcileEnv(t)

		// alpha: claude 目录同内容散装副本 → plain_same（AgentIDs 含共享目录的两个 ID）
		alphaSSOT := filepath.Join(env.ssotDir, "alpha")
		writeReconcileSkill(t, alphaSSOT, "sidecar\n")
		if err := copyDirRecursive(alphaSSOT, filepath.Join(env.claudeDir, "alpha")); err != nil {
			t.Fatal(err)
		}

		// beta: claude 分叉散装 → plain_diff
		betaSSOT := filepath.Join(env.ssotDir, "beta")
		writeReconcileSkill(t, betaSSOT, "ssot-sidecar\n")
		writeReconcileSkill(t, filepath.Join(env.claudeDir, "beta"), "local-sidecar\n")

		// stray: claude 散装、SSOT 无同名 → 属未管理视图，不得出现
		writeReconcileSkill(t, filepath.Join(env.claudeDir, "stray"), "")

		if err := env.store.Load(env.reg); err != nil {
			t.Fatal(err)
		}
		items := env.store.ScanConflicts(env.reg)
		if len(items) != 2 {
			t.Fatalf("expected 2 items, got %d: %+v", len(items), items)
		}
		if _, ok := findReconcileItem(items, ConflictPlainDiff, filepath.Join(env.claudeDir, "stray")); ok {
			t.Error("plain dir without SSOT twin must not appear (belongs to unmanaged view)")
		}
		alpha, ok := findReconcileItem(items, ConflictPlainSame, filepath.Join(env.claudeDir, "alpha"))
		if !ok {
			t.Fatal("plain_same for claude/alpha not found")
		}
		if len(alpha.AgentIDs) != 2 {
			t.Errorf("shared dir must aggregate both agent IDs, got %v", alpha.AgentIDs)
		}
		if alpha.SkillID != "skill:alpha" || !stringsHas(alpha.AgentIDs, "claude-code") || !stringsHas(alpha.AgentIDs, "claude-code-desktop") {
			t.Errorf("unexpected alpha item: %+v", alpha)
		}
	})

	t.Run("link entries classification", func(t *testing.T) {
		env := newReconcileEnv(t)

		// beta: SSOT 有、codex 错链 → wrong_target
		betaSSOT := filepath.Join(env.ssotDir, "beta")
		writeReconcileSkill(t, betaSSOT, "ssot-sidecar\n")
		other := t.TempDir()
		otherSkill := filepath.Join(other, "beta")
		writeReconcileSkill(t, otherSkill, "other\n")
		mustSymlink(t, otherSkill, filepath.Join(env.codexDir, "beta"))

		// gamma: SSOT 有、codex 死链 → broken_link
		writeReconcileSkill(t, filepath.Join(env.ssotDir, "gamma"), "")
		mustSymlink(t, filepath.Join(env.ssotDir, "no-such-gamma"), filepath.Join(env.codexDir, "gamma"))

		// ghost: codex 死链、SSOT 无同名 → orphan_link
		mustSymlink(t, filepath.Join(env.ssotDir, "no-such-ghost"), filepath.Join(env.codexDir, "ghost"))

		if err := env.store.Load(env.reg); err != nil {
			t.Fatal(err)
		}
		items := env.store.ScanConflicts(env.reg)
		if len(items) != 3 {
			t.Fatalf("expected 3 items, got %d: %+v", len(items), items)
		}
		if _, ok := findReconcileItem(items, ConflictBrokenLink, filepath.Join(env.codexDir, "gamma")); !ok {
			t.Error("broken_link for codex/gamma not found")
		}
		ghost, ok := findReconcileItem(items, ConflictOrphanLink, filepath.Join(env.codexDir, "ghost"))
		if !ok || ghost.SkillID != "" {
			t.Errorf("orphan_link must have empty SkillID, got %+v", ghost)
		}
		if _, ok := findReconcileItem(items, ConflictWrongTarget, filepath.Join(env.codexDir, "beta")); !ok {
			t.Error("wrong_target for codex/beta not found")
		}
		for i := 1; i < len(items); i++ {
			prev, cur := items[i-1], items[i]
			if prev.Kind > cur.Kind || (prev.Kind == cur.Kind && prev.Directory > cur.Directory) {
				t.Errorf("items not sorted at %d: %q/%q before %q/%q", i, prev.Kind, prev.Directory, cur.Kind, cur.Directory)
			}
		}
	})
}

func stringsHas(list []string, v string) bool {
	for _, s := range list {
		if s == v {
			return true
		}
	}
	return false
}

func TestScanConflicts_AcknowledgeAndDrift(t *testing.T) {
	env := newReconcileEnv(t)
	writeReconcileSkill(t, filepath.Join(env.ssotDir, "fork"), "ssot-sidecar\n")
	local := filepath.Join(env.claudeDir, "fork")
	writeReconcileSkill(t, local, "local-sidecar\n")
	if err := env.store.Load(env.reg); err != nil {
		t.Fatal(err)
	}
	ssotHash, _ := HashDir(filepath.Join(env.ssotDir, "fork"))
	localHash, _ := HashDir(local)
	if err := WriteConflictAck(env.ssotDir, conflictKey("fork", local), ConflictAck{
		SSOTHash: ssotHash, LocalHash: localHash, CheckedAt: "2026-09-24T00:00:00Z",
	}); err != nil {
		t.Fatal(err)
	}
	items := env.store.ScanConflicts(env.reg)
	it, ok := findReconcileItem(items, ConflictPlainDiff, local)
	if !ok {
		t.Fatal("plain_diff for claude/fork not found")
	}
	if !it.Acknowledged {
		t.Error("matching ack must mark item acknowledged")
	}

	// 本地漂移 → ack 失效重新上屏
	if err := os.WriteFile(filepath.Join(local, "drift.txt"), []byte("x\n"), 0644); err != nil {
		t.Fatal(err)
	}
	items = env.store.ScanConflicts(env.reg)
	it, _ = findReconcileItem(items, ConflictPlainDiff, local)
	if it.Acknowledged {
		t.Error("local drift must un-acknowledge the fork")
	}

	// 复位本地后 SSOT 漂移 → 同样失效
	if err := os.Remove(filepath.Join(local, "drift.txt")); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(env.ssotDir, "fork", "drift2.txt"), []byte("y\n"), 0644); err != nil {
		t.Fatal(err)
	}
	items = env.store.ScanConflicts(env.reg)
	it, _ = findReconcileItem(items, ConflictPlainDiff, local)
	if it.Acknowledged {
		t.Error("ssot drift must un-acknowledge the fork")
	}
}

func TestConvertSkillCopyToLink(t *testing.T) {
	t.Run("identical plain copy becomes symlink", func(t *testing.T) {
		env := newReconcileEnv(t)
		ssot := filepath.Join(env.ssotDir, "alpha")
		writeReconcileSkill(t, ssot, "x\n")
		target := filepath.Join(env.claudeDir, "alpha")
		if err := copyDirRecursive(ssot, target); err != nil {
			t.Fatal(err)
		}
		if err := env.store.Load(env.reg); err != nil {
			t.Fatal(err)
		}
		if err := env.store.ConvertSkillCopyToLink("skill:alpha", target, env.reg); err != nil {
			t.Fatal(err)
		}
		info, err := os.Lstat(target)
		if err != nil {
			t.Fatal(err)
		}
		// 形状断言按主机能力门控（Ruling 5）：无 symlink 权限的主机上
		// createSymlink 走既有 copy fallback；下方内容断言（无条件）
		// 仍然完整验证投影语义，可授权主机上形状仍严格断言。
		if canCreateSymlinks(t) && info.Mode()&os.ModeSymlink == 0 {
			t.Fatal("expected target to become a symlink")
		}
		before, _ := HashDir(ssot)
		after, _ := HashDir(target)
		if before == "" || before != after {
			t.Errorf("content must be preserved after conversion: %q vs %q", before, after)
		}
	})

	t.Run("diverged plain copy refused and untouched", func(t *testing.T) {
		env := newReconcileEnv(t)
		ssot := filepath.Join(env.ssotDir, "beta")
		writeReconcileSkill(t, ssot, "ssot-sidecar\n")
		target := filepath.Join(env.claudeDir, "beta")
		writeReconcileSkill(t, target, "local-sidecar\n")
		if err := env.store.Load(env.reg); err != nil {
			t.Fatal(err)
		}
		localBefore, _ := HashDir(target)
		err := env.store.ConvertSkillCopyToLink("skill:beta", target, env.reg)
		if err == nil {
			t.Fatal("expected refusal for diverged copy (D1 byte guard)")
		}
		localAfter, _ := HashDir(target)
		if localBefore != localAfter {
			t.Error("refused conversion must not modify local content")
		}
		if info, lerr := os.Lstat(target); lerr != nil || info.Mode()&os.ModeSymlink != 0 {
			t.Errorf("entry must remain a plain dir, info=%v err=%v", info, lerr)
		}
	})

	t.Run("dead link repaired", func(t *testing.T) {
		env := newReconcileEnv(t)
		writeReconcileSkill(t, filepath.Join(env.ssotDir, "gamma"), "")
		target := filepath.Join(env.codexDir, "gamma")
		mustSymlink(t, filepath.Join(env.ssotDir, "no-such"), target)
		if err := env.store.Load(env.reg); err != nil {
			t.Fatal(err)
		}
		if err := env.store.ConvertSkillCopyToLink("skill:gamma", target, env.reg); err != nil {
			t.Fatal(err)
		}
		if _, err := os.Stat(target); err != nil {
			t.Errorf("link must resolve after conversion: %v", err)
		}
	})

	t.Run("wrong-target link relinked", func(t *testing.T) {
		env := newReconcileEnv(t)
		writeReconcileSkill(t, filepath.Join(env.ssotDir, "delta"), "")
		other := filepath.Join(t.TempDir(), "delta")
		writeReconcileSkill(t, other, "")
		target := filepath.Join(env.codexDir, "delta")
		mustSymlink(t, other, target)
		if err := env.store.Load(env.reg); err != nil {
			t.Fatal(err)
		}
		if err := env.store.ConvertSkillCopyToLink("skill:delta", target, env.reg); err != nil {
			t.Fatal(err)
		}
		link, err := os.Readlink(target)
		if err != nil {
			t.Fatal(err)
		}
		wantAbs, _ := filepath.Abs(filepath.Join(env.ssotDir, "delta"))
		gotAbs, _ := filepath.Abs(link)
		if filepath.Clean(wantAbs) != filepath.Clean(gotAbs) {
			t.Errorf("link must point at SSOT, got %q want %q", link, wantAbs)
		}
	})

	t.Run("path outside agent dirs rejected", func(t *testing.T) {
		env := newReconcileEnv(t)
		writeReconcileSkill(t, filepath.Join(env.ssotDir, "eps"), "")
		outsider := filepath.Join(t.TempDir(), "eps")
		writeReconcileSkill(t, outsider, "")
		if err := env.store.Load(env.reg); err != nil {
			t.Fatal(err)
		}
		if err := env.store.ConvertSkillCopyToLink("skill:eps", outsider, env.reg); err == nil {
			t.Fatal("expected error for path not inside any agent skills dir")
		}
	})

	t.Run("unknown skill rejected", func(t *testing.T) {
		env := newReconcileEnv(t)
		if err := env.store.Load(env.reg); err != nil {
			t.Fatal(err)
		}
		if err := env.store.ConvertSkillCopyToLink("skill:nope", filepath.Join(env.claudeDir, "nope"), env.reg); err == nil {
			t.Fatal("expected error for unknown skill ID")
		}
	})
}

func TestKeepSkillFork(t *testing.T) {
	t.Run("diverged fork records ack matching ScanConflicts", func(t *testing.T) {
		env := newReconcileEnv(t)
		writeReconcileSkill(t, filepath.Join(env.ssotDir, "fork"), "ssot-sidecar\n")
		local := filepath.Join(env.claudeDir, "fork")
		writeReconcileSkill(t, local, "local-sidecar\n")
		if err := env.store.Load(env.reg); err != nil {
			t.Fatal(err)
		}
		if err := env.store.KeepSkillFork("skill:fork", local, env.reg); err != nil {
			t.Fatal(err)
		}
		items := env.store.ScanConflicts(env.reg)
		it, ok := findReconcileItem(items, ConflictPlainDiff, local)
		if !ok {
			t.Fatal("plain_diff item not found after keep")
		}
		if !it.Acknowledged {
			t.Error("kept fork must be acknowledged in subsequent scan")
		}
	})

	t.Run("identical copy rejected without ack", func(t *testing.T) {
		env := newReconcileEnv(t)
		ssot := filepath.Join(env.ssotDir, "same")
		writeReconcileSkill(t, ssot, "x\n")
		target := filepath.Join(env.claudeDir, "same")
		if err := copyDirRecursive(ssot, target); err != nil {
			t.Fatal(err)
		}
		if err := env.store.Load(env.reg); err != nil {
			t.Fatal(err)
		}
		if err := env.store.KeepSkillFork("skill:same", target, env.reg); err == nil {
			t.Fatal("expected error when nothing diverges")
		}
		if acks := ReadConflictAcks(env.ssotDir); len(acks) != 0 {
			t.Errorf("no ack must be written, got %d", len(acks))
		}
	})

	t.Run("path outside agent dirs rejected", func(t *testing.T) {
		env := newReconcileEnv(t)
		writeReconcileSkill(t, filepath.Join(env.ssotDir, "k"), "a\n")
		outsider := filepath.Join(t.TempDir(), "k")
		writeReconcileSkill(t, outsider, "b\n")
		if err := env.store.Load(env.reg); err != nil {
			t.Fatal(err)
		}
		if err := env.store.KeepSkillFork("skill:k", outsider, env.reg); err == nil {
			t.Fatal("expected error for foreign path")
		}
	})
}

// canCreateSymlinks probes whether this host may create symlinks
// (Windows: SeCreateSymbolicLinkPrivilege or Developer Mode).
// 形状断言按其结果门控；内容/行为断言始终无条件执行（Ruling 5）。
func canCreateSymlinks(t *testing.T) bool {
	t.Helper()
	probe := filepath.Join(t.TempDir(), "probe")
	if err := os.Symlink("x", probe); err != nil {
		t.Logf("symlink unavailable, shape assertion skipped: %v", err)
		return false
	}
	return true
}

func TestOverwriteSkillCopyFromSSOT(t *testing.T) {
	t.Run("diverged plain replaced by SSOT projection", func(t *testing.T) {
		env := newReconcileEnv(t)
		ssot := filepath.Join(env.ssotDir, "beta")
		writeReconcileSkill(t, ssot, "ssot-sidecar\n")
		target := filepath.Join(env.claudeDir, "beta")
		writeReconcileSkill(t, target, "local-sidecar\n")
		if err := env.store.Load(env.reg); err != nil {
			t.Fatal(err)
		}
		ssotHash, _ := HashDir(ssot)
		if err := env.store.OverwriteSkillCopyFromSSOT("skill:beta", target, env.reg); err != nil {
			t.Fatal(err)
		}
		info, err := os.Lstat(target)
		if err != nil {
			t.Fatal(err)
		}
		if canCreateSymlinks(t) && info.Mode()&os.ModeSymlink == 0 {
			t.Fatal("expected projection after overwrite")
		}
		got, _ := HashDir(target)
		if got != ssotHash {
			t.Errorf("content must equal SSOT after overwrite: %q vs %q", got, ssotHash)
		}
	})

	t.Run("missing entry rejected", func(t *testing.T) {
		env := newReconcileEnv(t)
		writeReconcileSkill(t, filepath.Join(env.ssotDir, "gamma"), "")
		if err := env.store.Load(env.reg); err != nil {
			t.Fatal(err)
		}
		err := env.store.OverwriteSkillCopyFromSSOT("skill:gamma", filepath.Join(env.claudeDir, "gamma"), env.reg)
		if err == nil {
			t.Fatal("expected error when entry absent")
		}
	})

	t.Run("identical plain replaced without guard", func(t *testing.T) {
		env := newReconcileEnv(t)
		ssot := filepath.Join(env.ssotDir, "same")
		writeReconcileSkill(t, ssot, "x\n")
		target := filepath.Join(env.claudeDir, "same")
		if err := copyDirRecursive(ssot, target); err != nil {
			t.Fatal(err)
		}
		if err := env.store.Load(env.reg); err != nil {
			t.Fatal(err)
		}
		if err := env.store.OverwriteSkillCopyFromSSOT("skill:same", target, env.reg); err != nil {
			t.Fatal(err)
		}
		if info, _ := os.Lstat(target); canCreateSymlinks(t) && info.Mode()&os.ModeSymlink == 0 {
			t.Error("expected symlink projection")
		}
	})
}

func TestAdoptSkillCopy(t *testing.T) {
	t.Run("diverged local adopted, source linked, backup created", func(t *testing.T) {
		env := newReconcileEnv(t)
		ssot := filepath.Join(env.ssotDir, "beta")
		writeReconcileSkill(t, ssot, "ssot-sidecar\n")
		target := filepath.Join(env.claudeDir, "beta")
		writeReconcileSkill(t, target, "local-sidecar\n")
		if err := env.store.Load(env.reg); err != nil {
			t.Fatal(err)
		}
		sk, err := env.store.AdoptSkillCopy("skill:beta", target, env.reg)
		if err != nil {
			t.Fatal(err)
		}
		got, err := os.ReadFile(filepath.Join(ssot, "extra.sh"))
		if err != nil {
			t.Fatal(err)
		}
		if string(got) != "local-sidecar\n" {
			t.Errorf("SSOT must carry adopted local content, got %q", got)
		}
		if info, _ := os.Lstat(target); canCreateSymlinks(t) && info.Mode()&os.ModeSymlink == 0 {
			t.Error("source must become a projection after adopt")
		}
		fresh, _ := HashDir(ssot)
		if sk.ContentHash != fresh {
			t.Errorf("returned skill hash stale: %q vs %q", sk.ContentHash, fresh)
		}
		entries, err := os.ReadDir(filepath.Join(env.home, ".agents", "skill-backups"))
		if err != nil || len(entries) == 0 {
			t.Errorf("expected a backup of original SSOT, err=%v entries=%d", err, len(entries))
		}
	})

	t.Run("identical content rejected with guidance", func(t *testing.T) {
		env := newReconcileEnv(t)
		ssot := filepath.Join(env.ssotDir, "same")
		writeReconcileSkill(t, ssot, "x\n")
		target := filepath.Join(env.claudeDir, "same")
		if err := copyDirRecursive(ssot, target); err != nil {
			t.Fatal(err)
		}
		if err := env.store.Load(env.reg); err != nil {
			t.Fatal(err)
		}
		if _, err := env.store.AdoptSkillCopy("skill:same", target, env.reg); err == nil {
			t.Fatal("expected error: identical content should use convert instead")
		}
	})

	t.Run("backup failure aborts without touching SSOT", func(t *testing.T) {
		env := newReconcileEnv(t)
		ssot := filepath.Join(env.ssotDir, "beta")
		writeReconcileSkill(t, ssot, "ssot-sidecar\n")
		target := filepath.Join(env.claudeDir, "beta")
		writeReconcileSkill(t, target, "local-sidecar\n")
		// 用普通文件占住 skill-backups 路径，强制造错
		blocker := filepath.Join(env.home, ".agents", "skill-backups")
		if err := os.WriteFile(blocker, []byte("occupied"), 0644); err != nil {
			t.Fatal(err)
		}
		if err := env.store.Load(env.reg); err != nil {
			t.Fatal(err)
		}
		ssotBefore, _ := HashDir(ssot)
		localBefore, _ := HashDir(target)
		if _, err := env.store.AdoptSkillCopy("skill:beta", target, env.reg); err == nil {
			t.Fatal("expected adopt to abort on backup failure")
		}
		ssotAfter, _ := HashDir(ssot)
		if ssotBefore != ssotAfter {
			t.Error("SSOT must remain untouched when backup fails")
		}
		localAfter, _ := HashDir(target)
		if localAfter != localBefore {
			t.Error("local source must remain untouched on abort")
		}
	})
}
