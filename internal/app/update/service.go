// Package update 提供应用更新的完整服务：GitHub releases.atom 订阅源解析、
// 版本比较、按 CI 命名规则构造下载地址（见 update.go），以及下载/暂停/续传/
// 安装的状态机（本文件 Service）。Service 不持有 App 状态，通过回调与宿主
// 解耦（事件触发、语言、安装后退出），可独立测试。
package update

import (
	"context"
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
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"agentpack/internal/appmeta"
	"agentpack/internal/config"
	"agentpack/internal/i18n"
)

// DownloadState 下载状态
type DownloadState int

const (
	StateIdle        DownloadState = iota // 空闲
	StateDownloading                      // 下载中
	StatePaused                           // 已暂停
	StateCompleted                        // 完成
	StateError                            // 错误
)

// StateNames 与 DownloadState 状态顺序一一对应。
var StateNames = [...]string{"idle", "downloading", "paused", "complete", "error"}

// maxUpdateDownloadSize 限制更新安装包最大体积（1GB），防止恶意/异常源无限流数据写满磁盘。
const maxUpdateDownloadSize int64 = 1 << 30

// maxDownloadDuration 限制单次下载总时长（30 分钟），防止服务器接受连接后
// 不发送数据（stalled）导致的 goroutine 永久阻塞。
const maxDownloadDuration = 30 * time.Minute

// updateCheckTTL 距上次成功检查后，复用缓存结果而不再请求 GitHub API 的时长。
// GitHub 未认证 API 限制为 60 次/小时/IP；配合端上"每会话仅启动静默检查一次"，
// 10 分钟 TTL 将单实例的检查频率压到 ≤6 次/小时，避免触发限流。
const updateCheckTTL = 10 * time.Minute

// updateCheckRateLimitBackoff 上次检查命中限流(403/429)后的退避时长。
// 限流状态下继续请求只会继续触发限流，用更长退避让限流窗口过去再重试。
const updateCheckRateLimitBackoff = 30 * time.Minute

// downloadHTTPClient 用于更新检查与更新包下载：Transport 设置 ResponseHeaderTimeout，
// 防止服务器接受连接后不响应头部导致的永久阻塞；总时长由调用方的
// context.WithTimeout 兜底。
var downloadHTTPClient = &http.Client{
	Transport: &http.Transport{
		ResponseHeaderTimeout: 30 * time.Second,
	},
}

// Service 管理更新检查与安装包下载/暂停/续传/安装。所有下载状态受内部 mu 保护。
type Service struct {
	mu    sync.Mutex
	state DownloadState
	// paused 暂停标志（原子操作，0=否，1=是）
	paused     int32
	cancel     context.CancelFunc
	done       chan struct{} // 当前下载 goroutine 结束信号
	pausedFile string        // 暂停时保存的临时文件路径
	offset     int64         // 暂停时的下载偏移量
	url        string        // 当前下载的 URL
	file       string        // 下载完成的安装包路径（供 Install 使用）
	dlTmpPath  string        // 当前下载的临时文件路径（.downloading），用于 Shutdown 兜底清理
	// tmpRemoved 记录 dlTmpPath 是否已被 Cleanup/Shutdown 移除。
	// 用 int32 而非 bool：Go 对 struct 中 bool 字段不能直接原子读写，
	// 用 int32 配合 atomic 操作保证 Shutdown 与下载 goroutine 的 os.Remove 互斥，
	// 避免并发两次 Remove 同一文件产生多余的 ErrNotExist 系统调用。
	tmpRemoved int32 // atomic: 0=未移除，1=已移除

	// CheckUpdate 的 singleflight + 结果缓存状态（checkMu 保护）。
	checkMu      sync.Mutex
	checkCh      chan checkRes
	checkAt      time.Time
	checkRateLtd bool
	checkResult  *UpdateCheckResult
	checkMsgKey  string
	checkMsgArgs map[string]interface{}
	checkErr     error

	// 与宿主的解耦回调
	emit          func(event string, data ...interface{})
	lang          func() string // 返回 "zh-CN"/"en"，用于 i18n 消息
	installExit   func()        // 安装器启动后退出应用（Windows 需覆盖 exe）
	beginInFlight func() error  // 下载开始时登记宿主 in-flight（关闭门控），失败表示应用关闭中
	endInFlight   func()        // 下载结束时释放宿主 in-flight
}

