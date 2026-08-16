package main

import (
	"context"
	_ "embed"
	"encoding/xml"
	"fmt"
	"html"
	"io"
	"log"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"agentpack/internal/appmeta"
	"agentpack/internal/config"
	"agentpack/internal/i18n"
)

// GitHub 仓库地址（owner/repo），用于检查更新
// 如需更换仓库，修改此常量即可
const githubRepo = "sugu6/AgentPack"

// maxUpdateDownloadSize 限制更新安装包最大体积（1GB），防止恶意/异常源无限流数据写满磁盘。
const maxUpdateDownloadSize int64 = 1 << 30

// maxDownloadDuration 限制单次下载总时长（30 分钟），防止服务器接受连接后
// 不发送数据（stalled）导致的 goroutine 永久阻塞。
const maxDownloadDuration = 30 * time.Minute

// maxReleaseBodySize 限制 GitHub release API 响应体大小（1MB），防止异常响应撑爆内存。
const maxReleaseBodySize = 1 << 20

// updateCheckTTL 距上次成功检查后，复用缓存结果而不再请求 GitHub API 的时长。
// GitHub 未认证 API 限制为 60 次/小时/IP；配合端上"每会话仅启动静默检查一次"，
// 10 分钟 TTL 将单实例的检查频率压到 ≤6 次/小时，避免触发限流。
const updateCheckTTL = 10 * time.Minute

// updateCheckRateLimitBackoff 上次检查命中限流(403/429)后的退避时长。
// 限流状态下继续请求只会继续触发限流，用更长退避让限流窗口过去再重试。
const updateCheckRateLimitBackoff = 30 * time.Minute

// downloadHTTPClient 用于更新包下载：Transport 设置 ResponseHeaderTimeout，
// 防止服务器接受连接后不响应头部导致的永久阻塞；总时长由 startDownload 的
// context.WithTimeout 兜底。
var downloadHTTPClient = &http.Client{
	Transport: &http.Transport{
		ResponseHeaderTimeout: 30 * time.Second,
	},
}

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

//go:embed build/config.yml
var buildConfigYML []byte

// init 注入应用版本到 appmeta，供各网络层构造 User-Agent（避免硬编码版本号）。
func init() {
	appmeta.Version = currentAppVersion()
}

func currentAppVersion() string {
	// 从 build/config.yml 的 info.version 字段提取版本号
	// 格式: version: "x.y.z" 或 version: 'x.y.z'
	for _, line := range strings.Split(string(buildConfigYML), "\n") {
		trimmed := strings.TrimSpace(line)
		// 匹配 info 块下的 version 字段（跳过顶层 version: '3'）
		if strings.HasPrefix(trimmed, "version:") {
			val := strings.TrimPrefix(trimmed, "version:")
			val = strings.TrimSpace(val)
			val = strings.Trim(val, "\"'")
			// 格式化版本号，只取 X.Y.Z 部分
			if idx := strings.IndexAny(val, " \t"); idx > 0 {
				val = val[:idx]
			}
			if strings.Count(val, ".") >= 2 {
				return val
			}
		}
	}
	return "0.0.0"
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

// CheckUpdate 检查是否有新版本。带 singleflight + 时间缓存：
//   - singleflight：启动静默检查（App.vue onMounted）与 Settings 页手动检查可能
//     并发触发，重复发起会导致两次 GitHub API 请求和两次 app:update-available
//     事件；waiter 直接复用首者的结果。
//   - 时间缓存：距上次检查仍在 TTL/限流退避窗口内时，直接复用缓存结果，不再
//     请求 GitHub API，避免高频次反复请求触发未认证限流（60 次/小时/IP）。
func (a *App) CheckUpdate() (res *UpdateCheckResult, err error) {
	// 先取语言，供缓存命中时按当前语言重生成消息（避免与 a.mu 嵌套加锁）
	a.mu.RLock()
	lang := i18n.ResolveLanguage(a.cfg.Settings.Language)
	a.mu.RUnlock()

	a.checkUpdateMu.Lock()
	if a.checkUpdateCh != nil {
		ch := a.checkUpdateCh
		a.checkUpdateMu.Unlock()
		res := <-ch
		return res.result, res.err
	}
	// 缓存命中：直接复用上次结果
	if cached := a.updateCheckCached(lang); cached != nil {
		a.checkUpdateMu.Unlock()
		return cached.result, cached.err
	}
	ch := make(chan checkUpdateRes, 1)
	a.checkUpdateCh = ch
	a.checkUpdateMu.Unlock()

	// panic 兜底：checkUpdateInternal 意外 panic 时 waiter 会永久阻塞
	// wails 调用线程且 checkUpdateCh 不清零（后续检查全部排队等待）。
	// 命名返回值 + recover：归还状态并把 panic 作为错误投递给 waiter。
	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("check update panic: %v", r)
			a.checkUpdateMu.Lock()
			a.checkUpdateCh = nil
			a.checkUpdateMu.Unlock()
			ch <- checkUpdateRes{err: err}
		}
	}()

	out := a.checkUpdateInternal()

	// 写入缓存供后续检查复用（限流结果使用更长的退避窗口）
	a.checkUpdateMu.Lock()
	a.updateCheckAt = time.Now()
	a.updateCheckRateLtd = out.rateLimited
	a.updateCheckResult = out.result
	a.updateCheckMsgKey = out.msgKey
	a.updateCheckMsgArgs = out.msgArgs
	a.updateCheckErr = out.err
	a.checkUpdateCh = nil
	a.checkUpdateMu.Unlock()
	ch <- checkUpdateRes{result: out.result, err: out.err}
	return out.result, out.err
}

