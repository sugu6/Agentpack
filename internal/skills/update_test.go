package skills

import (
	"agentpack/internal/config"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestFetchSkillTreeSHA(t *testing.T) {
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasSuffix(r.URL.Path, "/repos/anthropics/skills/git/trees/main") {
			http.NotFound(w, r)
			return
		}
		resp := githubTreeResponse{
			SHA: "abc123repoSha",
			Tree: []githubTreeItem{
				{Path: "README.md", Type: "blob", SHA: "sha1"},
				{Path: "filesystem", Type: "tree", SHA: "treeShaFileSystem"},
				{Path: "filesystem/SKILL.md", Type: "blob", SHA: "sha2"},
				{Path: "filesystem/utils.py", Type: "blob", SHA: "sha3"},
				{Path: "memory", Type: "tree", SHA: "treeShaMemory"},
				{Path: "memory/SKILL.md", Type: "blob", SHA: "sha4"},
			},
		}
		_ = json.NewEncoder(w).Encode(resp)
	})
	server := httptest.NewServer(handler)
	defer server.Close()

	originalProxy := config.DefaultGitHubProxy
	config.DefaultGitHubProxy = ""
	original := githubAPIBaseURL
	githubAPIBaseURL = server.URL
	defer func() {
		config.DefaultGitHubProxy = originalProxy
		githubAPIBaseURL = original
	}()

	sha, err := fetchSkillTreeSHA(context.Background(), "anthropics", "skills", "main", "filesystem")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if sha != "treeShaFileSystem" {
		t.Errorf("expected treeShaFileSystem, got %s", sha)
	}

	sha, err = fetchSkillTreeSHA(context.Background(), "anthropics", "skills", "main", "memory")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if sha != "treeShaMemory" {
		t.Errorf("expected treeShaMemory, got %s", sha)
	}

	_, err = fetchSkillTreeSHA(context.Background(), "anthropics", "skills", "main", "nonexistent")
	if err == nil {
		t.Fatal("expected error for nonexistent directory")
	}
}

func TestFetchSkillTreeSHA_NotFound(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.NotFound(w, r)
	}))
	defer server.Close()

	originalProxy := config.DefaultGitHubProxy
	config.DefaultGitHubProxy = ""
	original := githubAPIBaseURL
	githubAPIBaseURL = server.URL
	defer func() {
		config.DefaultGitHubProxy = originalProxy
		githubAPIBaseURL = original
	}()

	_, err := fetchSkillTreeSHA(context.Background(), "owner", "repo", "main", "dir")
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
			TreeSHA:   "sha123",
			CheckedAt: "2026-07-12T00:00:00Z",
		},
		"skill:memory": {
			TreeSHA:   "sha456",
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
	if result["skill:filesystem"].TreeSHA != "sha123" {
		t.Errorf("expected sha123, got %s", result["skill:filesystem"].TreeSHA)
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
