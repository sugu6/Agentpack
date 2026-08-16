package skills

import (
	"agentpack/internal/agents"
	"agentpack/internal/config"
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"
)

// mockFetchSHA 返回一个可编程的 fetchSkillCommitSHAFunc 替换
func mockFetchSHA(sha string, err error) func() {
	orig := fetchSkillCommitSHAFunc
	origJS := jsDelivrDataBase
	origGH := gitHubAPIBases
	// 强制 jsDelivr 主链路立即失败，使检测/更新走 git/tarball fallback，
	// 避免测试打到真实网络（jsDelivr 与真实仓库均不可达）。
	jsDelivrDataBase = "http://127.0.0.1:1"
	gitHubAPIBases = []string{"http://127.0.0.1:1"}
	fetchSkillCommitSHAFunc = func(ctx context.Context, owner, repo, branch string) (string, error) {
		return sha, err
	}
	return func() {
		fetchSkillCommitSHAFunc = orig
		jsDelivrDataBase = origJS
		gitHubAPIBases = origGH
	}
}

// forceGitFallbackForTest 隔离 jsDelivr/GitHub 真实网络，强制走 git fallback。
// 供直接替换 fetchSkillCommitSHAFunc 的测试使用（它们未走 mockFetchSHA）。
func forceGitFallbackForTest(t *testing.T) {
	t.Helper()
	origJS := jsDelivrDataBase
	origGH := gitHubAPIBases
	jsDelivrDataBase = "http://127.0.0.1:1"
	gitHubAPIBases = []string{"http://127.0.0.1:1"}
	t.Cleanup(func() {
		jsDelivrDataBase = origJS
		gitHubAPIBases = origGH
	})
}

func setupTestHome(t *testing.T) {
	t.Helper()
	tmpHome := t.TempDir()
	origHome, _ := os.UserHomeDir()
	os.Setenv("USERPROFILE", tmpHome)
	os.Setenv("HOME", tmpHome)
	t.Cleanup(func() {
		os.Setenv("USERPROFILE", origHome)
		os.Setenv("HOME", origHome)
	})
}

