package market

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// setMockBases 将所有可覆盖的 base URL 指向测试服务器。
// 必须覆盖 jsDelivrDataBase：scanRepo 优先走 jsDelivr Data API，
// 未覆盖时会向真实 data.jsdelivr.com 发请求（依赖网络且结果不确定）。
// mock 的 catch-all 对 /v1/packages/... 返回 404，驱动 jsDelivr 失败后
// 回退到 GitHub Trees API 路径。
func setMockBases(t *testing.T, server *httptest.Server) {
	t.Helper()
	origGHAPI := githubAPIBase
	githubAPIBase = server.URL
	t.Cleanup(func() { githubAPIBase = origGHAPI })
	origRawBase := githubRawBase
	githubRawBase = server.URL
	t.Cleanup(func() { githubRawBase = origRawBase })
	origDataBase := jsDelivrDataBase
	jsDelivrDataBase = server.URL
	t.Cleanup(func() { jsDelivrDataBase = origDataBase })
}

// newGitHubMockHandler 创建模拟 GitHub API 和 raw 服务的 handler
func newGitHubMockHandler(t *testing.T, trees map[string]githubTreeResponse) http.Handler {
	mux := http.NewServeMux()

	// GitHub Trees API: /repos/{owner}/{name}/git/trees/{branch}
	for path, treeResp := range trees {
		path := path
		treeResp := treeResp
		mux.HandleFunc(path, func(w http.ResponseWriter, r *http.Request) {
			_ = json.NewEncoder(w).Encode(treeResp)
		})
	}

	// 404 handler for specific paths
	mux.HandleFunc("/repos/testowner/notfound/git/trees/main", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(404)
	})

	// Raw content: catch-all，匹配所有未注册的路径
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		path := r.URL.Path
		// 只处理 SKILL.md 请求
		if !strings.HasSuffix(path, "/SKILL.md") {
			w.WriteHeader(404)
			return
		}
		// 根据路径返回不同的内容
		if strings.Contains(path, "skill-with-name") {
			_, _ = w.Write([]byte("---\nname: My Named Skill\ndescription: A skill with a name\n---\n# content\n"))
			return
		}
		if strings.Contains(path, "empty-meta") {
			_, _ = w.Write([]byte("---\n---\n# empty meta\n"))
			return
		}
		// 默认返回带 name 的 frontmatter
		_, _ = w.Write([]byte("---\nname: Default\n---\nbody"))
	})

	return mux
}

func TestGitHubSkillFetcher_SearchEmptyRepos(t *testing.T) {
	f := NewGitHubSkillFetcher(func() []RepoRef { return nil })
	got, err := f.Search(context.Background(), SearchOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Items) != 0 {
		t.Errorf("expected 0 items for empty repos, got %d", len(got.Items))
	}
}

func TestGitHubSkillFetcher_SearchWithRepos(t *testing.T) {
	treeResp := githubTreeResponse{
		Tree: []githubTreeItem{
			{Path: "skills/skill-with-name/SKILL.md", Type: "blob"},
			{Path: "skills/skill-with-name/scripts/run.sh", Type: "blob"},
			{Path: "skills/empty-meta/SKILL.md", Type: "blob"},
			{Path: "README.md", Type: "blob"},
			{Path: "scripts", Type: "tree"},
		},
	}

	server := httptest.NewServer(newGitHubMockHandler(t, map[string]githubTreeResponse{
		"/repos/testowner/testrepo/git/trees/main": treeResp,
	}))
	defer server.Close()

	setMockBases(t, server)

	f := NewGitHubSkillFetcher(func() []RepoRef {
		return []RepoRef{{Owner: "testowner", Name: "testrepo", Branch: "main"}}
	})

	got, err := f.Search(context.Background(), SearchOptions{Query: ""})
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Items) != 2 {
		t.Fatalf("expected 2 skills, got %d: %+v", len(got.Items), got.Items)
	}

	// 验证 skill-with-name 的 frontmatter 解析
	var named *MarketSkill
	for i := range got.Items {
		if got.Items[i].Directory == "skill-with-name" {
			named = &got.Items[i]
			break
		}
	}
	if named == nil {
		t.Fatal("expected to find skill-with-name")
	}
	if named.Name != "My Named Skill" {
		t.Errorf("expected name 'My Named Skill', got %q", named.Name)
	}
	if named.Description != "A skill with a name" {
		t.Errorf("expected description 'A skill with a name', got %q", named.Description)
	}
	if named.RepoOwner != "testowner" {
		t.Errorf("expected repoOwner 'testowner', got %q", named.RepoOwner)
	}
	if named.RepoName != "testrepo" {
		t.Errorf("expected repoName 'testrepo', got %q", named.RepoName)
	}
	if named.RepoBranch != "main" {
		t.Errorf("expected branch 'main', got %q", named.RepoBranch)
	}
	if named.Source != SourceGitHub {
		t.Errorf("expected source github, got %q", named.Source)
	}
	if named.Installs != 0 {
		t.Errorf("expected installs 0, got %d", named.Installs)
	}
	if named.ReadmeURL != "https://github.com/testowner/testrepo" {
		t.Errorf("expected readme URL, got %q", named.ReadmeURL)
	}
}

