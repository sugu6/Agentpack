package market

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func newSmitheryHandler() http.Handler {
	mux := http.NewServeMux()

	// GET /servers — 搜索端点
	mux.HandleFunc("/servers", func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		allServers := []smitheryServer{
		{
			ID:            "srv-1",
			QualifiedName: "googledrive",
			Namespace:     "gmail",
			DisplayName:   "Google Drive",
			Description:   "Upload, organize, and share files in the cloud.",
			Homepage:      "https://smithery.ai/servers/googledrive",
			UseCount:      json.Number("15682"),
			IsDeployed:    true,
			Remote:        true,
			Verified:      true,
			BySmithery:    true,
			Owner:         "org_01KPBXJTDN7ASH7MJ3958C05QY",
			CreatedAt:     "2025-11-19T07:26:50.147Z",
		},
		{
			ID:            "srv-2",
			QualifiedName: "dropbox",
			Namespace:     "dropbox",
			DisplayName:   "Dropbox",
			Description:   "Store, sync, and share files across devices.",
			Homepage:      "https://smithery.ai/servers/dropbox",
			UseCount:      json.Number("259"),
			IsDeployed:    true,
			Remote:        true,
			Verified:      false,
			BySmithery:    false,
			Owner:         "org_01KNB3A514RX0G4KHMMZAXTRA2",
			CreatedAt:     "2025-11-19T12:48:17.645Z",
		},
	}

		filtered := allServers
		if s := q.Get("q"); s != "" {
			filtered = filterSmithery(allServers, s)
		}

		pageSize := 30
		if ps := q.Get("pageSize"); ps != "" {
			if n, err := parseTestInt(ps); err == nil {
				pageSize = n
			}
		}

		_ = json.NewEncoder(w).Encode(smitherySearchResponse{
			Servers: filtered,
			Pagination: smitheryPagination{
				CurrentPage: 1,
				PageSize:    pageSize,
				TotalPages:  58,
				TotalCount:  116,
			},
		})
	})

	// GET /servers/{qualifiedName} — 详情端点（HTTP 型）
	mux.HandleFunc("/servers/googledrive", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(smitheryDetailResponse{
			QualifiedName: "googledrive",
			DisplayName:   "Google Drive",
			Description:   "Upload, organize, and share files in the cloud.",
			DeploymentURL: "https://googledrive.run.tools",
			Remote:        true,
			Connections: []smitheryConnection{
				{
					Type:          "http",
					DeploymentURL: "https://googledrive.run.tools",
					ConfigSchema:  json.RawMessage(`{}`),
				},
			},
		})
	})

	// GET /servers/filesystem — 详情端点（stdio 型）
	mux.HandleFunc("/servers/filesystem", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(smitheryDetailResponse{
			QualifiedName: "filesystem",
			DisplayName:   "Filesystem",
			Description:   "File system access",
			Remote:        false,
			Connections: []smitheryConnection{
				{
					Type: "stdio",
					ConfigSchema: json.RawMessage(`{
						"properties": {
							"API_KEY": {"title": "API Key", "description": "Your API key"},
							"REGION": {"title": "Region", "description": "AWS region"}
						}
					}`),
				},
			},
		})
	})

	// GET /servers/notfound — 返回 404
	mux.HandleFunc("/servers/notfound", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`{"error":"not found"}`))
	})

	return mux
}

func filterSmithery(servers []smitheryServer, query string) []smitheryServer {
	var result []smitheryServer
	for _, s := range servers {
		if strings.Contains(strings.ToLower(s.QualifiedName), strings.ToLower(query)) ||
			strings.Contains(strings.ToLower(s.DisplayName), strings.ToLower(query)) ||
			strings.Contains(strings.ToLower(s.Description), strings.ToLower(query)) {
			result = append(result, s)
		}
	}
	return result
}

func parseTestInt(s string) (int, error) {
	var n int
	for _, c := range s {
		if c < '0' || c > '9' {
			return 0, errInvalidInt
		}
		n = n*10 + int(c-'0')
	}
	return n, nil
}

var errInvalidInt = &parseError{msg: "invalid int"}

type parseError struct{ msg string }

func (e *parseError) Error() string { return e.msg }

