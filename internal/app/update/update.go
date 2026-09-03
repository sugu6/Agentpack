// Package update 提供应用更新的纯逻辑：GitHub releases.atom 订阅源解析、
// 版本比较、按 CI 命名规则构造安装包下载地址。不依赖 App 状态，可独立测试。
// 检查更新的编排（缓存/singleflight）与安装包下载/安装执行留在根目录 app.go。
package update

import (
	"encoding/xml"
	"fmt"
	"runtime"
	"strconv"
	"strings"
)

// GitHub 仓库地址（owner/repo），用于检查更新
// 如需更换仓库，修改此常量即可
const Repo = "sugu6/AgentPack"

// maxReleaseBodySize 限制 GitHub release API 响应体大小（1MB），防止异常响应撑爆内存。
const MaxReleaseBodySize = 1 << 20

// UpdateCheckResult 是检查更新的返回结构，前端通过 Wails 绑定调用
type UpdateCheckResult struct {
	HasUpdate      bool   `json:"hasUpdate"`
	CurrentVersion string `json:"currentVersion"`
	LatestVersion  string `json:"latestVersion"`
	Message        string `json:"message"`
	Changelog      string `json:"changelog"`
	ReleaseURL     string `json:"releaseUrl"`
	DownloadURL    string `json:"downloadUrl"`
	DownloadSize   int    `json:"downloadSize"`
	DownloadName   string `json:"downloadName"`
}

// atomFeed / atomEntry 解析 GitHub releases.atom 订阅源。
// 该订阅源由 GitHub 静态托管，不受 REST API 的未认证限流（60 次/小时/IP）限制，
// 是"检查更新"的限流无忧来源。首条 entry 通常为最新发布。
type atomFeed struct {
	XMLName xml.Name    `xml:"feed"`
	Entries []atomEntry `xml:"entry"`
}

type atomEntry struct {
	ID      string      `xml:"id"`
	Updated string      `xml:"updated"`
	Links   []atomLink  `xml:"link"`
	Title   string      `xml:"title"`
	Content atomContent `xml:"content"`
}

type atomLink struct {
	Rel  string `xml:"rel,attr"`
	Type string `xml:"type,attr"`
	Href string `xml:"href,attr"`
}

type atomContent struct {
	Type string `xml:"type,attr"`
	Body string `xml:",innerxml"`
}

// AtomFeed / AtomEntry 别名导出，供 App 层解析后使用。
type AtomFeed = atomFeed
type AtomEntry = atomEntry

// AtomEntryTag 从 atom entry 提取发布 tag（如 "v1.2.3"）。优先取 alternate 链接
// /releases/tag/ 后的部分；取不到则回退到 id 的最后一段。
func AtomEntryTag(e *AtomEntry) string {
	for _, l := range e.Links {
		if l.Rel == "alternate" {
			if i := strings.LastIndex(l.Href, "/releases/tag/"); i >= 0 {
				return l.Href[i+len("/releases/tag/"):]
			}
		}
	}
	if i := strings.LastIndex(e.ID, "/"); i >= 0 {
		return e.ID[i+1:]
	}
	return ""
}

// AtomEntryReleaseURL 返回发布的 HTML 页面 URL。
func AtomEntryReleaseURL(e *AtomEntry) string {
	for _, l := range e.Links {
		if l.Rel == "alternate" && l.Href != "" {
			return l.Href
		}
	}
	return ""
}

// BuildDownloadAsset 按 CI 命名规则确定性构造安装包下载 URL 与文件名，
// 与 .github/workflows/build.yml 的产物命名保持一致，无需依赖 API 资产列表。
func BuildDownloadAsset(version string) (url, name string) {
	return BuildDownloadAssetFor(version, runtime.GOOS, runtime.GOARCH)
}

