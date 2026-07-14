package market

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

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

	// 覆盖 base URLs
	origGHAPI := githubAPIBase
	githubAPIBase = server.URL
	defer func() { githubAPIBase = origGHAPI }()

	origRawBase := githubRawBase
	githubRawBase = server.URL
	defer func() { githubRawBase = origRawBase }()

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

	origGHAPI := githubAPIBase
	githubAPIBase = server.URL
	defer func() { githubAPIBase = origGHAPI }()
	origRawBase := githubRawBase
	githubRawBase = server.URL
	defer func() { githubRawBase = origRawBase }()

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

	origGHAPI := githubAPIBase
	githubAPIBase = server.URL
	defer func() { githubAPIBase = origGHAPI }()
	origRawBase := githubRawBase
	githubRawBase = server.URL
	defer func() { githubRawBase = origRawBase }()

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

func TestGitHubSkillFetcher_RepoNotFound(t *testing.T) {
	server := httptest.NewServer(newGitHubMockHandler(t, nil))
	defer server.Close()

	origGHAPI := githubAPIBase
	githubAPIBase = server.URL
	defer func() { githubAPIBase = origGHAPI }()

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