// NewService 创建更新服务。emit 用于触发前端事件；lang 返回当前语言；
// installExit 在安装器成功启动后回调（宿主应退出应用）；beginInFlight/endInFlight
// 由宿主接入下载的 in-flight 门控（关闭流程在下载进行中阻止退出）。
func NewService(emit func(string, ...interface{}), lang func() string, installExit func(), beginInFlight func() error, endInFlight func()) *Service {
	return &Service{
		state:         StateIdle,
		emit:          emit,
		lang:          lang,
		installExit:   installExit,
		beginInFlight: beginInFlight,
		endInFlight:   endInFlight,
	}
}

// Shutdown 取消活动下载并等待其清理（removeTmp）。供宿主关闭流程调用。
func (s *Service) Shutdown() {
	s.mu.Lock()
	var dlDone chan struct{}
	if s.cancel != nil {
		s.cancel()
		dlDone = s.done
	}
	// 兜底清理：进程退出前无法等待 goroutine 完成时，直接删除临时文件。
	// 用 atomic 标志与 goroutine 内的 removeTmp 互斥：已标记则跳过，
	// 避免 goroutine 后续也 Remove 已删除的文件产生无意义的 ErrNotExist。
	tmpPath := s.dlTmpPath
	if tmpPath != "" {
		s.mu.Unlock()
		if atomic.CompareAndSwapInt32(&s.tmpRemoved, 0, 1) {
			if err := os.Remove(tmpPath); err != nil && !os.IsNotExist(err) {
				log.Printf("update: remove stale tmp on shutdown %s: %v", tmpPath, err)
			}
		}
	} else {
		s.mu.Unlock()
	}
	if dlDone != nil {
		select {
		case <-dlDone:
		case <-time.After(2 * time.Second):
			log.Printf("update: download goroutine did not exit within 2s, tmp file may remain")
		}
	}
}

// ---------- 检查更新 ----------

type checkRes struct {
	result *UpdateCheckResult
	err    error
}

// CheckUpdate 检查是否有新版本。带 singleflight + 时间缓存：
//   - singleflight：启动静默检查与 Settings 页手动检查可能并发触发，
//     重复发起会导致两次 GitHub API 请求和两次事件；waiter 复用首者的结果。
//   - 时间缓存：距上次检查仍在 TTL/限流退避窗口内时直接复用缓存结果，
//     避免高频请求触发未认证限流（60 次/小时/IP）。
func (s *Service) CheckUpdate() (res *UpdateCheckResult, err error) {
	lang := s.lang()

	s.checkMu.Lock()
	if s.checkCh != nil {
		ch := s.checkCh
		s.checkMu.Unlock()
		res := <-ch
		return res.result, res.err
	}
	if cached := s.updateCheckCached(lang); cached != nil {
		s.checkMu.Unlock()
		return cached.result, cached.err
	}
	ch := make(chan checkRes, 1)
	s.checkCh = ch
	s.checkMu.Unlock()

	// panic 兜底：checkUpdateInternal 意外 panic 时 waiter 会永久阻塞
	// 宿主调用线程且 checkCh 不清零（后续检查全部排队等待）。
	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("check update panic: %v", r)
			s.checkMu.Lock()
			s.checkCh = nil
			s.checkMu.Unlock()
			ch <- checkRes{err: err}
		}
	}()

	out := s.checkUpdateInternal()

	s.checkMu.Lock()
	s.checkAt = time.Now()
	s.checkRateLtd = out.rateLimited
	s.checkResult = out.result
	s.checkMsgKey = out.msgKey
	s.checkMsgArgs = out.msgArgs
	s.checkErr = out.err
	s.checkCh = nil
	s.checkMu.Unlock()
	ch <- checkRes{result: out.result, err: out.err}
	return out.result, out.err
}