func TestFetchSkillCommitSHAImpl_Success(t *testing.T) {
	// 真实 git ls-remote 测试必须显式启用，避免普通测试触发网络认证。
	realTestRequired(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	sha, err := fetchSkillCommitSHAImpl(ctx, "obra", "superpowers", "main")
	if err != nil {
		t.Skipf("git ls-remote failed (network issue): %v", err)
	}
	if sha == "" {
		t.Error("expected non-empty SHA")
	}
	t.Logf("got SHA: %s", sha)
}

func TestFetchSkillCommitSHAImpl_InvalidRepo(t *testing.T) {
	// 真实 git ls-remote 测试必须显式启用，避免普通测试触发网络认证。
	realTestRequired(t)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	_, err := fetchSkillCommitSHAImpl(ctx, "nonexistent-owner-xyz", "nonexistent-repo-xyz", "main")
	if err == nil {
		t.Error("expected error for invalid repo")
	}
}

func TestReadUpdateCache_NonExistent(t *testing.T) {
	setupTestHome(t)
	result := readUpdateCache()
	if result != nil {
		t.Errorf("expected nil for non-existent cache, got %v", result)
	}
}

func TestWriteAndReadUpdateCache(t *testing.T) {
	setupTestHome(t)

	data := map[string]updateCacheEntry{
		"skill:filesystem": {
			CommitSHA: "sha123",
			CheckedAt: "2026-07-12T00:00:00Z",
		},
		"skill:memory": {
			CommitSHA: "sha456",
			CheckedAt: "2026-07-12T00:00:01Z",
		},
	}

	if err := writeUpdateCache(data); err != nil {
		t.Fatalf("writeUpdateCache failed: %v", err)
	}

	cachePath := filepath.Join(t.TempDir(), "..", ".agentpack", "skill-update-cache.json")
	_ = cachePath // path verification done via readback

	result := readUpdateCache()
	if result == nil {
		t.Fatal("expected non-nil cache")
	}
	if len(result) != 2 {
		t.Fatalf("expected 2 entries, got %d", len(result))
	}
	if result["skill:filesystem"].CommitSHA != "sha123" {
		t.Errorf("expected sha123, got %s", result["skill:filesystem"].CommitSHA)
	}
}

func TestCheckUpdates_NoGitHubSkills(t *testing.T) {
	store := NewStore(t.TempDir(), SyncMethodSymlink)
	results := store.CheckUpdates(nil)
	if results != nil {
		t.Errorf("expected nil for no GitHub skills, got %v", results)
	}
}

func TestCheckUpdates_DetectsUpdate(t *testing.T) {
	setupTestHome(t)
	cleanup := mockFetchSHA("new-sha-xyz", nil)
	defer cleanup()

	// 写入旧缓存基线
	cache := map[string]updateCacheEntry{
		"skill:pdf": {
			CommitSHA: "old-sha-abc",
			CheckedAt: "2026-07-01T00:00:00Z",
		},
	}
	if err := writeUpdateCache(cache); err != nil {
		t.Fatalf("writeUpdateCache: %v", err)
	}

	store := NewStore(t.TempDir(), SyncMethodSymlink)
	store.skills["skill:pdf"] = Skill{
		ID: "skill:pdf", Directory: "pdf",
		RepoOwner: "owner", RepoName: "repo", RepoBranch: "main",
	}

	results := store.CheckUpdates(nil)
	if len(results) != 1 {
		t.Fatalf("expected 1 result, got %d", len(results))
	}
	if !results[0].HasUpdate {
		t.Error("expected HasUpdate=true when remote SHA differs from cached baseline")
	}
	if results[0].RemoteHash != "new-sha-xyz" {
		t.Errorf("expected remote hash new-sha-xyz, got %s", results[0].RemoteHash)
	}
}

func TestCheckUpdates_NoUpdate(t *testing.T) {
	setupTestHome(t)
	cleanup := mockFetchSHA("same-sha-123", nil)
	defer cleanup()

	cache := map[string]updateCacheEntry{
		"skill:memory": {
			CommitSHA: "same-sha-123",
			CheckedAt: "2026-07-01T00:00:00Z",
		},
	}
	if err := writeUpdateCache(cache); err != nil {
		t.Fatalf("writeUpdateCache: %v", err)
	}

	store := NewStore(t.TempDir(), SyncMethodSymlink)
	store.skills["skill:memory"] = Skill{
		ID: "skill:memory", Directory: "memory",
		RepoOwner: "owner", RepoName: "repo", RepoBranch: "main",
	}

	results := store.CheckUpdates(nil)
	if len(results) != 1 {
		t.Fatalf("expected 1 result, got %d", len(results))
	}
	if results[0].HasUpdate {
		t.Error("expected HasUpdate=false when remote SHA matches cached baseline")
	}
}

func TestCheckUpdates_NoCache_NoUpdate(t *testing.T) {
	setupTestHome(t)
	cleanup := mockFetchSHA("some-sha", nil)
	defer cleanup()

	store := NewStore(t.TempDir(), SyncMethodSymlink)
	store.skills["skill:pdf"] = Skill{
		ID: "skill:pdf", Directory: "pdf",
		RepoOwner: "owner", RepoName: "repo", RepoBranch: "main",
	}

	results := store.CheckUpdates(nil)
	if len(results) != 1 {
		t.Fatalf("expected 1 result, got %d", len(results))
	}
	if results[0].HasUpdate {
		t.Error("expected HasUpdate=false on first check (no baseline to compare)")
	}
}

func TestCheckUpdates_APIError(t *testing.T) {
	setupTestHome(t)
	cleanup := mockFetchSHA("", fmt.Errorf("git ls-remote failed: repository not found"))
	defer cleanup()

	store := NewStore(t.TempDir(), SyncMethodSymlink)
	store.skills["skill:pdf"] = Skill{
		ID: "skill:pdf", Directory: "pdf",
		RepoOwner: "owner", RepoName: "repo", RepoBranch: "main",
	}

	results := store.CheckUpdates(nil)
	if len(results) != 1 {
		t.Fatalf("expected 1 result, got %d", len(results))
	}
	if results[0].Error == "" {
		t.Error("expected error message for fetch failure")
	}
	if results[0].HasUpdate {
		t.Error("expected HasUpdate=false on fetch error")
	}
}

func TestCheckUpdates_MultipleSkills(t *testing.T) {
	setupTestHome(t)
	forceGitFallbackForTest(t)

	// mock 返回不同 repo 不同 SHA
	orig := fetchSkillCommitSHAFunc
	defer func() { fetchSkillCommitSHAFunc = orig }()

	callCount := int32(0)
	fetchSkillCommitSHAFunc = func(ctx context.Context, owner, repo, branch string) (string, error) {
		atomic.AddInt32(&callCount, 1)
		if repo == "skills-repo" {
			return "new-sha", nil
		}
		return "same-sha", nil
	}
	_ = callCount

	cache := map[string]updateCacheEntry{
		"skill:pdf":    {CommitSHA: "old-sha", CheckedAt: "2026-07-01T00:00:00Z"},
		"skill:memory": {CommitSHA: "same-sha", CheckedAt: "2026-07-01T00:00:00Z"},
	}
	if err := writeUpdateCache(cache); err != nil {
		t.Fatalf("writeUpdateCache: %v", err)
	}

	store := NewStore(t.TempDir(), SyncMethodSymlink)
	store.skills["skill:pdf"] = Skill{
		ID: "skill:pdf", Directory: "pdf",
		RepoOwner: "owner", RepoName: "skills-repo", RepoBranch: "main",
	}
	store.skills["skill:memory"] = Skill{
		ID: "skill:memory", Directory: "memory",
		RepoOwner: "owner", RepoName: "memory-repo", RepoBranch: "main",
	}

	results := store.CheckUpdates(nil)
	if len(results) != 2 {
		t.Fatalf("expected 2 results, got %d", len(results))
	}

	var pdfResult, memoryResult *UpdateStatus
	for i := range results {
		if results[i].SkillID == "skill:pdf" {
			pdfResult = &results[i]
		}
		if results[i].SkillID == "skill:memory" {
			memoryResult = &results[i]
		}
	}

	if pdfResult == nil || !pdfResult.HasUpdate {
		t.Error("pdf should have HasUpdate=true (SHA changed)")
	}
	if memoryResult == nil || memoryResult.HasUpdate {
		t.Error("memory should have HasUpdate=false (SHA unchanged)")
	}
}

func TestCheckUpdates_DeduplicatesRepos(t *testing.T) {
	setupTestHome(t)
	forceGitFallbackForTest(t)

	// 同一仓库下有多个 skill
	orig := fetchSkillCommitSHAFunc
	defer func() { fetchSkillCommitSHAFunc = orig }()

	var callCount int32
	fetchSkillCommitSHAFunc = func(ctx context.Context, owner, repo, branch string) (string, error) {
		atomic.AddInt32(&callCount, 1)
		return "shared-sha", nil
	}

	cache := map[string]updateCacheEntry{
		"skill:pdf":      {CommitSHA: "old-sha", CheckedAt: "2026-07-01T00:00:00Z"},
		"skill:memory":   {CommitSHA: "old-sha", CheckedAt: "2026-07-01T00:00:00Z"},
		"skill:seqthink": {CommitSHA: "old-sha", CheckedAt: "2026-07-01T00:00:00Z"},
	}
	if err := writeUpdateCache(cache); err != nil {
		t.Fatalf("writeUpdateCache: %v", err)
	}

	store := NewStore(t.TempDir(), SyncMethodSymlink)
	// 3 个 skill 来自同一仓库
	for _, dir := range []string{"pdf", "memory", "seqthink"} {
		store.skills["skill:"+dir] = Skill{
			ID: "skill:" + dir, Directory: dir,
			RepoOwner: "obra", RepoName: "superpowers", RepoBranch: "main",
		}
	}

	results := store.CheckUpdates(nil)
	if len(results) != 3 {
		t.Fatalf("expected 3 results, got %d", len(results))
	}

	// git ls-remote 应该只被调用 1 次（去重）
	if atomic.LoadInt32(&callCount) != 1 {
		t.Errorf("expected 1 git ls-remote call for same repo, got %d", callCount)
	}

	// 所有 skill 都应检测到更新
	for _, r := range results {
		if !r.HasUpdate {
			t.Errorf("expected HasUpdate=true for %s", r.SkillID)
		}
	}
}

func TestCacheSkillCommitSHA(t *testing.T) {
	setupTestHome(t)
	cleanup := mockFetchSHA("cached-sha-789", nil)
	defer cleanup()

	err := CacheSkillCommitSHA("skill:pdf", "owner", "repo", "main")
	if err != nil {
		t.Fatalf("CacheSkillCommitSHA failed: %v", err)
	}

	result := readUpdateCache()
	if result == nil {
		t.Fatal("expected non-nil cache after CacheSkillCommitSHA")
	}
	if result["skill:pdf"].CommitSHA != "cached-sha-789" {
		t.Errorf("expected cached-sha-789, got %s", result["skill:pdf"].CommitSHA)
	}
}

func TestCacheSkillCommitSHA_SetsBaselineForCheckUpdates(t *testing.T) {
	setupTestHome(t)
	forceGitFallbackForTest(t)

	orig := fetchSkillCommitSHAFunc
	defer func() { fetchSkillCommitSHAFunc = orig }()

	callCount := int32(0)
	fetchSkillCommitSHAFunc = func(ctx context.Context, owner, repo, branch string) (string, error) {
		n := atomic.AddInt32(&callCount, 1)
		if n <= 1 {
			return "sha-v1", nil
		}
		return "sha-v2", nil
	}

	// 1. 安装时缓存基线
	err := CacheSkillCommitSHA("skill:pdf", "owner", "repo", "main")
	if err != nil {
		t.Fatalf("CacheSkillCommitSHA: %v", err)
	}

	// 2. 后来检查更新（远程 SHA 已变化）
	store := NewStore(t.TempDir(), SyncMethodSymlink)
	store.skills["skill:pdf"] = Skill{
		ID: "skill:pdf", Directory: "pdf",
		RepoOwner: "owner", RepoName: "repo", RepoBranch: "main",
	}

	results := store.CheckUpdates(nil)
	if len(results) != 1 {
		t.Fatalf("expected 1 result, got %d", len(results))
	}
	if !results[0].HasUpdate {
		t.Error("expected HasUpdate=true after baseline was set and remote SHA changed")
	}
}

func TestUpdateSkill_NoRepoInfo(t *testing.T) {
	store := NewStore(t.TempDir(), SyncMethodSymlink)
	store.skills["skill:local"] = Skill{
		ID:        "skill:local",
		Directory: "local",
	}

	_, err := store.UpdateSkill("skill:local", nil)
	if err == nil {
		t.Fatal("expected error for skill without repo info")
	}
}

func TestUpdateSkill_NotFound(t *testing.T) {
	store := NewStore(t.TempDir(), SyncMethodSymlink)

	_, err := store.UpdateSkill("nonexistent", nil)
	if err == nil {
		t.Fatal("expected error for nonexistent skill")
	}
}

func TestUpdateSkill_FailedDownloadPreservesOldVersion(t *testing.T) {
	setupSkillCapableAgentHome(t)
	tmp := t.TempDir()
	ssotDir := filepath.Join(tmp, "ssot")
	makeSkillDir(t, ssotDir, "demo", "---\nname: demo\n---\n# old")

	// 隔离备用代理与直连，确保所有 tarball 候选都指向 mock（500），
	// 避免测试在 CI/有网环境真实请求 ghfast.top / codeload 导致结果不稳定。
	origFallback := tarballFallbackURLs
	tarballFallbackURLs = nil
	t.Cleanup(func() { tarballFallbackURLs = origFallback })

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer server.Close()
	originalProxy := config.DefaultGitHubProxy
	config.DefaultGitHubProxy = server.URL + "/"
	t.Cleanup(func() { config.DefaultGitHubProxy = originalProxy })
	cleanupSHA := mockFetchSHA("new-sha", nil)
	defer cleanupSHA()

	store := NewStore(ssotDir, SyncMethodCopy)
	store.skills["skill:demo"] = Skill{
		ID: "skill:demo", Directory: "demo", RepoOwner: "owner", RepoName: "repo", RepoBranch: "main",
	}
	reg := newSkillTestRegistry()
	store.bindings["skill:demo"] = map[string]bool{"claude-code": true}

	if _, err := store.UpdateSkill("skill:demo", reg); err == nil {
		t.Fatal("expected update to fail when the new tarball cannot be downloaded")
	}
	content, err := os.ReadFile(filepath.Join(ssotDir, "demo", "SKILL.md"))
	if err != nil {
		t.Fatalf("old skill was removed after failed update: %v", err)
	}
	if string(content) != "---\nname: demo\n---\n# old" {
		t.Fatalf("old skill content changed after failed update: %q", content)
	}
}

func TestUpdateSkill_UsesLiveBindings(t *testing.T) {
	setupSkillCapableAgentHome(t)
	tmp := t.TempDir()
	ssotDir := filepath.Join(tmp, "ssot")
	makeSkillDir(t, ssotDir, "demo", "---\nname: demo\n---\n# old")

	tarball := createTestTarball(t, "repo", map[string]string{
		"demo": "---\nname: demo\n---\n# new",
	})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write(tarball)
	}))
	defer server.Close()
	originalProxy := config.DefaultGitHubProxy
	config.DefaultGitHubProxy = server.URL + "/"
	t.Cleanup(func() { config.DefaultGitHubProxy = originalProxy })
	cleanupSHA := mockFetchSHA("new-sha", nil)
	defer cleanupSHA()

	store := NewStore(ssotDir, SyncMethodCopy)
	store.skills["skill:demo"] = Skill{
		ID: "skill:demo", Directory: "demo", BoundAgents: []string{"claude-code", "opencode"},
		RepoOwner: "owner", RepoName: "repo", RepoBranch: "main",
	}
	// opencode is disabled; only claude-code is a live binding.
	store.bindings["skill:demo"] = map[string]bool{"claude-code": true}
	reg := newSkillTestRegistry()
	reg.Register(agents.Agent{ID: "opencode", Name: "OpenCode", Status: agents.StatusEnabled})

	if _, err := store.UpdateSkill("skill:demo", reg); err != nil {
		t.Fatalf("update failed: %v", err)
	}

	newContent, err := os.ReadFile(filepath.Join(ssotDir, "demo", "SKILL.md"))
	if err != nil {
		t.Fatalf("read updated SSOT skill: %v", err)
	}
	if string(newContent) != "---\nname: demo\n---\n# new" {
		t.Fatalf("expected new SSOT content, got %q", newContent)
	}
	activeTarget := filepath.Join(reg.AgentSkillsDir("claude-code"), "demo", "SKILL.md")
	activeContent, err := os.ReadFile(activeTarget)
	if err != nil {
		t.Fatalf("active agent was not synchronized: %v", err)
	}
	if string(activeContent) != string(newContent) {
		t.Fatalf("active agent content mismatch: %q", activeContent)
	}
	disabledTarget := filepath.Join(reg.AgentSkillsDir("opencode"), "demo")
	if _, err := os.Stat(disabledTarget); !os.IsNotExist(err) {
		t.Fatalf("disabled agent was synchronized during update: %v", err)
	}
}

