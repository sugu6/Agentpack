package main

import (
	"context"
	_ "embed"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync/atomic"
	"time"

	"agentpack/internal/config"
	"agentpack/internal/i18n"
)

// GitHub 仓库地址（owner/repo），用于检查更新
// 如需更换仓库，修改此常量即可
const githubRepo = "sugu6/AgentPack"

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

//go:embed wails.json
var wailsJSON []byte

func currentAppVersion() string {
	var cfg struct {
		Info struct {
			ProductVersion string `json:"productVersion"`
		} `json:"info"`
	}
	if err := json.Unmarshal(wailsJSON, &cfg); err != nil {
		return "0.0.0"
	}
	v := strings.TrimSpace(cfg.Info.ProductVersion)
	if v == "" {
		return "0.0.0"
	}
	return v
}

type githubRelease struct {
	TagName     string         `json:"tag_name"`
	Name        string         `json:"name"`
	Body        string         `json:"body"`
	HTMLURL     string         `json:"html_url"`
	PreRelease  bool           `json:"prerelease"`
	Draft       bool           `json:"draft"`
	PublishedAt string         `json:"published_at"`
	Assets      []releaseAsset `json:"assets"`
}

type releaseAsset struct {
	Name               string `json:"name"`
	BrowserDownloadURL string `json:"browser_download_url"`
	ContentType        string `json:"content_type"`
	Size               int    `json:"size"`
}

func (a *App) CheckUpdate() (*UpdateCheckResult, error) {
	current := currentAppVersion()
	lang := i18n.ResolveLanguage(a.cfg.Settings.Language)

	// GitHub API 直连,不走代理:
	// gh-proxy.com 等公共代理共享 IP 调用 api.github.com 极易触发 403 限流,
	// 导致永远拿不到 release 数据。文件下载在 StartDownloadUpdate 中单独走代理。
	url := fmt.Sprintf("https://api.github.com/repos/%s/releases/latest", githubRepo)

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
		req.Header.Set("Accept", "application/vnd.github+json")
		req.Header.Set("User-Agent", fmt.Sprintf("AgentPack/%s (%s; %s)", current, runtime.GOOS, runtime.GOARCH))

		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			cancel()
			lastErr = err
			continue
		}

		if resp.StatusCode == http.StatusNotFound {
			cancel()
			resp.Body.Close()
			return &UpdateCheckResult{
				HasUpdate:      false,
				CurrentVersion: current,
				LatestVersion:  current,
				Message:        i18n.T(lang, "update.message.noRelease"),
			}, nil
		}

		if resp.StatusCode == 403 || resp.StatusCode == 429 {
			cancel()
			resp.Body.Close()
			return &UpdateCheckResult{
				HasUpdate:      false,
				CurrentVersion: current,
				LatestVersion:  current,
				Message:        i18n.T(lang, "update.message.rateLimited"),
			}, nil
		}

		if resp.StatusCode != http.StatusOK {
			body, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
			cancel()
			resp.Body.Close()
			lastErr = fmt.Errorf("GitHub API 返回 %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
			continue
		}

		var release githubRelease
		if err := json.NewDecoder(resp.Body).Decode(&release); err != nil {
			resp.Body.Close()
			cancel()
			lastErr = err
			continue
		}
		resp.Body.Close()
		cancel()

		latest := strings.TrimPrefix(release.TagName, "v")
		hasUpdate := compareVersions(current, latest) < 0

		downloadURL, downloadName := "", ""
		downloadSize := 0
		if hasUpdate {
			if asset := matchPlatformAsset(release.Assets); asset != nil {
				downloadURL = asset.BrowserDownloadURL
				downloadSize = asset.Size
				downloadName = asset.Name
			}
		}

		message := i18n.T(lang, "update.message.latest", map[string]interface{}{"version": current})
		if hasUpdate {
			message = i18n.T(lang, "update.message.hasUpdate", map[string]interface{}{"version": latest})
		}

		return &UpdateCheckResult{
			HasUpdate:      hasUpdate,
			CurrentVersion: current,
			LatestVersion:  latest,
			Message:        message,
			Changelog:      release.Body,
			ReleaseURL:     release.HTMLURL,
			DownloadURL:    downloadURL,
			DownloadSize:   downloadSize,
			DownloadName:   downloadName,
		}, nil
	}

	return &UpdateCheckResult{
		HasUpdate:      false,
		CurrentVersion: current,
		LatestVersion:  current,
		Message:        i18n.T(lang, "update.message.networkFailed", map[string]interface{}{"error": lastErr.Error()}),
	}, nil
}

