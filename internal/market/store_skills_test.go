package market

import (
	"context"
	"errors"
	"strings"
	"testing"
)

var errStub = errors.New("stub error")

// stubSkillFetcher 是测试用的 SkillFetcher stub
type stubSkillFetcher struct {
	source Source
	skills []MarketSkill
	err    error
}

func (s *stubSkillFetcher) Source() Source { return s.source }
func (s *stubSkillFetcher) Search(ctx context.Context, opts SearchOptions) (*SearchResultSkills, error) {
	if s.err != nil {
		return nil, s.err
	}
	// 按 query 过滤
	var items []MarketSkill
	q := opts.Query
	for _, item := range s.skills {
		if q == "" || strings.Contains(item.Name, q) || strings.Contains(item.Directory, q) {
			items = append(items, item)
		}
	}
	return &SearchResultSkills{Items: items, Total: len(items), Page: 1}, nil
}

func TestSearchAllSkills_MergeAndSort(t *testing.T) {
	store := NewStore(t.TempDir())

	// GitHub 源（无 installs）
	store.RegisterSkillFetcher(&stubSkillFetcher{
		source: SourceGitHub,
		skills: []MarketSkill{
			{ID: "gh1", Name: "Alpha", Directory: "alpha", Source: SourceGitHub, RepoOwner: "a", RepoName: "repo", Installs: 0},
			{ID: "gh2", Name: "Beta", Directory: "beta", Source: SourceGitHub, RepoOwner: "b", RepoName: "repo", Installs: 0},
		},
	})

	// skills.sh 源（有 installs）
	store.RegisterSkillFetcher(&stubSkillFetcher{
		source: SourceSkillsSh,
		skills: []MarketSkill{
			{ID: "ss1", Name: "Gamma", Directory: "gamma", Source: SourceSkillsSh, RepoOwner: "c", RepoName: "repo", Installs: 1000},
			{ID: "ss2", Name: "Delta", Directory: "delta", Source: SourceSkillsSh, RepoOwner: "d", RepoName: "repo", Installs: 500},
		},
	})

	got, err := store.SearchAllSkills(context.Background(), SearchOptions{PageSize: 30}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Items) != 4 {
		t.Fatalf("expected 4 items, got %d", len(got.Items))
	}

	// 验证按 Installs 降序排序
	if got.Items[0].Installs < got.Items[1].Installs {
		t.Errorf("expected descending order, got %d before %d", got.Items[0].Installs, got.Items[1].Installs)
	}
	// 第一个应该是 installs=1000 的 Gamma
	if got.Items[0].Name != "Gamma" {
		t.Errorf("expected first item 'Gamma', got %q", got.Items[0].Name)
	}
}

func TestSearchAllSkills_DedupPreferSkillsSh(t *testing.T) {
	store := NewStore(t.TempDir())

	// GitHub 和 skills.sh 有相同的 skill（同 owner/repo/directory）
	store.RegisterSkillFetcher(&stubSkillFetcher{
		source: SourceGitHub,
		skills: []MarketSkill{
			{ID: "gh1", Name: "Alpha-GH", Directory: "alpha", Source: SourceGitHub, RepoOwner: "a", RepoName: "repo", Installs: 0},
		},
	})

	store.RegisterSkillFetcher(&stubSkillFetcher{
		source: SourceSkillsSh,
		skills: []MarketSkill{
			{ID: "ss1", Name: "Alpha-SS", Directory: "alpha", Source: SourceSkillsSh, RepoOwner: "a", RepoName: "repo", Installs: 100},
		},
	})

	got, err := store.SearchAllSkills(context.Background(), SearchOptions{PageSize: 30}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Items) != 1 {
		t.Fatalf("expected 1 item after dedup, got %d", len(got.Items))
	}
	// 应该保留 skills.sh 条目（有 installs）
	if got.Items[0].Source != SourceSkillsSh {
		t.Errorf("expected skills.sh source after dedup, got %q", got.Items[0].Source)
	}
	if got.Items[0].Name != "Alpha-SS" {
		t.Errorf("expected 'Alpha-SS', got %q", got.Items[0].Name)
	}
	if got.Items[0].Installs != 100 {
		t.Errorf("expected installs 100, got %d", got.Items[0].Installs)
	}
}

