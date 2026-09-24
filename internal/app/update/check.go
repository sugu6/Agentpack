package update

import (
	"context"
	"encoding/xml"
	"fmt"
	"html"
	"io"
	"net/http"
	"runtime"
	"strings"
	"time"

	"agentpack/internal/appmeta"
	"agentpack/internal/i18n"
)

// updateCheckTTL 距上次成功检查后，复用缓存结果而不再请求 GitHub API 的时长。
// GitHub 未认证 API 限制为 60 次/小时/IP；配合端上"每会话仅启动静默检查一次"，
// 10 分钟 TTL 将单实例的检查频率压到 ≤6 次/小时，避免触发限流。
const updateCheckTTL = 10 * time.Minute

// updateCheckRateLimitBackoff 上次检查命中限流(403/429)后的退避时长。
// 限流状态下继续请求只会继续触发限流，用更长退避让限流窗口过去再重试。
const updateCheckRateLimitBackoff = 30 * time.Minute

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