// TestGitHubSkillFetcher_NestedSkillDirs 验证嵌套路径 (如 skills/pdf/SKILL.md) 能正确扫描
// 这是 anthropics/skills 仓库的实际结构：17 个 skills 嵌套在 skills/ 子目录下
// 之前的 bug：只取最后一段 "pdf" 拼接 raw URL，导致 .../main/pdf/SKILL.md 404
// 修复后：使用完整相对路径 "skills/pdf" 拼接，得到 .../main/skills/pdf/SKILL.md 200
func TestGitHubSkillFetcher_NestedSkillDirs(t *testing.T) {
	treeResp := githubTreeResponse{
		Tree: []githubTreeItem{
			// 嵌套路径：skills/pdf/SKILL.md
			{Path: "skills/pdf/SKILL.md", Type: "blob"},
			{Path: "skills/pdf/scripts/render.py", Type: "blob"},
			// 嵌套路径：skills/xlsx/SKILL.md
			{Path: "skills/xlsx/SKILL.md", Type: "blob"},
			// 嵌套路径：skills/template/SKILL.md
			{Path: "skills/template/SKILL.md", Type: "blob"},
			// 非技能文件
			{Path: "README.md", Type: "blob"},
		},
	}

	server := httptest.NewServer(newGitHubMockHandler(t, map[string]githubTreeResponse{
		"/repos/anthropics/skills/git/trees/main": treeResp,
	}))
	defer server.Close()

	setMockBases(t, server)

	f := NewGitHubSkillFetcher(func() []RepoRef {
		return []RepoRef{{Owner: "anthropics", Name: "skills", Branch: "main"}}
	})

	got, err := f.Search(context.Background(), SearchOptions{Query: ""})
	if err != nil {
		t.Fatal(err)
	}
	// 应该扫到 3 个 skills（pdf, xlsx, template）
	if len(got.Items) != 3 {
		t.Fatalf("expected 3 skills (pdf, xlsx, template), got %d: %+v", len(got.Items), got.Items)
	}

	// 验证 directory 是最后一段（不是完整路径）
	dirs := make(map[string]bool, len(got.Items))
	for _, s := range got.Items {
		dirs[s.Directory] = true
	}
	if !dirs["pdf"] {
		t.Errorf("expected directory 'pdf', got: %v", dirs)
	}
	if !dirs["xlsx"] {
		t.Errorf("expected directory 'xlsx', got: %v", dirs)
	}
	if !dirs["template"] {
		t.Errorf("expected directory 'template', got: %v", dirs)
	}
}

func TestGitHubSkillFetcher_SearchWithQuery(t *testing.T) {
	treeResp := githubTreeResponse{
		Tree: []githubTreeItem{
			{Path: "skills/alpha/SKILL.md", Type: "blob"},
			{Path: "skills/beta/SKILL.md", Type: "blob"},
		},
	}

	server := httptest.NewServer(newGitHubMockHandler(t, map[string]githubTreeResponse{
		"/repos/testowner/testrepo/git/trees/main": treeResp,
	}))
	defer server.Close()

	setMockBases(t, server)

	f := NewGitHubSkillFetcher(func() []RepoRef {
		return []RepoRef{{Owner: "testowner", Name: "testrepo", Branch: "main"}}
	})

	got, err := f.Search(context.Background(), SearchOptions{Query: "alpha"})
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Items) != 1 {
		t.Fatalf("expected 1 item for query 'alpha', got %d", len(got.Items))
	}
	if got.Items[0].Directory != "alpha" {
		t.Errorf("expected directory 'alpha', got %q", got.Items[0].Directory)
	}
}