func TestUpdateSkills_EmptyList(t *testing.T) {
	store := NewStore(t.TempDir(), SyncMethodSymlink)
	result := store.UpdateSkills(nil, nil)
	if len(result.Updated) != 0 || len(result.Errors) != 0 {
		t.Errorf("expected empty result for empty input, got updated=%d errors=%d",
			len(result.Updated), len(result.Errors))
	}
}

func TestUpdateSkillsResult_Structure(t *testing.T) {
	result := UpdateSkillsResult{
		Updated: []Skill{{ID: "skill:a", Directory: "a"}},
		Errors:  []UpdateError{{SkillID: "skill:b", Error: "failed"}},
	}
	if len(result.Updated) != 1 {
		t.Errorf("expected 1 updated, got %d", len(result.Updated))
	}
	if len(result.Errors) != 1 {
		t.Errorf("expected 1 error, got %d", len(result.Errors))
	}
	if result.Updated[0].ID != "skill:a" {
		t.Errorf("expected skill:a, got %s", result.Updated[0].ID)
	}
	if result.Errors[0].SkillID != "skill:b" {
		t.Errorf("expected skill:b, got %s", result.Errors[0].SkillID)
	}
}

func TestCheckUpdates_DefaultBranch(t *testing.T) {
	setupTestHome(t)
	cleanup := mockFetchSHA("sha-main", nil)
	defer cleanup()

	store := NewStore(t.TempDir(), SyncMethodSymlink)
	store.skills["skill:pdf"] = Skill{
		ID: "skill:pdf", Directory: "pdf",
		RepoOwner: "owner", RepoName: "repo",
	}

	results := store.CheckUpdates(nil)
	if len(results) != 1 {
		t.Fatalf("expected 1 result, got %d", len(results))
	}
	if results[0].RemoteHash != "sha-main" {
		t.Errorf("expected remote hash sha-main for default branch, got %s", results[0].RemoteHash)
	}
}

