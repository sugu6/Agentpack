package market

import (
	"context"
	"testing"
)

func TestStore_UnknownSource(t *testing.T) {
	dir := t.TempDir()
	st := NewStore(dir)
	_, err := st.Search(context.Background(), "nope", SearchOptions{})
	if err == nil {
		t.Fatal("expected error for unknown source")
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

func TestStore_GetServerWithoutFetcher(t *testing.T) {
	dir := t.TempDir()
	st := NewStore(dir)
	_, err := st.GetServer(context.Background(), SourceOfficial, "any")
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
