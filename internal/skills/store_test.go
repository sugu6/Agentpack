package skills

import (
	"agentpack/internal/agents"
	"os"
	"path/filepath"
	"testing"
)

func TestHasSkillManifest(t *testing.T) {
	dir := t.TempDir()
	if HasSkillManifest(dir) {
		t.Error("expected false for empty dir")
	}
	if err := os.WriteFile(filepath.Join(dir, "SKILL.md"), []byte("---\nname: test\n---"), 0644); err != nil {
		t.Fatal(err)
	}
	if !HasSkillManifest(dir) {
		t.Error("expected true for dir with SKILL.md")
	}
}

func TestParseSkillMetadata_SingleLine(t *testing.T) {
	meta := ParseSkillMetadata([]byte("---\nname: my-skill\ndescription: A test skill\n---\n# Content"))
	if meta.Name != "my-skill" {
		t.Errorf("expected name 'my-skill', got %q", meta.Name)
	}
	if meta.Description != "A test skill" {
		t.Errorf("expected description 'A test skill', got %q", meta.Description)
	}
}

func TestParseSkillMetadata_MultiLineDescription(t *testing.T) {
	meta := ParseSkillMetadata([]byte("---\nname: my-skill\ndescription:\n  This is a\n  multi-line description\n---\n# Content"))
	if meta.Name != "my-skill" {
		t.Errorf("expected name 'my-skill', got %q", meta.Name)
	}
	if meta.Description != "This is a multi-line description" {
		t.Errorf("expected folded description, got %q", meta.Description)
	}
}

func TestParseSkillMetadata_NoFrontmatter(t *testing.T) {
	meta := ParseSkillMetadata([]byte("# Just content\nno frontmatter"))
	if meta.Name != "" {
		t.Errorf("expected empty name, got %q", meta.Name)
	}
}

func TestParseSkillMetadata_QuotedValues(t *testing.T) {
	meta := ParseSkillMetadata([]byte("---\nname: \"quoted-name\"\ndescription: 'quoted desc'\n---"))
	if meta.Name != "quoted-name" {
		t.Errorf("expected 'quoted-name', got %q", meta.Name)
	}
	if meta.Description != "quoted desc" {
		t.Errorf("expected 'quoted desc', got %q", meta.Description)
	}
}

func TestParseSkillMetadata_BOM(t *testing.T) {
	meta := ParseSkillMetadata([]byte("\uFEFF---\nname: bom-skill\n---"))
	if meta.Name != "bom-skill" {
		t.Errorf("expected 'bom-skill', got %q", meta.Name)
	}
}

func TestValidateDirectoryName(t *testing.T) {
	tests := []struct {
		name    string
		wantErr bool
	}{
		{"my-skill", false},
		{"my_skill", false},
		{"hello.world", false},
		{"", true},
		{".", true},
		{"..", true},
		{".hidden", true},
		{"path/with/slash", true},
		{`path\with\backslash`, true},
	}
	for _, tt := range tests {
		err := ValidateDirectoryName(tt.name)
		if (err != nil) != tt.wantErr {
			t.Errorf("ValidateDirectoryName(%q) error = %v, wantErr = %v", tt.name, err, tt.wantErr)
		}
	}
}

func TestReadSkillMetadata(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "SKILL.md"), []byte("---\nname: test-skill\ndescription: A test\n---\n# Content"), 0644); err != nil {
		t.Fatal(err)
	}
	meta, err := ReadSkillMetadata(dir)
	if err != nil {
		t.Fatal(err)
	}
	if meta.Name != "test-skill" {
		t.Errorf("expected 'test-skill', got %q", meta.Name)
	}
}

func TestReadSkillMetadata_NoFile(t *testing.T) {
	dir := t.TempDir()
	_, err := ReadSkillMetadata(dir)
	if err == nil {
		t.Fatal("expected error for missing SKILL.md")
	}
}