func setSmitheryBase(u string) {
	smitheryBaseURL = u
}

func TestSmitheryFetcher_Search(t *testing.T) {
	server := httptest.NewServer(newSmitheryHandler())
	defer server.Close()
	setSmitheryBase(server.URL)
	defer setSmitheryBase("https://registry.smithery.ai")

	f := NewSmitheryFetcher()
	result, err := f.Search(context.Background(), SearchOptions{Query: "", Page: 1, PageSize: 30})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(result.Items) != 2 {
		t.Fatalf("expected 2 items, got %d", len(result.Items))
	}

	// 验证字段映射
	first := result.Items[0]
	if first.Name != "googledrive" {
		t.Errorf("expected Name=googledrive, got %s", first.Name)
	}
	if first.Title != "Google Drive" {
		t.Errorf("expected Title=Google Drive, got %s", first.Title)
	}
	if first.Installs != 15682 {
		t.Errorf("expected Installs=15682, got %d", first.Installs)
	}
	if first.Source != SourceSmithery {
		t.Errorf("expected Source=%s, got %s", SourceSmithery, first.Source)
	}
	if first.SourceID != "googledrive" {
		t.Errorf("expected SourceID=googledrive, got %s", first.SourceID)
	}
	if first.Homepage != "https://smithery.ai/servers/googledrive" {
		t.Errorf("unexpected Homepage: %s", first.Homepage)
	}
	// 验证 Smithery 特有字段（用于前端筛选）
	if !first.BySmithery {
		t.Errorf("expected BySmithery=true, got %v", first.BySmithery)
	}
	if !first.IsDeployed {
		t.Errorf("expected IsDeployed=true, got %v", first.IsDeployed)
	}
	if !first.IsVerified {
		t.Errorf("expected IsVerified=true, got %v", first.IsVerified)
	}
	if !first.IsRemote {
		t.Errorf("expected IsRemote=true, got %v", first.IsRemote)
	}

	// 验证分页
	if result.Total != 116 {
		t.Errorf("expected Total=116, got %d", result.Total)
	}
	if !result.HasMore {
		t.Error("expected HasMore=true")
	}
	if result.NextPage != "2" {
		t.Errorf("expected NextPage=2, got %s", result.NextPage)
	}
}

func TestSmitheryFetcher_SearchWithQuery(t *testing.T) {
	server := httptest.NewServer(newSmitheryHandler())
	defer server.Close()
	setSmitheryBase(server.URL)
	defer setSmitheryBase("https://registry.smithery.ai")

	f := NewSmitheryFetcher()
	result, err := f.Search(context.Background(), SearchOptions{Query: "drive", Page: 1, PageSize: 30})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(result.Items) != 1 {
		t.Fatalf("expected 1 item, got %d", len(result.Items))
	}
	if result.Items[0].Name != "googledrive" {
		t.Errorf("expected googledrive, got %s", result.Items[0].Name)
	}
}

func TestSmitheryFetcher_SearchServerError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer server.Close()
	setSmitheryBase(server.URL)
	defer setSmitheryBase("https://registry.smithery.ai")

	f := NewSmitheryFetcher()
	_, err := f.Search(context.Background(), SearchOptions{Page: 1, PageSize: 30})
	if err == nil {
		t.Fatal("expected error on 500 status")
	}
	if !strings.Contains(err.Error(), "smithery fetch:") {
		t.Errorf("unexpected error message: %v", err)
	}
}

func TestSmitheryFetcher_Get_HTTPType(t *testing.T) {
	server := httptest.NewServer(newSmitheryHandler())
	defer server.Close()
	setSmitheryBase(server.URL)
	defer setSmitheryBase("https://registry.smithery.ai")

	f := NewSmitheryFetcher()
	srv, err := f.Get(context.Background(), "googledrive")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if srv.Transport != "http" {
		t.Errorf("expected Transport=http, got %s", srv.Transport)
	}
	if srv.URL != "https://googledrive.run.tools" {
		t.Errorf("expected URL=https://googledrive.run.tools, got %s", srv.URL)
	}
	// http 型不应填充 command/args
	if srv.Command != "" {
		t.Errorf("expected empty Command for http type, got %s", srv.Command)
	}
}

