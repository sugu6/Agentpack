package update

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"sync/atomic"
	"time"

	"agentpack/internal/appmeta"
	"agentpack/internal/config"
	"agentpack/internal/i18n"
)

// maxUpdateDownloadSize 限制更新安装包最大体积（1GB），防止恶意/异常源无限流数据写满磁盘。
const maxUpdateDownloadSize int64 = 1 << 30

// maxDownloadDuration 限制单次下载总时长（30 分钟），防止服务器接受连接后
// 不发送数据（stalled）导致的 goroutine 永久阻塞。
const maxDownloadDuration = 30 * time.Minute

// allowedRedirectHost 判定更新下载链允许跳转到的下一段 host（封闭白名单，P2-3）。
// 实测依据：gh-proxy 资产 200 原地服务（0 重定向）；直连资产单跳
// github.com → release-assets.githubusercontent.com。任何白名单外 host
// （含全部 IP 字面量——私网/环回/链路本地/云元数据永不会命中白名单字符串；
// 以及 gh-proxy 未来若改跳镜像域）一律 fail-closed：宁可断下载也不接受不明对端。
func allowedRedirectHost(host string) bool {
	host = strings.ToLower(host)
	if host == "github.com" || host == "gh-proxy.com" || host == "githubusercontent.com" {
		return true
	}
	return strings.HasSuffix(host, ".githubusercontent.com")
}

// expandedAssetsBase 方案E 摘要页抓取基址：默认直连 github.com（与 atom 检查流
// 同域同信任级、不走代理），测试覆写为本地服务。
var expandedAssetsBase = "https://github.com"

// expandedAssets* 正则按开标签块配对 资产名 与 摘要值（块内属性顺序无关）。
// 实测真实元素为 <clipboard-copy>（见 fixture）——泛化匹配任意元素名，
// GitHub 改标签名不破坏解析；改属性结构才 fail-closed。
// 实测结构见 testdata/expanded_assets_v0.3.0.html；GitHub 改版破坏结构时
// fetchExpectedDigest 解析失败 → fail-closed 拒绝下载。
var (
	expandedAssetsTagRE  = regexp.MustCompile(`<[a-zA-Z][^>]*>`)
	expandedAssetsNameRE = regexp.MustCompile(`aria-label="Copy to clipboard digest for ([^"]+)"`)
	expandedAssetsValRE  = regexp.MustCompile(`value="([A-Za-z0-9]+):([0-9a-fA-F]{64,})"`)
)

// downloadHTTPClient 用于更新检查与更新包下载：Transport 设置 ResponseHeaderTimeout，
// 防止服务器接受连接后不响应头部导致的永久阻塞；总时长由调用方的
// context.WithTimeout 兜底。CheckRedirect 按封闭白名单逐跳校验下一跳 host
// （P2-3）：非白名单（含 IP 字面量/私网/镜像域）直接报错终止，fail-closed。
var downloadHTTPClient = &http.Client{
	Transport: &http.Transport{
		ResponseHeaderTimeout: 30 * time.Second,
	},
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			// 保留默认策略的10跳上限
			if len(via) >= 10 {
				return fmt.Errorf("stopped after 10 redirects")
			}
			// 重定向跳逐跳校验（P2-3）。首跳 URL 由 ValidateDownloadURL 白名单
			// + 代理改写确定性构造，不走此校验（测试中 httptest 代理亦依赖此）。
			if !allowedRedirectHost(req.URL.Hostname()) {
				return fmt.Errorf("refuse redirect to untrusted host: %s", req.URL.Hostname())
			}
			return nil
		},
}

// ---------- 下载 ----------

// ValidateDownloadURL 校验更新下载 URL 只允许本仓库的 CI 发布下载资产。
// 下载服务经 Wails 绑定暴露给渲染进程（可达面比"仅内部调用"大），且
// 实际经第三方代理（gh-proxy.com）拉取后直接安装执行——把来源限制在
// "github.com/{Repo}/releases/download/" 是防任意 host/任意仓库注入的最小边界：
// 仅锁 github.com 仍允许投毒任意 GitHub 仓库的 release 资产（P2-2），
// 故前缀与 update.go 的 BuildDownloadAssetFor URL 模板同源钉死。
func ValidateDownloadURL(url string) error {
	prefix := "https://github.com/" + Repo + "/releases/download/"
	if !strings.HasPrefix(url, prefix) {
		return fmt.Errorf("refuse to download update from untrusted URL: %s", url)
	}
	return nil
}

