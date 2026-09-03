package store

import (
	"agentpack/internal/market/types"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"testing"
)

func TestStore_UnknownSource(t *testing.T) {
	dir := t.TempDir()
	st := NewStore(dir)
	_, err := st.Search(context.Background(), "nope", types.SearchOptions{})
	if err == nil {
		t.Fatal("expected error for unknown source")
	}
}

func TestCacheKey(t *testing.T) {
	dir := t.TempDir()
	st := NewStore(dir)
	k1 := st.cacheKey("search", types.SourceOfficial, types.SearchOptions{Query: "a", Page: 1})
	k2 := st.cacheKey("search", types.SourceOfficial, types.SearchOptions{Query: "a", Page: 1})
	k3 := st.cacheKey("search", types.SourceOfficial, types.SearchOptions{Query: "b", Page: 1})
	if k1 != k2 {
		t.Errorf("expected same key for same opts, got %s vs %s", k1, k2)
	}
	if k1 == k3 {
		t.Errorf("expected different keys for different query")
	}
}

func TestStore_GetServerWithoutFetcher(t *testing.T) {
	dir := t.TempDir()
	st := NewStore(dir)
	_, err := st.GetServer(context.Background(), types.SourceOfficial, "any")
	if err == nil {
		t.Fatal("expected error when no fetcher registered")
	}
}

func TestSources_Empty(t *testing.T) {
	dir := t.TempDir()
	st := NewStore(dir)
	sources := st.Sources()
	if len(sources) != 0 {
		t.Errorf("expected 0 sources, got %d: %v", len(sources), sources)
	}
}

var errStub = errors.New("stub error")

// stubSkillFetcher 是测试用的 types.SkillFetcher stub
type stubSkillFetcher struct {
	source types.Source
	skills []types.MarketSkill
	err    error
}

func (s *stubSkillFetcher) Source() types.Source { return s.source }

func (s *stubSkillFetcher) Search(ctx context.Context, opts types.SearchOptions) (*types.SearchResultSkills, error) {
	if s.err != nil {
		return nil, s.err
	}
	// 按 query 过滤
	var items []types.MarketSkill
	q := opts.Query
	for _, item := range s.skills {
		if q == "" || strings.Contains(item.Name, q) || strings.Contains(item.Directory, q) {
			items = append(items, item)
		}
	}
	return &types.SearchResultSkills{Items: items, Total: len(items), Page: 1}, nil
}

