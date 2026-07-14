package market

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

// newAPIMockServer 创建模拟 skills.sh /api/search 服务器
func newAPIMockServer(t *testing.T) *httptest.Server {
	mux := http.NewServeMux()

	mux.HandleFunc("/api/search", func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query().Get("q")
		limit := r.URL.Query().Get("limit")
		offset := r.URL.Query().Get("offset")

		// 模拟非空查询返回匹配结果
		var skills string
		if q == "all" {
			skills = `[
				{"id":"anthropics/skills/pdf","skillId":"pdf","name":"pdf","installs":154526,"source":"anthropics/skills"},
				{"id":"vercel-labs/skills/find-skills","skillId":"find-skills","name":"find-skills","installs":1400000,"source":"vercel-labs/skills"},
				{"id":"microsoft/azure-skills/azure-ai","skillId":"azure-ai","name":"azure-ai","installs":290000,"source":"microsoft/azure-skills"},
				{"id":"mintlify.com/mintlify","skillId":"mintlify","name":"mintlify","installs":50000,"source":"mintlify.com/mintlify"}
			]`
		} else if q == "pdf" {
			skills = `[
				{"id":"anthropics/skills/pdf","skillId":"pdf","name":"pdf","installs":154526,"source":"anthropics/skills"},
				{"id":"openai/skills/pdf","skillId":"pdf","name":"pdf","installs":9644,"source":"openai/skills"}
			]`
		} else {
			skills = `[]`
		}

		w.Header().Set("Content-Type", "application/json")
		// 解析 skills 数量以构造 count
		count := 0
		switch q {
		case "all":
			count = 4
		case "pdf":
			count = 2
		}
		// 简单模拟分页：当 offset > 0 时返回空数组
		if offset != "0" {
			skills = `[]`
			count = 0
		}
		_, _ = w.Write([]byte(`{"query":"` + q + `","searchType":"fuzzy","skills":` + skills + `,"count":` + itoa(count) + `,"duration_ms":50}`))
		// 使用 limit 避免 unused 警告
		_ = limit
	})

	return httptest.NewServer(mux)
}

// itoa 简单整数转字符串（避免引入 strconv 仅用于测试）
func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var buf [16]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	return string(buf[i:])
}

func TestSkillsShFetcher_SearchEmpty(t *testing.T) {
	server := newAPIMockServer(t)
	defer server.Close()

	skillsShAPIBase = server.URL
	defer func() { skillsShAPIBase = "https://skills.sh" }()

	f := NewSkillsShFetcher()
	got, err := f.Search(context.Background(), SearchOptions{Query: "", PageSize: 30})
	if err != nil {
		t.Fatal(err)
	}
	// 空查询应直接返回空，不调用 API
	if len(got.Items) != 0 {
		t.Fatalf("expected 0 items for empty query, got %d", len(got.Items))
	}
}

func TestSkillsShFetcher_SearchWithQuery(t *testing.T) {
	server := newAPIMockServer(t)
	defer server.Close()

	skillsShAPIBase = server.URL
	defer func() { skillsShAPIBase = "https://skills.sh" }()

	f := NewSkillsShFetcher()
	got, err := f.Search(context.Background(), SearchOptions{Query: "pdf", PageSize: 30})
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Items) != 2 {
		t.Fatalf("expected 2 items matching 'pdf', got %d", len(got.Items))
	}
	// 验证第一条是 anthropics/skills/pdf
	first := got.Items[0]
	if first.RepoOwner != "anthropics" || first.RepoName != "skills" {
		t.Errorf("expected anthropics/skills, got %s/%s", first.RepoOwner, first.RepoName)
	}
	if first.Directory != "pdf" {
		t.Errorf("expected directory 'pdf', got %q", first.Directory)
	}
	if first.Installs != 154526 {
		t.Errorf("expected installs=154526, got %d", first.Installs)
	}
	if first.Source != SourceSkillsSh {
		t.Errorf("expected source %q, got %q", SourceSkillsSh, first.Source)
	}
}

func TestSkillsShFetcher_SearchShortQuery(t *testing.T) {
	server := newAPIMockServer(t)
	defer server.Close()

	skillsShAPIBase = server.URL
	defer func() { skillsShAPIBase = "https://skills.sh" }()

	f := NewSkillsShFetcher()
	// 短查询（< 2 字符）应返回空，不调用 API
	got, err := f.Search(context.Background(), SearchOptions{Query: "a", PageSize: 30})
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Items) != 0 {
		t.Errorf("expected 0 items for short query, got %d", len(got.Items))
	}
}

func TestSkillsShFetcher_SearchFiltersNonGitHub(t *testing.T) {
	server := newAPIMockServer(t)
	defer server.Close()

	skillsShAPIBase = server.URL
	defer func() { skillsShAPIBase = "https://skills.sh" }()

	f := NewSkillsShFetcher()
	// 使用非空查询 "all" 触发 API 调用，mock 返回 4 条（含 mintlify.com）
	got, err := f.Search(context.Background(), SearchOptions{Query: "all", PageSize: 30})
	if err != nil {
		t.Fatal(err)
	}
	// 4 个 API 条目中，mintlify.com 被过滤（含 "."），应剩 3 个
	if len(got.Items) != 3 {
		t.Fatalf("expected 3 items (mintlify filtered), got %d", len(got.Items))
	}
	for _, item := range got.Items {
		// 不应包含 mintlify.com（owner 含 "."）
		if item.RepoOwner == "mintlify.com" {
			t.Errorf("non-github source 'mintlify.com' should be filtered out")
		}
	}
}

func TestSkillsShFetcher_SearchServerError(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/api/search", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(500)
	})
	server := httptest.NewServer(mux)
	defer server.Close()

	skillsShAPIBase = server.URL
	defer func() { skillsShAPIBase = "https://skills.sh" }()

	f := NewSkillsShFetcher()
	_, err := f.Search(context.Background(), SearchOptions{Query: "test", PageSize: 30})
	if err == nil {
		t.Fatal("expected error for 500 response, got nil")
	}
}

func TestSkillsShFetcher_SearchPageSizeClamping(t *testing.T) {
	server := newAPIMockServer(t)
	defer server.Close()

	skillsShAPIBase = server.URL
	defer func() { skillsShAPIBase = "https://skills.sh" }()

	f := NewSkillsShFetcher()
	// pageSize=0 应回退到默认值 30；使用非空查询触发 API
	got, err := f.Search(context.Background(), SearchOptions{Query: "all", PageSize: 0})
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Items) != 3 {
		t.Errorf("expected 3 items (mintlify filtered), got %d", len(got.Items))
	}
}

func TestSkillsShFetcher_SearchHasMore(t *testing.T) {
	server := newAPIMockServer(t)
	defer server.Close()

	skillsShAPIBase = server.URL
	defer func() { skillsShAPIBase = "https://skills.sh" }()

	f := NewSkillsShFetcher()
	// count=4，limit=2，offset=0 → HasMore=true（使用非空查询触发 API）
	got, err := f.Search(context.Background(), SearchOptions{Query: "all", PageSize: 2})
	if err != nil {
		t.Fatal(err)
	}
	if !got.HasMore {
		t.Errorf("expected HasMore=true when count(4) > offset(0) + limit(2)")
	}
}
