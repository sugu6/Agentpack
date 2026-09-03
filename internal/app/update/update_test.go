package update

import (
	"encoding/xml"
	"html"
	"strings"
	"testing"
)

// sampleAtomFeed 模拟 GitHub releases.atom 输出：首条为预发布，第二条为正式版。
const sampleAtomFeed = `<?xml version="1.0" encoding="UTF-8"?>
<feed xmlns="http://www.w3.org/2005/Atom">
  <id>tag:github.com,2008:https://github.com/sugu6/AgentPack/releases</id>
  <updated>2026-08-16T00:00:00Z</updated>
  <entry>
    <id>tag:github.com,2008:Repository/1/v1.1.0-beta</id>
    <updated>2026-08-15T00:00:00Z</updated>
    <link rel="alternate" type="text/html" href="https://github.com/sugu6/AgentPack/releases/tag/v1.1.0-beta"/>
    <title>v1.1.0-beta</title>
    <content type="html">&lt;h2&gt;Beta&lt;/h2&gt;</content>
  </entry>
  <entry>
    <id>tag:github.com,2008:Repository/1/v1.0.0</id>
    <updated>2026-08-10T00:00:00Z</updated>
    <link rel="alternate" type="text/html" href="https://github.com/sugu6/AgentPack/releases/tag/v1.0.0"/>
    <title>v1.0.0</title>
    <content type="html">&lt;h2&gt;Stable&lt;/h2&gt;&lt;p&gt;notes&lt;/p&gt;</content>
  </entry>
</feed>`

func TestAtomFeed_ParseAndPickStableEntry(t *testing.T) {
	var feed AtomFeed
	if err := xml.Unmarshal([]byte(sampleAtomFeed), &feed); err != nil {
		t.Fatalf("unmarshal atom feed: %v", err)
	}
	if len(feed.Entries) != 2 {
		t.Fatalf("expected 2 entries, got %d", len(feed.Entries))
	}

	// 应跳过预发布 v1.1.0-beta，选中正式版 v1.0.0
	var entry *AtomEntry
	for i := range feed.Entries {
		tag := AtomEntryTag(&feed.Entries[i])
		version := strings.TrimPrefix(tag, "v")
		if version != "" && PreReleaseSuffix(version) == "" {
			entry = &feed.Entries[i]
			break
		}
	}
	if entry == nil {
		t.Fatal("expected to find a stable entry")
	}
	if got := AtomEntryTag(entry); got != "v1.0.0" {
		t.Errorf("expected tag v1.0.0, got %q", got)
	}
	if got := AtomEntryReleaseURL(entry); got != "https://github.com/sugu6/AgentPack/releases/tag/v1.0.0" {
		t.Errorf("unexpected release URL: %q", got)
	}
}

func TestAtomFeed_ParseContentUnescapesHTML(t *testing.T) {
	var feed AtomFeed
	if err := xml.Unmarshal([]byte(sampleAtomFeed), &feed); err != nil {
		t.Fatalf("unmarshal atom feed: %v", err)
	}
	// 第二条 entry 的 content 内是 HTML 转义后的 "<h2>Stable</h2><p>notes</p>"
	entry := &feed.Entries[1]
	if got := html.UnescapeString(entry.Content.Body); got != "<h2>Stable</h2><p>notes</p>" {
		t.Errorf("content should be unescaped to HTML, got %q", got)
	}
}

func TestAtomFeed_NoStableEntry(t *testing.T) {
	// 只有预发布的订阅源：不应命中正式版
	onlyPre := strings.ReplaceAll(sampleAtomFeed, "v1.0.0", "v1.0.0-rc.1")
	onlyPre = strings.Replace(onlyPre, "<h2>Stable</h2><p>notes</p>", "<h2>RC</h2>", 1)
	var feed AtomFeed
	if err := xml.Unmarshal([]byte(onlyPre), &feed); err != nil {
		t.Fatalf("unmarshal atom feed: %v", err)
	}
	for i := range feed.Entries {
		tag := AtomEntryTag(&feed.Entries[i])
		version := strings.TrimPrefix(tag, "v")
		if version != "" && PreReleaseSuffix(version) == "" {
			t.Fatalf("unexpected stable entry in prerelease-only feed: %s", tag)
		}
	}
}

func TestBuildDownloadAssetFor(t *testing.T) {
	cases := []struct {
		goos, goarch string
		wantName     string
		wantURL      string
	}{
		{"windows", "amd64", "AgentPack-1.2.3-windows-amd64-installer.exe",
			"https://github.com/sugu6/AgentPack/releases/download/v1.2.3/AgentPack-1.2.3-windows-amd64-installer.exe"},
		{"windows", "arm64", "AgentPack-1.2.3-windows-arm64-installer.exe",
			"https://github.com/sugu6/AgentPack/releases/download/v1.2.3/AgentPack-1.2.3-windows-arm64-installer.exe"},
		{"darwin", "amd64", "AgentPack-1.2.3-macos-universal.dmg",
			"https://github.com/sugu6/AgentPack/releases/download/v1.2.3/AgentPack-1.2.3-macos-universal.dmg"},
		{"darwin", "arm64", "AgentPack-1.2.3-macos-universal.dmg",
			"https://github.com/sugu6/AgentPack/releases/download/v1.2.3/AgentPack-1.2.3-macos-universal.dmg"},
		{"linux", "amd64", "AgentPack-1.2.3-linux-amd64.tar.gz",
			"https://github.com/sugu6/AgentPack/releases/download/v1.2.3/AgentPack-1.2.3-linux-amd64.tar.gz"},
		{"linux", "arm64", "AgentPack-1.2.3-linux-arm64.tar.gz",
			"https://github.com/sugu6/AgentPack/releases/download/v1.2.3/AgentPack-1.2.3-linux-arm64.tar.gz"},
		{"freebsd", "amd64", "", ""},
	}
	for _, c := range cases {
		url, name := BuildDownloadAssetFor("1.2.3", c.goos, c.goarch)
		if name != c.wantName || url != c.wantURL {
			t.Errorf("BuildDownloadAssetFor(%s,%s) = (%q,%q), want (%q,%q)",
				c.goos, c.goarch, url, name, c.wantURL, c.wantName)
		}
	}
}

func TestCompareVersions(t *testing.T) {
	cases := []struct {
		a, b string
		want int
	}{
		{"1.0.0", "1.0.0", 0},
		{"1.0.1", "1.0.0", 1},
		{"1.0.0", "1.0.1", -1},
		{"2.0.0", "1.9.9", 1},
		{"1.0.0", "1.0.0-beta", 1}, // 正式版 > 预发布
		{"1.0.0-beta", "1.0.0", -1},
		{"1.0.0-beta.2", "1.0.0-beta.10", -1}, // 数字段按数值比较
		{"v1.2.3", "1.2.3", 0},                // 前导 v 忽略
	}
	for _, c := range cases {
		if got := CompareVersions(c.a, c.b); got != c.want {
			t.Errorf("CompareVersions(%q,%q) = %d, want %d", c.a, c.b, got, c.want)
		}
	}
}