func TestCheckUpdates_DifferentBranchesSameRepo(t *testing.T) {
	setupTestHome(t)
	forceGitFallbackForTest(t)

	orig := fetchSkillCommitSHAFunc
	defer func() { fetchSkillCommitSHAFunc = orig }()

	var callCount int32
	fetchSkillCommitSHAFunc = func(ctx context.Context, owner, repo, branch string) (string, error) {
		atomic.AddInt32(&callCount, 1)
		if branch == "main" {
			return "sha-main", nil
		}
		return "sha-dev", nil
	}

	store := NewStore(t.TempDir(), SyncMethodSymlink)
	store.skills["skill:a"] = Skill{
		ID: "skill:a", Directory: "a",
		RepoOwner: "owner", RepoName: "repo", RepoBranch: "main",
	}
	store.skills["skill:b"] = Skill{
		ID: "skill:b", Directory: "b",
		RepoOwner: "owner", RepoName: "repo", RepoBranch: "dev",
	}

	results := store.CheckUpdates(nil)
	if len(results) != 2 {
		t.Fatalf("expected 2 results, got %d", len(results))
	}

	if atomic.LoadInt32(&callCount) != 2 {
		t.Errorf("expected 2 git ls-remote calls for different branches, got %d", callCount)
	}

	var aResult, bResult *UpdateStatus
	for i := range results {
		if results[i].SkillID == "skill:a" {
			aResult = &results[i]
		}
		if results[i].SkillID == "skill:b" {
			bResult = &results[i]
		}
	}
	if aResult == nil || aResult.RemoteHash != "sha-main" {
		t.Errorf("expected skill:a remote hash sha-main, got %v", aResult)
	}
	if bResult == nil || bResult.RemoteHash != "sha-dev" {
		t.Errorf("expected skill:b remote hash sha-dev, got %v", bResult)
	}
}