type checkUpdateRes struct {
	result *UpdateCheckResult
	err    error
}

// updateCheckCached 返回缓存的上次检查结果（若仍在有效期内），否则返回 nil。
// 调用方须持有 checkUpdateMu。命中时用当前语言重生成消息，避免缓存来自
// 不同语言设置时的文案错位。
func (a *App) updateCheckCached(lang string) *checkUpdateRes {
	if a.updateCheckResult == nil && a.updateCheckErr == nil {
		return nil
	}
	ttl := updateCheckTTL
	if a.updateCheckRateLtd {
		ttl = updateCheckRateLimitBackoff
	}
	if time.Since(a.updateCheckAt) >= ttl {
		return nil
	}
	out := &checkUpdateRes{err: a.updateCheckErr}
	if a.updateCheckResult != nil {
		cp := *a.updateCheckResult
		cp.Message = i18n.T(lang, a.updateCheckMsgKey, a.updateCheckMsgArgs)
		out.result = &cp
	}
	return out
}

// checkUpdateInternalOut 是 checkUpdateInternal 的返回载体，附带消息 i18n 信息
// 与是否限流标记，供 CheckUpdate 写入缓存。
type checkUpdateInternalOut struct {
	result      *UpdateCheckResult
	msgKey      string
	msgArgs     map[string]interface{}
	rateLimited bool
	err         error
}

