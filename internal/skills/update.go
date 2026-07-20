package skills

import (
	"agentpack/internal/config"
	"context"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
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

// fetchSkillCommitSHAFunc 是获取远程 commit SHA 的函数，可被测试替换
var fetchSkillCommitSHAFunc = fetchSkillCommitSHAImpl

// fetchSkillCommitSHAImpl 使用 git ls-remote 获取指定 repo 分支的最新 commit SHA
// 不受 GitHub REST API rate limit 限制
func fetchSkillCommitSHAImpl(ctx context.Context, owner, repo, branch string) (string, error) {
	repoURL := fmt.Sprintf("https://github.com/%s/%s.git", owner, repo)
	// 如果配置了 gh-proxy，使用代理 URL
	if proxy := config.DefaultGitHubProxy; proxy != "" {
		repoURL = proxy + repoURL
	}
	ref := fmt.Sprintf("refs/heads/%s", branch)

	cmd := exec.CommandContext(ctx, "git", "ls-remote", repoURL, ref)
	out, err := cmd.Output()
	if err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok {
			return "", fmt.Errorf("git ls-remote failed: %s", strings.TrimSpace(string(exitErr.Stderr)))
		}
		return "", fmt.Errorf("git ls-remote: %w", err)
	}

	// 输出格式: "abc123...\trefs/heads/main\n"
	line := strings.TrimSpace(string(out))
	if line == "" {
		return "", fmt.Errorf("no ref %s found in %s/%s", ref, owner, repo)
	}

	parts := strings.SplitN(line, "\t", 2)
	if len(parts) < 2 || parts[0] == "" {
		return "", fmt.Errorf("unexpected git ls-remote output: %q", line)
	}

	return parts[0], nil
}

// CacheSkillCommitSHA 为指定 skill 获取并缓存远程 commit SHA
func CacheSkillCommitSHA(skillID, owner, repo, branch string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	sha, err := fetchSkillCommitSHAFunc(ctx, owner, repo, branch)
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
// 按 (owner, repo, branch) 去重，同一仓库只查一次 commit SHA
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

	// 按 (owner, repo, branch) 去重，只查一次 API
	type repoKey struct {
		owner, repo, branch string
	}
	repoSHAs := make(map[repoKey]string)   // repo -> commit SHA
	repoErrors := make(map[repoKey]string)  // repo -> error
	repoChecked := make(map[repoKey]bool)   // 是否已查询
	var repoMu sync.Mutex
	sem := make(chan struct{}, 5) // 并发限制

	// 收集所有唯一的 repo key
	var uniqueRepos []repoKey
	seen := make(map[repoKey]bool)
	for _, sk := range skillsList {
		branch := sk.RepoBranch
		if branch == "" {
			branch = "main"
		}
		rk := repoKey{sk.RepoOwner, sk.RepoName, branch}
		if !seen[rk] {
			seen[rk] = true
			uniqueRepos = append(uniqueRepos, rk)
		}
	}

	// 并发查询每个唯一仓库的 commit SHA
	var wg sync.WaitGroup
	for _, rk := range uniqueRepos {
		wg.Add(1)
		go func(key repoKey) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()

			ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
			defer cancel()

			sha, err := fetchSkillCommitSHAFunc(ctx, key.owner, key.repo, key.branch)
			repoMu.Lock()
			defer repoMu.Unlock()
			if err != nil {
				repoErrors[key] = err.Error()
			} else {
				repoSHAs[key] = sha
			}
			repoChecked[key] = true
		}(rk)
	}
	wg.Wait()

	// 为每个 skill 生成 UpdateStatus
	results := make([]UpdateStatus, len(skillsList))
	for i, sk := range skillsList {
		branch := sk.RepoBranch
		if branch == "" {
			branch = "main"
		}
		rk := repoKey{sk.RepoOwner, sk.RepoName, branch}

		status := UpdateStatus{
			SkillID:   sk.ID,
			Directory: sk.Directory,
			CheckedAt: time.Now().UTC().Format(time.RFC3339),
		}

		if errMsg, hasErr := repoErrors[rk]; hasErr {
			status.Error = errMsg
		} else if sha, ok := repoSHAs[rk]; ok {
			status.RemoteHash = sha
			// 与缓存中的基线对比
			if cached, ok := cache[sk.ID]; ok {
				if cached.CommitSHA != "" && cached.CommitSHA != sha {
					status.HasUpdate = true
				}
			}
			// 更新缓存
			cache[sk.ID] = updateCacheEntry{
				CommitSHA: sha,
				CheckedAt: status.CheckedAt,
			}
		}

		results[i] = status
	}

	// 持久化更新后的缓存（仅在有成功查询时写入，避免错误覆盖基线）
	if len(repoSHAs) > 0 {
		if err := writeUpdateCache(cache); err != nil {
			log.Printf("warning: write update cache: %v", err)
		}
	}

	return results
}

