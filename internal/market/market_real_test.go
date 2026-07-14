package market

import (
	"context"
	"os"
	"testing"
	"time"
)

// realTestRequired 判断是否运行真实网络测试。
// 设置环境变量 AGENTPACK_REALNET=1 启用，默认跳过以避免在离线/CI 环境失败。
func realTestRequired(t *testing.T) {
	t.Helper()
	if os.Getenv("AGENTPACK_REALNET") != "1" {
		t.Skip("skipping real network test; set AGENTPACK_REALNET=1 to run")
	}
}

// TestReal_SkillsShSearch 真实调用 skills.sh API 搜索 skill 列表。
// 验证第三方市场搜索链路是否端到端可用。
func TestReal_SkillsShSearch(t *testing.T) {
	realTestRequired(t)

	f := NewSkillsShFetcher()
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	result, err := f.Search(ctx, SearchOptions{Query: "pdf", PageSize: 20, Page: 1})
	if err != nil {
		t.Fatalf("skills.sh search failed: %v", err)
	}
	if result == nil || len(result.Items) == 0 {
		t.Fatalf("expected non-empty result from skills.sh for query 'pdf', got: %+v", result)
	}
	t.Logf("skills.sh returned %d items for 'pdf'", len(result.Items))

	// 验证返回的 MarketSkill 关键字段非空
	first := result.Items[0]
	if first.Directory == "" {
		t.Errorf("first item Directory is empty: %+v", first)
	}
	if first.RepoOwner == "" || first.RepoName == "" {
		t.Errorf("first item repo info missing: %+v", first)
	}
	if first.Source != SourceSkillsSh {
		t.Errorf("expected source %s, got %s", SourceSkillsSh, first.Source)
	}
	t.Logf("first item: dir=%s repo=%s/%s fullPath=%s",
		first.Directory, first.RepoOwner, first.RepoName, first.FullPath)
}

// TestReal_GitHubSkillScan 真实扫描 anthropics/skills 仓库的 SKILL.md 列表。
// 验证 GitHub fetcher 对真实仓库的端到端扫描能力。
func TestReal_GitHubSkillScan(t *testing.T) {
	realTestRequired(t)

	f := NewGitHubSkillFetcher(func() []RepoRef {
		return []RepoRef{{Owner: "anthropics", Name: "skills", Branch: "main"}}
	})
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()

	result, err := f.Search(ctx, SearchOptions{Query: "", PageSize: 100, Page: 1})
	if err != nil {
		t.Fatalf("github skill scan failed: %v", err)
	}
	if result == nil || len(result.Items) == 0 {
		t.Fatalf("expected non-empty result from anthropics/skills, got: %+v", result)
	}
	t.Logf("anthropics/skills returned %d skills", len(result.Items))

	// anthropics/skills 仓库应包含 pdf skill（位于 skills/pdf/SKILL.md）
	foundPDF := false
	for _, item := range result.Items {
		if item.Directory == "pdf" {
			foundPDF = true
			if item.RepoOwner != "anthropics" || item.RepoName != "skills" {
				t.Errorf("pdf skill repo mismatch: owner=%s name=%s", item.RepoOwner, item.RepoName)
			}
			if item.FullPath != "skills/pdf" {
				t.Errorf("pdf skill FullPath mismatch: got %s, want skills/pdf", item.FullPath)
			}
			if item.Source != SourceGitHub {
				t.Errorf("expected source %s, got %s", SourceGitHub, item.Source)
			}
			break
		}
	}
	if !foundPDF {
		t.Errorf("pdf skill not found in anthropics/skills scan results")
	}
}

// TestReal_SearchAllSkills 真实合并搜索 skills.sh + GitHub 两个来源。
// 验证 market.Store 的多来源合并、去重、排序链路。
func TestReal_SearchAllSkills(t *testing.T) {
	realTestRequired(t)

	s := NewStore(t.TempDir())
	s.RegisterSkillFetcher(NewGitHubSkillFetcher(func() []RepoRef {
		return []RepoRef{{Owner: "anthropics", Name: "skills", Branch: "main"}}
	}))
	s.RegisterSkillFetcher(NewSkillsShFetcher())

	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()

	result, err := s.SearchAllSkills(ctx, SearchOptions{Query: "pdf", PageSize: 50, Page: 1},
		[]Source{SourceGitHub, SourceSkillsSh})
	if err != nil {
		t.Fatalf("SearchAllSkills failed: %v", err)
	}
	if result == nil || len(result.Items) == 0 {
		t.Fatalf("expected non-empty merged result, got: %+v", result)
	}
	t.Logf("merged search returned %d items (total=%d, hasMore=%v)",
		len(result.Items), result.Total, result.HasMore)

	// 验证至少有一个条目来自 GitHub（anthropics/skills）
	hasGitHub := false
	for _, item := range result.Items {
		if item.Source == SourceGitHub && item.RepoOwner == "anthropics" {
			hasGitHub = true
			break
		}
	}
	if !hasGitHub {
		t.Errorf("expected at least one item from GitHub anthropics/skills")
	}
}