func (a *App) checkUpdateInternal() checkUpdateInternalOut {
	current := currentAppVersion()
	a.mu.RLock()
	lang := i18n.ResolveLanguage(a.cfg.Settings.Language)
	a.mu.RUnlock()

	// 使用 GitHub releases.atom 静态订阅源而非 REST API：
	// atom 订阅源不受未认证 API 限流影响，可放心频繁检查而不触发"请求过于频繁"。
	// 直连 github.com（不走代理），与更新包下载解耦；下载仍在 StartDownloadUpdate 走代理。
	url := fmt.Sprintf("https://github.com/%s/releases.atom", githubRepo)

	var lastErr error
	for attempt := 0; attempt < 3; attempt++ {
		if attempt > 0 {
			time.Sleep(1 * time.Second)
		}

		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
		if err != nil {
			cancel()
			lastErr = err
			continue
		}
		req.Header.Set("Accept", "application/atom+xml")
		req.Header.Set("User-Agent", fmt.Sprintf("AgentPack/%s (%s; %s)", current, runtime.GOOS, runtime.GOARCH))

		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			cancel()
			lastErr = err
			continue
		}

		body, rerr := io.ReadAll(io.LimitReader(resp.Body, maxReleaseBodySize+1))
		resp.Body.Close()
		cancel()

		if resp.StatusCode == http.StatusNotFound {
			return checkUpdateInternalOut{
				result: &UpdateCheckResult{
					HasUpdate:      false,
					CurrentVersion: current,
					LatestVersion:  current,
					Message:        i18n.T(lang, "update.message.noRelease"),
				},
				msgKey: "update.message.noRelease",
			}
		}

		if resp.StatusCode == 403 || resp.StatusCode == 429 {
			// atom 一般不进限流，但稳妥起见按限流退避处理
			return checkUpdateInternalOut{
				result: &UpdateCheckResult{
					HasUpdate:      false,
					CurrentVersion: current,
					LatestVersion:  current,
					Message:        i18n.T(lang, "update.message.rateLimited"),
				},
				msgKey:      "update.message.rateLimited",
				rateLimited: true,
			}
		}

		if resp.StatusCode != http.StatusOK {
			lastErr = fmt.Errorf("GitHub atom feed 返回 %d", resp.StatusCode)
			continue
		}
		if rerr != nil {
			lastErr = rerr
			continue
		}
		if len(body) > maxReleaseBodySize {
			lastErr = fmt.Errorf("release feed body exceeds %d bytes", maxReleaseBodySize)
			continue
		}

		var feed atomFeed
		if err := xml.Unmarshal(body, &feed); err != nil {
			lastErr = err
			continue
		}

		// 取最新"正式版"（跳过预发布后缀），与 REST /releases/latest 语义一致
		var entry *atomEntry
		for i := range feed.Entries {
			tag := atomEntryTag(&feed.Entries[i])
			version := strings.TrimPrefix(tag, "v")
			if version != "" && preReleaseSuffix(version) == "" {
				entry = &feed.Entries[i]
				break
			}
		}
		// 无任何正式版发布视为"无发布"
		if entry == nil {
			return checkUpdateInternalOut{
				result: &UpdateCheckResult{
					HasUpdate:      false,
					CurrentVersion: current,
					LatestVersion:  current,
					Message:        i18n.T(lang, "update.message.noRelease"),
				},
				msgKey: "update.message.noRelease",
			}
		}

		tag := atomEntryTag(entry)
		latest := strings.TrimPrefix(tag, "v")
		hasUpdate := compareVersions(current, latest) < 0

		// atom 不携带资产下载链接，按 CI 命名规则确定性构造；体积未知(0)，由下载进度回填
		downloadURL, downloadName := "", ""
		if hasUpdate {
			downloadURL, downloadName = buildDownloadAsset(latest)
		}

		msgKey := "update.message.latest"
		msgArgs := map[string]interface{}{"version": current}
		message := i18n.T(lang, "update.message.latest", map[string]interface{}{"version": current})
		if hasUpdate {
			msgKey = "update.message.hasUpdate"
			msgArgs = map[string]interface{}{"version": latest}
			message = i18n.T(lang, "update.message.hasUpdate", map[string]interface{}{"version": latest})
		}

		return checkUpdateInternalOut{
			result: &UpdateCheckResult{
				HasUpdate:      hasUpdate,
				CurrentVersion: current,
				LatestVersion:  latest,
				Message:        message,
				Changelog:      html.UnescapeString(entry.Content.Body),
				ReleaseURL:     atomEntryReleaseURL(entry),
				DownloadURL:    downloadURL,
				DownloadSize:   0,
				DownloadName:   downloadName,
			},
			msgKey:  msgKey,
			msgArgs: msgArgs,
		}
	}

	return checkUpdateInternalOut{
		result: &UpdateCheckResult{
			HasUpdate:      false,
			CurrentVersion: current,
			LatestVersion:  current,
			Message:        i18n.T(lang, "update.message.networkFailed", map[string]interface{}{"error": lastErr.Error()}),
		},
		msgKey:  "update.message.networkFailed",
		msgArgs: map[string]interface{}{"error": lastErr.Error()},
	}
}