// UpdateSkill updates a single skill to the latest remote version
func (s *Store) UpdateSkill(skillID string, reg *agents.Registry) (Skill, error) {
	s.mu.RLock()
	sk, ok := s.skills[skillID]
	if !ok {
		s.mu.RUnlock()
		return Skill{}, fmt.Errorf("skill %s not found", skillID)
	}
	if sk.RepoOwner == "" || sk.RepoName == "" {
		s.mu.RUnlock()
		return Skill{}, fmt.Errorf("skill %s has no repo info", skillID)
	}
	// Copy needed fields before releasing lock
	boundAgents := copySlice(sk.BoundAgents)
	s.mu.RUnlock()

	branch := sk.RepoBranch
	if branch == "" {
		branch = "main"
	}

	// Get FullPath from skill or lock file
	fullPath := sk.FullPath
	if fullPath == "" {
		if lk, ok := ParseAgentsLock()[sk.Directory]; ok {
			fullPath = lk.FullPath
		}
	}

	// Build tarball URL
	tarballURL := fmt.Sprintf("https://codeload.github.com/%s/%s/tar.gz/refs/heads/%s",
		sk.RepoOwner, sk.RepoName, branch)

	input := TarballInstallInput{
		TarballURL: tarballURL,
		Directory:  sk.Directory,
		FullPath:   fullPath,
		RepoOwner:  sk.RepoOwner,
		RepoName:   sk.RepoName,
		RepoBranch: branch,
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	// Remove old version from SSOT
	ssotPath := filepath.Join(s.ssotDir, sk.Directory)
	if err := RemovePath(ssotPath); err != nil {
		return Skill{}, fmt.Errorf("remove old skill dir: %w", err)
	}

	// Install new version
	updated, err := s.InstallFromTarball(ctx, input, boundAgents, reg)
	if err != nil {
		return Skill{}, fmt.Errorf("install updated skill: %w", err)
	}

	// Update commit SHA cache
	if err := CacheSkillCommitSHA(skillID, sk.RepoOwner, sk.RepoName, branch); err != nil {
		log.Printf("warning: cache commit SHA after update: %v", err)
	}

	return updated, nil
}

// UpdateSkills batch-updates multiple skills
func (s *Store) UpdateSkills(skillIDs []string, reg *agents.Registry) UpdateSkillsResult {
	if len(skillIDs) == 0 {
		return UpdateSkillsResult{}
	}

	var successes []Skill
	var errs []UpdateError
	sem := make(chan struct{}, 3)
	var mu sync.Mutex
	var wg sync.WaitGroup

	for _, id := range skillIDs {
		wg.Add(1)
		go func(skillID string) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()

			updated, err := s.UpdateSkill(skillID, reg)
			mu.Lock()
			defer mu.Unlock()
			if err != nil {
				errs = append(errs, UpdateError{
					SkillID: skillID,
					Error:   err.Error(),
				})
			} else {
				successes = append(successes, updated)
			}
		}(id)
	}

	wg.Wait()
	return UpdateSkillsResult{Updated: successes, Errors: errs}
}
