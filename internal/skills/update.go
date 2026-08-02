package skills

import (
	"agentpack/internal/config"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"agentpack/internal/agents"
	"agentpack/internal/iowriter"
	"agentpack/internal/shared"
)

// errTreeStale 表示 jsDelivr 文件树可能过时（树中文件下载 404，
// 如仓库结构已变更但 CDN 缓存未刷新），需要切换到 GitHub 实时树重试。
var errTreeStale = errors.New("jsdelivr tree appears stale")

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

var writeUpdateCacheFunc = writeUpdateCacheImpl

// updateCacheMu 串行化更新缓存文件的读-改-写：
// UpdateSkills 会并发调用 CacheSkillCommitSHA，不加锁时后写的整份文件会
// 覆盖先写的条目，导致部分技能丢失更新检测基线。
var updateCacheMu sync.Mutex

func writeUpdateCacheImpl(skills map[string]updateCacheEntry) error {
	return writeUpdateCache(skills)
}

// resolveDefaultBranch 返回 skill 实际使用的分支名
// 若 RepoBranch 非空则直接返回；否则先返回 main，失败后再回退到 master
func resolveDefaultBranch(sk *Skill) string {
	if sk.RepoBranch != "" {
		return sk.RepoBranch
	}
	return "main"
}

// fetchWithBranchFallback 先尝试 branch，失败后回退到 fallback
func fetchWithBranchFallback(ctx context.Context, owner, repo, branch, fallback string) (string, error) {
	if branch == "" {
		branch = "main"
	}
	sha, err := fetchSkillCommitSHAFunc(ctx, owner, repo, branch)
	if err == nil {
		return sha, nil
	}
	if fallback != "" && fallback != branch {
		return fetchSkillCommitSHAFunc(ctx, owner, repo, fallback)
	}
	return "", err
}

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
	// 禁止 git 交互式凭据提示与 Git Credential Manager 弹窗：
	// 需要认证时立即失败返回，而不是唤起系统凭据窗口。
	cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0", "GCM_INTERACTIVE=never")
	hideGitConsoleWindow(cmd)
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
	updateCacheMu.Lock()
	defer updateCacheMu.Unlock()
	cache := readUpdateCache()
	if cache == nil {
		cache = make(map[string]updateCacheEntry)
	}
	cache[skillID] = updateCacheEntry{
		CommitSHA: sha,
		CheckedAt: time.Now().UTC().Format(time.RFC3339),
	}
	return writeUpdateCacheFunc(cache)
}

// resolveSkillFullPath 返回技能在仓库中的完整相对路径（如 "skills/pdf"）。
// 优先使用技能自身记录的 FullPath，缺失时从 lock 文件回退。
func resolveSkillFullPath(sk *Skill) string {
	if sk.FullPath != "" {
		return sk.FullPath
	}
	if lk, ok := ParseAgentsLock()[sk.Directory]; ok {
		return lk.FullPath
	}
	return ""
}