// TestGitHubSkillFetcher_CDNFailureFallback 验证当 jsDelivr CDN 拉取 SKILL.md 失败时
// （404/5xx/网络错误），skill 不会被跳过，而是以降级形式（Name=directory，Description=""）出现。
// 这是 v11 cacheVersion 修复的核心：避免 CDN 限流导致 skills 数量大幅减少。
func TestGitHubSkillFetcher_CDNFailureFallback(t *testing.T) {
	treeResp := githubTreeResponse{
		Tree: []githubTreeItem{
			// ok skill：SKILL.md 正常返回
			{Path: "skills/ok-skill/SKILL.md", Type: "blob"},
			// 404 skill：CDN 返回 404
			{Path: "skills/missing/SKILL.md", Type: "blob"},
			// 500 skill：CDN 返回 500
			{Path: "skills/server-error/SKILL.md", Type: "blob"},
		},
	}

	mux := http.NewServeMux()
	// Trees API
	mux.HandleFunc("/repos/testowner/testrepo/git/trees/main", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(treeResp)
	})
	// SKILL.md raw content：模拟 CDN 故障
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		path := r.URL.Path
		if !strings.HasSuffix(path, "/SKILL.md") {
			w.WriteHeader(404)
			return
		}
		switch {
		case strings.Contains(path, "/ok-skill/"):
			_, _ = w.Write([]byte("---\nname: OK Skill\ndescription: works fine\n---\nbody"))
		case strings.Contains(path, "/missing/"):
			w.WriteHeader(404)
		case strings.Contains(path, "/server-error/"):
			w.WriteHeader(http.StatusNotFound)
		default:
			w.WriteHeader(404)
		}
	})
	server := httptest.NewServer(mux)
	defer server.Close()

	setMockBases(t, server)

	f := NewGitHubSkillFetcher(func() []RepoRef {
		return []RepoRef{{Owner: "testowner", Name: "testrepo", Branch: "main"}}
	})

	got, err := f.Search(context.Background(), SearchOptions{Query: ""})
	if err != nil {
		t.Fatal(err)
	}

	// 关键断言：3 个 skill 都必须出现，不能因为 CDN 失败而跳过
	if len(got.Items) != 3 {
		t.Fatalf("expected 3 skills (CDN 失败应降级而非跳过), got %d: %+v", len(got.Items), got.Items)
	}

	byDir := make(map[string]MarketSkill, len(got.Items))
	for _, s := range got.Items {
		byDir[s.Directory] = s
	}

	// ok-skill：应使用 SKILL.md 中的 name 和 description
	ok, exists := byDir["ok-skill"]
	if !exists {
		t.Fatal("expected ok-skill to be present")
	}
	if ok.Name != "OK Skill" {
		t.Errorf("ok-skill name = %q, want %q", ok.Name, "OK Skill")
	}
	if ok.Description != "works fine" {
		t.Errorf("ok-skill description = %q, want %q", ok.Description, "works fine")
	}

	// missing (CDN 404)：应降级为 Name=directory，Description=""
	missing, exists := byDir["missing"]
	if !exists {
		t.Fatal("expected missing skill to still appear (fallback), but it was skipped")
	}
	if missing.Name != "missing" {
		t.Errorf("missing skill fallback name = %q, want %q", missing.Name, "missing")
	}
	if missing.Description != "" {
		t.Errorf("missing skill fallback description = %q, want empty", missing.Description)
	}
	// 关键字段必须正确，安装仍可正常工作
	if missing.Directory != "missing" || missing.FullPath != "skills/missing" {
		t.Errorf("missing skill install fields wrong: directory=%q fullPath=%q", missing.Directory, missing.FullPath)
	}
	if missing.RepoOwner != "testowner" || missing.RepoName != "testrepo" {
		t.Errorf("missing skill repo fields wrong: owner=%q name=%q", missing.RepoOwner, missing.RepoName)
	}

	// server-error (CDN 500)：同样应降级
	se, exists := byDir["server-error"]
	if !exists {
		t.Fatal("expected server-error skill to still appear (fallback), but it was skipped")
	}
	if se.Name != "server-error" {
		t.Errorf("server-error skill fallback name = %q, want %q", se.Name, "server-error")
	}
	if se.Description != "" {
		t.Errorf("server-error skill fallback description = %q, want empty", se.Description)
	}
}