func TestStore_LoadEmpty(t *testing.T) {
	dir := t.TempDir()
	// Store.Load 需要非 nil registry（调用 SkillCapableAgentIDs）
	reg := agents.NewRegistry()
	s := NewStore(dir, SyncMethodSymlink)
	if err := s.Load(reg); err != nil {
		t.Fatal(err)
	}
	list := s.List()
	if len(list) != 0 {
		t.Errorf("expected empty list, got %d", len(list))
	}
}

func TestHashDir(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "file.txt"), []byte("hello"), 0644); err != nil {
		t.Fatal(err)
	}
	h, complete := HashDir(dir)
	if !complete {
		t.Error("expected complete hash")
	}
	if h == "" {
		t.Error("expected non-empty hash")
	}
	h2, _ := HashDir(dir)
	if h != h2 {
		t.Error("expected same hash for same content")
	}
}

func TestHashDir_EmptyDir(t *testing.T) {
	dir := t.TempDir()
	h, complete := HashDir(dir)
	if !complete {
		t.Error("expected complete hash for empty dir")
	}
	if h == "" {
		t.Error("expected non-empty hash even for empty dir")
	}
}

func TestRemovePath_File(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "test.txt")
	if err := os.WriteFile(path, []byte("data"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := RemovePath(path); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Error("expected file to be removed")
	}
}

func TestRemovePath_Dir(t *testing.T) {
	dir := t.TempDir()
	sub := filepath.Join(dir, "subdir")
	if err := os.MkdirAll(sub, 0755); err != nil {
		t.Fatal(err)
	}
	if err := RemovePath(sub); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(sub); !os.IsNotExist(err) {
		t.Error("expected dir to be removed")
	}
}

func TestRemovePath_NonExistent(t *testing.T) {
	if err := RemovePath("/nonexistent/path"); err != nil {
		t.Errorf("expected nil for non-existent path, got %v", err)
	}
}

func TestResolveSSOTDir(t *testing.T) {
	home, err := os.UserHomeDir()
	if err != nil {
		t.Fatal(err)
	}
	got := ResolveSSOTDir(StorageAgentpack)
	expected := filepath.Join(home, ".agentpack", "skills")
	if got != expected {
		t.Errorf("expected %s, got %s", expected, got)
	}
	got = ResolveSSOTDir(StorageUnified)
	expected = filepath.Join(home, ".agents", "skills")
	if got != expected {
		t.Errorf("expected %s, got %s", expected, got)
	}
}

func TestSyncMethod_Valid(t *testing.T) {
	if !SyncMethodSymlink.Valid() {
		t.Error("expected symlink to be valid")
	}
	if !SyncMethodCopy.Valid() {
		t.Error("expected copy to be valid")
	}
	if SyncMethod("invalid").Valid() {
		t.Error("expected invalid method to be invalid")
	}
}

func TestStorageLocation_Valid(t *testing.T) {
	if !StorageAgentpack.Valid() {
		t.Error("expected agentpack to be valid")
	}
	if !StorageUnified.Valid() {
		t.Error("expected unified to be valid")
	}
	if StorageLocation("invalid").Valid() {
		t.Error("expected invalid location to be invalid")
	}
}

// makeSkillDir 在 dir 下创建一个含 SKILL.md 的 skill 子目录
func makeSkillDir(t *testing.T, dir, name, body string) {
	t.Helper()
	skillDir := filepath.Join(dir, name)
	if err := os.MkdirAll(skillDir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(skillDir, "SKILL.md"), []byte(body), 0644); err != nil {
		t.Fatal(err)
	}
}