// expandedAssetsPage 从资产下载 URL 解析资产名（host 无关，只认
// /releases/download/{tag}/{asset} 段，兼容代理形 URL），构造清单页地址。
func expandedAssetsPage(downloadURL string) (page, asset string, err error) {
	const marker = "/releases/download/"
	i := strings.Index(downloadURL, marker)
	if i < 0 {
		return "", "", fmt.Errorf("cannot parse tag from URL: %s", downloadURL)
	}
	rest := downloadURL[i+len(marker):]
	parts := strings.SplitN(rest, "/", 2)
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		return "", "", fmt.Errorf("cannot parse tag/asset from URL: %s", downloadURL)
	}
	asset = strings.SplitN(parts[1], "?", 2)[0]
	if asset == "" {
		return "", "", fmt.Errorf("empty asset name in URL: %s", downloadURL)
	}
	return fmt.Sprintf("%s/%s/releases/expanded_assets/%s", expandedAssetsBase, Repo, parts[0]), asset, nil
}

// fetchExpectedDigest 直连 github.com 抓取发布资产清单页，按资产名提取 sha256
// 摘要（方案E：与 atom 同信任锚，无 REST API、无 .sha256 构建产物、CI 零改动）。
// 返回 (digest hex 小写, 非 sha256 算法名或空, 错误)；错误态由调用方 fail-closed。
// 纯函数（无 Service 状态依赖）；ctx 派生自下载上下文：取消/关停即中断抓取，
// 避免 Cancel 后孤儿抓取存活、翻转新下载状态（30s 仅作总时长兜底）。
func fetchExpectedDigest(ctx context.Context, downloadURL string) (digest, algo string, err error) {
	page, asset, err := expandedAssetsPage(downloadURL)
	if err != nil {
		return "", "", err
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, page, nil)
	if err != nil {
		return "", "", err
	}
	req.Header.Set("Accept", "text/html")
	req.Header.Set("User-Agent", fmt.Sprintf("AgentPack/%s (%s; %s)", appmeta.Version, runtime.GOOS, runtime.GOARCH))
	resp, err := downloadHTTPClient.Do(req)
	if err != nil {
		return "", "", fmt.Errorf("fetch release digest page: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", "", fmt.Errorf("release digest page returned %d", resp.StatusCode)
	}
	body, rerr := io.ReadAll(io.LimitReader(resp.Body, MaxReleaseBodySize+1))
	if rerr != nil {
		return "", "", fmt.Errorf("read release digest page: %w", rerr)
	}
	if len(body) > MaxReleaseBodySize {
		return "", "", fmt.Errorf("release digest page exceeds %d bytes", MaxReleaseBodySize)
	}
	for _, btn := range expandedAssetsTagRE.FindAll(body, -1) {
		nm := expandedAssetsNameRE.FindSubmatch(btn)
		if nm == nil || string(nm[1]) != asset {
			continue
		}
		vm := expandedAssetsValRE.FindSubmatch(btn)
		if vm == nil {
			continue
		}
		if !strings.EqualFold(string(vm[1]), "sha256") {
			return "", string(vm[1]), fmt.Errorf("digest for %s uses unsupported algorithm %s", asset, vm[1])
		}
		return strings.ToLower(string(vm[2])), "", nil
	}
	return "", "", fmt.Errorf("digest for asset %s not found (markup changed or asset missing)", asset)
}

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
	if !resume {
		if err := ValidateDownloadURL(url); err != nil {
			return err
		}
	}
	// 仅非续传且配置了代理时改写（空代理 = 直连，与 market/skills 的空值语义一致）。
	// 拼接契约统一走 config.ProxyJoin（保留目标 URL 的 scheme，与 skills 链路一致），
	// 同时避免用户配置不带尾斜杠时拼出 "gh-proxy.comgithub.com/..." 缺斜杠畸形。
	if !resume {
		url = config.ProxyJoin(config.DefaultGitHubProxy, url)
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
	// tmpRemoved 是单向 CAS：复位归本次下载，否则会话内首次清理后所有
	// 后续 removeTmp 都空转，第二次失败的下载会残留 .downloading。
	atomic.StoreInt32(&s.tmpRemoved, 0)
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
		// f 在响应校验通过后才创建；提前声明以便 removeTmp 先关闭句柄再删除：
		// Windows 下对仍打开的文件 os.Remove 会失败（"being used by another process"），
		// 且 tmpRemoved CAS 置1后无人重试 → .downloading 永久残留。
		var f *os.File
		removeTmp := func() {
			if f != nil {
				f.Close()
				f = nil
			}
			// 与 Shutdown 的 tmpRemoved 标志互斥：CAS 认领，已认领则跳过，
			// 避免与 Shutdown 并发调用 os.Remove。
			if !atomic.CompareAndSwapInt32(&s.tmpRemoved, 0, 1) {
				return
			}
			// 句柄已关闭后 Remove 仍失败（权限/占用）：回滚标志，使标志如实
			// 反映"未删除"，避免 Shutdown 的兜底路径看到 1 而跳过删除。
			if err := os.Remove(dlTmpPath); err != nil && !os.IsNotExist(err) {
				atomic.StoreInt32(&s.tmpRemoved, 0)
			}
		}

		// fail 统一错误收尾：清理临时文件（含句柄关闭）+ 状态置 Error + 错误事件；
		// 与 emitError 的区别仅是多一次 removeTmp，调用方自行 return。
		fail := func(msgKey string, args map[string]interface{}) {
			removeTmp()
			emitError(msgKey, args)
		}

		// onTimeout 超时收尾（原两处逐字重复的超时块去重）：清理临时文件、
		// 复位 url/offset 并通知前端——不复位会残留 Downloading 导致 UI
		// 永久卡死且后续下载被并发守卫拒绝。主动取消不走这里（静默清理）。
		onTimeout := func() {
			removeTmp()
			s.mu.Lock()
			s.state = StateError
			s.url = ""
			s.offset = 0
			s.mu.Unlock()
			emitError("update.download.failed", map[string]interface{}{"error": fmt.Sprintf("download timed out after %s", maxDownloadDuration)})
		}

		req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
		if err != nil {
			fail("update.download.failed", map[string]interface{}{"error": err.Error()})
			return
		}
		req.Header.Set("User-Agent", "AgentPack/"+appmeta.Version)
		// 更新安装包必须取最新版本，禁用 HTTP 缓存。
		req.Header.Set("Cache-Control", "no-cache, no-store, must-revalidate")
		req.Header.Set("Pragma", "no-cache")

		// 方案E：下载请求前直连发布页取 sha256 摘要；取不到 = fail-closed 不发起下载
		expected, algo, derr := fetchExpectedDigest(ctx, url)
		if derr != nil {
			// 取消/关停中断抓取属预期：与下载中取消同语义，静默清理——
			// 不改状态、不发事件（否则孤儿 goroutine 会把新下载翻成 Error）。
			if ctx.Err() != nil {
				removeTmp()
				return
			}
			if algo != "" {
				fail("update.download.verifyUnsupported", map[string]interface{}{"algo": algo})
			} else {
				fail("update.download.verifyUnavailable", map[string]interface{}{"error": derr.Error()})
			}
			return
		}
		s.mu.Lock()
		s.expectedDigest = expected
		s.mu.Unlock()
		hasher := sha256.New()
		if offset > 0 {
			req.Header.Set("Range", fmt.Sprintf("bytes=%d-", offset))
		}

		resp, err := downloadHTTPClient.Do(req)
		if err != nil {
			// 抓取刚成功、Do 被取消竞态打断：同取消静默语义
			if ctx.Err() != nil {
				removeTmp()
				return
			}
			fail("update.download.failed", map[string]interface{}{"error": err.Error()})
			return
		}
		defer resp.Body.Close()

		if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusPartialContent {
			fail("update.download.serverError", map[string]interface{}{"code": resp.StatusCode})
			return
		}

		// 下载体积上限：Content-Length 预检（Range 续传时按完整大小判断）
		if resp.ContentLength > 0 {
			totalExpected := resp.ContentLength
			if resp.StatusCode == http.StatusPartialContent {
				totalExpected += offset
			}
			if totalExpected > maxUpdateDownloadSize {
				fail("update.download.failed", map[string]interface{}{"error": fmt.Sprintf("file too large: %d bytes (max %d)", totalExpected, maxUpdateDownloadSize)})
				return
			}
		}

		if dlName == "." || dlName == ".." {
			fail("update.download.failed", map[string]interface{}{"error": "invalid download filename"})
			return
		}

		// 服务器忽略 Range（返回 200）时无法续传，退化为从头下载。
		// 续传仅在 206 且临时文件实际大小与记录偏移一致时追加，否则从头重建，
		// 防止以错误偏移追加产生重叠或空洞的损坏文件。
		resumed := offset > 0 && resp.StatusCode == http.StatusPartialContent
		var written int64
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
					// 续传完整性校验需整文件摘要：补喂已落盘的前 offset 字节
					if rf, rerr := os.Open(dlTmpPath); rerr != nil {
						f.Close()
						f = nil
					} else {
						if _, cerr := io.CopyN(hasher, rf, offset); cerr != nil {
							f.Close()
							f = nil
						}
						rf.Close()
					}
				}
			}
		}
		if f == nil {
			// 续传目标文件缺失或尺寸不符：无法安全消费 206 流（该流从 offset 起），
			// 必须从头重新请求。当前响应直接作废。
			if resumed {
				fail("update.download.failed", map[string]interface{}{"error": "cannot resume: temp file missing or size mismatch"})
				return
			}
			os.Remove(dlTmpPath)
			f, err = os.Create(dlTmpPath)
			if err != nil {
				fail("update.download.failed", map[string]interface{}{"error": err.Error()})
				return
			}
			written = 0
		}
		defer func() {
			if f != nil {
				f.Close()
			}
		}()

		// 流式哈希与落盘同步（零额外 IO）；续传时前缀字节已在上方补喂
		sink := io.MultiWriter(f, hasher)

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
				// 主动取消由 Cancel 复位并静默清理；超时（30min 上限）才需通知前端。
				if ctx.Err() == context.DeadlineExceeded {
					onTimeout()
				} else {
					removeTmp()
				}
				return
			default:
			}
			n, readErr := resp.Body.Read(buf)
			if n > 0 {
				// 流式大小限制：防止无 Content-Length 的响应无限写入磁盘
				if written+int64(n) > maxUpdateDownloadSize {
					fail("update.download.failed", map[string]interface{}{"error": fmt.Sprintf("file exceeds %d bytes", maxUpdateDownloadSize)})
					return
				}
				if _, writeErr := sink.Write(buf[:n]); writeErr != nil {
					fail("update.download.failed", map[string]interface{}{"error": writeErr.Error()})
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
					fail("update.download.failed", map[string]interface{}{"error": fmt.Sprintf("incomplete download: got %d bytes, expected %d", written, totalSize)})
					return
				}
				// 分块传输（无 Content-Length，totalSize <= 0）时无法获知预期大小，
				// 但至少拒绝 0 字节的"空完成"（安装包不可能为空；续传场景
				// written=offset>0 天然通过）。
				if totalSize <= 0 && written == 0 {
					fail("update.download.failed", map[string]interface{}{"error": "empty download: server sent no data"})
					return
				}
				break
			}
			if readErr != nil {
				// 用户主动取消（Cancel）时静默退出，不再把状态改写为 Error，
				// 避免取消后 UI 仍收到"下载失败"的错误事件。
				if ctx.Err() == context.DeadlineExceeded {
					onTimeout()
					return
				}
				if ctx.Err() != nil {
					removeTmp()
					return
				}
				fail("update.download.failed", map[string]interface{}{"error": readErr.Error()})
				return
			}
		}
		f.Close()
		f = nil

		// 方案E 校验之一：rename 前与发布页摘要比对；不符 = 传输/代理层被投毒，
		// 删除临时文件 fail-closed（复核之二在 Install exec 前）。
		sum := hex.EncodeToString(hasher.Sum(nil))
		if sum != expected {
			fail("update.download.verifyMismatch", map[string]interface{}{
				"expected": expected[:12],
				"actual":   sum[:12],
			})
			return
		}

		// 下载完成后重命名: .downloading → 正式文件名
		if err := os.Rename(dlTmpPath, dlPath); err != nil {
			fail("update.download.failed", map[string]interface{}{"error": err.Error()})
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
