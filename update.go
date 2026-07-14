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
	"time"

	"agentpack/internal/config"
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

// wailsJSON 通过 go:embed 在编译时嵌入 wails.json，用于读取版本号
//
//go:embed wails.json
var wailsJSON []byte

// currentAppVersion 从嵌入的 wails.json 中解析 productVersion
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

// githubRelease 对应 GitHub Releases API 的响应结构
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

// CheckUpdate 调用 GitHub Releases API 检查最新版本
func (a *App) CheckUpdate() (*UpdateCheckResult, error) {
	current := currentAppVersion()

	url := config.DefaultGitHubProxy + fmt.Sprintf("https://api.github.com/repos/%s/releases/latest", githubRepo)

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
				Message:        "尚未发布任何版本",
			}, nil
		}

		if resp.StatusCode == 403 || resp.StatusCode == 429 {
			cancel()
			resp.Body.Close()
			return &UpdateCheckResult{
				HasUpdate:      false,
				CurrentVersion: current,
				LatestVersion:  current,
				Message:        "GitHub API 请求过于频繁，请稍后再试",
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

		downloadURL, downloadName, downloadSize := "", "", 0
		if hasUpdate {
			downloadURL, downloadName, downloadSize = matchPlatformAsset(release.Assets)
		}

		message := fmt.Sprintf("当前已是最新版本 v%s", current)
		if hasUpdate {
			message = fmt.Sprintf("发现新版本 v%s", latest)
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
		Message:        fmt.Sprintf("网络请求失败: %v", lastErr),
	}, nil
}

// compareVersions 比较两个语义化版本号
// 返回 -1 表示 a < b，0 表示相等，1 表示 a > b
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

func (a *App) StartDownloadUpdate(url string) error {
	ctx, cancel := context.WithCancel(context.Background())
	a.mu.Lock()
	if a.downloadCancel != nil {
		a.downloadCancel()
	}
	a.downloadCancel = cancel
	a.mu.Unlock()

	go func() {
		a.beginInFlight()
		defer a.endInFlight()
		defer cancel()

		req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
		if err != nil {
			a.emit("update:download:error", map[string]string{"message": err.Error()})
			return
		}
		req.Header.Set("User-Agent", "AgentPack/"+currentAppVersion())

		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			a.emit("update:download:error", map[string]string{"message": err.Error()})
			return
		}
		defer resp.Body.Close()

		if resp.StatusCode != http.StatusOK {
			a.emit("update:download:error", map[string]string{"message": fmt.Sprintf("服务器返回 %d", resp.StatusCode)})
			return
		}

		totalSize := resp.ContentLength
		var downloaded int64
		lastTime := time.Now()
		var lastBytes int64

		fileName := filepath.Base(url)
		tmpPath := filepath.Join(os.TempDir(), "agentpack-update-"+fileName)
		f, err := os.Create(tmpPath)
		if err != nil {
			a.emit("update:download:error", map[string]string{"message": err.Error()})
			return
		}
		defer f.Close()

		removeTmp := func() { os.Remove(tmpPath) }
		buf := make([]byte, 32*1024)
		for {
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
					a.emit("update:download:error", map[string]string{"message": writeErr.Error()})
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
				a.emit("update:download:error", map[string]string{"message": readErr.Error()})
				return
			}
		}

		a.emit("update:download:complete", map[string]interface{}{
			"filePath": tmpPath,
			"fileName": fileName,
		})
	}()

	return nil
}

func (a *App) CancelDownload() {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.downloadCancel != nil {
		a.downloadCancel()
		a.downloadCancel = nil
	}
}

func (a *App) OpenDownloadedFile(filePath string) error {
	switch runtime.GOOS {
	case "windows":
		return exec.Command("explorer.exe", "/select,", filePath).Start()
	case "darwin":
		return exec.Command("open", "-R", filePath).Start()
	default:
		return exec.Command("xdg-open", filepath.Dir(filePath)).Start()
	}
}

func parseVersionParts(v string) []int {
	v = strings.TrimPrefix(v, "v")
	// 去除预发布后缀（如 -beta.1）
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

func matchPlatformAsset(assets []releaseAsset) (string, string, int) {
	target := fmt.Sprintf("%s_%s", runtime.GOOS, runtime.GOARCH)
	targetLower := strings.ToLower(target)
	for _, a := range assets {
		if strings.Contains(strings.ToLower(a.Name), targetLower) {
			return config.DefaultGitHubProxy + a.BrowserDownloadURL, a.Name, a.Size
		}
	}
	return "", "", 0
}