// TestAutoAdopt_AdoptsUnmanaged 验证场景1：agent 目录有未管理 skill，纳管后进入 SSOT 并同步回 agent 目录
func TestAutoAdopt_AdoptsUnmanaged(t *testing.T) {
	tmp := t.TempDir()
	ssotDir := filepath.Join(tmp, "ssot")
	agentDir := filepath.Join(tmp, "agent-skills")
	if err := os.MkdirAll(agentDir, 0755); err != nil {
		t.Fatal(err)
	}
	makeSkillDir(t, agentDir, "my-skill", "---\nname: my-skill\n---\n# body")

	s := NewStore(ssotDir, SyncMethodCopy)
	capableIDs := []string{"agent-a"}
	resolver := func(id string) string { return agentDir }

	result := s.autoAdoptWith(capableIDs, resolver)
	if len(result.Adopted) != 1 {
		t.Fatalf("expected 1 adopted, got %d (errors: %v)", len(result.Adopted), result.Errors)
	}
	if result.Adopted[0].Directory != "my-skill" {
		t.Errorf("expected 'my-skill', got %q", result.Adopted[0].Directory)
	}
	// SSOT 应包含副本
	if !HasSkillManifest(filepath.Join(ssotDir, "my-skill")) {
		t.Error("expected skill in SSOT after adoption")
	}
	// 主列表应包含该 skill
	list := s.List()
	if len(list) != 1 || list[0].Directory != "my-skill" {
		t.Errorf("expected list to contain my-skill, got %v", list)
	}
}

// TestAutoAdopt_ConflictSSOTOverrides 验证场景2：SSOT 已有同名（内容不同），用 SSOT 覆盖 agent 目录
func TestAutoAdopt_ConflictSSOTOverrides(t *testing.T) {
	tmp := t.TempDir()
	ssotDir := filepath.Join(tmp, "ssot")
	agentDir := filepath.Join(tmp, "agent-skills")
	if err := os.MkdirAll(agentDir, 0755); err != nil {
		t.Fatal(err)
	}
	// SSOT 已有同名 skill（内容 v1）
	makeSkillDir(t, ssotDir, "shared", "---\nname: shared\n---\n# v1-from-ssot")
	// agent 目录有同名但内容不同（v2）
	makeSkillDir(t, agentDir, "shared", "---\nname: shared\n---\n# v2-from-agent")

	s := NewStore(ssotDir, SyncMethodCopy)
	// 先 Load 让 store 知道 SSOT 中已有 shared
	reg := agents.NewRegistry()
	if err := s.Load(reg); err != nil {
		t.Fatal(err)
	}

	capableIDs := []string{"agent-a"}
	resolver := func(id string) string { return agentDir }
	result := s.autoAdoptWith(capableIDs, resolver)

	if len(result.Conflicts) != 1 {
		t.Fatalf("expected 1 conflict, got %d (adopted: %d, errors: %v)", len(result.Conflicts), len(result.Adopted), result.Errors)
	}
	if result.Conflicts[0].Directory != "shared" {
		t.Errorf("expected conflict on 'shared', got %q", result.Conflicts[0].Directory)
	}
	// agent 目录的 shared 应被 SSOT 版本覆盖
	content, err := os.ReadFile(filepath.Join(agentDir, "shared", "SKILL.md"))
	if err != nil {
		t.Fatal(err)
	}
	if !contains(string(content), "v1-from-ssot") {
		t.Errorf("expected agent dir to be overridden with SSOT v1, got: %s", content)
	}
}