// atomEntryTag 从 atom entry 提取发布 tag（如 "v1.2.3"）。优先取 alternate 链接
// /releases/tag/ 后的部分；取不到则回退到 id 的最后一段。
func atomEntryTag(e *atomEntry) string {
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

// atomEntryReleaseURL 返回发布的 HTML 页面 URL。
func atomEntryReleaseURL(e *atomEntry) string {
	for _, l := range e.Links {
		if l.Rel == "alternate" && l.Href != "" {
			return l.Href
		}
	}
	return ""
}

// buildDownloadAsset 按 CI 命名规则确定性构造安装包下载 URL 与文件名，
// 与 .github/workflows/build.yml 的产物命名保持一致，无需依赖 API 资产列表。
func buildDownloadAsset(version string) (url, name string) {
	return buildDownloadAssetFor(version, runtime.GOOS, runtime.GOARCH)
}

// buildDownloadAssetFor 是 buildDownloadAsset 的平台参数化版本，便于测试覆盖各端。
func buildDownloadAssetFor(version, goos, goarch string) (url, name string) {
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
	base := fmt.Sprintf("https://github.com/%s/releases/download/v%s/%s", githubRepo, v, asset)
	return base, asset
}

func compareVersions(a, b string) int {
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
	aPre := preReleaseSuffix(a)
	bPre := preReleaseSuffix(b)
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

// comparePreRelease 按 semver 规则比较两个预发布后缀：
// 以 "." 分段；数字段按数值比较（beta.10 > beta.2）；字母段按 ASCII；
// 数字标识符 < 字母标识符；短后缀 < 长后缀（同一前缀时）。

// preReleaseSuffix 返回版本字符串的预发布后缀（"-" 之后的部分），无后缀返回 ""。
func preReleaseSuffix(v string) string {
	if idx := strings.Index(v, "-"); idx >= 0 {
		return v[idx+1:]
	}
	return ""
}

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

func (a *App) GetAppVersion() string {
	return currentAppVersion()
}

func downloadDir() string {
	switch runtime.GOOS {
	case "windows":
		if home := os.Getenv("USERPROFILE"); home != "" {
			if fi, err := os.Stat(filepath.Join(home, "Downloads")); err == nil && fi.IsDir() {
				return filepath.Join(home, "Downloads")
			}
		}
	case "darwin":
		if xdg := os.Getenv("XDG_DOWNLOAD_DIR"); xdg != "" {
			return xdg
		}
		if home, err := os.UserHomeDir(); err == nil {
			if fi, err := os.Stat(filepath.Join(home, "Downloads")); err == nil && fi.IsDir() {
				return filepath.Join(home, "Downloads")
			}
		}
	default:
		if xdg := os.Getenv("XDG_DOWNLOAD_DIR"); xdg != "" {
			return xdg
		}
		if home, err := os.UserHomeDir(); err == nil {
			if fi, err := os.Stat(filepath.Join(home, "Downloads")); err == nil && fi.IsDir() {
				return filepath.Join(home, "Downloads")
			}
		}
	}
	return os.TempDir()
}

// StartDownloadUpdate 开始一次全新的下载（从头开始）
func (a *App) StartDownloadUpdate(url string) error {
	a.mu.Lock()
	// 新下载：清理任何遗留的暂停状态与临时文件
	if a.downloadPausedFile != "" {
		os.Remove(a.downloadPausedFile)
		a.downloadPausedFile = ""
	}
	// 上次下载完成/失败后再次下载：把状态复位回 Idle，否则 startDownload 的
	// 并发守卫（仅 Idle/Paused 放行）会拒绝本次新下载，且旧的 completed 路径
	// 在 downloadURL 清空后无法通过 ResumeDownload 复用。
	if a.downloadState == DownloadStateCompleted || a.downloadState == DownloadStateError {
		a.downloadState = DownloadStateIdle
		a.downloadedFile = ""
	}
	a.downloadOffset = 0
	a.mu.Unlock()
	atomic.StoreInt32(&a.paused, 0)
	return a.startDownload(url, 0, false)
}

// startDownload 执行下载，offset > 0 时通过 Range 请求实现断点续传。
// resume 为 true 时，url/offset 在持锁状态下从 a.downloadURL/a.downloadOffset 读取，
// 与状态转换处于同一临界区，避免 ResumeDownload 读取与执行之间被 CancelDownload
// 清空状态的检查-使用竞态。
func (a *App) startDownload(url string, offset int64, resume bool) error {
	if !resume && !strings.HasPrefix(url, config.DefaultGitHubProxy) {
		url = config.DefaultGitHubProxy + strings.TrimPrefix(strings.TrimPrefix(url, "https://"), "http://")
	}

	// 总时长限制：防止服务器接受连接后不发送数据导致 goroutine 永久阻塞
	ctx, cancel := context.WithTimeout(context.Background(), maxDownloadDuration)
	a.mu.Lock()
	// 并发保护：仅允许从 Idle 或 Paused 状态启动下载。
	// 防止 ResumeDownload 与 StartDownloadUpdate 并发调用 startDownload 导致
	// 两次 os.Create(O_TRUNC) 截断同一文件并交错写入。
	if a.downloadState != DownloadStateIdle && a.downloadState != DownloadStatePaused {
		a.mu.Unlock()
		cancel()
		return fmt.Errorf("download already in progress")
	}
	if resume {
		// 在持锁状态下读取保存的续传参数并完成状态转换
		url = a.downloadURL
		offset = a.downloadOffset
		if url == "" {
			a.mu.Unlock()
			cancel()
			return fmt.Errorf("no saved download position")
		}
		// 暂停发生在 0 字节（请求尚未返回就点了暂停）时保存的 offset==0：
		// 若在此拒绝，状态停留在 Paused 且 URL 不清空，用户再点恢复必然再次
		// 失败，永久卡死；退化为从头下载（os.Create 覆盖同一临时文件）保持续传语义。
		if offset < 0 {
			offset = 0
		}
		a.downloadPausedFile = ""
	}
	a.downloadURL = url
	a.downloadState = DownloadStateDownloading
	if a.downloadCancel != nil {
		a.downloadCancel()
	}
	a.downloadCtx = ctx
	a.downloadCancel = cancel
	a.downloadDone = make(chan struct{})
	done := a.downloadDone
	// in-flight 登记必须在同一临界区内完成：若在解锁后登记，此窗口内
	// CancelDownload 可并发取消 ctx 并把状态复位为 Idle，随后 goroutine
	// 仍带着已取消的 ctx 启动，用户取消后仍会收到下载失败事件。
	if err := a.beginInFlightLocked(); err != nil {
		a.mu.Unlock()
		cancel()
		close(done)
		a.mu.Lock()
		a.downloadState = DownloadStateIdle
		a.downloadOffset = 0
		a.downloadURL = ""
		a.downloadCancel = nil
		a.downloadDone = nil
		a.mu.Unlock()
		return err
	}
	a.mu.Unlock()

	go func() {
		defer a.endInFlight()
		defer cancel()
		defer close(done)
		a.mu.RLock()
		lang := i18n.ResolveLanguage(a.cfg.Settings.Language)
		a.mu.RUnlock()

		emitError := func(msgKey string, args map[string]interface{}) {
			a.mu.Lock()
			a.downloadState = DownloadStateError
			a.mu.Unlock()
			a.emit("update:download:error", map[string]string{"message": i18n.T(lang, msgKey, args)})
		}

		// 在发起请求前确定临时文件路径：resume 时暂停文件（dlPath + ".downloading"）
		// 已存在，若请求构造/网络/状态码等后续分支失败仅 emitError 而不清理，
		// 且 downloadPausedFile 已被清空、状态已置 Error，该文件将永久残留
		// 在 Downloads 目录（下次同 URL 下载靠 os.Create 覆盖才能回收）。
		// 剥离 query：代理改写后的 URL 可能带 ?xxx 参数，filepath.Base 不会
		// 去掉它，Windows 上 os.Create 对含 ? 的文件名直接失败
		dlName := strings.Split(filepath.Base(url), "?")[0]
		dlPath := filepath.Join(downloadDir(), dlName)
		dlTmpPath := dlPath + ".downloading"
		removeTmp := func() { os.Remove(dlTmpPath) }

		req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
		if err != nil {
			removeTmp()
			emitError("update.download.failed", map[string]interface{}{"error": err.Error()})
			return
		}
		req.Header.Set("User-Agent", "AgentPack/"+currentAppVersion())
		// 更新安装包必须取最新版本，禁用 HTTP 缓存：代理/CDN 若缓存了旧版本，
		// 用户会拿到过期安装包。此头确保每次下载都从源站拉最新文件。
		req.Header.Set("Cache-Control", "no-cache, no-store, must-revalidate")
		req.Header.Set("Pragma", "no-cache")
		if offset > 0 {
			req.Header.Set("Range", fmt.Sprintf("bytes=%d-", offset))
		}

		resp, err := downloadHTTPClient.Do(req)
		if err != nil {
			removeTmp()
			emitError("update.download.failed", map[string]interface{}{"error": err.Error()})
			return
		}
		defer resp.Body.Close()

		if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusPartialContent {
			removeTmp()
			emitError("update.download.serverError", map[string]interface{}{"code": resp.StatusCode})
			return
		}

		// 下载体积上限：Content-Length 预检（Range 续传时按完整大小判断）
		if resp.ContentLength > 0 {
			totalExpected := resp.ContentLength
			if resp.StatusCode == http.StatusPartialContent {
				totalExpected += offset
			}
			if totalExpected > maxUpdateDownloadSize {
				removeTmp()
				emitError("update.download.failed", map[string]interface{}{"error": fmt.Sprintf("file too large: %d bytes (max %d)", totalExpected, maxUpdateDownloadSize)})
				return
			}
		}

		if dlName == "." || dlName == ".." {
			removeTmp()
			emitError("update.download.failed", map[string]interface{}{"error": "invalid download filename"})
			return
		}

		// 服务器忽略 Range（返回 200）时无法续传，退化为从头下载
		resumed := offset > 0 && resp.StatusCode == http.StatusPartialContent
		var downloaded int64
		var f *os.File
		if resumed {
			f, err = os.OpenFile(dlTmpPath, os.O_APPEND|os.O_WRONLY, 0600)
			if err == nil {
				// 校验临时文件实际大小与记录的偏移一致，防止暂停期间文件被
				// 外部截断/覆盖后以错误偏移追加（产生重叠或空洞的损坏文件）。
				if fi, statErr := f.Stat(); statErr != nil || fi.Size() != offset {
					f.Close()
					f = nil
				} else {
					downloaded = offset
				}
			}
		}
		if f == nil {
			// 续传目标文件缺失或尺寸不符：无法安全消费 206 流（该流从 offset 起），
			// 必须从头重新请求。当前响应直接作废。
			if resumed {
				os.Remove(dlTmpPath)
				emitError("update.download.failed", map[string]interface{}{"error": "cannot resume: temp file missing or size mismatch"})
				return
			}
			os.Remove(dlTmpPath)
			f, err = os.Create(dlTmpPath)
			if err != nil {
				emitError("update.download.failed", map[string]interface{}{"error": err.Error()})
				return
			}
			downloaded = 0
		}
		defer func() {
			if f != nil {
				f.Close()
			}
		}()

		// totalSize 统一为文件总大小：206 响应的 ContentLength 只是剩余部分
		totalSize := resp.ContentLength
		if resumed && totalSize > 0 {
			totalSize += offset
		}

		lastTime := time.Now()
		lastBytes := downloaded
		// 读缓冲越大越能摊薄 syscall / 循环开销。在高速链路 / 高频代理
		// （gh-proxy.com 远端拉取再回传）下，越小越跑不满带宽。1MB 对下载
		// 安装包这类大文件显著提速，内存开销（1MB）可忽略；暂停/取消仍在
		// 每次 Read 之间检查，单次填充耗时在慢速链路上仅毫秒级。
		buf := make([]byte, 1024*1024)

		for {
			// 暂停：保留临时文件与偏移量，等待 ResumeDownload
			if atomic.LoadInt32(&a.paused) != 0 {
				atomic.StoreInt32(&a.paused, 0)
				f.Close()
				f = nil // 置 nil，避免 defer 对同一句柄二次 Close
				// 取消竞态：暂停请求与 CancelDownload 并发时，若取消已经触发
				// （ctx 已 done），禁止把状态"复活"回 Paused——否则 downloadURL 已被
				// CancelDownload 清空，ResumeDownload 报 "no saved download position"，
				// 用户永远无法再恢复。
				if ctx.Err() != nil {
					removeTmp()
					return
				}
				a.mu.Lock()
				a.downloadPausedFile = dlTmpPath
				a.downloadOffset = downloaded
				a.downloadState = DownloadStatePaused
				a.mu.Unlock()
				percent := 0.0
				if totalSize > 0 {
					percent = float64(downloaded) / float64(totalSize) * 100
				}
				a.emit("update:download:paused", map[string]interface{}{
					"downloaded": downloaded,
					"total":      totalSize,
					"percent":    percent,
					"fileName":   dlName,
				})
				return
			}
			select {
			case <-ctx.Done():
				removeTmp()
				// 区分主动取消与超时：主动取消由 CancelDownload 复位状态并清理，
				// 超时（30min 上限）则必须复位状态并通知前端，否则状态残留
				// Downloading 导致 UI 永久卡死且后续下载被并发守卫拒绝。
				if ctx.Err() == context.DeadlineExceeded {
					a.mu.Lock()
					a.downloadState = DownloadStateError
					a.downloadURL = ""
					a.downloadOffset = 0
					a.mu.Unlock()
					emitError("update.download.failed", map[string]interface{}{"error": fmt.Sprintf("download timed out after %s", maxDownloadDuration)})
				}
				return
			default:
			}
			n, readErr := resp.Body.Read(buf)
			if n > 0 {
				// 流式大小限制：防止无 Content-Length 的响应无限写入磁盘
				if downloaded+int64(n) > maxUpdateDownloadSize {
					removeTmp()
					emitError("update.download.failed", map[string]interface{}{"error": fmt.Sprintf("file exceeds %d bytes", maxUpdateDownloadSize)})
					return
				}
				if _, writeErr := f.Write(buf[:n]); writeErr != nil {
					removeTmp()
					emitError("update.download.failed", map[string]interface{}{"error": writeErr.Error()})
					return
				}
				downloaded += int64(n)
				if time.Since(lastTime) > 200*time.Millisecond {
					speed := float64(downloaded-lastBytes) / time.Since(lastTime).Seconds()
					percent := 0.0
					if totalSize > 0 {
						percent = float64(downloaded) / float64(totalSize) * 100
					}
					a.emit("update:download:progress", map[string]interface{}{
						"downloaded": downloaded,
						"total":      totalSize,
						"speed":      speed,
						"percent":    percent,
					})
					lastTime = time.Now()
					lastBytes = downloaded
				}
			}
			if readErr == io.EOF {
				// 完整性校验：已知总大小时必须校验下载字节数，防止服务器提前断开
				// 或 CDN 截断导致的残缺文件被当作完成安装包。
				if totalSize > 0 && downloaded != totalSize {
					removeTmp()
					emitError("update.download.failed", map[string]interface{}{"error": fmt.Sprintf("incomplete download: got %d bytes, expected %d", downloaded, totalSize)})
					return
				}
				// 分块传输（无 Content-Length，totalSize <= 0）时无法获知预期大小，
				// 但至少拒绝 0 字节的"空完成"（安装包不可能为空；续传场景
				// downloaded=offset>0 天然通过）。
				if totalSize <= 0 && downloaded == 0 {
					removeTmp()
					emitError("update.download.failed", map[string]interface{}{"error": "empty download: server sent no data"})
					return
				}
				break
			}
			if readErr != nil {
				// 用户主动取消（CancelDownload）时静默退出，不再把状态改写为 Error，
				// 避免取消后 UI 仍收到"下载失败"的错误事件。
				if ctx.Err() == context.DeadlineExceeded {
					removeTmp()
					a.mu.Lock()
					a.downloadState = DownloadStateError
					a.downloadURL = ""
					a.downloadOffset = 0
					a.mu.Unlock()
					emitError("update.download.failed", map[string]interface{}{"error": fmt.Sprintf("download timed out after %s", maxDownloadDuration)})
					return
				}
				if ctx.Err() != nil {
					removeTmp()
					return
				}
				removeTmp()
				emitError("update.download.failed", map[string]interface{}{"error": readErr.Error()})
				return
			}
		}
		f.Close()
		f = nil

		// 下载完成后重命名: .downloading → 正式文件名
		if err := os.Rename(dlTmpPath, dlPath); err != nil {
			removeTmp()
			emitError("update.download.failed", map[string]interface{}{"error": err.Error()})
			return
		}

		a.mu.Lock()
		a.downloadState = DownloadStateCompleted
		a.downloadPausedFile = ""
		a.downloadOffset = 0
		a.downloadURL = ""
		a.downloadedFile = dlPath
		a.mu.Unlock()

		// 不自动启动安装程序与退出：由前端提示用户确认后调用 InstallUpdate
		a.emit("update:download:complete", map[string]interface{}{
			"filePath": dlPath,
			"fileName": dlName,
		})
	}()

	return nil
}

// InstallUpdate 启动已下载的安装程序并退出应用。
// 必须退出主程序，否则 Windows 上安装器无法覆盖正在运行的 exe。
func (a *App) InstallUpdate() error {
	a.mu.RLock()
	dlPath := a.downloadedFile
	a.mu.RUnlock()
	if dlPath == "" {
		return fmt.Errorf("no downloaded installer")
	}
	if _, err := os.Stat(dlPath); err != nil {
		return fmt.Errorf("installer not found: %w", err)
	}

	// 防御性校验：确认下载到的是可执行的安装程序，避免下载到 zip/HTML 错误页后
	// exec 启动失败、用户只看到笼统的"启动安装程序失败"。
	if err := validateUpdateInstaller(dlPath); err != nil {
		return err
	}

	// 完全脱离父进程启动安装程序
	// Windows 安装器为 user 级（RequestExecutionLevel=user，无需 UAC 提权），
	// 用 CreateProcess（exec.Command）即可直接启动；不经 cmd.exe，避免文件名中的
	// & 等 cmd.exe 元字符导致命令注入。非 Windows 平台用 open/xdg-open。
	switch runtime.GOOS {
	case "windows":
		cmd := exec.Command(dlPath)
		hideConsoleWindow(cmd)
		if err := cmd.Start(); err != nil {
			return err
		}
	case "darwin":
		if err := exec.Command("open", dlPath).Start(); err != nil {
			return err
		}
	default:
		if err := exec.Command("xdg-open", dlPath).Start(); err != nil {
			return err
		}
	}

	go func() {
		time.Sleep(1 * time.Second)
		// 安装程序启动后必须退出主程序（Windows 安装器需覆盖正在运行的 exe）。
		// 不走 a.Quit()：其 inFlight 门控会拦截退出——安装/备份等后台任务在途时
		// 应用永远不退出，安装器覆盖失败。此处直接放行（跳过 inFlight 检查），
		// ServiceShutdown 内部仍有 5s 在途任务等待 + 2s 下载清理兜底。
		a.mu.Lock()
		a.allowClose = true
		a.mu.Unlock()
		a.wailsApp.Quit()
	}()
	return nil
}

// validateUpdateInstaller 校验下载的更新文件确实是可执行的安装程序。
// Windows 上安装包为 .exe/.msi；非 Windows 平台由 open/xdg-open 处理任意文件，跳过校验。
func validateUpdateInstaller(path string) error {
	if runtime.GOOS != "windows" {
		return nil
	}
	ext := strings.ToLower(filepath.Ext(path))
	if ext != ".exe" && ext != ".msi" {
		return fmt.Errorf("下载的更新文件不是可执行的安装程序: %s", filepath.Base(path))
	}
	return nil
}

func (a *App) CancelDownload() error {
	a.mu.Lock()
	var done chan struct{}
	if a.downloadCancel != nil {
		a.downloadCancel()
		a.downloadCancel = nil
		done = a.downloadDone
	}
	// 暂停状态下取消：主动删除保留的临时文件
	if a.downloadPausedFile != "" {
		os.Remove(a.downloadPausedFile)
		a.downloadPausedFile = ""
	}
	a.downloadState = DownloadStateIdle
	a.downloadOffset = 0
	a.downloadURL = ""
	atomic.StoreInt32(&a.paused, 0)
	a.mu.Unlock()

	// 等待旧下载 goroutine 完全退出后再返回，确保其 removeTmp/defer 清理
	// 不会与紧接着的 StartDownloadUpdate 竞争同一临时文件。
	// 下载 goroutine 的 ctx 有 30 分钟上限但取消后应秒退；3 秒兜底防止
	// 极端卡死（网络读不返回）时此调用无限阻塞应用退出流程。
	if done != nil {
		select {
		case <-done:
		case <-time.After(3 * time.Second):
			log.Printf("CancelDownload: download goroutine did not exit within 3s, tmp cleanup may race next download")
		}
	}
	return nil
}

func parseVersionParts(v string) []int {
	v = strings.TrimPrefix(v, "v")
	if idx := strings.IndexAny(v, "-+"); idx >= 0 {
		v = v[:idx]
	}
	parts := strings.Split(v, ".")
	result := make([]int, 0, len(parts))
	for _, p := range parts {
		n := 0
		for _, ch := range p {
			if ch < '0' || ch > '9' {
				break
			}
			n = n*10 + int(ch-'0')
		}
		result = append(result, n)
	}
	return result
}
