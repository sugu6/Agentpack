package update

import (
	"encoding/xml"
	"html"
	"io"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
	"time"
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

// TestValidateDownloadURL 验证更新下载 URL 白名单（仅本仓库 CI 发布资产：
// P2-2 钉死 owner/repo + /releases/download/ 前缀，防任意 GitHub 仓库投毒）。
func TestValidateDownloadURL(t *testing.T) {
	cases := []struct {
		url    string
		wantOK bool
	}{
		{"https://github.com/sugu6/AgentPack/releases/download/v1.0.0/AgentPack-1.0.0-windows-amd64-installer.exe", true},
		{"https://github.com/other/repo/releases/download/v1/a.exe", false},                   // 任意仓库必须拒绝
		{"https://github.com/sugu6/AgentPackX/releases/download/v1/a.exe", false},             // 同名前缀仓库必须拒绝
		{"https://github.com/sugu6/AgentPack/releases/tag/v1.0.0", false},                     // 非下载路径必须拒绝
		{"http://github.com/sugu6/AgentPack/releases/download/v1/a.exe", false},               // 必须 https
		{"https://gh-proxy.com/github.com/sugu6/AgentPack/releases/download/v1/a.exe", false}, // 代理形 URL 不走白名单（startDownload 校验发生在改写前）
		{"https://evil.com/a.exe", false},
		{"", false},
	}
	for _, c := range cases {
		err := ValidateDownloadURL(c.url)
		if c.wantOK && err != nil {
			t.Errorf("ValidateDownloadURL(%q) = %v, want nil", c.url, err)
		}
		if !c.wantOK && err == nil {
			t.Errorf("ValidateDownloadURL(%q) = nil, want error", c.url)
		}
	}
}

// atomRoundTripper 拦截全部请求返回固定 atom 订阅源，并统计请求次数与注入延迟，
// 用于把 CheckUpdate 的 GitHub 请求隔离到本地（不改 Repo/URL 常量）。
type atomRoundTripper struct {
	body  string
	delay time.Duration
	hits  int32
}

func (rt *atomRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	atomic.AddInt32(&rt.hits, 1)
	if rt.delay > 0 {
		time.Sleep(rt.delay)
	}
	return &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{},
		Body:       io.NopCloser(strings.NewReader(rt.body)),
		Request:    req,
	}, nil
}

// TestCheckUpdate_SingleflightBroadcastsToAllWaiters 钉住 singleflight 的广播语义：
// 任意数量的并发 CheckUpdate 都必须返回，且只产生一次网络请求。
// 旧实现用带缓冲的单值通道传递结果，第 3 个及之后的 waiter 会永久阻塞。
func TestCheckUpdate_SingleflightBroadcastsToAllWaiters(t *testing.T) {
	rt := &atomRoundTripper{body: sampleAtomFeed, delay: 100 * time.Millisecond}
	orig := downloadHTTPClient
	downloadHTTPClient = &http.Client{Transport: rt}
	t.Cleanup(func() { downloadHTTPClient = orig })

	svc := NewService(nil, func() string { return "en" }, nil, nil, nil)

	const n = 5
	done := make(chan struct{}, n)
	for i := 0; i < n; i++ {
		go func() {
			if _, err := svc.CheckUpdate(); err != nil {
				t.Errorf("CheckUpdate: %v", err)
			}
			done <- struct{}{}
		}()
	}
	for i := 0; i < n; i++ {
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			t.Fatalf("concurrent CheckUpdate #%d blocked: singleflight must broadcast to all waiters", i+1)
		}
	}
	if h := atomic.LoadInt32(&rt.hits); h != 1 {
		t.Errorf("network requests = %d, want 1 (singleflight must coalesce concurrent checks)", h)
	}
}
