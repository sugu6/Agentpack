package market

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestRegistryFetcher_Source(t *testing.T) {
	f := NewRegistryFetcher()
	if f.Source() != SourceOfficial {
		t.Fatalf("expected %q, got %q", SourceOfficial, f.Source())
	}
}

func TestRegistryFetcher_Get_NotFound(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		json.NewEncoder(w).Encode(registryListResponse{
			Servers:  []registryServerEntry{},
			Metadata: registryMetadata{Count: 0},
		})
	}))
	defer ts.Close()

	f := &RegistryFetcher{hc: NewHTTPClient(), baseURL: ts.URL}
	_, err := f.Get(context.Background(), "nonexistent/server")
	if err == nil {
		t.Fatal("expected error for not found server")
	}
}

func TestRegistryFetcher_Get_Found(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		json.NewEncoder(w).Encode(registryListResponse{
			Servers: []registryServerEntry{
				{
					Server: registryServer{
						Name:        "io.github.test/test-server",
						Title:       "Test Server",
						Description: "A test server",
						Version:     "1.0.0",
						WebsiteURL:  "https://example.com",
						Packages: []registryPackage{{
							RegistryType: "npm",
							Identifier:   "test-server",
							Version:      "1.0.0",
							RuntimeHint:  "npx",
							Transport:    registryTransport{Type: "stdio"},
							RuntimeArguments: []registryArgument{
								{Value: "-y", Type: "positional"},
							},
						}},
					},
					Meta: registryMetaWrapper{
						Official: registryMeta{
							Status:    "active",
							IsLatest:  true,
							UpdatedAt: time.Now().UTC().Format(time.RFC3339),
						},
					},
				},
			},
			Metadata: registryMetadata{Count: 1},
		})
	}))
	defer ts.Close()

	f := &RegistryFetcher{hc: NewHTTPClient(), baseURL: ts.URL}
	server, err := f.Get(context.Background(), "io.github.test/test-server")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if server.Name != "Test Server" {
		t.Fatalf("expected name 'Test Server', got %q", server.Name)
	}
	if server.ID != "registry:io.github.test/test-server" {
		t.Fatalf("expected ID 'registry:io.github.test/test-server', got %q", server.ID)
	}
	if server.Title != "Test Server" {
		t.Fatalf("expected Title 'Test Server', got %q", server.Title)
	}
	if server.SourceID != "io.github.test/test-server" {
		t.Fatalf("expected sourceID 'io.github.test/test-server', got %q", server.SourceID)
	}
	if server.Command != "npx" {
		t.Fatalf("expected command 'npx', got %q", server.Command)
	}
}