// CheckUpdates 检查所有已安装 skills 的远程更新。
// 仅检查 RepoOwner/RepoName 非空的条目（从 GitHub 安装的 skills）。
// 主链路：jsDelivr data API 文件树 + 逐文件内容 hash 对比（中国地区可达性好、
// 且按技能目录精确判断，避免仓库级误报）。
// fallback：jsDelivr 不可达时回退到 git ls-remote + commit 缓存基线（旧机制）。
func (s *Store) CheckUpdates(reg *agents.Registry) []UpdateStatus {
	s.mu.RLock()
	skillsList := make([]Skill, 0, len(s.skills))
	for _, sk := range s.skills {
		if sk.RepoOwner != "" && sk.RepoName != "" {
			skillsList = append(skillsList, sk)
		}
	}
	ssotDir := s.ssotDir
	s.mu.RUnlock()

	if len(skillsList) == 0 {
		return nil
	}

	// 读取缓存基线（git fallback 模式使用）
	cache := readUpdateCache()
	if cache == nil {
		cache = make(map[string]updateCacheEntry)
	}

	// 按 (owner, repo, branch) 去重，同一仓库只查一次
	type repoKey struct {
		owner, repo, branch string
	}
	var uniqueRepos []repoKey
	seen := make(map[repoKey]bool)
	for _, sk := range skillsList {
		branch := resolveDefaultBranch(&sk)
		rk := repoKey{sk.RepoOwner, sk.RepoName, branch}
		if !seen[rk] {
			seen[rk] = true
			uniqueRepos = append(uniqueRepos, rk)
		}
	}

	type repoResult struct {
		tree   remoteTree // jsDelivr / GitHub 文件树
		gitSHA string     // git fallback 的分支头 commit SHA
		err    error
	}
	repoRes := make(map[repoKey]repoResult)
	var repoMu sync.Mutex
	sem := make(chan struct{}, 5) // 并发限制
	var wg sync.WaitGroup
	for _, rk := range uniqueRepos {
		wg.Add(1)
		go func(key repoKey) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()

			treeCtx, cancelTree := context.WithTimeout(context.Background(), 30*time.Second)
			tree, treeErr := fetchRemoteTree(treeCtx, key.owner, key.repo, key.branch)
			cancelTree()
			if treeErr == nil {
				repoMu.Lock()
				repoRes[key] = repoResult{tree: tree}
				repoMu.Unlock()
				return
			}

			// jsDelivr 不可达（或仓库/分支不识别）：回退 git ls-remote
			gitCtx, cancelGit := context.WithTimeout(context.Background(), 15*time.Second)
			sha, gitErr := fetchWithBranchFallback(gitCtx, key.owner, key.repo, key.branch, "master")
			cancelGit()
			repoMu.Lock()
			if gitErr != nil {
				repoRes[key] = repoResult{err: fmt.Errorf("jsdelivr: %v; git fallback: %w", treeErr, gitErr)}
			} else {
				repoRes[key] = repoResult{gitSHA: sha}
			}
			repoMu.Unlock()
		}(rk)
	}
	wg.Wait()

	// 为每个 skill 生成 UpdateStatus
	now := time.Now().UTC().Format(time.RFC3339)
	results := make([]UpdateStatus, len(skillsList))
	gitWroteCache := false
	for i, sk := range skillsList {
		branch := resolveDefaultBranch(&sk)
		rk := repoKey{sk.RepoOwner, sk.RepoName, branch}
		res := repoRes[rk]

		status := UpdateStatus{
			SkillID:   sk.ID,
			Directory: sk.Directory,
			CheckedAt: now,
		}

		switch {
		case res.err != nil:
			// 查询失败：保留错误信息，不更新缓存基线
			status.Error = res.err.Error()
		case len(res.tree.files) > 0:
			// 内容级：远程树 vs 本地文件（jsDelivr SHA-256 / GitHub blob SHA-1）
			fullPath := resolveSkillFullPath(&sk)
			localDir := filepath.Join(ssotDir, sk.Directory)
			if fullPath == "" {
				// 存量数据缺失 fullPath：从远程树定位真实技能目录，
				// 避免把整个仓库当成技能内容导致永久误报"有更新"。
				fullPath = resolveSkillDirInTree(res.tree.files, sk.Directory)
				if fullPath == "" {
					// 名字定位失败：按内容扫描（目录名可与本地不同，
					// 与回填功能的"内容优先"匹配保持一致）。
					locateCtx, cancelLocate := context.WithTimeout(context.Background(), 30*time.Second)
					var lerr error
					fullPath, lerr = findSkillDirByContent(locateCtx, res.tree, sk.RepoOwner, sk.RepoName, branch, localDir)
					cancelLocate()
					if lerr != nil {
						status.Error = fmt.Sprintf("locate skill by content: %v", lerr)
						results[i] = status
						continue
					}
					if fullPath == "" {
						// 名字与内容都定位不到：来源关联无效（如 skills.sh 元数据
						// 与仓库实际不符），移除错误关联并静默跳过，不显示失败。
						log.Printf("CheckUpdates: %s: source %s/%s has no matching skill, removing lock entry",
							sk.Directory, sk.RepoOwner, sk.RepoName)
						if rerr := RemoveAgentsLockEntry(sk.Directory); rerr != nil {
							log.Printf("CheckUpdates: remove invalid lock entry for %s: %v", sk.Directory, rerr)
						}
						results[i] = status
						continue
					}
				}
				// 修复存量数据：把推断出的路径写回 lock 文件
				if err := persistSkillRepoInfo(sk, branch, fullPath, localDir); err != nil {
					log.Printf("warning: persist full path for %s: %v", sk.Directory, err)
				}
			}
			changed, hasDiff := skillRemoteDiffWith(res.tree, fullPath, localDir)
			if hasDiff {
				// jsDelivr 树 hash 可能与实际文件内容不一致（实测存在），
				// 对候选差异做字节级下载验证；下载失败（树过时/网络）时
				// 用 GitHub 实时树交叉验证。
				verifyCtx, cancelVerify := context.WithTimeout(context.Background(), 30*time.Second)
				realChanged, verr := verifyChangedFiles(verifyCtx, sk, fullPath, branch, localDir, res.tree, changed)
				cancelVerify()
				if verr != nil {
					ghCtx, cancelGH := context.WithTimeout(context.Background(), 30*time.Second)
					ghTree, gerr := fetchGitHubFileTree(ghCtx, sk.RepoOwner, sk.RepoName, branch)
					cancelGH()
					if gerr == nil {
						ghRT := remoteTree{files: ghTree, source: treeSourceGitHub, hashFn: gitBlobSHA1Hex}
						ghChanged, ghDiff := skillRemoteDiffWith(ghRT, fullPath, localDir)
						if !ghDiff {
							hasDiff = false
						} else {
							changed = ghChanged
						}
					} else {
						status.Error = fmt.Sprintf("verify update: %v; github tree: %v", verr, gerr)
						results[i] = status
						continue
					}
				} else {
					hasDiff = len(realChanged) > 0
					changed = realChanged
				}
			}
			if hasDiff {
				status.HasUpdate = true
				status.RemoteHash = remoteTreeHash(res.tree, fullPath)
				status.LocalHash = localTreeHash(res.tree.hashFn, localDir)
				status.ChangedFiles = changed
			}
		case res.gitSHA != "":
			// git fallback：与缓存基线对比
			status.RemoteHash = res.gitSHA
			if cached, ok := cache[sk.ID]; ok {
				if cached.CommitSHA != "" && cached.CommitSHA != res.gitSHA {
					status.HasUpdate = true
				}
			}
			cache[sk.ID] = updateCacheEntry{
				CommitSHA: res.gitSHA,
				CheckedAt: status.CheckedAt,
			}
			gitWroteCache = true
		}

		results[i] = status
	}

	// 仅在 git fallback 模式持久化缓存基线
	if gitWroteCache {
		updateCacheMu.Lock()
		if err := writeUpdateCacheFunc(cache); err != nil {
			log.Printf("warning: write update cache: %v", err)
		}
		updateCacheMu.Unlock()
	}

	return results
}