func TestCheckUpdates_ReturnsCheckedAt(t *testing.T) {
	setupTestHome(t)
	cleanup := mockFetchSHA("sha-now", nil)
	defer cleanup()

	store := NewStore(t.TempDir(), SyncMethodSymlink)
	store.skills["skill:pdf"] = Skill{
		ID: "skill:pdf", Directory: "pdf",
		RepoOwner: "owner", RepoName: "repo", RepoBranch: "main",
	}

	before := time.Now().UTC()
	results := store.CheckUpdates(nil)
	after := time.Now().UTC()

	if len(results) != 1 {
		t.Fatalf("expected 1 result, got %d", len(results))
	}

	checkedAt, err := time.Parse(time.RFC3339, results[0].CheckedAt)
	if err != nil {
		t.Fatalf("invalid CheckedAt format: %v", err)
	}
	if checkedAt.Before(before.Add(-time.Second)) || checkedAt.After(after.Add(time.Second)) {
		t.Errorf("CheckedAt %v not within expected window [%v, %v]", checkedAt, before, after)
	}
}

func TestCheckUpdates_CacheWriteFailure_StillReturnsResults(t *testing.T) {
	setupTestHome(t)

	origWrite := writeUpdateCacheFunc
	writeUpdateCacheFunc = func(_ map[string]updateCacheEntry) error {
		return fmt.Errorf("disk full")
	}
	defer func() { writeUpdateCacheFunc = origWrite }()

	cleanup := mockFetchSHA("sha-1", nil)
	defer cleanup()

	store := NewStore(t.TempDir(), SyncMethodSymlink)
	store.skills["skill:pdf"] = Skill{
		ID: "skill:pdf", Directory: "pdf",
		RepoOwner: "owner", RepoName: "repo", RepoBranch: "main",
	}

	results := store.CheckUpdates(nil)
	if len(results) != 1 {
		t.Fatalf("expected 1 result despite cache write failure, got %d", len(results))
	}
	if results[0].RemoteHash != "sha-1" {
		t.Errorf("expected remote hash sha-1, got %s", results[0].RemoteHash)
	}
}