func TestSearchAllSkills_MergeAndSort(t *testing.T) {
	store := NewStore(t.TempDir())

	// GitHub 源（无 installs）
	store.RegisterSkillFetcher(&stubSkillFetcher{
		source: types.SourceGitHub,
		skills: []types.MarketSkill{
			{ID: "gh1", Name: "Alpha", Directory: "alpha", Source: types.SourceGitHub, RepoOwner: "a", RepoName: "repo", Installs: 0, ContentHash: "stub"},
			{ID: "gh2", Name: "Beta", Directory: "beta", Source: types.SourceGitHub, RepoOwner: "b", RepoName: "repo", Installs: 0, ContentHash: "stub"},
		},
	})

	// skills.sh 源（有 installs）
	store.RegisterSkillFetcher(&stubSkillFetcher{
		source: types.SourceSkillsSh,
		skills: []types.MarketSkill{
			{ID: "ss1", Name: "Gamma", Directory: "gamma", Source: types.SourceSkillsSh, RepoOwner: "c", RepoName: "repo", Installs: 1000, ContentHash: "stub"},
			{ID: "ss2", Name: "Delta", Directory: "delta", Source: types.SourceSkillsSh, RepoOwner: "d", RepoName: "repo", Installs: 500, ContentHash: "stub"},
		},
	})

	got, err := store.SearchAllSkills(context.Background(), types.SearchOptions{PageSize: 30}, nil)
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
		source: types.SourceGitHub,
		skills: []types.MarketSkill{
			{ID: "gh1", Name: "Alpha-GH", Directory: "alpha", Source: types.SourceGitHub, RepoOwner: "a", RepoName: "repo", Installs: 0, ContentHash: "stub"},
		},
	})

	store.RegisterSkillFetcher(&stubSkillFetcher{
		source: types.SourceSkillsSh,
		skills: []types.MarketSkill{
			{ID: "ss1", Name: "Alpha-SS", Directory: "alpha", Source: types.SourceSkillsSh, RepoOwner: "a", RepoName: "repo", Installs: 100, ContentHash: "stub"},
		},
	})

	got, err := store.SearchAllSkills(context.Background(), types.SearchOptions{PageSize: 30}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Items) != 1 {
		t.Fatalf("expected 1 item after dedup, got %d", len(got.Items))
	}
	// 应该保留 skills.sh 条目（有 installs）
	if got.Items[0].Source != types.SourceSkillsSh {
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
		source: types.SourceGitHub,
		skills: []types.MarketSkill{
			{ID: "gh1", Name: "Alpha", Directory: "alpha", Source: types.SourceGitHub, RepoOwner: "a", RepoName: "repo", Installs: 0, ContentHash: "stub"},
		},
	})

	// skills.sh 源失败
	store.RegisterSkillFetcher(&stubSkillFetcher{
		source: types.SourceSkillsSh,
		err:    errStub,
	})

	got, err := store.SearchAllSkills(context.Background(), types.SearchOptions{PageSize: 30}, nil)
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

	got, err := store.SearchAllSkills(context.Background(), types.SearchOptions{PageSize: 30}, nil)
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
	skills := []types.MarketSkill{
		{ID: "s1", Name: "S1", Directory: "d1", Source: types.SourceSkillsSh, RepoOwner: "a", RepoName: "repo", Installs: 100, ContentHash: "stub"},
		{ID: "s2", Name: "S2", Directory: "d2", Source: types.SourceSkillsSh, RepoOwner: "b", RepoName: "repo", Installs: 90, ContentHash: "stub"},
		{ID: "s3", Name: "S3", Directory: "d3", Source: types.SourceSkillsSh, RepoOwner: "c", RepoName: "repo", Installs: 80, ContentHash: "stub"},
		{ID: "s4", Name: "S4", Directory: "d4", Source: types.SourceSkillsSh, RepoOwner: "d", RepoName: "repo", Installs: 70, ContentHash: "stub"},
		{ID: "s5", Name: "S5", Directory: "d5", Source: types.SourceSkillsSh, RepoOwner: "e", RepoName: "repo", Installs: 60, ContentHash: "stub"},
	}
	store.RegisterSkillFetcher(&stubSkillFetcher{
		source: types.SourceSkillsSh,
		skills: skills,
	})

	// pageSize=3，应只返回前 3 个
	got, err := store.SearchAllSkills(context.Background(), types.SearchOptions{PageSize: 3}, nil)
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
	items := []types.MarketSkill{
		{ID: "1", Directory: "alpha", RepoOwner: "a", RepoName: "repo", Source: types.SourceGitHub},
		{ID: "2", Directory: "beta", RepoOwner: "b", RepoName: "repo", Source: types.SourceGitHub},
	}
	got := dedupSkills(items)
	if len(got) != 2 {
		t.Errorf("expected 2 items (no dups), got %d", len(got))
	}
}

func TestDedupSkills_KeepFirstWhenSameSource(t *testing.T) {
	items := []types.MarketSkill{
		{ID: "1", Name: "First", Directory: "alpha", RepoOwner: "a", RepoName: "repo", Source: types.SourceGitHub, Installs: 10},
		{ID: "2", Name: "Second", Directory: "alpha", RepoOwner: "a", RepoName: "repo", Source: types.SourceGitHub, Installs: 20},
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
	items := []types.MarketSkill{
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

// TestDiagnostic_OldCacheKeys 计算各 source 的缓存 key，帮助定位哪个缓存文件属于哪个 source
func TestDiagnostic_OldCacheKeys(t *testing.T) {
	// 实际调用时 Page 未设置，默认为 0
	opts := types.SearchOptions{Query: "", Page: 0, PageSize: 30}

	sources := []struct {
		kind   string
		source types.Source
	}{
		{"search-skills", types.SourceGitHub},
		{"search-skills", types.SourceSkillsSh},
		{"search", types.SourceOfficial},
	}

	t.Log("=== NEW cache keys (v2, Page=0) ===")
	for _, s := range sources {
		payload := fmt.Sprintf("v%d|%s|%s|%s|%s|%d|%d",
			cacheVersion, s.kind, s.source, opts.Query, opts.Cursor, opts.Page, opts.PageSize)
		sum := sha256.Sum256([]byte(payload))
		key := hex.EncodeToString(sum[:16]) + ".json"
		t.Logf("source=%-12s kind=%-13s payload=%q key=%s", s.source, s.kind, payload, key)
	}

	// 实际运行中的缓存文件
	t.Log("=== Actual cache files in dir ===")
	t.Log("1b7c5d1508d121c057dc9088e4bc86dd.json (10923 bytes) - official MCP")
	t.Log("b8348d9cb1e5a1571d791ef24befd398.json (47 bytes) - empty skills result")
}
