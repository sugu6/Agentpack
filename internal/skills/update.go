package skills

import (
	"agentpack/internal/agents"
	"agentpack/internal/iowriter"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// updateCachePath 返回更新检测缓存的文件路径
func updateCachePath() (string, error) {
	dir, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, ".agentpack", "skill-update-cache.json"), nil
}

// updateCacheEntry 是缓存中单个 skill 的条目
type updateCacheEntry struct {
	TreeSHA   string `json:"treeSha"`
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

// githubTreesClient 是更新检测专用的 HTTP client（避免依赖 market 包）
var githubTreesClient = &http.Client{Timeout: 15 * time.Second}

// githubTreeResponse 是 GitHub Trees API 的响应结构（仅含更新检测所需字段）
type githubTreeResponse struct {
	SHA  string           `json:"sha"`
	Tree []githubTreeItem `json:"tree"`
}

type githubTreeItem struct {
	Path string `json:"path"`
	Mode string `json:"mode"`
	Type string `json:"type"` // "blob" | "tree" | "commit"
	SHA  string `json:"sha"`
}

var githubAPIBaseURL = "https://api.github.com"

// fetchSkillTreeSHA 获取指定 skill 目录的远程 tree SHA
// 返回 (treeSHA, error)
func fetchSkillTreeSHA(ctx context.Context, owner, repo, branch, directory string) (string, error) {
	treeURL := fmt.Sprintf("%s/repos/%s/%s/git/trees/%s?recursive=1",
		githubAPIBaseURL, url.PathEscape(owner), url.PathEscape(repo), url.PathEscape(branch))

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, treeURL, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("User-Agent", "AgentPack/0.1 (+https://github.com/anomalyco/agentpack)")
	req.Header.Set("Accept", "application/json")

	resp, err := githubTreesClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("github trees api: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode == 404 {
		return "", fmt.Errorf("repo %s/%s branch %s not found", owner, repo, branch)
	}
	if resp.StatusCode != 200 {
		return "", fmt.Errorf("github trees api: status %d", resp.StatusCode)
	}

	var body githubTreeResponse
	if err := json.NewDecoder(io.LimitReader(resp.Body, 5*1024*1024)).Decode(&body); err != nil {
		return "", fmt.Errorf("decode trees response: %w", err)
	}

	// 在 tree 中查找匹配 directory 的条目
	// directory 是 skill 的目录名（如 "filesystem"）
	// tree 中的 path 格式可能是 "filesystem/SKILL.md", "filesystem/utils.py" 等
	// 我们需要找到 type=="tree" 且 path==directory 的条目
	// 或 path 的第一段 == directory 的 tree 条目
	for _, item := range body.Tree {
		if item.Type != "tree" {
			continue
		}
		// 精确匹配：path == directory
		if item.Path == directory {
			return item.SHA, nil
		}
	}

	// 如果未找到精确匹配的 tree 条目，可能是 skill 在仓库根目录
	// 此时整个 repo tree SHA 即为代表
	return body.SHA, nil
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
				LocalHash: skill.ContentHash,
				CheckedAt: time.Now().UTC().Format(time.RFC3339),
			}

			branch := skill.RepoBranch
			if branch == "" {
				branch = "main"
			}

			ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
			defer cancel()

			remoteSHA, err := fetchSkillTreeSHA(ctx, skill.RepoOwner, skill.RepoName, branch, skill.Directory)
			if err != nil {
				status.Error = err.Error()
				results[idx] = status
				return
			}

			status.RemoteHash = remoteSHA

			// 与缓存中的基线对比
			cacheKey := skill.ID
			if cached, ok := cache[cacheKey]; ok {
				if cached.TreeSHA != "" && cached.TreeSHA != remoteSHA {
					status.HasUpdate = true
				}
			}
			// 首次检查（无缓存）时 HasUpdate=false，仅记录基线

			// 更新缓存
			cacheMu.Lock()
			cache[cacheKey] = updateCacheEntry{
				TreeSHA:   remoteSHA,
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