func TestCheckUpdates_NonGitHubSkillSkipped(t *testing.T) {
	setupTestHome(t)
	cleanup := mockFetchSHA("sha-1", nil)
	defer cleanup()

	store := NewStore(t.TempDir(), SyncMethodSymlink)
	store.skills["skill:local"] = Skill{
		ID:        "skill:local",
		Directory: "local",
		FullPath:  "/tmp/local",
	}

	results := store.CheckUpdates(nil)
	if results != nil {
		t.Errorf("expected nil for non-GitHub skills, got %v", results)
	}
}

func TestCacheSkillCommitSHA_OverwritesExisting(t *testing.T) {
	setupTestHome(t)

	callCount := int32(0)
	origFetch := fetchSkillCommitSHAFunc
	fetchSkillCommitSHAFunc = func(ctx context.Context, owner, repo, branch string) (string, error) {
		n := atomic.AddInt32(&callCount, 1)
		if n == 1 {
			return "sha-v1", nil
		}
		return "sha-v2", nil
	}
	defer func() { fetchSkillCommitSHAFunc = origFetch }()

	if err := CacheSkillCommitSHA("skill:pdf", "owner", "repo", "main"); err != nil {
		t.Fatalf("first CacheSkillCommitSHA failed: %v", err)
	}
	if err := CacheSkillCommitSHA("skill:pdf", "owner", "repo", "main"); err != nil {
		t.Fatalf("second CacheSkillCommitSHA failed: %v", err)
	}

	cache := readUpdateCache()
	if cache == nil {
		t.Fatal("expected non-nil cache")
	}
	if cache["skill:pdf"].CommitSHA != "sha-v2" {
		t.Errorf("expected sha-v2 after overwrite, got %s", cache["skill:pdf"].CommitSHA)
	}
}

