package market

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func newOfficialHandler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/v0.1/servers", func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		results := []officialListItem{
			{
				Server: officialServerDetail{
					Name:        "io.example/filesystem",
					Title:       "Filesystem",
					Description: "File system access",
					Version:     "1.0.0",
					Website:     "https://example.com",
					Repo:        &officialRepo{URL: "https://github.com/example/filesystem"},
					Packages: []officialPackage{{
						RegistryName: "npm",
						Identifier:   "@example/filesystem",
						Transport:    &officialTransport{Type: "stdio"},
					}},
				},
			},
			{
				Server: officialServerDetail{
					Name:        "io.example/redis",
					Title:       "Redis",
					Description: "Redis database access",
					Version:     "1.0.0",
					Packages: []officialPackage{{
						RegistryName: "oci",
						Identifier:   "mcp/redis",
						Transport:    &officialTransport{Type: "stdio"},
					}},
				},
			},
		}
		if s := q.Get("search"); s != "" {
			filtered := results[:0]
			for _, r := range results {
				if strings.Contains(r.Server.Name, s) || strings.Contains(r.Server.Description, s) {
					filtered = append(filtered, r)
				}
			}
			results = filtered
		}
		_ = json.NewEncoder(w).Encode(officialListResponse{
			Servers: results,
			Metadata: &struct {
				Count      int    `json:"count"`
				NextCursor string `json:"nextCursor"`
			}{Count: len(results), NextCursor: ""},
		})
	})
	mux.HandleFunc("/v0.1/servers/io.example/filesystem/versions/latest", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"server": officialServerDetail{
				Name:        "io.example/filesystem",
				Title:       "Filesystem",
				Description: "File system access",
				Version:     "1.0.0",
				Packages: []officialPackage{{
					RegistryName: "npm",
					Identifier:   "@example/filesystem",
					Transport:    &officialTransport{Type: "stdio"},
				}},
			},
		})
	})
	mux.HandleFunc("/v0.1/servers/io.example%2Ffilesystem/versions/latest", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"server": officialServerDetail{
				Name:        "io.example/filesystem",
				Title:       "Filesystem",
				Description: "File system access",
				Version:     "1.0.0",
				Packages: []officialPackage{{
					RegistryName: "npm",
					Identifier:   "@example/filesystem",
					Transport:    &officialTransport{Type: "stdio"},
				}},
			},
		})
	})
	return mux
}

func TestOfficialFetcher_Search(t *testing.T) {
	server := httptest.NewServer(newOfficialHandler())
	defer server.Close()
	originalBase := officialBaseURL
	setOfficialBase(server.URL)
	defer setOfficialBase(originalBase)

	f := NewOfficialFetcher()
	got, err := f.Search(context.Background(), SearchOptions{Query: ""})
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Items) != 2 {
		t.Errorf("expected 2 results, got %d", len(got.Items))
	}
	for _, srv := range got.Items {
		if srv.Source != SourceOfficial {
			t.Errorf("expected source=official, got %s", srv.Source)
		}
		if srv.Command == "" {
			t.Errorf("expected command for %s, got empty", srv.Name)
		}
		if srv.Transport == "" {
			t.Errorf("expected transport for %s, got empty", srv.Name)
		}
	}
}

func TestOfficialFetcher_SearchWithQuery(t *testing.T) {
	server := httptest.NewServer(newOfficialHandler())
	defer server.Close()
	originalBase := officialBaseURL
	setOfficialBase(server.URL)
	defer setOfficialBase(originalBase)

	f := NewOfficialFetcher()
	got, err := f.Search(context.Background(), SearchOptions{Query: "redis"})
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Items) != 1 {
		t.Errorf("expected 1 result for 'redis', got %d", len(got.Items))
	}
}

func TestOfficialFetcher_Get(t *testing.T) {
	server := httptest.NewServer(newOfficialHandler())
	defer server.Close()
	originalBase := officialBaseURL
	setOfficialBase(server.URL)
	defer setOfficialBase(originalBase)

	f := NewOfficialFetcher()
	got, err := f.Get(context.Background(), "io.example/filesystem")
	if err != nil {
		t.Fatal(err)
	}
	if got.Name != "io.example/filesystem" {
		t.Errorf("unexpected name %s", got.Name)
	}
}

func TestOfficialFetcher_ConvertNPMPackage(t *testing.T) {
	d := officialServerDetail{
		Name: "test",
		Packages: []officialPackage{{
			RegistryName: "npm",
			Identifier:   "@scope/pkg",
		}},
	}
	srv := convertOfficial(d)
	if srv.Command != "npx" {
		t.Errorf("expected npx, got %q", srv.Command)
	}
	if len(srv.Args) < 2 || srv.Args[0] != "-y" {
		t.Errorf("expected args to start with -y, got %v", srv.Args)
	}
}

