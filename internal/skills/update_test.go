package skills

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

func TestFetchSkillCommitSHA(t *testing.T) {
	// 模拟 GitHub Commits API
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// 验证 URL 路径
		if r.URL.Path != "/repos/anthropics/skills/commits/main" {
			http.NotFound(w, r)
			return
		}
		// 返回模拟的 commits 响应
		resp := []githubCommitItem{
			{SHA: "commitShaMain"},
		}
		_ = json.NewEncoder(w).Encode(resp)
	})
	server := httptest.NewServer(handler)
	defer server.Close()

	original := githubAPIBaseURL
	githubAPIBaseURL = server.URL
	defer func() { githubAPIBaseURL = original }()

	// 测试：获取已知分支的 commit SHA
	sha, err := fetchSkillCommitSHA(context.Background(), "anthropics", "skills", "main")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if sha != "commitShaMain" {
		t.Errorf("expected commitShaMain, got %s", sha)
	}
}

func TestFetchSkillCommitSHA_NotFound(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.NotFound(w, r)
	}))
	defer server.Close()

	original := githubAPIBaseURL
	githubAPIBaseURL = server.URL
	defer func() { githubAPIBaseURL = original }()

	_, err := fetchSkillCommitSHA(context.Background(), "owner", "repo", "main")
	if err == nil {
		t.Fatal("expected error for 404")
	}
}

func TestReadUpdateCache_NonExistent(t *testing.T) {
	// 使用临时 HOME 目录
	tmpHome := t.TempDir()
	origHome, _ := os.UserHomeDir()
	os.Setenv("USERPROFILE", tmpHome)
	os.Setenv("HOME", tmpHome)
	defer func() {
		os.Setenv("USERPROFILE", origHome)
		os.Setenv("HOME", origHome)
	}()

	result := readUpdateCache()
	if result != nil {
		t.Errorf("expected nil for non-existent cache, got %v", result)
	}
}

func TestWriteAndReadUpdateCache(t *testing.T) {
	tmpHome := t.TempDir()
	origHome, _ := os.UserHomeDir()
	os.Setenv("USERPROFILE", tmpHome)
	os.Setenv("HOME", tmpHome)
	defer func() {
		os.Setenv("USERPROFILE", origHome)
		os.Setenv("HOME", origHome)
	}()

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

	// 验证文件存在
	cachePath := filepath.Join(tmpHome, ".agentpack", "skill-update-cache.json")
	info, err := os.Stat(cachePath)
	if err != nil {
		t.Fatalf("cache file not found: %v", err)
	}
	if info.Size() == 0 {
		t.Error("expected non-empty cache file")
	}

	// 读取验证
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
	// 没有 GitHub skills，应返回 nil
	results := store.CheckUpdates(nil)
	if results != nil {
		t.Errorf("expected nil for no GitHub skills, got %v", results)
	}
}