func TestCheckUpdates_FailedSkill_DoesNotOverwriteCacheBaseline(t *testing.T) {
	setupTestHome(t)
	forceGitFallbackForTest(t)

	orig := fetchSkillCommitSHAFunc
	defer func() { fetchSkillCommitSHAFunc = orig }()

	fetchSkillCommitSHAFunc = func(ctx context.Context, owner, repo, branch string) (string, error) {
		if repo == "fail-repo" {
			return "", fmt.Errorf("network error")
		}
		return "new-sha", nil
	}

	cache := map[string]updateCacheEntry{
		"skill:ok":   {CommitSHA: "old-sha", CheckedAt: "2026-07-01T00:00:00Z"},
		"skill:fail": {CommitSHA: "baseline-sha", CheckedAt: "2026-07-01T00:00:00Z"},
	}
	if err := writeUpdateCache(cache); err != nil {
		t.Fatalf("writeUpdateCache failed: %v", err)
	}

	store := NewStore(t.TempDir(), SyncMethodSymlink)
	store.skills["skill:ok"] = Skill{
		ID: "skill:ok", Directory: "ok",
		RepoOwner: "owner", RepoName: "ok-repo", RepoBranch: "main",
	}
	store.skills["skill:fail"] = Skill{
		ID: "skill:fail", Directory: "fail",
		RepoOwner: "owner", RepoName: "fail-repo", RepoBranch: "main",
	}

	results := store.CheckUpdates(nil)
	if len(results) != 2 {
		t.Fatalf("expected 2 results, got %d", len(results))
	}

	var okResult, failResult *UpdateStatus
	for i := range results {
		if results[i].SkillID == "skill:ok" {
			okResult = &results[i]
		}
		if results[i].SkillID == "skill:fail" {
			failResult = &results[i]
		}
	}

	if okResult == nil || !okResult.HasUpdate {
		t.Error("skill:ok should have HasUpdate=true (SHA changed)")
	}
	if failResult == nil {
		t.Fatal("skill:fail result not found")
	}
	if failResult.Error == "" {
		t.Error("skill:fail should have error message")
	}
	if failResult.HasUpdate {
		t.Error("skill:fail should not have HasUpdate=true on fetch error")
	}

	// 验证失败 skill 的缓存基线未被覆盖
	updatedCache := readUpdateCache()
	if updatedCache == nil {
		t.Fatal("expected non-nil cache after CheckUpdates")
	}
	if updatedCache["skill:fail"].CommitSHA != "baseline-sha" {
		t.Errorf("skill:fail baseline should not be overwritten, got %s", updatedCache["skill:fail"].CommitSHA)
	}
}

func TestCheckUpdates_MasterFallback(t *testing.T) {
	setupTestHome(t)
	forceGitFallbackForTest(t)

	orig := fetchSkillCommitSHAFunc
	defer func() { fetchSkillCommitSHAFunc = orig }()

	fetchSkillCommitSHAFunc = func(ctx context.Context, owner, repo, branch string) (string, error) {
		if branch == "main" {
			return "", fmt.Errorf("main not found")
		}
		if branch == "master" {
			return "sha-master", nil
		}
		return "", fmt.Errorf("unexpected branch: %s", branch)
	}

	store := NewStore(t.TempDir(), SyncMethodSymlink)
	store.skills["skill:pdf"] = Skill{
		ID: "skill:pdf", Directory: "pdf",
		RepoOwner: "owner", RepoName: "repo",
	}

	results := store.CheckUpdates(nil)
	if len(results) != 1 {
		t.Fatalf("expected 1 result, got %d", len(results))
	}

	if results[0].RemoteHash != "sha-master" {
		t.Errorf("expected sha-master from fallback, got %s", results[0].RemoteHash)
	}
	if results[0].Error != "" {
		t.Errorf("expected no error on successful fallback, got %s", results[0].Error)
	}
}