// UpdateSkill updates a single skill to the latest remote version.
// 主链路：jsDelivr 内容级文件增量更新（只替换变化的文件，保留本地额外文件）。
// fallback：jsDelivr 不可达时回退到 tarball 全量重装（旧机制）。
// 两条链路失败时都会从备份恢复旧版本，保证现场不被破坏。
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
	// Copy the live binding state before releasing the lock. The serialized
	// BoundAgents field can be stale after an individual agent is toggled.
	boundAgents := copySlice(boundAgentsFromMap(s.bindings, skillID))
	ssotDir := s.ssotDir
	syncMethod := s.syncMethod
	s.mu.RUnlock()

	branch := resolveDefaultBranch(&sk)
	fullPath := resolveSkillFullPath(&sk)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	// Back up the old version before replacing it so any failure can restore
	// the previous working state.
	backupDir, err := os.MkdirTemp("", "skill-update-backup-")
	if err != nil {
		return Skill{}, fmt.Errorf("create update backup dir: %w", err)
	}
	defer os.RemoveAll(backupDir)
	ssotPath := filepath.Join(ssotDir, sk.Directory)
	backupPath := filepath.Join(backupDir, sk.Directory)
	if err := copyDirRecursive(ssotPath, backupPath); err != nil {
		return Skill{}, fmt.Errorf("backup old skill: %w", err)
	}

	restoreFromBackup := func() error {
		if err := RemovePath(ssotPath); err != nil {
			return err
		}
		return copyDirRecursive(backupPath, ssotPath)
	}
	resyncAgents := func() error {
		var errs []string
		for _, agID := range boundAgents {
			agentDir := reg.AgentSkillsDir(agID)
			if agentDir == "" {
				continue
			}
			if syncErr := SyncToAgentDir(ssotPath, filepath.Join(agentDir, sk.Directory), syncMethod); syncErr != nil {
				errs = append(errs, fmt.Sprintf("agent %s: %v", agID, syncErr))
			}
		}
		if len(errs) > 0 {
			return fmt.Errorf("%s", strings.Join(errs, "; "))
		}
		return nil
	}

	// 主链路：jsDelivr 文件级增量更新
	updated, err := s.updateSkillViaJsDelivr(ctx, sk, fullPath, branch, ssotDir, boundAgents, reg, syncMethod)
	if err == nil {
		// 刷新 commit 缓存基线（jsDelivr 检测不依赖它，但保留 git fallback 可用）
		if cacheErr := CacheSkillCommitSHA(skillID, sk.RepoOwner, sk.RepoName, branch); cacheErr != nil {
			log.Printf("warning: cache commit SHA after update: %v", cacheErr)
		}
		return updated, nil
	}
	// jsDelivr 树过时（CDN 缓存旧路径）：切换到 GitHub 实时树重试
	if errors.Is(err, errTreeStale) {
		log.Printf("UpdateSkill: jsdelivr tree stale for %s, retrying with github tree", skillID)
		updated, err2 := s.updateSkillViaGitHubTree(ctx, sk, fullPath, branch, ssotDir, boundAgents, reg, syncMethod)
		if err2 == nil {
			if cacheErr := CacheSkillCommitSHA(skillID, sk.RepoOwner, sk.RepoName, branch); cacheErr != nil {
				log.Printf("warning: cache commit SHA after update: %v", cacheErr)
			}
			return updated, nil
		}
		err = fmt.Errorf("jsdelivr: %v; github tree: %w", err, err2)
	}
	log.Printf("UpdateSkill: tree-based update failed for %s: %v, falling back to tarball", skillID, err)

	// fallback：先恢复备份（jsDelivr 阶段可能已部分写入），再走 tarball 全量
	if restoreErr := restoreFromBackup(); restoreErr != nil {
		return Skill{}, fmt.Errorf("jsdelivr update: %v; restore backup: %w", err, restoreErr)
	}
	if err := RemovePath(ssotPath); err != nil {
		return Skill{}, fmt.Errorf("remove old skill dir: %w", err)
	}

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
	updated, err = s.InstallFromTarball(ctx, input, boundAgents, reg)
	if err != nil {
		restoreErr := restoreFromBackup()
		if syncErr := resyncAgents(); syncErr != nil {
			if restoreErr == nil {
				restoreErr = syncErr
			} else {
				restoreErr = fmt.Errorf("%v; resync agents: %w", restoreErr, syncErr)
			}
		}
		if restoreErr != nil {
			return Skill{}, fmt.Errorf("install updated skill: %v; restore old skill: %w", err, restoreErr)
		}
		return Skill{}, fmt.Errorf("install updated skill: %w", err)
	}

	// Update commit SHA cache
	if err := CacheSkillCommitSHA(skillID, sk.RepoOwner, sk.RepoName, branch); err != nil {
		log.Printf("warning: cache commit SHA after update: %v", err)
	}
	return updated, nil
}