// TestAutoAdopt_SharedDirMerged 验证场景3：多个 agent 共享同一目录，只纳管一次但 AgentIDs 合并
func TestAutoAdopt_SharedDirMerged(t *testing.T) {
	tmp := t.TempDir()
	ssotDir := filepath.Join(tmp, "ssot")
	sharedDir := filepath.Join(tmp, "shared-skills")
	if err := os.MkdirAll(sharedDir, 0755); err != nil {
		t.Fatal(err)
	}
	makeSkillDir(t, sharedDir, "common", "---\nname: common\n---\n# body")

	s := NewStore(ssotDir, SyncMethodCopy)
	// 两个 agent 共享同一目录
	capableIDs := []string{"agent-a", "agent-b"}
	resolver := func(id string) string { return sharedDir }

	result := s.autoAdoptWith(capableIDs, resolver)
	if len(result.Adopted) != 1 {
		t.Fatalf("expected 1 adopted, got %d (errors: %v)", len(result.Adopted), result.Errors)
	}
	adopted := result.Adopted[0]
	if adopted.Directory != "common" {
		t.Errorf("expected 'common', got %q", adopted.Directory)
	}
	if len(adopted.AgentIDs) != 2 {
		t.Errorf("expected 2 agent IDs, got %d: %v", len(adopted.AgentIDs), adopted.AgentIDs)
	}
	// 绑定应包含两个 agent
	sk, ok := s.Get("skill:common")
	if !ok {
		t.Fatal("expected skill:common in store")
	}
	if len(sk.BoundAgents) != 2 {
		t.Errorf("expected 2 bound agents, got %d: %v", len(sk.BoundAgents), sk.BoundAgents)
	}
}

// TestAutoAdopt_NoSkills 验证场景4：agent 目录无可读 skill，返回空结果
func TestAutoAdopt_NoSkills(t *testing.T) {
	tmp := t.TempDir()
	ssotDir := filepath.Join(tmp, "ssot")
	agentDir := filepath.Join(tmp, "agent-skills")
	if err := os.MkdirAll(agentDir, 0755); err != nil {
		t.Fatal(err)
	}
	// 空目录，无 skill

	s := NewStore(ssotDir, SyncMethodCopy)
	capableIDs := []string{"agent-a"}
	resolver := func(id string) string { return agentDir }

	result := s.autoAdoptWith(capableIDs, resolver)
	if len(result.Adopted) != 0 || len(result.Conflicts) != 0 || len(result.Errors) != 0 {
		t.Errorf("expected empty result, got adopted=%d conflicts=%d errors=%d",
			len(result.Adopted), len(result.Conflicts), len(result.Errors))
	}
}

func contains(s, substr string) bool {
	for i := 0; i+len(substr) <= len(s); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}

// TestSyncToAgentDir_CleansStaleTempDirs 验证 SyncToAgentDir 在同步前
// 自动清理目标父目录下残留的 .skill-tmp-* 孤儿目录。
func TestSyncToAgentDir_CleansStaleTempDirs(t *testing.T) {
	tmp := t.TempDir()
	source := filepath.Join(tmp, "src")
	makeSkillDir(t, source, "", "---\nname: test\n---\n# body")
	// source 本身就是 skill 目录（有 SKILL.md）

	agentDir := filepath.Join(tmp, "agent-skills")
	dest := filepath.Join(agentDir, "test")

	// 在 agent 目录下创建模拟的孤儿临时目录
	staleDir := filepath.Join(agentDir, ".skill-tmp-1234567890")
	if err := os.MkdirAll(filepath.Join(staleDir, "sub"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(staleDir, "SKILL.md"), []byte("x"), 0644); err != nil {
		t.Fatal(err)
	}
	staleDir2 := filepath.Join(agentDir, ".skill-tmp-9876543210")
	if err := os.MkdirAll(staleDir2, 0755); err != nil {
		t.Fatal(err)
	}

	// 执行同步
	if err := SyncToAgentDir(source, dest, SyncMethodCopy); err != nil {
		t.Fatalf("SyncToAgentDir: %v", err)
	}

	// 验证孤儿目录已被清理
	if _, err := os.Stat(staleDir); !os.IsNotExist(err) {
		t.Errorf("stale temp dir %s should be removed", staleDir)
	}
	if _, err := os.Stat(staleDir2); !os.IsNotExist(err) {
		t.Errorf("stale temp dir %s should be removed", staleDir2)
	}
	// 验证目标已同步
	if !HasSkillManifest(dest) {
		t.Error("dest should contain SKILL.md after sync")
	}
}