func matchPlatformAsset(assets []releaseAsset) *releaseAsset {
	goos := runtime.GOOS
	goarch := runtime.GOARCH
	platform := fmt.Sprintf("%s-%s", goos, goarch)
	// 精确匹配 goos-goarch（如 "windows-amd64"、"linux-arm64"）
	for i := range assets {
		if strings.Contains(assets[i].Name, platform) {
			return &assets[i]
		}
	}
	// OS 别名匹配（如 darwin → macos-universal）
	alias := goos
	switch goos {
	case "darwin":
		alias = "macos"
	}
	aliasPlatform := fmt.Sprintf("%s-%s", alias, goarch)
	for i := range assets {
		if strings.Contains(assets[i].Name, aliasPlatform) {
			return &assets[i]
		}
	}
	// OS-only 匹配（用于 universal 构建等场景）
	osPrefix := goos + "-"
	for i := range assets {
		if strings.Contains(assets[i].Name, osPrefix) && !strings.Contains(assets[i].Name, "Source code") {
			return &assets[i]
		}
	}
	aliasPrefix := alias + "-"
	for i := range assets {
		if strings.Contains(assets[i].Name, aliasPrefix) && !strings.Contains(assets[i].Name, "Source code") {
			return &assets[i]
		}
	}
	// 最终兜底：第一个非 Source code 的 asset
	for i := range assets {
		if !strings.Contains(assets[i].Name, "Source code") {
			return &assets[i]
		}
	}
	return nil
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
	return 0
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
	a.downloadOffset = 0
	a.mu.Unlock()
	atomic.StoreInt32(&a.paused, 0)
	return a.startDownload(url, 0)
}

// startDownload 执行下载，offset > 0 时通过 Range 请求实现断点续传
func (a *App) startDownload(url string, offset int64) error {
	if !strings.HasPrefix(url, config.DefaultGitHubProxy) {
		url = config.DefaultGitHubProxy + strings.TrimPrefix(url, "https://")
	}

	ctx, cancel := context.WithCancel(context.Background())
	a.mu.Lock()
	a.downloadURL = url
	a.downloadState = DownloadStateDownloading
	if a.downloadCancel != nil {
		a.downloadCancel()
	}
	a.downloadCtx = ctx
	a.downloadCancel = cancel
	a.mu.Unlock()

	go func() {
		a.beginInFlight()
		defer a.endInFlight()
		defer cancel()
		lang := i18n.ResolveLanguage(a.cfg.Settings.Language)

		emitError := func(msgKey string, args map[string]interface{}) {
			a.mu.Lock()
			a.downloadState = DownloadStateError
			a.mu.Unlock()
			a.emit("update:download:error", map[string]string{"message": i18n.T(lang, msgKey, args)})
		}

		req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
		if err != nil {
			emitError("update.download.failed", map[string]interface{}{"error": err.Error()})
			return
		}
		req.Header.Set("User-Agent", "AgentPack/"+currentAppVersion())
		if offset > 0 {
			req.Header.Set("Range", fmt.Sprintf("bytes=%d-", offset))
		}

		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			emitError("update.download.failed", map[string]interface{}{"error": err.Error()})
			return
		}
		defer resp.Body.Close()

		if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusPartialContent {
			emitError("update.download.serverError", map[string]interface{}{"code": resp.StatusCode})
			return
		}

		fileName := filepath.Base(url)
		dlPath := filepath.Join(downloadDir(), fileName)
		dlTmpPath := dlPath + ".downloading"

		// 服务器忽略 Range（返回 200）时无法续传，退化为从头下载
		resumed := offset > 0 && resp.StatusCode == http.StatusPartialContent
		var downloaded int64
		var f *os.File
		if resumed {
			f, err = os.OpenFile(dlTmpPath, os.O_APPEND|os.O_WRONLY, 0600)
			if err == nil {
				downloaded = offset
			}
		}
		if f == nil {
			os.Remove(dlTmpPath)
			f, err = os.Create(dlTmpPath)
			if err != nil {
				emitError("update.download.failed", map[string]interface{}{"error": err.Error()})
				return
			}
			downloaded = 0
		}
		defer f.Close()

		// totalSize 统一为文件总大小：206 响应的 ContentLength 只是剩余部分
		totalSize := resp.ContentLength
		if resumed && totalSize > 0 {
			totalSize += offset
		}

		lastTime := time.Now()
		lastBytes := downloaded
		removeTmp := func() { os.Remove(dlTmpPath) }
		buf := make([]byte, 32*1024)

		for {
			// 暂停：保留临时文件与偏移量，等待 ResumeDownload
			if atomic.LoadInt32(&a.paused) != 0 {
				atomic.StoreInt32(&a.paused, 0)
				f.Close()
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
					"fileName":   fileName,
				})
				return
			}
			select {
			case <-ctx.Done():
				removeTmp()
				return
			default:
			}
			n, readErr := resp.Body.Read(buf)
			if n > 0 {
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
				break
			}
			if readErr != nil {
				removeTmp()
				emitError("update.download.failed", map[string]interface{}{"error": readErr.Error()})
				return
			}
		}
		f.Close()

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
			"fileName": fileName,
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

	// 完全脱离父进程启动安装程序
	switch runtime.GOOS {
	case "windows":
		if err := exec.Command("cmd", "/c", "start", "", dlPath).Start(); err != nil {
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
		a.Quit()
	}()
	return nil
}

func (a *App) CancelDownload() error {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.downloadCancel != nil {
		a.downloadCancel()
		a.downloadCancel = nil
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