// updateSkillViaJsDelivr 使用 jsDelivr 内容 CDN 做文件级增量更新。
// jsDelivr 树不可用或下载 404（树过时）时返回错误，由调用方决定切换
// GitHub 实时树或回退 tarball。
func (s *Store) updateSkillViaJsDelivr(ctx context.Context, sk Skill, fullPath, branch, ssotDir string, boundAgents []string, reg *agents.Registry, syncMethod SyncMethod) (Skill, error) {
	tree, err := fetchRemoteTree(ctx, sk.RepoOwner, sk.RepoName, branch)
	if err != nil {
		return Skill{}, fmt.Errorf("fetch remote file tree: %w", err)
	}
	return s.updateSkillViaTree(ctx, sk, fullPath, branch, ssotDir, boundAgents, reg, syncMethod, tree, false)
}

// updateSkillViaGitHubTree 使用 GitHub Trees API（实时）+ raw/代理下载重试更新，
// 用于 jsDelivr 树过时（CDN 缓存旧路径）的场景。GitHub 树是权威来源，
// 下载 404 视为远端文件已删除/移动，跳过而不是整体失败。
func (s *Store) updateSkillViaGitHubTree(ctx context.Context, sk Skill, fullPath, branch, ssotDir string, boundAgents []string, reg *agents.Registry, syncMethod SyncMethod) (Skill, error) {
	tree, err := fetchGitHubFileTree(ctx, sk.RepoOwner, sk.RepoName, branch)
	if err != nil {
		return Skill{}, fmt.Errorf("fetch github file tree: %w", err)
	}
	rt := remoteTree{files: tree, source: treeSourceGitHub, hashFn: gitBlobSHA1Hex}
	return s.updateSkillViaTree(ctx, sk, fullPath, branch, ssotDir, boundAgents, reg, syncMethod, rt, true)
}