func TestGitHubSkillFetcher_RepoNotFound(t *testing.T) {
	server := httptest.NewServer(newGitHubMockHandler(t, nil))
	defer server.Close()

	setMockBases(t, server)

	f := NewGitHubSkillFetcher(func() []RepoRef {
		return []RepoRef{{Owner: "testowner", Name: "notfound", Branch: "main"}}
	})

	got, err := f.Search(context.Background(), SearchOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Items) != 0 {
		t.Errorf("expected 0 items for failed repo, got %d", len(got.Items))
	}
}

// newJSDelivrMockHandler 创建模拟 jsDelivr Data API 与 CDN raw 服务的 handler
func newJSDelivrMockHandler(t *testing.T, treeResp jsDelivrTreeResponse) http.Handler {
	mux := http.NewServeMux()
	// Data API: /v1/packages/gh/{owner}/{repo}@{branch}?structure=flat
	mux.HandleFunc("/v1/packages/gh/", func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "truncated-repo") {
			treeResp.Truncated = true
		}
		_ = json.NewEncoder(w).Encode(treeResp)
	})
	// CDN raw: catch-all，只处理 SKILL.md
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasSuffix(r.URL.Path, "/SKILL.md") {
			w.WriteHeader(404)
			return
		}
		_, _ = w.Write([]byte("---\nname: JSDelivr Skill\n---\nbody"))
	})
	return mux
}

// TestGitHubSkillFetcher_JSDelivrTree 验证 jsDelivr Data API 主链路：
// flat 文件树解析出 skills/ 下的 SKILL.md，不经过 GitHub API。
func TestGitHubSkillFetcher_JSDelivrTree(t *testing.T) {
	treeResp := jsDelivrTreeResponse{
		Name:    "gh/testowner/testrepo",
		Version: "main",
		Files: []struct {
			Name string `json:"name"`
			Size int64  `json:"size"`
		}{
			{Name: "/skills/alpha/SKILL.md"},
			{Name: "/skills/beta/SKILL.md"},
			{Name: "/README.md"},
		},
	}

	server := httptest.NewServer(newJSDelivrMockHandler(t, treeResp))
	defer server.Close()

	setMockBases(t, server)

	f := NewGitHubSkillFetcher(func() []RepoRef {
		return []RepoRef{{Owner: "testowner", Name: "testrepo", Branch: "main"}}
	})

	got, err := f.Search(context.Background(), SearchOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Items) != 2 {
		t.Fatalf("expected 2 skills via jsdelivr tree, got %d: %+v", len(got.Items), got.Items)
	}
	byDir := map[string]MarketSkill{}
	for _, s := range got.Items {
		byDir[s.Directory] = s
	}
	alpha, ok := byDir["alpha"]
	if !ok {
		t.Fatal("expected skill alpha")
	}
	if alpha.FullPath != "skills/alpha" {
		t.Errorf("alpha fullPath = %q, want %q", alpha.FullPath, "skills/alpha")
	}
	if alpha.Name != "JSDelivr Skill" {
		t.Errorf("alpha name = %q, want frontmatter name", alpha.Name)
	}
	if alpha.RepoBranch != "main" {
		t.Errorf("alpha repoBranch = %q, want %q", alpha.RepoBranch, "main")
	}
}

// TestGitHubSkillFetcher_JSDelivrDefaultBranchAlias 验证未配置分支时使用
// jsDelivr @master 别名扫描（自动解析为仓库默认分支）；别名不落库——
// 真实默认分支解析失败（GitHub API 不可达）时保持空串，由安装侧分支
// 枚举（main 优先）决定，避免把别名误当真实分支（双分支仓库安装错内容）。
func TestGitHubSkillFetcher_JSDelivrDefaultBranchAlias(t *testing.T) {
	var requestedPath string
	treeResp := jsDelivrTreeResponse{
		Name:    "gh/testowner/testrepo",
		Version: "master",
		Files: []struct {
			Name string `json:"name"`
			Size int64  `json:"size"`
		}{
			{Name: "/skills/alpha/SKILL.md"},
		},
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/v1/packages/gh/", func(w http.ResponseWriter, r *http.Request) {
		requestedPath = r.URL.Path
		_ = json.NewEncoder(w).Encode(treeResp)
	})
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasSuffix(r.URL.Path, "/SKILL.md") {
			w.WriteHeader(404)
			return
		}
		_, _ = w.Write([]byte("---\nname: Alias Skill\n---\nbody"))
	})
	server := httptest.NewServer(mux)
	defer server.Close()

	setMockBases(t, server)

	f := NewGitHubSkillFetcher(func() []RepoRef {
		return []RepoRef{{Owner: "testowner", Name: "testrepo"}} // 未配置分支
	})

	got, err := f.Search(context.Background(), SearchOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Items) != 1 {
		t.Fatalf("expected 1 skill via jsdelivr default branch alias, got %d", len(got.Items))
	}
	if got.Items[0].RepoBranch != "" {
		t.Errorf("repoBranch = %q, want empty (alias must not be persisted; install enumeration decides)", got.Items[0].RepoBranch)
	}
	if !strings.HasSuffix(requestedPath, "@master") {
		t.Errorf("expected data API request with @master alias, got path %q", requestedPath)
	}
}