// updateCheckCached 返回缓存的上次检查结果（若仍在有效期内），否则返回 nil。
// 调用方须持有 checkMu。命中时用当前语言重生成消息。
func (s *Service) updateCheckCached(lang string) *checkRes {
	if s.checkResult == nil && s.checkErr == nil {
		return nil
	}
	ttl := updateCheckTTL
	if s.checkRateLtd {
		ttl = updateCheckRateLimitBackoff
	}
	if time.Since(s.checkAt) >= ttl {
		return nil
	}
	out := &checkRes{err: s.checkErr}
	if s.checkResult != nil {
		cp := *s.checkResult
		cp.Message = i18n.T(lang, s.checkMsgKey, s.checkMsgArgs)
		out.result = &cp
	}
	return out
}

type checkUpdateInternalOut struct {
	result      *UpdateCheckResult
	msgKey      string
	msgArgs     map[string]interface{}
	rateLimited bool
	err         error
}

func (s *Service) checkUpdateInternal() checkUpdateInternalOut {
	current := appmeta.Version
	lang := s.lang()

	// 使用 GitHub releases.atom 静态订阅源而非 REST API：
	// atom 订阅源不受未认证 API 限流影响，可放心频繁检查而不触发"请求过于频繁"。
	// 直连 github.com（不走代理），与更新包下载解耦；下载仍在 StartDownload 走代理。
	url := fmt.Sprintf("https://github.com/%s/releases.atom", Repo)

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

		resp, err := downloadHTTPClient.Do(req)
		if err != nil {
			cancel()
			lastErr = err
			continue
		}

		body, rerr := io.ReadAll(io.LimitReader(resp.Body, MaxReleaseBodySize+1))
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
		if len(body) > MaxReleaseBodySize {
			lastErr = fmt.Errorf("release feed body exceeds %d bytes", MaxReleaseBodySize)
			continue
		}

		var feed AtomFeed
		if err := xml.Unmarshal(body, &feed); err != nil {
			lastErr = err
			continue
		}

		// 取最新"正式版"（跳过预发布后缀），与 REST /releases/latest 语义一致
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

		tag := AtomEntryTag(entry)
		latest := strings.TrimPrefix(tag, "v")
		hasUpdate := CompareVersions(current, latest) < 0

		// atom 不携带资产下载链接，按 CI 命名规则确定性构造；体积未知(0)，由下载进度回填
		downloadURL, downloadName := "", ""
		if hasUpdate {
			downloadURL, downloadName = BuildDownloadAsset(latest)
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
				ReleaseURL:     AtomEntryReleaseURL(entry),
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

// ---------- 下载 ----------

// StartDownload 开始一次全新的下载（从头开始）
func (s *Service) StartDownload(url string) error {
	s.mu.Lock()
	// 新下载：清理任何遗留的暂停状态与临时文件
	if s.pausedFile != "" {
		if err := os.Remove(s.pausedFile); err != nil && !os.IsNotExist(err) {
			// 删除失败不阻断新下载（同路径会被 os.Create 截断），仅记录便于排查磁盘占用
			log.Printf("update: remove stale paused file %s: %v", s.pausedFile, err)
		}
		s.pausedFile = ""
	}
	// 上次下载完成/失败后再次下载：把状态复位回 Idle，否则 startDownload 的
	// 并发守卫（仅 Idle/Paused 放行）会拒绝本次新下载。
	if s.state == StateCompleted || s.state == StateError {
		s.state = StateIdle
		s.file = ""
	}
	s.offset = 0
	s.mu.Unlock()
	atomic.StoreInt32(&s.paused, 0)
	return s.startDownload(url, 0, false)
}

// Pause 暂停当前正在进行的下载
func (s *Service) Pause() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.state != StateDownloading {
		return fmt.Errorf("no active download")
	}
	// 置位必须在持锁临界区内完成：下载 goroutine 在锁内做状态转换
	// (Downloading→Completed/Paused)。若解锁后再置位，窗口期内下载可能已完成。
	atomic.StoreInt32(&s.paused, 1)
	return nil
}

// Resume 恢复已暂停的下载
func (s *Service) Resume() error {
	s.mu.Lock()
	if s.state != StatePaused {
		s.mu.Unlock()
		return fmt.Errorf("no paused download to resume")
	}
	atomic.StoreInt32(&s.paused, 0)
	s.mu.Unlock()
	// 由 startDownload(resume=true) 在持锁时从 s.url/s.offset 读取并转换状态，
	// 消除与 Cancel 清空状态之间的检查-使用竞态。
	return s.startDownload("", 0, true)
}

// GetState 返回当前下载状态、暂停文件名与偏移量（供前端查询）。
func (s *Service) GetState() (state string, fileName string, offset int64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if i := int(s.state); i >= 0 && i < len(StateNames) {
		state = StateNames[i]
	}
	// pausedFile 是完整临时路径（xxx.downloading），前端只展示文件名。
	name := s.pausedFile
	if name != "" {
		name = filepath.Base(name)
	}
	return state, name, s.offset
}

// Cancel 取消当前下载并清理临时文件
func (s *Service) Cancel() error {
	s.mu.Lock()
	var done chan struct{}
	if s.cancel != nil {
		s.cancel()
		s.cancel = nil
		done = s.done
	}
	// 暂停状态下取消：主动删除保留的临时文件
	if s.pausedFile != "" {
		if err := os.Remove(s.pausedFile); err != nil && !os.IsNotExist(err) {
			// 删除失败不阻断取消流程，仅记录便于排查临时文件残留
			log.Printf("update: remove paused file on cancel %s: %v", s.pausedFile, err)
		}
		s.pausedFile = ""
	}
	// 兜底清理：进程异常退出时可能残留的 .downloading 文件
	if s.dlTmpPath != "" {
		if err := os.Remove(s.dlTmpPath); err != nil && !os.IsNotExist(err) {
			log.Printf("update: remove stale tmp on cancel %s: %v", s.dlTmpPath, err)
		}
		s.dlTmpPath = ""
	}
	s.state = StateIdle
	s.offset = 0
	s.url = ""
	s.file = ""
	atomic.StoreInt32(&s.paused, 0)
	s.mu.Unlock()
	// 等待旧下载 goroutine 完全退出后再返回，确保其 removeTmp/defer 清理
	// 不会与紧接着的 StartDownload 竞争同一临时文件。
	// 下载 goroutine 的 ctx 有 30 分钟上限但取消后应秒退；3 秒兜底防止
	// 极端卡死（网络读不返回）时此调用无限阻塞应用退出流程。
	if done != nil {
		select {
		case <-done:
		case <-time.After(3 * time.Second):
			log.Printf("update: cancel download: goroutine did not exit within 3s, tmp cleanup may race next download")
		}
	}
	return nil
}

// startDownload 执行下载，offset > 0 时通过 Range 请求实现断点续传。
// resume 为 true 时，url/offset 在持锁状态下从 s.url/s.offset 读取，
// 与状态转换处于同一临界区，避免 Resume 读取与执行之间被 Cancel
// 清空状态的检查-使用竞态。
func (s *Service) startDownload(url string, offset int64, resume bool) error {
	if !resume && !strings.HasPrefix(url, config.DefaultGitHubProxy) {
		url = config.DefaultGitHubProxy + strings.TrimPrefix(strings.TrimPrefix(url, "https://"), "http://")
	}

	// 总时长限制：防止服务器接受连接后不发送数据导致 goroutine 永久阻塞
	ctx, cancel := context.WithTimeout(context.Background(), maxDownloadDuration)
	s.mu.Lock()
	// 并发保护：仅允许从 Idle 或 Paused 状态启动下载。
	// 防止 Resume 与 StartDownload 并发调用 startDownload 导致
	// 两次 os.Create(O_TRUNC) 截断同一文件并交错写入。
	if s.state != StateIdle && s.state != StatePaused {
		s.mu.Unlock()
		cancel()
		return fmt.Errorf("download already in progress")
	}
	if resume {
		// 在持锁状态下读取保存的续传参数并完成状态转换
		url = s.url
		offset = s.offset
		if url == "" {
			s.mu.Unlock()
			cancel()
			return fmt.Errorf("no saved download position")
		}
		// 暂停发生在 0 字节（请求尚未返回就点了暂停）时保存的 offset==0：
		// 退化为从头下载（os.Create 覆盖同一临时文件）保持续传语义。
		if offset < 0 {
			offset = 0
		}
		s.pausedFile = ""
	}
	s.url = url
	s.state = StateDownloading
	if s.cancel != nil {
		s.cancel()
	}
	s.cancel = cancel
	s.done = make(chan struct{})
	done := s.done
	// in-flight 登记必须在同一临界区内完成：若在解锁后登记，此窗口内
	// Cancel 可并发取消 ctx 并把状态复位为 Idle，随后 goroutine 仍带着
	// 已取消的 ctx 启动，用户取消后仍会收到下载失败事件。
	if s.beginInFlight != nil {
		if err := s.beginInFlight(); err != nil {
			s.state = StateIdle
			s.offset = 0
			s.url = ""
			s.cancel = nil
			s.done = nil
			s.mu.Unlock()
			cancel()
			return err
		}
	}
	// 在持锁状态下预先计算临时文件路径，使 Cancel/Shutdown 在持有锁的窗口内
	// 能读取到有效路径并完成清理；goroutine 内部通过 tmpRemoved 原子标志与
	// 这些清理路径互斥，避免并发 os.Remove。
	dlName := strings.Split(filepath.Base(url), "?")[0]
	dlPath := filepath.Join(downloadDir(), dlName)
	s.dlTmpPath = dlPath + ".downloading"
	s.mu.Unlock()

	go func() {
		defer func() {
			if s.endInFlight != nil {
				s.endInFlight()
			}
		}()
		defer close(done)
		defer cancel()
		lang := s.lang()

		emitError := func(msgKey string, args map[string]interface{}) {
			s.mu.Lock()
			s.state = StateError
			s.mu.Unlock()
			s.emit("update:download:error", map[string]string{"message": i18n.T(lang, msgKey, args)})
		}

		dlTmpPath := s.dlTmpPath
		removeTmp := func() {
			// 与 Shutdown 的 tmpRemoved 原子标志互斥：已移除则跳过，
			// 避免与 Shutdown 并发调用 os.Remove 产生多余的 ErrNotExist。
			if !atomic.CompareAndSwapInt32(&s.tmpRemoved, 0, 1) {
				return
			}
			os.Remove(dlTmpPath)
		}

		req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
		if err != nil {
			removeTmp()
			emitError("update.download.failed", map[string]interface{}{"error": err.Error()})
			return
		}
		req.Header.Set("User-Agent", "AgentPack/"+appmeta.Version)
		// 更新安装包必须取最新版本，禁用 HTTP 缓存。
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

		// 服务器忽略 Range（返回 200）时无法续传，退化为从头下载。
		// 续传仅在 206 且临时文件实际大小与记录偏移一致时追加，否则从头重建，
		// 防止以错误偏移追加产生重叠或空洞的损坏文件。
		resumed := offset > 0 && resp.StatusCode == http.StatusPartialContent
		var written int64
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
					written = offset
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
			written = 0
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
		lastBytes := written
		// 读缓冲不是限速，而是每次 Read 系统调用可取的最大字节数；值越大越能
		// 摊薄 syscall 开销、跑满带宽。预分配 4MB 大缓冲，对单连接下载已是
		// 满速级别（内存占用 4MB 可忽略）。暂停/取消仍在每次 Read 之间检查，
		// 单次填充耗时在慢速链路上仅数毫秒，不影响响应。
		buf := make([]byte, 4*1024*1024)

		for {
			// 暂停：保留临时文件与偏移量，等待 Resume
			if atomic.LoadInt32(&s.paused) != 0 {
				atomic.StoreInt32(&s.paused, 0)
				f.Close()
				f = nil // 置 nil，避免 defer 对同一句柄二次 Close
				// 取消竞态：暂停请求与 Cancel 并发时，若取消已经触发
				// （ctx 已 done），禁止把状态"复活"回 Paused——否则 url 已被
				// Cancel 清空，Resume 报 "no saved download position"，用户
				// 永远无法再恢复。
				if ctx.Err() != nil {
					removeTmp()
					return
				}
				s.mu.Lock()
				s.pausedFile = dlTmpPath
				s.offset = written
				s.state = StatePaused
				s.mu.Unlock()
				percent := 0.0
				if totalSize > 0 {
					percent = float64(written) / float64(totalSize) * 100
				}
				s.emit("update:download:paused", map[string]interface{}{
					"downloaded": written,
					"total":      totalSize,
					"percent":    percent,
					"fileName":   dlName,
				})
				return
			}
			select {
			case <-ctx.Done():
				removeTmp()
				// 区分主动取消与超时：主动取消由 Cancel 复位状态并清理，
				// 超时（30min 上限）则必须复位状态并通知前端，否则状态残留
				// Downloading 导致 UI 永久卡死且后续下载被并发守卫拒绝。
				if ctx.Err() == context.DeadlineExceeded {
					s.mu.Lock()
					s.state = StateError
					s.url = ""
					s.offset = 0
					s.mu.Unlock()
					emitError("update.download.failed", map[string]interface{}{"error": fmt.Sprintf("download timed out after %s", maxDownloadDuration)})
				}
				return
			default:
			}
			n, readErr := resp.Body.Read(buf)
			if n > 0 {
				// 流式大小限制：防止无 Content-Length 的响应无限写入磁盘
				if written+int64(n) > maxUpdateDownloadSize {
					removeTmp()
					emitError("update.download.failed", map[string]interface{}{"error": fmt.Sprintf("file exceeds %d bytes", maxUpdateDownloadSize)})
					return
				}
				if _, writeErr := f.Write(buf[:n]); writeErr != nil {
					removeTmp()
					emitError("update.download.failed", map[string]interface{}{"error": writeErr.Error()})
					return
				}
				written += int64(n)
				if time.Since(lastTime) > 200*time.Millisecond {
					speed := float64(written-lastBytes) / time.Since(lastTime).Seconds()
					percent := 0.0
					if totalSize > 0 {
						percent = float64(written) / float64(totalSize) * 100
					}
					s.emit("update:download:progress", map[string]interface{}{
						"downloaded": written,
						"total":      totalSize,
						"speed":      speed,
						"percent":    percent,
					})
					lastTime = time.Now()
					lastBytes = written
				}
			}
			if readErr == io.EOF {
				// 完整性校验：已知总大小时必须校验下载字节数，防止服务器提前断开
				// 或 CDN 截断导致的残缺文件被当作完成安装包。
				if totalSize > 0 && written != totalSize {
					removeTmp()
					emitError("update.download.failed", map[string]interface{}{"error": fmt.Sprintf("incomplete download: got %d bytes, expected %d", written, totalSize)})
					return
				}
				// 分块传输（无 Content-Length，totalSize <= 0）时无法获知预期大小，
				// 但至少拒绝 0 字节的"空完成"（安装包不可能为空；续传场景
				// written=offset>0 天然通过）。
				if totalSize <= 0 && written == 0 {
					removeTmp()
					emitError("update.download.failed", map[string]interface{}{"error": "empty download: server sent no data"})
					return
				}
				break
			}
			if readErr != nil {
				// 用户主动取消（Cancel）时静默退出，不再把状态改写为 Error，
				// 避免取消后 UI 仍收到"下载失败"的错误事件。
				if ctx.Err() == context.DeadlineExceeded {
					removeTmp()
					s.mu.Lock()
					s.state = StateError
					s.url = ""
					s.offset = 0
					s.mu.Unlock()
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

		s.mu.Lock()
		s.state = StateCompleted
		s.pausedFile = ""
		s.offset = 0
		s.url = ""
		s.file = dlPath
		s.mu.Unlock()

		// 不自动启动安装程序与退出：由前端提示用户确认后调用 InstallUpdate
		s.emit("update:download:complete", map[string]interface{}{
			"filePath": dlPath,
			"fileName": dlName,
		})
	}()
	return nil
}

// Install 启动已下载的安装程序并通知宿主退出应用。
// 必须退出主程序，否则 Windows 上安装器无法覆盖正在运行的 exe。
func (s *Service) Install() error {
	s.mu.Lock()
	dlPath := s.file
	s.mu.Unlock()
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
	// 注意：Windows 下直接使用 CreateProcess（exec.Command(dlPath)）启动。
	// 安装器为 machine 级，launch 时会触发提权、以全新 STARTUPINFO 重新拉起，
	// 故不压制安装器窗口。不经 cmd.exe，避免文件名中的 & 等元字符导致命令注入。
	switch runtime.GOOS {
	case "windows":
		cmd := exec.Command(dlPath)
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
		if s.installExit != nil {
			s.installExit()
		}
	}()
	return nil
}

// validateUpdateInstaller 校验安装包文件类型与完整性（非空）。
func validateUpdateInstaller(path string) error {
	info, err := os.Stat(path)
	if err != nil {
		return fmt.Errorf("stat installer: %w", err)
	}
	if info.Size() == 0 {
		return fmt.Errorf("installer file is empty")
	}
	switch runtime.GOOS {
	case "windows":
		ext := strings.ToLower(filepath.Ext(path))
		if ext != ".exe" && ext != ".msi" {
			return fmt.Errorf("invalid installer type for windows: %s (expected .exe or .msi)", ext)
		}
	case "darwin":
		if ext := strings.ToLower(filepath.Ext(path)); ext != ".dmg" {
			return fmt.Errorf("invalid installer type for macos: %s (expected .dmg)", ext)
		}
	case "linux":
		if ext := strings.ToLower(filepath.Ext(path)); ext != ".tar.gz" && ext != ".tgz" {
			return fmt.Errorf("invalid installer type for linux: %s (expected .tar.gz or .tgz)", ext)
		}
	}
	return nil
}

// downloadDir 返回默认下载目录（优先系统 Downloads，失败回退临时目录）。
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

// CleanStaleDownloads 清理 Downloads 目录中残留的 .downloading 临时文件。
// 删除超过 24 小时未修改的临时文件，防止进程异常退出后永久残留。
// 在 ServiceStartup 时调用一次即可。
func CleanStaleDownloads() {
	cleanStaleDownloads(downloadDir())
}

func cleanStaleDownloads(dir string) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	cutoff := time.Now().Add(-24 * time.Hour)
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".downloading") {
			continue
		}
		info, err := entry.Info()
		if err != nil {
			continue
		}
		if info.ModTime().Before(cutoff) {
			_ = os.Remove(filepath.Join(dir, entry.Name()))
		}
	}
}