func TestCheckUpdates_ErrorsPerSkill(t *testing.T) {
	setupTestHome(t)
	forceGitFallbackForTest(t)

	orig := fetchSkillCommitSHAFunc
	defer func() { fetchSkillCommitSHAFunc = orig }()

	fetchSkillCommitSHAFunc = func(ctx context.Context, owner, repo, branch string) (string, error) {
		if owner == "owner" && repo == "fail-repo" {
			return "", fmt.Errorf("rate limit")
		}
		return "sha-ok", nil
	}

	store := NewStore(t.TempDir(), SyncMethodSymlink)
	store.skills["skill:ok"] = Skill{
		ID: "skill:ok", Directory: "ok",
		RepoOwner: "owner", RepoName: "ok-repo", RepoBranch: "main",
	}
	store.skills["skill:fail1"] = Skill{
		ID: "skill:fail1", Directory: "fail1",
		RepoOwner: "owner", RepoName: "fail-repo", RepoBranch: "main",
	}
	store.skills["skill:fail2"] = Skill{
		ID: "skill:fail2", Directory: "fail2",
		RepoOwner: "owner", RepoName: "fail-repo", RepoBranch: "main",
	}

	results := store.CheckUpdates(nil)
	if len(results) != 3 {
		t.Fatalf("expected 3 results, got %d", len(results))
	}

	skillMap := make(map[string]*UpdateStatus)
	for i := range results {
		skillMap[results[i].SkillID] = &results[i]
	}

	if skillMap["skill:ok"].Error != "" {
		t.Errorf("skill:ok should not have error, got %s", skillMap["skill:ok"].Error)
	}
	if skillMap["skill:fail1"].Error == "" {
		t.Error("skill:fail1 should have error")
	}
	if skillMap["skill:fail2"].Error == "" {
		t.Error("skill:fail2 should have error")
	}
	if skillMap["skill:fail1"].Error != skillMap["skill:fail2"].Error {
		t.Error("same repo should have same error for both skills")
	}
}

func TestSafeRelPath(t *testing.T) {
	valid := map[string]string{
		"SKILL.md":        "SKILL.md",
		"skills/pdf/a.md": "skills/pdf/a.md",
		"a/b/c/":          "a/b/c",
		"./a/b":           "a/b",
	}
	for in, want := range valid {
		got, err := safeRelPath(in)
		if err != nil {
			t.Errorf("safeRelPath(%q): unexpected error %v", in, err)
			continue
		}
		if got != want {
			t.Errorf("safeRelPath(%q) = %q, want %q", in, got, want)
		}
	}

	invalid := []string{
		"",                                // 空路径
		"..",                              // 目录穿越
		"../evil",                         // 目录穿越
		"a/../../evil",                    // 目录穿越
		"a\\..\\evil",                     // Windows 分隔符穿越
		"..\\..\\Windows\\System32\\evil", // Windows 逃逸
		"C:/evil",                         // 盘符
		"/abs/path",                       // 绝对路径
		"a:",                              // 盘符后缀
	}
	for _, in := range invalid {
		if got, err := safeRelPath(in); err == nil {
			t.Errorf("safeRelPath(%q) = %q, want error", in, got)
		}
	}
}

// TestWriteTmpFile_RejectsTraversal 验证 writeTmpFile 拒绝穿越路径，
// 确保 updateSkillViaTree 下载写入不会逃出临时目录。
func TestWriteTmpFile_RejectsTraversal(t *testing.T) {
	tmpDir := t.TempDir()
	evil := "..\\..\\..\\..\\..\\Windows\\System32\\evil.txt"
	if err := writeTmpFile(tmpDir, evil, []byte("x")); err == nil {
		t.Fatalf("writeTmpFile should reject path traversal %q", evil)
	}
	if _, err := os.Stat(filepath.Join(tmpDir, "..", "..", "..", "..", "..", "Windows", "System32", "evil.txt")); err == nil {
		t.Fatal("traversal file must not exist outside tmpDir")
	}
}