// BuildDownloadAssetFor 是 BuildDownloadAsset 的平台参数化版本，便于测试覆盖各端。
func BuildDownloadAssetFor(version, goos, goarch string) (url, name string) {
	v := version
	var asset string
	switch goos {
	case "windows":
		asset = fmt.Sprintf("AgentPack-%s-windows-%s-installer.exe", v, goarch)
	case "darwin":
		asset = fmt.Sprintf("AgentPack-%s-macos-universal.dmg", v)
	case "linux":
		asset = fmt.Sprintf("AgentPack-%s-linux-%s.tar.gz", v, goarch)
	default:
		return "", ""
	}
	base := fmt.Sprintf("https://github.com/%s/releases/download/v%s/%s", Repo, v, asset)
	return base, asset
}

// CompareVersions 比较两个版本号：a<b 返回 -1，a>b 返回 1，相等返回 0。
func CompareVersions(a, b string) int {
	aParts := parseVersionParts(a)
	bParts := parseVersionParts(b)
	maxLen := len(aParts)
	if len(bParts) > maxLen {
		maxLen = len(bParts)
	}
	for i := 0; i < maxLen; i++ {
		av, bv := 0, 0
		if i < len(aParts) {
			av = aParts[i]
		}
		if i < len(bParts) {
			bv = bParts[i]
		}
		if av < bv {
			return -1
		}
		if av > bv {
			return 1
		}
	}
	// 数字部分相同：预发布版本（含 "-" 后缀，如 1.2.3-beta）排在正式版之后。
	// parseVersionParts 会把 "1.2.3-beta" 与 "1.2.3" 解析成相同的 [1,2,3]，
	// 若不在此区分，正式版发布时会被误判为"无更新"。
	aPre := PreReleaseSuffix(a)
	bPre := PreReleaseSuffix(b)
	switch {
	case aPre == "" && bPre != "":
		return 1
	case aPre != "" && bPre == "":
		return -1
	case aPre != "" && bPre != "":
		if c := comparePreRelease(aPre, bPre); c != 0 {
			return c
		}
	}
	return 0
}

// PreReleaseSuffix 返回版本字符串的预发布后缀（"-" 之后的部分），无后缀返回 ""。
func PreReleaseSuffix(v string) string {
	if idx := strings.Index(v, "-"); idx >= 0 {
		return v[idx+1:]
	}
	return ""
}

// comparePreRelease 按 semver 规则比较两个预发布后缀：
// 以 "." 分段；数字段按数值比较（beta.10 > beta.2）；字母段按 ASCII；
// 数字标识符 < 字母标识符；短后缀 < 长后缀（同一前缀时）。
func comparePreRelease(a, b string) int {
	as := strings.Split(a, ".")
	bs := strings.Split(b, ".")
	for i := 0; ; i++ {
		if i >= len(as) && i >= len(bs) {
			return 0
		}
		var av, bv string
		if i < len(as) {
			av = as[i]
		}
		if i < len(bs) {
			bv = bs[i]
		}
		if av == bv {
			continue
		}
		if av == "" {
			return -1
		}
		if bv == "" {
			return 1
		}
		an, aErr := strconv.Atoi(av)
		bn, bErr := strconv.Atoi(bv)
		aIsNum, bIsNum := aErr == nil, bErr == nil
		switch {
		case aIsNum && bIsNum:
			if an < bn {
				return -1
			}
			return 1
		case aIsNum:
			return -1
		case bIsNum:
			return 1
		default:
			if av < bv {
				return -1
			}
			return 1
		}
	}
}

// parseVersionParts 将版本号拆成数字段（忽略预发布/构建后缀）。
func parseVersionParts(v string) []int {
	v = strings.TrimPrefix(v, "v")
	if idx := strings.IndexAny(v, "-+"); idx >= 0 {
		v = v[:idx]
	}
	parts := strings.Split(v, ".")
	out := make([]int, 0, len(parts))
	for _, p := range parts {
		n := 0
		for _, ch := range p {
			if ch < '0' || ch > '9' {
				break
			}
			n = n*10 + int(ch-'0')
		}
		out = append(out, n)
	}
	return out
}
