package main

import (
	"context"
	_ "embed"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"runtime"
	"strings"
	"time"
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
	TagName     string `json:"tag_name"`
	Name        string `json:"name"`
	Body        string `json:"body"`
	HTMLURL     string `json:"html_url"`
	PreRelease  bool   `json:"prerelease"`
	Draft       bool   `json:"draft"`
	PublishedAt string `json:"published_at"`
}

// CheckUpdate 调用 GitHub Releases API 检查最新版本
func (a *App) CheckUpdate() (*UpdateCheckResult, error) {
	current := currentAppVersion()

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	url := fmt.Sprintf("https://api.github.com/repos/%s/releases/latest", githubRepo)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, fmt.Errorf("创建请求失败: %w", err)
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("User-Agent", fmt.Sprintf("AgentPack/%s (%s; %s)", current, runtime.GOOS, runtime.GOARCH))

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return &UpdateCheckResult{
			HasUpdate:      false,
			CurrentVersion: current,
			LatestVersion:  current,
			Message:        fmt.Sprintf("网络请求失败: %v", err),
		}, nil
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusNotFound {
		return &UpdateCheckResult{
			HasUpdate:      false,
			CurrentVersion: current,
			LatestVersion:  current,
			Message:        "尚未发布任何版本",
		}, nil
	}

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return &UpdateCheckResult{
			HasUpdate:      false,
			CurrentVersion: current,
			LatestVersion:  current,
			Message:        fmt.Sprintf("GitHub API 返回 %d: %s", resp.StatusCode, strings.TrimSpace(string(body))),
		}, nil
	}

	var release githubRelease
	if err := json.NewDecoder(resp.Body).Decode(&release); err != nil {
		return &UpdateCheckResult{
			HasUpdate:      false,
			CurrentVersion: current,
			LatestVersion:  current,
			Message:        fmt.Sprintf("解析响应失败: %v", err),
		}, nil
	}

	latest := strings.TrimPrefix(release.TagName, "v")
	hasUpdate := compareVersions(current, latest) < 0

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