func TestRegistryFetcher_Search_IsLatestFilter(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		json.NewEncoder(w).Encode(registryListResponse{
			Servers: []registryServerEntry{
				{
					Server: registryServer{
						Name:        "io.github.test/old-server",
						Description: "Old version",
						Version:     "0.9.0",
					},
					Meta: registryMetaWrapper{
						Official: registryMeta{Status: "active", IsLatest: false, UpdatedAt: "2026-01-01T00:00:00Z"},
					},
				},
				{
					Server: registryServer{
						Name:        "io.github.test/new-server",
						Description: "New version",
						Version:     "1.0.0",
					},
					Meta: registryMetaWrapper{
						Official: registryMeta{Status: "active", IsLatest: true, UpdatedAt: "2026-06-01T00:00:00Z"},
					},
				},
			},
			Metadata: registryMetadata{Count: 2},
		})
	}))
	defer ts.Close()

	f := &RegistryFetcher{hc: NewHTTPClient(), baseURL: ts.URL}
	result, err := f.Search(context.Background(), SearchOptions{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(result.Items) != 1 {
		t.Fatalf("expected 1 item (only isLatest=true), got %d", len(result.Items))
	}
	if result.Items[0].SourceID != "io.github.test/new-server" {
		t.Fatalf("expected new-server, got %q", result.Items[0].SourceID)
	}
}

func TestRegistryFetcher_Search_Pagination(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		cursor := r.URL.Query().Get("cursor")
		if cursor == "" {
			// First page
			json.NewEncoder(w).Encode(registryListResponse{
				Servers: []registryServerEntry{
					{
						Server: registryServer{
							Name: "io.github.test/server-a", Description: "Server A", Version: "1.0.0",
							Packages: []registryPackage{{
								RegistryType: "npm", Identifier: "server-a", Version: "1.0.0",
								RuntimeHint: "npx", Transport: registryTransport{Type: "stdio"},
							}},
						},
						Meta: registryMetaWrapper{
							Official: registryMeta{Status: "active", IsLatest: true, UpdatedAt: "2026-06-01T00:00:00Z"},
						},
					},
				},
				Metadata: registryMetadata{NextCursor: "page2", Count: 1},
			})
		} else {
			// Second page
			json.NewEncoder(w).Encode(registryListResponse{
				Servers: []registryServerEntry{
					{
						Server: registryServer{
							Name: "io.github.test/server-b", Description: "Server B", Version: "2.0.0",
							Remotes: []registryRemote{{Type: "streamable-http", URL: "https://example.com/mcp"}},
						},
						Meta: registryMetaWrapper{
							Official: registryMeta{Status: "active", IsLatest: true, UpdatedAt: "2026-06-15T00:00:00Z"},
						},
					},
				},
				Metadata: registryMetadata{Count: 1},
			})
		}
	}))
	defer ts.Close()

	f := &RegistryFetcher{hc: NewHTTPClient(), baseURL: ts.URL}

	// First page: no cursor
	result, err := f.Search(context.Background(), SearchOptions{PageSize: 30})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(result.Items) != 1 {
		t.Fatalf("expected 1 item on page 1, got %d", len(result.Items))
	}
	if result.Items[0].Name != "server-a" {
		t.Fatalf("expected 'server-a', got %q", result.Items[0].Name)
	}
	if !result.HasMore {
		t.Fatal("expected HasMore=true on page 1")
	}
	if result.NextPage != "page2" {
		t.Fatalf("expected NextPage='page2', got %q", result.NextPage)
	}

	// Second page: with cursor
	result, err = f.Search(context.Background(), SearchOptions{PageSize: 30, Cursor: "page2"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(result.Items) != 1 {
		t.Fatalf("expected 1 item on page 2, got %d", len(result.Items))
	}
	if result.Items[0].Name != "server-b" {
		t.Fatalf("expected 'server-b', got %q", result.Items[0].Name)
	}
	if result.HasMore {
		t.Fatal("expected HasMore=false on page 2")
	}
	if result.NextPage != "" {
		t.Fatalf("expected empty NextPage on page 2, got %q", result.NextPage)
	}
}

func TestRegistryFetcher_Search_RemoteServer(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(registryListResponse{
			Servers: []registryServerEntry{
				{
					Server: registryServer{
						Name:        "com.example/remote-server",
						Description: "A remote MCP server",
						Version:     "1.0.0",
						Remotes:     []registryRemote{{Type: "streamable-http", URL: "https://example.com/mcp"}},
					},
					Meta: registryMetaWrapper{
						Official: registryMeta{Status: "active", IsLatest: true, UpdatedAt: "2026-06-01T00:00:00Z"},
					},
				},
			},
			Metadata: registryMetadata{Count: 1},
		})
	}))
	defer ts.Close()

	f := &RegistryFetcher{hc: NewHTTPClient(), baseURL: ts.URL}
	result, err := f.Search(context.Background(), SearchOptions{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(result.Items) != 1 {
		t.Fatalf("expected 1 item, got %d", len(result.Items))
	}
	server := result.Items[0]
	if server.Transport != "streamable-http" {
		t.Fatalf("expected transport 'streamable-http' for remote server, got %q", server.Transport)
	}
	if server.URL != "https://example.com/mcp" {
		t.Fatalf("expected URL 'https://example.com/mcp', got %q", server.URL)
	}
}

func TestRegistryFetcher_PublisherTags(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(registryListResponse{
			Servers: []registryServerEntry{
				{
					Server: registryServer{
						Name:        "com.example/test",
						Description: "Test server with publisher tags",
						Version:     "1.0.0",
						Remotes:     []registryRemote{{Type: "streamable-http", URL: "https://example.com/mcp"}},
					},
					Meta: registryMetaWrapper{
						Official: registryMeta{Status: "active", IsLatest: true, UpdatedAt: "2026-06-01T00:00:00Z"},
						PublisherProvided: &registryPublisherProvided{
							Categories: []string{"category1", "category2"},
							Keywords:   []string{"keyword1", "keyword2"},
						},
					},
				},
			},
			Metadata: registryMetadata{Count: 1},
		})
	}))
	defer ts.Close()

	f := &RegistryFetcher{hc: NewHTTPClient(), baseURL: ts.URL}
	result, err := f.Search(context.Background(), SearchOptions{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(result.Items) != 1 {
		t.Fatalf("expected 1 item, got %d", len(result.Items))
	}
	item := result.Items[0]
	if len(item.Tags) != 4 {
		t.Fatalf("expected 4 tags (2 categories + 2 keywords), got %d: %v", len(item.Tags), item.Tags)
	}
	if item.Tags[0] != "category1" || item.Tags[2] != "keyword1" {
		t.Fatalf("unexpected tags: %v", item.Tags)
	}
}

// TestRegistryFetcher_DeriveCommandFromRegistryType 验证当 runtimeHint 为空时，
// 能根据 registryType 正确派生命令（npm→npx -y, pypi→uvx, oci→docker run）
// 模拟 context7 场景：registryType=npm, runtimeHint="", identifier="@upstash/context7-mcp"
func TestRegistryFetcher_DeriveCommandFromRegistryType(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(registryListResponse{
			Servers: []registryServerEntry{
				{
					Server: registryServer{
						Name:    "io.github.upstash/context7",
						Title:   "Context7",
						Version: "1.0.31",
						Packages: []registryPackage{{
							RegistryType: "npm",
							Identifier:   "@upstash/context7-mcp",
							Version:      "1.0.31",
							Transport:    registryTransport{Type: "stdio"},
						}},
					},
					Meta: registryMetaWrapper{
						Official: registryMeta{Status: "active", IsLatest: true, UpdatedAt: "2026-06-01T00:00:00Z"},
					},
				},
			},
			Metadata: registryMetadata{Count: 1},
		})
	}))
	defer ts.Close()

	f := &RegistryFetcher{hc: NewHTTPClient(), baseURL: ts.URL}
	result, err := f.Search(context.Background(), SearchOptions{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(result.Items) != 1 {
		t.Fatalf("expected 1 item, got %d", len(result.Items))
	}
	item := result.Items[0]
	// 应派生 command=npx
	if item.Command != "npx" {
		t.Fatalf("expected command 'npx' (derived from registryType=npm), got %q", item.Command)
	}
	// 应包含 -y 和 identifier
	expectedArgs := []string{"-y", "@upstash/context7-mcp"}
	if len(item.Args) != len(expectedArgs) {
		t.Fatalf("expected %d args %v, got %d: %v", len(expectedArgs), expectedArgs, len(item.Args), item.Args)
	}
	for i, want := range expectedArgs {
		if item.Args[i] != want {
			t.Fatalf("expected arg[%d]=%q, got %q", i, want, item.Args[i])
		}
	}
	// 应包含 npm tag
	foundNpm := false
	for _, tag := range item.Tags {
		if tag == "npm" {
			foundNpm = true
			break
		}
	}
	if !foundNpm {
		t.Fatalf("expected tags to contain 'npm', got %v", item.Tags)
	}
}

// TestRegistryFetcher_Search_PrefersIsLatest 验证搜索去重时优先选择 isLatest=true 的版本
func TestRegistryFetcher_Search_PrefersIsLatest(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(registryListResponse{
			Servers: []registryServerEntry{
				{
					Server: registryServer{
						Name:    "io.github.test/server",
						Version: "1.0.0",
						Packages: []registryPackage{{
							RegistryType: "npm", Identifier: "old-pkg", Version: "1.0.0",
							Transport: registryTransport{Type: "stdio"},
						}},
					},
					Meta: registryMetaWrapper{
						Official: registryMeta{Status: "active", IsLatest: false, UpdatedAt: "2026-01-01T00:00:00Z"},
					},
				},
				{
					Server: registryServer{
						Name:    "io.github.test/server",
						Version: "2.0.0",
						Packages: []registryPackage{{
							RegistryType: "npm", Identifier: "new-pkg", Version: "2.0.0",
							Transport: registryTransport{Type: "stdio"},
						}},
					},
					Meta: registryMetaWrapper{
						Official: registryMeta{Status: "active", IsLatest: true, UpdatedAt: "2026-06-01T00:00:00Z"},
					},
				},
			},
			Metadata: registryMetadata{Count: 2},
		})
	}))
	defer ts.Close()

	f := &RegistryFetcher{hc: NewHTTPClient(), baseURL: ts.URL}
	// 搜索模式（有 query），应去重并优先 isLatest=true
	result, err := f.Search(context.Background(), SearchOptions{Query: "server"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(result.Items) != 1 {
		t.Fatalf("expected 1 item (deduplicated), got %d", len(result.Items))
	}
	item := result.Items[0]
	// 应使用 v2.0.0 的 identifier
	if len(item.Args) < 1 || item.Args[len(item.Args)-1] != "new-pkg" {
		t.Fatalf("expected identifier 'new-pkg' from isLatest version, got args=%v", item.Args)
	}
}