func TestOfficialFetcher_ConvertPyPIPackage(t *testing.T) {
	d := officialServerDetail{
		Name: "test",
		Packages: []officialPackage{{
			RegistryName: "pypi",
			Identifier:   "mypkg",
		}},
	}
	srv := convertOfficial(d)
	if srv.Command != "uvx" {
		t.Errorf("expected uvx, got %q", srv.Command)
	}
	if len(srv.Args) < 1 || srv.Args[0] != "mypkg" {
		t.Errorf("expected args to start with mypkg, got %v", srv.Args)
	}
}

func TestOfficialFetcher_ConvertOCIPackage(t *testing.T) {
	d := officialServerDetail{
		Name: "test",
		Packages: []officialPackage{{
			RegistryName: "oci",
			Identifier:   "mcp/redis",
		}},
	}
	srv := convertOfficial(d)
	if srv.Command != "docker" {
		t.Errorf("expected docker, got %q", srv.Command)
	}
	if len(srv.Args) < 4 {
		t.Errorf("expected at least 4 docker args, got %v", srv.Args)
	}
}

func TestStore_SearchFallsBackToCache(t *testing.T) {
	server := httptest.NewServer(newOfficialHandler())
	defer server.Close()
	originalBase := officialBaseURL
	setOfficialBase(server.URL)
	defer setOfficialBase(originalBase)

	dir := t.TempDir()
	st := NewStore(dir)
	st.RegisterServer(NewOfficialFetcher())

	got, err := st.Search(context.Background(), SourceOfficial, SearchOptions{Query: ""})
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Items) == 0 {
		t.Fatal("expected results")
	}
	got2, err := st.Search(context.Background(), SourceOfficial, SearchOptions{Query: ""})
	if err != nil {
		t.Fatal(err)
	}
	if len(got2.Items) != len(got.Items) {
		t.Errorf("cache returned different count: %d vs %d", len(got2.Items), len(got.Items))
	}
}

func TestStore_UnknownSource(t *testing.T) {
	dir := t.TempDir()
	st := NewStore(dir)
	_, err := st.Search(context.Background(), "nope", SearchOptions{})
	if err == nil {
		t.Fatal("expected error for unknown source")
	}
}

func TestStore_GetServerRequiresOfficial(t *testing.T) {
	dir := t.TempDir()
	st := NewStore(dir)
	_, err := st.GetServer(context.Background(), SourceOfficial, "any")
	if err == nil {
		t.Fatal("expected error when no fetcher registered")
	}
}

func TestCacheKey(t *testing.T) {
	dir := t.TempDir()
	st := NewStore(dir)
	k1 := st.cacheKey("search", SourceOfficial, SearchOptions{Query: "a", Page: 1})
	k2 := st.cacheKey("search", SourceOfficial, SearchOptions{Query: "a", Page: 1})
	k3 := st.cacheKey("search", SourceOfficial, SearchOptions{Query: "b", Page: 1})
	if k1 != k2 {
		t.Errorf("expected same key for same opts, got %s vs %s", k1, k2)
	}
	if k1 == k3 {
		t.Errorf("expected different keys for different query")
	}
}

func TestReadWriteCache(t *testing.T) {
	dir := t.TempDir()
	st := NewStore(dir)
	result := &SearchResultServers{
		Items: []MarketServer{{Name: "test"}},
		Total: 1,
	}
	key := st.cacheKey("search", SourceOfficial, SearchOptions{Query: "x"})
	st.writeCache(key, result)

	// 验证缓存文件已写入磁盘
	cached, ok := st.readCache(key)
	if !ok {
		t.Fatal("expected cache hit after writeCache")
	}
	if cached.Total != 1 {
		t.Errorf("expected total=1, got %d", cached.Total)
	}
}

func TestSources(t *testing.T) {
	dir := t.TempDir()
	st := NewStore(dir)
	st.RegisterServer(NewOfficialFetcher())
	sources := st.Sources()
	if len(sources) != 1 {
		t.Errorf("expected 1 source, got %d: %v", len(sources), sources)
	}
}

func TestOfficialFetcher_BuildsURL(t *testing.T) {
	f := NewOfficialFetcher()
	if f.hc == nil {
		t.Fatal("expected client")
	}
	if f.hc.client.Timeout.Seconds() < 5 {
		t.Errorf("expected timeout >= 5s, got %v", f.hc.client.Timeout)
	}
}

func setOfficialBase(u string) {
	override := strings.TrimPrefix(u, "http://")
	if override == "" {
		override = strings.TrimPrefix(u, "https://")
	}
	scheme := "https"
	if strings.HasPrefix(u, "http://") {
		scheme = "http"
	}
	officialBaseURL = scheme + "://" + override
}
