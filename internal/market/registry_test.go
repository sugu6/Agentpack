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
	pageCalls := 0
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		pageCalls++
		if pageCalls == 1 {
			json.NewEncoder(w).Encode(registryListResponse{
				Servers: []registryServerEntry{
					{
						Server: registryServer{
							Name:        "io.github.test/server-a",
							Description: "Server A",
							Version:     "1.0.0",
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
			json.NewEncoder(w).Encode(registryListResponse{
				Servers: []registryServerEntry{
					{
						Server: registryServer{
							Name:        "io.github.test/server-b",
							Description: "Server B",
							Version:     "2.0.0",
							Remotes:     []registryRemote{{Type: "streamable-http", URL: "https://example.com/mcp"}},
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
	result, err := f.Search(context.Background(), SearchOptions{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(result.Items) != 2 {
		t.Fatalf("expected 2 items (2 pages), got %d", len(result.Items))
	}
	if pageCalls != 2 {
		t.Fatalf("expected 2 API calls, got %d", pageCalls)
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
	if server.Transport != "sse" {
		t.Fatalf("expected transport 'sse' for remote server, got %q", server.Transport)
	}
	if server.URL != "https://example.com/mcp" {
		t.Fatalf("expected URL 'https://example.com/mcp', got %q", server.URL)
	}
}