func TestSearchAllSkills_PartialFailure(t *testing.T) {
	store := NewStore(t.TempDir())

	// GitHub 源正常
	store.RegisterSkillFetcher(&stubSkillFetcher{
		source: SourceGitHub,
		skills: []MarketSkill{
			{ID: "gh1", Name: "Alpha", Directory: "alpha", Source: SourceGitHub, RepoOwner: "a", RepoName: "repo", Installs: 0},
		},
	})

	// skills.sh 源失败
	store.RegisterSkillFetcher(&stubSkillFetcher{
		source: SourceSkillsSh,
		err:    errStub,
	})

	got, err := store.SearchAllSkills(context.Background(), SearchOptions{PageSize: 30}, nil)
	if err != nil {
		t.Fatalf("expected nil error for partial failure, got %v", err)
	}
	if len(got.Items) != 1 {
		t.Fatalf("expected 1 item from successful source, got %d", len(got.Items))
	}
	if got.Items[0].Name != "Alpha" {
		t.Errorf("expected 'Alpha', got %q", got.Items[0].Name)
	}
}

func TestSearchAllSkills_EmptySources(t *testing.T) {
	store := NewStore(t.TempDir())

	got, err := store.SearchAllSkills(context.Background(), SearchOptions{PageSize: 30}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Items) != 0 {
		t.Errorf("expected 0 items for no sources, got %d", len(got.Items))
	}
}

func TestSearchAllSkills_PageSizeTruncation(t *testing.T) {
	store := NewStore(t.TempDir())

	// 注册 5 个 skills
	skills := []MarketSkill{
		{ID: "s1", Name: "S1", Directory: "d1", Source: SourceSkillsSh, RepoOwner: "a", RepoName: "repo", Installs: 100},
		{ID: "s2", Name: "S2", Directory: "d2", Source: SourceSkillsSh, RepoOwner: "b", RepoName: "repo", Installs: 90},
		{ID: "s3", Name: "S3", Directory: "d3", Source: SourceSkillsSh, RepoOwner: "c", RepoName: "repo", Installs: 80},
		{ID: "s4", Name: "S4", Directory: "d4", Source: SourceSkillsSh, RepoOwner: "d", RepoName: "repo", Installs: 70},
		{ID: "s5", Name: "S5", Directory: "d5", Source: SourceSkillsSh, RepoOwner: "e", RepoName: "repo", Installs: 60},
	}
	store.RegisterSkillFetcher(&stubSkillFetcher{
		source: SourceSkillsSh,
		skills: skills,
	})

	// pageSize=3，应只返回前 3 个
	got, err := store.SearchAllSkills(context.Background(), SearchOptions{PageSize: 3}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Items) != 3 {
		t.Fatalf("expected 3 items after truncation, got %d", len(got.Items))
	}
	if got.Total != 5 {
		t.Errorf("expected total 5, got %d", got.Total)
	}
	if !got.HasMore {
		t.Error("expected HasMore=true")
	}
	if got.NextPage != "2" {
		t.Errorf("expected NextPage='2', got %q", got.NextPage)
	}
}

func TestDedupSkills_NoDuplicateKeys(t *testing.T) {
	items := []MarketSkill{
		{ID: "1", Directory: "alpha", RepoOwner: "a", RepoName: "repo", Source: SourceGitHub},
		{ID: "2", Directory: "beta", RepoOwner: "b", RepoName: "repo", Source: SourceGitHub},
	}
	got := dedupSkills(items)
	if len(got) != 2 {
		t.Errorf("expected 2 items (no dups), got %d", len(got))
	}
}

func TestDedupSkills_KeepFirstWhenSameSource(t *testing.T) {
	items := []MarketSkill{
		{ID: "1", Name: "First", Directory: "alpha", RepoOwner: "a", RepoName: "repo", Source: SourceGitHub, Installs: 10},
		{ID: "2", Name: "Second", Directory: "alpha", RepoOwner: "a", RepoName: "repo", Source: SourceGitHub, Installs: 20},
	}
	got := dedupSkills(items)
	if len(got) != 1 {
		t.Fatalf("expected 1 item, got %d", len(got))
	}
	// 同源时保留首次出现的
	if got[0].Name != "First" {
		t.Errorf("expected 'First', got %q", got[0].Name)
	}
}

func TestSortSkillsByInstalls(t *testing.T) {
	items := []MarketSkill{
		{ID: "1", Name: "A", Installs: 10},
		{ID: "2", Name: "B", Installs: 100},
		{ID: "3", Name: "C", Installs: 50},
	}
	sortSkillsByInstalls(items)
	if items[0].Name != "B" {
		t.Errorf("expected first 'B' (100 installs), got %q", items[0].Name)
	}
	if items[1].Name != "C" {
		t.Errorf("expected second 'C' (50 installs), got %q", items[1].Name)
	}
	if items[2].Name != "A" {
		t.Errorf("expected third 'A' (10 installs), got %q", items[2].Name)
	}
}