// updateSkillViaTree 使用给定文件树做文件级增量更新：
//  1. 过滤技能目录文件（含每个文件的内容 hash）
//  2. 只下载与本地不一致的文件到临时目录（下载失败不改动现场）
//  3. 全部下载成功后合并进 SSOT（覆盖变化文件，保留本地额外文件）
//  4. 重建 agent 目录副本/链接
//
// skipMissing 为 true 时（GitHub 实时树），下载 404 视为远端文件已删除，跳过；
// 为 false 时（jsDelivr 树），下载 404 返回 errTreeStale 触发 GitHub 树重试。
func (s *Store) updateSkillViaTree(ctx context.Context, sk Skill, fullPath, branch, ssotDir string, boundAgents []string, reg *agents.Registry, syncMethod SyncMethod, tree remoteTree, skipMissing bool) (Skill, error) {
	if fullPath == "" {
		// 存量数据缺失 fullPath：从远程树定位真实技能目录
		fullPath = resolveSkillDirInTree(tree.files, sk.Directory)
		if fullPath == "" {
			return Skill{}, fmt.Errorf("cannot locate skill directory %q in remote tree", sk.Directory)
		}
	}
	remoteFiles := filterTreeByPrefix(tree.files, fullPath)
	if len(remoteFiles) == 0 {
		return Skill{}, fmt.Errorf("no remote files found for %q in %s/%s@%s",
			fullPath, sk.RepoOwner, sk.RepoName, branch)
	}

	ssotPath := filepath.Join(ssotDir, sk.Directory)
	tmpDir, err := os.MkdirTemp("", "skill-update-files-")
	if err != nil {
		return Skill{}, fmt.Errorf("create download dir: %w", err)
	}
	defer os.RemoveAll(tmpDir)

	// 先全部下载到临时目录，避免下载中途失败破坏 SSOT
	downloadCtx, cancelDownload := context.WithCancel(ctx)
	defer cancelDownload()
	const downloadConcurrency = 5
	sem := make(chan struct{}, downloadConcurrency)
	var wg sync.WaitGroup
	var mu sync.Mutex
	var changed []string
	var firstErr error
	for rel, rh := range remoteFiles {
		rel, rh := rel, rh
		wg.Add(1)
		go func() {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()

			target := filepath.Join(ssotPath, filepath.FromSlash(rel))
			if localHashEqual(target, rh, tree.hashFn) {
				return
			}
			// 下载 URL 使用完整仓库路径（fullPath + 相对技能目录路径）
			remotePath := rel
			if prefix := strings.Trim(fullPath, "/"); prefix != "" {
				remotePath = prefix + "/" + rel
			}
			data, derr := downloadRemoteFile(downloadCtx, sk.RepoOwner, sk.RepoName, branch, remotePath,
				tree.source == treeSourceJsDelivr)
			if derr != nil {
				var se *httpStatusError
				is404 := errors.As(derr, &se) && se.status == http.StatusNotFound
				if is404 && tree.source == treeSourceJsDelivr {
					// jsDelivr 对该文件不可服务（大文件限制/缓存差异）或树过时：
					// 先用 raw/代理重试一次；raw 也 404 才判定树过时切 GitHub 树。
					data2, derr2 := downloadRemoteFile(downloadCtx, sk.RepoOwner, sk.RepoName, branch, remotePath, false)
					if derr2 == nil {
						if werr := writeTmpFile(tmpDir, rel, data2); werr != nil {
							mu.Lock()
							if firstErr == nil {
								firstErr = werr
							}
							mu.Unlock()
							cancelDownload()
							return
						}
						mu.Lock()
						changed = append(changed, rel)
						mu.Unlock()
						return
					}
					mu.Lock()
					if firstErr == nil {
						firstErr = errTreeStale
					}
					mu.Unlock()
					cancelDownload()
					return
				}
				if is404 && skipMissing {
					// GitHub 实时树：文件不存在视为已删除/移动，跳过
					log.Printf("updateSkillViaTree: %s: skip missing remote file %s", sk.Directory, rel)
					return
				}
				mu.Lock()
				if firstErr == nil {
					firstErr = fmt.Errorf("download %s: %w", rel, derr)
				}
				mu.Unlock()
				cancelDownload()
				return
			}
			if werr := writeTmpFile(tmpDir, rel, data); werr != nil {
				mu.Lock()
				if firstErr == nil {
					firstErr = werr
				}
				mu.Unlock()
				cancelDownload()
				return
			}
			mu.Lock()
			changed = append(changed, rel)
			mu.Unlock()
		}()
	}
	wg.Wait()
	if firstErr != nil {
		return Skill{}, firstErr
	}

	if len(changed) == 0 {
		// 已是最新：仅刷新元数据
		return s.refreshSkillAfterFileUpdate(sk.ID, ssotPath)
	}
	sort.Strings(changed)
	log.Printf("updateSkillViaTree: %s: %d file(s) changed: %v", sk.Directory, len(changed), changed)

	// 合并进 SSOT（覆盖变化文件，保留本地额外文件）
	if err := mergeDirOverwrite(tmpDir, ssotPath); err != nil {
		return Skill{}, fmt.Errorf("apply updated files: %w", err)
	}

	// 重建 agent 目录副本/链接
	for _, agID := range boundAgents {
		agentDir := reg.AgentSkillsDir(agID)
		if agentDir == "" {
			continue
		}
		target := filepath.Join(agentDir, sk.Directory)
		if err := RemovePath(target); err != nil {
			return Skill{}, fmt.Errorf("remove agent copy for %s: %w", agID, err)
		}
		if err := SyncToAgentDir(ssotPath, target, syncMethod); err != nil {
			return Skill{}, fmt.Errorf("sync to agent %s: %w", agID, err)
		}
	}

	// 修复存量数据：fullPath 缺失时把推断出的路径写回 lock 文件
	if sk.FullPath == "" {
		if err := persistSkillRepoInfo(sk, branch, fullPath, ssotPath); err != nil {
			log.Printf("warning: persist full path for %s: %v", sk.Directory, err)
		}
	}

	return s.refreshSkillAfterFileUpdate(sk.ID, ssotPath)
}

