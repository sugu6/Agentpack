package market

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"testing"
)

// TestDiagnostic_OldCacheKeys 计算各 source 的缓存 key，帮助定位哪个缓存文件属于哪个 source
func TestDiagnostic_OldCacheKeys(t *testing.T) {
	// 实际调用时 Page 未设置，默认为 0
	opts := SearchOptions{Query: "", Page: 0, PageSize: 30}

	sources := []struct {
		kind   string
		source Source
	}{
		{"search-skills", SourceGitHub},
		{"search-skills", SourceSkillsSh},
		{"search", SourceOfficial},
		{"search", SourceSmithery},
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
	t.Log("ffead36ba13dda6c557f536daf8c2629.json (14337 bytes) - smithery MCP")
}
