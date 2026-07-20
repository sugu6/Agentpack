package skills

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"
)

// mockFetchSHA 返回一个可编程的 fetchSkillCommitSHAFunc 替换
func mockFetchSHA(sha string, err error) func() {
	orig := fetchSkillCommitSHAFunc
	fetchSkillCommitSHAFunc = func(ctx context.Context, owner, repo, branch string) (string, error) {
		return sha, err
	}
	return func() { fetchSkillCommitSHAFunc = orig }
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
	// 测试真实 git ls-remote 调用（可能因网络失败，不作为必须通过的测试）
	if testing.Short() {
		t.Skip("skipping real git ls-remote test in short mode")
	}
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