func TestSmitheryFetcher_Get_StdioType(t *testing.T) {
	server := httptest.NewServer(newSmitheryHandler())
	defer server.Close()
	setSmitheryBase(server.URL)
	defer setSmitheryBase("https://registry.smithery.ai")

	f := NewSmitheryFetcher()
	srv, err := f.Get(context.Background(), "filesystem")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if srv.Transport != "stdio" {
		t.Errorf("expected Transport=stdio, got %s", srv.Transport)
	}
	if srv.Command != "npx" {
		t.Errorf("expected Command=npx, got %s", srv.Command)
	}
	expectedArgs := []string{"-y", "smithery@latest", "run", "filesystem"}
	if len(srv.Args) != len(expectedArgs) {
		t.Fatalf("expected %d args, got %d: %v", len(expectedArgs), len(srv.Args), srv.Args)
	}
	for i, want := range expectedArgs {
		if srv.Args[i] != want {
			t.Errorf("expected Args[%d]=%s, got %s", i, want, srv.Args[i])
		}
	}
	// 验证从 configSchema 提取的环境变量
	if srv.Env == nil {
		t.Fatal("expected non-nil Env")
	}
	if len(srv.Env) != 2 {
		t.Errorf("expected 2 env keys, got %d: %v", len(srv.Env), srv.Env)
	}
	if _, ok := srv.Env["API_KEY"]; !ok {
		t.Error("expected API_KEY in Env")
	}
	if _, ok := srv.Env["REGION"]; !ok {
		t.Error("expected REGION in Env")
	}
}

func TestSmitheryFetcher_Get_NotFound(t *testing.T) {
	server := httptest.NewServer(newSmitheryHandler())
	defer server.Close()
	setSmitheryBase(server.URL)
	defer setSmitheryBase("https://registry.smithery.ai")

	f := NewSmitheryFetcher()
	_, err := f.Get(context.Background(), "notfound")
	if err == nil {
		t.Fatal("expected error for not found")
	}
	if !strings.Contains(err.Error(), "not found") {
		t.Errorf("unexpected error message: %v", err)
	}
}

func TestSmitheryFetcher_SearchPageSizeClamping(t *testing.T) {
	server := httptest.NewServer(newSmitheryHandler())
	defer server.Close()
	setSmitheryBase(server.URL)
	defer setSmitheryBase("https://registry.smithery.ai")

	f := NewSmitheryFetcher()
	// PageSize > 100 应被截断为 100
	result, err := f.Search(context.Background(), SearchOptions{PageSize: 500})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result == nil {
		t.Fatal("expected non-nil result")
	}
}

func TestParseSmitheryUseCount(t *testing.T) {
	tests := []struct {
		input json.Number
		want  int
	}{
		{json.Number("15682"), 15682},
		{json.Number("0"), 0},
		{json.Number(""), 0},
		{json.Number("invalid"), 0},
	}
	for _, tt := range tests {
		got := parseSmitheryUseCount(tt.input)
		if got != tt.want {
			t.Errorf("parseSmitheryUseCount(%s) = %d, want %d", tt.input, got, tt.want)
		}
	}
}

func TestExtractSmitheryEnvKeys(t *testing.T) {
	// 空输入
	if env := extractSmitheryEnvKeys(nil); env != nil {
		t.Errorf("expected nil for empty input, got %v", env)
	}

	// 无 properties
	if env := extractSmitheryEnvKeys(json.RawMessage(`{}`)); env != nil {
		t.Errorf("expected nil for empty schema, got %v", env)
	}

	// 有 properties
	schema := json.RawMessage(`{
		"properties": {
			"API_KEY": {"title": "API Key"},
			"SECRET": {"title": "Secret"}
		}
	}`)
	env := extractSmitheryEnvKeys(schema)
	if env == nil {
		t.Fatal("expected non-nil env")
	}
	if len(env) != 2 {
		t.Fatalf("expected 2 keys, got %d", len(env))
	}
	if env["API_KEY"] != "" || env["SECRET"] != "" {
		t.Error("expected empty values for env keys")
	}

	// 无效 JSON
	if env := extractSmitheryEnvKeys(json.RawMessage(`invalid`)); env != nil {
		t.Errorf("expected nil for invalid JSON, got %v", env)
	}
}