// persistSkillRepoInfo 将技能仓库信息（含推断出的 fullPath）写回
// ~/.agents/.skill-lock.json，用于修复历史安装数据缺失的路径字段。
func persistSkillRepoInfo(sk Skill, branch, fullPath, ssotPath string) error {
	if sk.RepoOwner == "" || sk.RepoName == "" {
		return nil
	}
	if branch == "" {
		branch = "main"
	}
	return WriteAgentsLock(AgentsLockEntry{
		Directory:  sk.Directory,
		Source:     sk.RepoOwner + "/" + sk.RepoName,
		SourceType: "github",
		SourceURL:  "https://github.com/" + sk.RepoOwner + "/" + sk.RepoName,
		SkillPath:  ssotPath,
		Branch:     branch,
		FullPath:   fullPath,
	})
}

// writeTmpFile 将下载内容写入临时目录中对应的相对路径。
func writeTmpFile(tmpDir, rel string, data []byte) error {
	tmpTarget := filepath.Join(tmpDir, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(tmpTarget), 0755); err != nil {
		return err
	}
	return os.WriteFile(tmpTarget, data, 0644)
}

// refreshSkillAfterFileUpdate 重新计算技能内容 hash 并刷新内存中的
// ContentHash / UpdatedAt，返回最新副本。
func (s *Store) refreshSkillAfterFileUpdate(skillID, ssotPath string) (Skill, error) {
	contentHash, complete := HashDir(ssotPath)
	if !complete {
		log.Printf("warning: skill content hash may be incomplete for %s", ssotPath)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	sk, ok := s.skills[skillID]
	if !ok {
		return Skill{}, fmt.Errorf("skill %s not found after update", skillID)
	}
	sk.ContentHash = contentHash
	sk.UpdatedAt = shared.NowRFC3339()
	s.skills[skillID] = sk
	sk.BoundAgents = copySlice(boundAgentsFromMap(s.bindings, skillID))
	return sk, nil
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