// TestGitHubSkillFetcher_JSDelivrTruncated 验证超大仓库（>3000 文件，
// truncated=true）被跳过，而不是静默返回不完整列表。
func TestGitHubSkillFetcher_JSDelivrTruncated(t *testing.T) {
	treeResp := jsDelivrTreeResponse{
		Name:      "gh/testowner/truncated-repo",
		Version:   "main",
		Truncated: true,
	}
	server := httptest.NewServer(newJSDelivrMockHandler(t, treeResp))
	defer server.Close()

	setMockBases(t, server)

	f := NewGitHubSkillFetcher(func() []RepoRef {
		return []RepoRef{{Owner: "testowner", Name: "truncated-repo", Branch: "main"}}
	})

	got, err := f.Search(context.Background(), SearchOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Items) != 0 {
		t.Errorf("expected truncated repo to be skipped, got %d items", len(got.Items))
	}
}

func TestParseSkillDirs(t *testing.T) {
	dirs := parseSkillDirs([]string{
		"skills/pdf/SKILL.md",
		"skills/pdf/scripts/render.py",
		"skills/xlsx/SKILL.md",
		"SKILL.md",               // 根目录，排除
		"template/SKILL.md",      // 非 skills/ 前缀，排除
		"skills/nested/SKILL.md", // 正常
	})
	if len(dirs) != 3 {
		t.Fatalf("expected 3 skill dirs, got %d: %+v", len(dirs), dirs)
	}
	if d, ok := dirs["pdf"]; !ok || d.fullPath != "skills/pdf" {
		t.Errorf("pdf dir wrong: %+v", dirs["pdf"])
	}
	if d, ok := dirs["nested"]; !ok || d.fullPath != "skills/nested" {
		t.Errorf("nested dir wrong: %+v", dirs["nested"])
	}
}

func TestValidateRepoRef(t *testing.T) {
	tests := []struct {
		name    string
		repo    RepoRef
		wantErr bool
	}{
		{"valid", RepoRef{Owner: "anthropics", Name: "skills", Branch: "main"}, false},
		{"empty owner", RepoRef{Owner: "", Name: "skills", Branch: "main"}, true},
		{"empty name", RepoRef{Owner: "anthropics", Name: "", Branch: "main"}, true},
		{"owner with slash", RepoRef{Owner: "a/b", Name: "skills", Branch: "main"}, true},
		{"owner with semicolon", RepoRef{Owner: "a;b", Name: "skills", Branch: "main"}, true},
		{"branch with space", RepoRef{Owner: "a", Name: "skills", Branch: "main feat"}, true},
		{"branch with valid chars", RepoRef{Owner: "a", Name: "skills", Branch: "feature/branch-1"}, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := validateRepoRef(tt.repo)
			if (err != nil) != tt.wantErr {
				t.Errorf("validateRepoRef() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

func TestParseSkillFrontmatter(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		wantName string
		wantDesc string
	}{
		{
			name:     "simple",
			input:    "---\nname: My Skill\ndescription: A skill\n---\nbody",
			wantName: "My Skill",
			wantDesc: "A skill",
		},
		{
			name:     "quoted values",
			input:    "---\nname: \"Quoted Name\"\ndescription: 'Single quoted'\n---\n",
			wantName: "Quoted Name",
			wantDesc: "Single quoted",
		},
		{
			name:     "no frontmatter",
			input:    "# just markdown\nno frontmatter",
			wantName: "",
			wantDesc: "",
		},
		{
			name:     "empty",
			input:    "",
			wantName: "",
			wantDesc: "",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := parseSkillFrontmatter([]byte(tt.input))
			if got.Name != tt.wantName {
				t.Errorf("name = %q, want %q", got.Name, tt.wantName)
			}
			if got.Description != tt.wantDesc {
				t.Errorf("description = %q, want %q", got.Description, tt.wantDesc)
			}
		})
	}
}
