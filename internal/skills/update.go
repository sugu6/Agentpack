package skills

import (
	"agentpack/internal/config"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"time"

	"agentpack/internal/agents"
	"agentpack/internal/iowriter"
)

// updateCachePath 返回更新检测缓存的文件路径
func updateCachePath() (string, error) {
	dir := config.AgentPackDir()
	if dir == "" {
		return "", fmt.Errorf("agentpack dir not available")
	}
	return filepath.Join(dir, "skill-update-cache.json"), nil
}

// updateCacheEntry 是缓存中单个 skill 的条目
type updateCacheEntry struct {
	CommitSHA string `json:"commitSha"`
	CheckedAt string `json:"checkedAt"`
}

// updateCacheFile 是缓存文件的结构
type updateCacheFile struct {
	Skills map[string]updateCacheEntry `json:"skills"`
}

// readUpdateCache 读取更新检测缓存（返回 skillID -> entry）
func readUpdateCache() map[string]updateCacheEntry {
	path, err := updateCachePath()
	if err != nil {
		return nil
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	var cache updateCacheFile
	if err := json.Unmarshal(data, &cache); err != nil {
		return nil
	}
	return cache.Skills
}

// writeUpdateCache 原子写入更新检测缓存
func writeUpdateCache(skills map[string]updateCacheEntry) error {
	path, err := updateCachePath()
	if err != nil {
		return err
	}
	cache := updateCacheFile{Skills: skills}
	data, err := json.MarshalIndent(cache, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return err
	}
	return iowriter.WriteAtomic(path, data, 0600)
}

// githubAPIClient 是更新检测专用的 HTTP client（避免依赖 market 包）
var githubAPIClient = &http.Client{Timeout: 15 * time.Second}

// githubCommitItem 是 GitHub Commits API 响应中单个 commit 的结构
type githubCommitItem struct {
	SHA string `json:"sha"`
}

var githubAPIBaseURL = "https://api.github.com"

// fetchSkillCommitSHA 获取指定 repo 分支的最新 commit SHA
// 返回 (commitSHA, error)
func fetchSkillCommitSHA(ctx context.Context, owner, repo, branch string) (string, error) {
	commitsURL := fmt.Sprintf("%s/repos/%s/%s/commits/%s?per_page=1",
		githubAPIBaseURL, owner, repo, branch)

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, commitsURL, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("User-Agent", "AgentPack/0.1 (+https://github.com/anomalyco/agentpack)")
	req.Header.Set("Accept", "application/json")

	resp, err := githubAPIClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("github commits api: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode == 404 {
		return "", fmt.Errorf("repo %s/%s branch %s not found", owner, repo, branch)
	}
	if resp.StatusCode != 200 {
		return "", fmt.Errorf("github commits api: status %d", resp.StatusCode)
	}

	var body []githubCommitItem
	if err := json.NewDecoder(io.LimitReader(resp.Body, 5*1024*1024)).Decode(&body); err != nil {
		return "", fmt.Errorf("decode commits response: %w", err)
	}

	if len(body) == 0 {
		return "", fmt.Errorf("no commits found in repository %s/%s branch %s", owner, repo, branch)
	}

	return body[0].SHA, nil
}

// CacheSkillCommitSHA 为指定 skill 获取并缓存远程 commit SHA
func CacheSkillCommitSHA(skillID, owner, repo, branch string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	sha, err := fetchSkillCommitSHA(ctx, owner, repo, branch)
	if err != nil {
		return err
	}
	cache := readUpdateCache()
	if cache == nil {
		cache = make(map[string]updateCacheEntry)
	}
	cache[skillID] = updateCacheEntry{
		CommitSHA: sha,
		CheckedAt: time.Now().UTC().Format(time.RFC3339),
	}
	return writeUpdateCache(cache)
}

// CheckUpdates 检查所有已安装 skills 的远程更新
// 仅检查 RepoOwner/RepoName 非空的条目（从 GitHub 安装的 skills）
// 并发限制为 5，避免触发 GitHub API rate limit
func (s *Store) CheckUpdates(reg *agents.Registry) []UpdateStatus {
	s.mu.RLock()
	skillsList := make([]Skill, 0, len(s.skills))
	for _, sk := range s.skills {
		if sk.RepoOwner != "" && sk.RepoName != "" {
			skillsList = append(skillsList, sk)
		}
	}
	s.mu.RUnlock()

	if len(skillsList) == 0 {
		return nil
	}

	// 读取缓存基线
	cache := readUpdateCache()
	if cache == nil {
		cache = make(map[string]updateCacheEntry)
	}

	results := make([]UpdateStatus, len(skillsList))
	sem := make(chan struct{}, 5) // 并发限制
	var wg sync.WaitGroup
	var cacheMu sync.Mutex

	for i, sk := range skillsList {
		wg.Add(1)
		go func(idx int, skill Skill) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()

			status := UpdateStatus{
				SkillID:   skill.ID,
				Directory: skill.Directory,
				CheckedAt: time.Now().UTC().Format(time.RFC3339),
			}

			branch := skill.RepoBranch
			if branch == "" {
				branch = "main"
			}

			ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
			defer cancel()

			remoteSHA, err := fetchSkillCommitSHA(ctx, skill.RepoOwner, skill.RepoName, branch)
			if err != nil {
				status.Error = err.Error()
				results[idx] = status
				return
			}

			status.RemoteHash = remoteSHA

			// 与缓存中的基线对比
			cacheKey := skill.ID
			if cached, ok := cache[cacheKey]; ok {
				if cached.CommitSHA != "" && cached.CommitSHA != remoteSHA {
					status.HasUpdate = true
				}
			}
			// 首次检查（无缓存）时 HasUpdate=false，仅记录基线

			// 更新缓存
			cacheMu.Lock()
			cache[cacheKey] = updateCacheEntry{
				CommitSHA: remoteSHA,
				CheckedAt: status.CheckedAt,
			}
			cacheMu.Unlock()

			results[idx] = status
		}(i, sk)
	}

	wg.Wait()

	// 持久化更新后的缓存（失败不影响返回结果，但记录日志以便排查）
	if err := writeUpdateCache(cache); err != nil {
		log.Printf("warning: write update cache: %v", err)
	}

	return results
}
