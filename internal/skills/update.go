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
	CommitSHA    string   `json:"commitSha"`
	CheckedAt    string   `json:"checkedAt"`
	TreeHash     string   `json:"treeHash,omitempty"`     // 远程树聚合 hash（内容级检测）
	LocalHash    string   `json:"localHash,omitempty"`    // 本地内容聚合 hash（内容级检测）
	HasUpdate    bool     `json:"hasUpdate,omitempty"`    // 上次确认的结果
	ChangedFiles []string `json:"changedFiles,omitempty"` // 上次确认的差异文件
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
	prev := cache[skillID]
	cache[skillID] = updateCacheEntry{
		CommitSHA:    sha,
		CheckedAt:    time.Now().UTC().Format(time.RFC3339),
		TreeHash:     prev.TreeHash,
		LocalHash:    prev.LocalHash,
		HasUpdate:    prev.HasUpdate,
		ChangedFiles: prev.ChangedFiles,
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
	wroteCache := false
	// modified 记录本次运行更新过的缓存条目，写回前用于合并到最新缓存，
	// 避免与并发的 CacheSkillCommitSHA 互相覆盖（read-modify-write）。
	modified := make(map[string]bool)
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
			// fullPath 缺失或已失效（远程目录改名/移动）时重新定位：失效路径下
			// filterTreeByPrefix 返回空集，skillRemoteDiffWith 恒判"无更新"，
			// 且空集的 TreeHash 是常量，会被缓存固化为永久假阴性。
			if fullPath == "" || len(filterTreeByPrefix(res.tree.files, fullPath)) == 0 {
				if fullPath != "" {
					log.Printf("CheckUpdates: %s: stale full path %q, relocating", sk.Directory, fullPath)
				}
				// 从远程树定位真实技能目录，避免把整个仓库当成技能内容
				// 导致永久误报"有更新"。
				fullPath = resolveSkillDirInTree(res.tree.files, sk.Directory)
				if fullPath == "" {
					// 名字定位失败：按内容扫描（目录名可与本地不同，
					// 与回填功能的"内容优先"匹配保持一致）。
					locateCtx, cancelLocate := context.WithTimeout(context.Background(), 30*time.Second)
					var lerr error
					var truncated bool
					fullPath, truncated, lerr = findSkillDirByContent(locateCtx, res.tree, sk.RepoOwner, sk.RepoName, branch, localDir)
					cancelLocate()
					if lerr != nil {
						status.Error = fmt.Sprintf("locate skill by content: %v", lerr)
						results[i] = status
						continue
					}
					if fullPath == "" {
						if truncated {
							// 候选被限量截断（仓库含 SKILL.md 的目录过多）：本次
							// 无法判定"来源无效"，保守跳过并保留来源关联——
							// 删除关联是破坏性操作，且截断场景下目标目录可能
							// 恰好排在候选之外。
							log.Printf("CheckUpdates: %s: skill dir scan truncated, keeping source association",
								sk.Directory)
							results[i] = status
							continue
						}
						// 名字与内容都定位不到：来源关联无效（如 skills.sh 元数据
						// 与仓库实际不符），移除错误关联并静默跳过，不显示失败。
						log.Printf("CheckUpdates: %s: source %s/%s has no matching skill, removing lock entry",
							sk.Directory, sk.RepoOwner, sk.RepoName)
						if rerr := RemoveAgentsLockEntry(sk.Directory); rerr != nil {
							log.Printf("CheckUpdates: remove invalid lock entry for %s: %v", sk.Directory, rerr)
						}
						// 内存同步清理：仅删 lock 条目时，List 优先读内存，UI 仍
						// 显示旧来源、UpdateSkill 仍用无效来源走完整失败链路，
						// 直到下次 Load 才一致。清内存后 UI 立即回到"无来源"状态，
						// 更新按钮随 RepoOwner 判空禁用，行为与 lock 文件一致。
						s.mu.Lock()
						if cur, ok := s.skills[sk.ID]; ok {
							cur.RepoOwner = ""
							cur.RepoName = ""
							cur.RepoBranch = ""
							cur.FullPath = ""
							s.skills[sk.ID] = cur
						}
						s.mu.Unlock()
						results[i] = status
						continue
					}
				}
				// 修复存量数据：把推断出的路径写回 lock 文件
				if err := persistSkillRepoInfo(sk, branch, fullPath, localDir); err != nil {
					log.Printf("warning: persist full path for %s: %v", sk.Directory, err)
				}
			}
			treeAgg := remoteTreeHash(res.tree, fullPath)
			localAgg := localTreeHash(res.tree.hashFn, localDir)
			// 远程树与本地内容均未变化时复用上次确认的结果，
			// 跳过逐文件 diff 与差异文件下载验证（检查的主要耗时来源）。
			if cached, ok := cache[sk.ID]; ok &&
				cached.TreeHash == treeAgg && cached.LocalHash == localAgg {
				status.RemoteHash = treeAgg
				status.LocalHash = localAgg
				status.HasUpdate = cached.HasUpdate
				status.ChangedFiles = cached.ChangedFiles
				results[i] = status
				continue
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
				status.RemoteHash = treeAgg
				status.LocalHash = localAgg
				status.ChangedFiles = changed
			}
			// 按字段合并保留旧条目：内容级路径不写 CommitSHA，git fallback 路径
			// 不写 TreeHash/LocalHash，整条替换会互相覆盖，导致内容↔git 模式
			// 切换后基线丢失（检测延迟一个周期）与每次更新后内容级缓存 miss。
			prev := cache[sk.ID]
			cache[sk.ID] = updateCacheEntry{
				TreeHash:     treeAgg,
				LocalHash:    localAgg,
				HasUpdate:    status.HasUpdate,
				ChangedFiles: status.ChangedFiles,
				CheckedAt:    status.CheckedAt,
				CommitSHA:    prev.CommitSHA,
			}
			modified[sk.ID] = true
			wroteCache = true
		case res.gitSHA != "":
			// git fallback：与缓存基线对比
			status.RemoteHash = res.gitSHA
			if cached, ok := cache[sk.ID]; ok {
				if cached.CommitSHA != "" && cached.CommitSHA != res.gitSHA {
					status.HasUpdate = true
				}
			}
			// 按字段合并保留旧条目（见内容级路径注释）
			prev := cache[sk.ID]
			cache[sk.ID] = updateCacheEntry{
				CommitSHA:    res.gitSHA,
				CheckedAt:    status.CheckedAt,
				TreeHash:     prev.TreeHash,
				LocalHash:    prev.LocalHash,
				HasUpdate:    status.HasUpdate,
				ChangedFiles: prev.ChangedFiles,
			}
			modified[sk.ID] = true
			wroteCache = true
		}

		results[i] = status
	}

	// 内容级或 git fallback 任一产生新结果时持久化缓存
	if wroteCache {
		updateCacheMu.Lock()
		// 重新读取最新缓存，合并本次新增/更新的条目，避免与并发的
		// CacheSkillCommitSHA 互相覆盖（read-modify-write，与 CacheSkillCommitSHA 一致）。
		latest := readUpdateCache()
		if latest == nil {
			latest = make(map[string]updateCacheEntry)
		}
		for id := range modified {
			latest[id] = cache[id]
		}
		if err := writeUpdateCacheFunc(latest); err != nil {
			log.Printf("warning: write update cache: %v", err)
		}
		updateCacheMu.Unlock()
	}

	return results
}

// skillStillManaged 检查技能是否仍在 store 纳管中。
// 用于更新流程的写回拦截点：更新下载期间用户可能已卸载该技能，
// 重建/重装目录会静默逆转卸载操作。
func (s *Store) skillStillManaged(skillID string) bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	_, ok := s.skills[skillID]
	return ok
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
	// the previous working state. 备份阶段持 importMu 与 Import/Uninstall 互斥，
	// 防止读到被并发 Import 半写入的 SSOT 目录。
	backupDir, err := os.MkdirTemp("", "skill-update-backup-")
	if err != nil {
		return Skill{}, fmt.Errorf("create update backup dir: %w", err)
	}
	defer os.RemoveAll(backupDir)
	ssotPath := filepath.Join(ssotDir, sk.Directory)
	backupPath := filepath.Join(backupDir, sk.Directory)
	s.importMu.Lock()
	copyErr := copyDirRecursive(ssotPath, backupPath)
	s.importMu.Unlock()
	if copyErr != nil {
		return Skill{}, fmt.Errorf("backup old skill: %w", copyErr)
	}

	restoreFromBackup := func() error {
		s.importMu.Lock()
		defer s.importMu.Unlock()
		// 锁内复查：等待锁期间用户可能已卸载该技能（Uninstall 已删除 SSOT
		// 目录）或换源重装同名技能（目录已是新来源内容）。此时恢复旧备份
		// 会重建被删除的目录（把用户的卸载静默逆转成孤儿纳管）或覆盖新来源
		// 内容，必须中止。与 RemovePath 前（605 行）的复查语义一致。
		if !s.skillStillManaged(skillID) {
			return fmt.Errorf("skill %s was uninstalled during update, aborting", skillID)
		}
		s.mu.RLock()
		current := s.skills[skillID]
		s.mu.RUnlock()
		if current.RepoOwner != sk.RepoOwner || current.RepoName != sk.RepoName {
			return fmt.Errorf(
				"skill %s source changed during update (%s/%s -> %s/%s), aborting",
				sk.Directory, sk.RepoOwner, sk.RepoName, current.RepoOwner, current.RepoName)
		}
		if err := RemovePath(ssotPath); err != nil {
			return err
		}
		return copyDirRecursive(backupPath, ssotPath)
	}
	resyncAgents := func() error {
		s.importMu.Lock()
		defer s.importMu.Unlock()
		// 与树链路重建同理：用实时 bindings 而非入口快照，
		// 避免把下载期间被解绑的 agent 目录重建出来。
		s.mu.RLock()
		live := boundAgentsFromMap(s.bindings, skillID)
		s.mu.RUnlock()
		var errs []string
		for _, agID := range live {
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

	// 树更新阶段用户可能已卸载该技能：此时不得重装（恢复备份 + tarball
	// 安装会重建目录并重新纳管，把用户的卸载操作静默逆转）。
	// 这里是锁外快速路径；真正的竞态防护在下面两个 importMu 临界区内
	//（RemovePath 前、tarball 安装后各复查一次）。
	if !s.skillStillManaged(skillID) {
		return Skill{}, fmt.Errorf("skill %s was uninstalled during update, aborting", skillID)
	}

	// fallback：先恢复备份（jsDelivr 阶段可能已部分写入），再走 tarball 全量
	if restoreErr := restoreFromBackup(); restoreErr != nil {
		return Skill{}, fmt.Errorf("jsdelivr update: %v; restore backup: %w", err, restoreErr)
	}
	// RemovePath 与 Uninstall 的目录删除必须互斥（都改 SSOT 目录树）。
	// 持 importMu 期间复查：等待锁时用户可能已完成卸载或换源重装同名技能，
	// 此时目录已被 Uninstall 删除（或已被新来源的 ImportWithDirName 重建），
	// 放弃删除直接中止，避免重建已删除目录或覆盖新来源的同名技能。
	s.importMu.Lock()
	if !s.skillStillManaged(skillID) {
		s.importMu.Unlock()
		return Skill{}, fmt.Errorf("skill %s was uninstalled during update, aborting", skillID)
	}
	s.mu.RLock()
	current := s.skills[skillID]
	s.mu.RUnlock()
	if current.RepoOwner != sk.RepoOwner || current.RepoName != sk.RepoName {
		s.importMu.Unlock()
		return Skill{}, fmt.Errorf(
			"skill %s source changed during update (%s/%s -> %s/%s), aborting",
			sk.Directory, sk.RepoOwner, sk.RepoName, current.RepoOwner, current.RepoName)
	}
	if err := RemovePath(ssotPath); err != nil {
		s.importMu.Unlock()
		return Skill{}, fmt.Errorf("remove old skill dir: %w", err)
	}
	s.importMu.Unlock()

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
	// 分支兜底：仓库默认分支可能是 main 或 master（或其他名字），
	// 入口分支（backfill 回填/旧数据/枚举失败遗留）固定为其中一端时，
	// 另一端 404 后必须重试另一分支，否则 master-only 仓库在入口
	// branch=master（树链路失败后落到 tarball）时永久更新失败。
	// 与 InstallMarketSkill 的 [branch, main, master] 枚举保持同一语义。
	if err != nil && (branch == "main" || branch == "master") {
		alt := "master"
		if branch == "master" {
			alt = "main"
		}
		altURL := fmt.Sprintf("https://codeload.github.com/%s/%s/tar.gz/refs/heads/%s",
			sk.RepoOwner, sk.RepoName, alt)
		input.TarballURL = altURL
		input.RepoBranch = alt
		updated, err = s.InstallFromTarball(ctx, input, boundAgents, reg)
		if err == nil {
			branch = alt
		}
	}
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

	// tarball 安装完成：InstallFromTarball 持锁执行期间 Uninstall 被阻塞，
	// 装完后才可能并发删除（目录 + 内存 + lock）。此处持 importMu 复查：
	// 若已卸载，清理刚被 ImportWithDirName 写回的内存记录并报错，
	// 绝不把 lock/缓存写回——否则会重建条目，把卸载操作静默逆转。
	s.importMu.Lock()
	defer s.importMu.Unlock()
	if !s.skillStillManaged(skillID) {
		if err := RemovePath(ssotPath); err != nil {
			log.Printf("cleanup SSOT after concurrent uninstall of %s: %v", sk.Directory, err)
		}
		s.mu.Lock()
		delete(s.skills, skillID)
		delete(s.bindings, skillID)
		s.mu.Unlock()
		return Skill{}, fmt.Errorf("skill %s was uninstalled during update, aborting", skillID)
	}
	// 同源校验：若已换源重装同名技能，InstallFromTarball 刚装的是旧仓库内容，
	// 与 Uninstall 被并发逆转同理——清掉旧仓库刚写入的目录与内存记录。
	s.mu.RLock()
	current = s.skills[skillID]
	s.mu.RUnlock()
	if current.RepoOwner != sk.RepoOwner || current.RepoName != sk.RepoName {
		if err := RemovePath(ssotPath); err != nil {
			log.Printf("cleanup SSOT after source change of %s: %v", sk.Directory, err)
		}
		s.mu.Lock()
		delete(s.skills, skillID)
		delete(s.bindings, skillID)
		s.mu.Unlock()
		return Skill{}, fmt.Errorf(
			"skill %s source changed during update (%s/%s -> %s/%s), aborting",
			sk.Directory, sk.RepoOwner, sk.RepoName, current.RepoOwner, current.RepoName)
	}

	// 撤销快照式重绑：InstallFromTarball 用入口快照 boundAgents 把下载期间
	// 被用户解绑的 agent（ToggleAgent disable 已删除目录与绑定记录）重新
	// recordBindingLocked 并重建了目录。此处对比实时 bindings，撤销快照中
	// 多余的部分，防止解绑操作被静默逆转。（树链路已在重建前用实时
	// bindings，无此问题；此处只针对 tarball 全量重装路径。）
	s.mu.RLock()
	liveAgents := boundAgentsFromMap(s.bindings, skillID)
	s.mu.RUnlock()
	for _, agID := range boundAgents {
		live := false
		for _, la := range liveAgents {
			if la == agID {
				live = true
				break
			}
		}
		if live {
			continue
		}
		// 撤销绑定并删除刚重建的目录副本
		if agentDir := reg.AgentSkillsDir(agID); agentDir != "" {
			if err := RemovePath(filepath.Join(agentDir, sk.Directory)); err != nil {
				log.Printf("cleanup agent %s copy after concurrent unbind of %s: %v", agID, sk.Directory, err)
			}
		}
		s.mu.Lock()
		delete(s.bindings[skillID], agID)
		s.mu.Unlock()
	}

	// 修复存量数据：fullPath 缺失/被重新定位/分支被 master 兜底修正时写回
	// lock 文件。fullPath 为空（调用方未传入）时不写回，避免用空值覆盖
	// 有效的历史记录。
	if fullPath != "" && (sk.FullPath == "" || sk.FullPath != fullPath || sk.RepoBranch != branch) {
		if err := persistSkillRepoInfo(sk, branch, fullPath, ssotPath); err != nil {
			log.Printf("warning: persist full path for %s: %v", sk.Directory, err)
		}
		// 内存同步：List 优先读内存，不更新则 UI 与下次 UpdateSkill 入口
		// 仍拿旧分支（每次更新重复 main 404 + master 重试）。
		s.mu.Lock()
		if cur, ok := s.skills[skillID]; ok && cur.RepoBranch != branch {
			cur.RepoBranch = branch
			s.skills[skillID] = cur
		}
		s.mu.Unlock()
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
		// fullPath 失效（远程目录改名/移动）：按技能目录名重新定位真实路径。
		// 否则更新会永久失败，且 persistSkillRepoInfo 继续把过时路径写回 lock 文件。
		relocated := resolveSkillDirInTree(tree.files, sk.Directory)
		if relocated == "" {
			return Skill{}, fmt.Errorf("no remote files found for %q in %s/%s@%s",
				fullPath, sk.RepoOwner, sk.RepoName, branch)
		}
		log.Printf("updateSkillViaTree: %s: stale full path %q, relocated to %q",
			sk.Directory, fullPath, relocated)
		fullPath = relocated
		remoteFiles = filterTreeByPrefix(tree.files, fullPath)
	}
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

			// 远程路径必须通过校验才能拼入本地路径，防止恶意仓库逃逸目标目录
			safeRel, err := safeRelPath(rel)
			if err != nil {
				mu.Lock()
				if firstErr == nil {
					firstErr = err
				}
				mu.Unlock()
				cancelDownload()
				return
			}
			rel = safeRel

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
		// 已是最新：若上次更新在"重建 agent 目录"阶段失败（merge 已成功、
		// 目录同步中断），此后每次 diff 都为空、agent 目录永远缺文件——
		// 刷新元数据不会重建，Resync 也只在显式触发时运行。这里持锁
		// 检查并补齐缺失的 agent 目录（幂等：已存在则跳过）。
		s.importMu.Lock()
		defer s.importMu.Unlock()
		if !s.skillStillManaged(sk.ID) {
			return Skill{}, fmt.Errorf("skill %q was uninstalled during update, aborting", sk.Directory)
		}
		s.mu.RLock()
		liveAgents := boundAgentsFromMap(s.bindings, sk.ID)
		s.mu.RUnlock()
		for _, agID := range liveAgents {
			agentDir := reg.AgentSkillsDir(agID)
			if agentDir == "" {
				continue
			}
			target := filepath.Join(agentDir, sk.Directory)
			if _, statErr := os.Stat(target); statErr == nil {
				continue
			}
			log.Printf("updateSkillViaTree: %s: agent dir %s missing after previous update, rebuilding", sk.Directory, agID)
			if err := RemovePath(target); err != nil {
				return Skill{}, fmt.Errorf("remove stale agent copy for %s: %w", agID, err)
			}
			if err := SyncToAgentDir(ssotPath, target, syncMethod); err != nil {
				return Skill{}, fmt.Errorf("sync to agent %s: %w", agID, err)
			}
		}
		return s.refreshSkillAfterFileUpdate(sk.ID, ssotPath)
	}
	sort.Strings(changed)
	log.Printf("updateSkillViaTree: %s: %d file(s) changed: %v", sk.Directory, len(changed), changed)

	// 合并进 SSOT（覆盖变化文件，保留本地额外文件）并重建 agent 目录。
	// 写 SSOT 阶段持 importMu 与 Import/Uninstall 的临界区互斥。
	s.importMu.Lock()
	defer s.importMu.Unlock()

	// 下载期间用户可能已卸载该技能（Uninstall 持 importMu 删除 SSOT 与
	// 内存记录）。合并前重新校验：技能已不存在时放弃更新，避免重建
	// 已删除目录、把卸载操作静默逆转成重装。
	if !s.skillStillManaged(sk.ID) {
		return Skill{}, fmt.Errorf("skill %q was uninstalled during update, aborting", sk.Directory)
	}
	// 来源身份校验：skillID 是 "skill:"+目录名，同名技能 ID 相同。用户可能在
	// 下载期间卸载本技能并从另一个仓库重装同名技能——此时内存记录已存在
	//（新来源），上面的"仍纳管"检查会通过。若来源不一致，用旧仓库文件
	// mergeDirOverwrite 会静默篡改新技能内容，persistSkillRepoInfo 还会把
	// 旧仓库来源写回 lock 文件，后续 CheckUpdates 拿错仓库持续"更新"。
	s.mu.RLock()
	current := s.skills[sk.ID]
	s.mu.RUnlock()
	if current.RepoOwner != sk.RepoOwner || current.RepoName != sk.RepoName {
		return Skill{}, fmt.Errorf(
			"skill %q source changed during update (%s/%s -> %s/%s), aborting",
			sk.Directory, sk.RepoOwner, sk.RepoName, current.RepoOwner, current.RepoName)
	}

	if err := mergeDirOverwrite(tmpDir, ssotPath); err != nil {
		return Skill{}, fmt.Errorf("apply updated files: %w", err)
	}

	// 重建 agent 目录副本/链接。
	// 不使用入口快照 boundAgents：下载耗时数分钟，期间用户可能已解绑个别
	// agent（ToggleAgent disable 已删除其目录与绑定记录），按快照重建会把
	// 解绑操作静默逆转。持 importMu 时读实时 bindings（ToggleAgent 同持
	// importMu，锁内读取与删除互斥）。
	s.mu.RLock()
	liveAgents := boundAgentsFromMap(s.bindings, sk.ID)
	s.mu.RUnlock()
	for _, agID := range liveAgents {
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

	// 修复存量数据：fullPath 缺失/被重新定位/分支被 master 兜底修正时写回
	// lock 文件。master-only 仓库若不写回修正后的 Branch，每次更新都先
	// 失败 main（404）再重试 master，且下一轮入口 resolveDefaultBranch
	// 仍从旧值得到 main，重复两轮请求。
	if sk.FullPath == "" || sk.FullPath != fullPath || sk.RepoBranch != branch {
		if err := persistSkillRepoInfo(sk, branch, fullPath, ssotPath); err != nil {
			log.Printf("warning: persist full path for %s: %v", sk.Directory, err)
		}
		// 内存同步：List 优先读内存，不更新则 UI 与下次 UpdateSkill 入口
		// 仍拿旧分支/旧 fullPath，与 lock 文件不一致；FullPath 不同步时
		// resolveSkillFullPath 继续返回失效旧值，CheckUpdates 每轮重定位。
		s.mu.Lock()
		if cur, ok := s.skills[sk.ID]; ok && (cur.RepoBranch != branch || cur.FullPath != fullPath) {
			cur.RepoBranch = branch
			cur.FullPath = fullPath
			s.skills[sk.ID] = cur
		}
		s.mu.Unlock()
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

// safeRelPath 校验远程树返回的相对路径，确保其可安全拼入本地文件系统路径。
// 远程树（GitHub Trees API / jsDelivr）虽然 git 层面禁止 ".." 组件，但允许
// 文件名含反斜杠（Linux/macOS 上是合法字符）；在 Windows 上反斜杠会被视为
// 路径分隔符，未经校验的 rel 可借 filepath.Join 的 Clean 逃出目标目录造成
// 任意文件写入。返回清洗后的正斜杠相对路径。
func safeRelPath(rel string) (string, error) {
	if rel == "" {
		return "", fmt.Errorf("empty relative path")
	}
	// 拒绝 Windows 分隔符、盘符及绝对路径前缀（跨平台统一视为危险形态）
	if strings.ContainsAny(rel, "\\:") {
		return "", fmt.Errorf("unsafe relative path: %q", rel)
	}
	if strings.HasPrefix(rel, "/") {
		return "", fmt.Errorf("absolute path rejected: %q", rel)
	}
	clean := filepath.ToSlash(filepath.Clean(rel))
	if clean == "." {
		return "", fmt.Errorf("unsafe relative path: %q", rel)
	}
	for _, seg := range strings.Split(clean, "/") {
		if seg == "" || seg == "." || seg == ".." {
			return "", fmt.Errorf("unsafe path segment in %q", rel)
		}
	}
	return clean, nil
}

// writeTmpFile 将下载内容写入临时目录中对应的相对路径。
func writeTmpFile(tmpDir, rel string, data []byte) error {
	safeRel, err := safeRelPath(rel)
	if err != nil {
		return err
	}
	tmpTarget := filepath.Join(tmpDir, filepath.FromSlash(safeRel))
	if err := os.MkdirAll(filepath.Dir(tmpTarget), 0755); err != nil {
		return err
	}
	return os.WriteFile(tmpTarget, data, 0644)
}

// refreshSkillAfterFileUpdate 重新计算技能内容 hash 并刷新内存中的
// ContentHash / SkillMdHash / UpdatedAt，返回最新副本。
func (s *Store) refreshSkillAfterFileUpdate(skillID, ssotPath string) (Skill, error) {
	contentHash, complete := HashDir(ssotPath)
	if !complete {
		log.Printf("warning: skill content hash may be incomplete for %s", ssotPath)
	}
	skillMdHash, _ := HashSkillMarkdown(ssotPath)
	s.mu.Lock()
	defer s.mu.Unlock()
	sk, ok := s.skills[skillID]
	if !ok {
		return Skill{}, fmt.Errorf("skill %s not found after update", skillID)
	}
	sk.ContentHash = contentHash
	sk.SkillMdHash = skillMdHash
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

	// 去重：前端可能因状态未及时刷新而重复提交同一 skill，
	// 重复更新会并发拉取同一仓库并竞争写 SSOT。
	seen := make(map[string]struct{}, len(skillIDs))
	var ids []string
	for _, id := range skillIDs {
		if _, ok := seen[id]; ok {
			continue
		}
		seen[id] = struct{}{}
		ids = append(ids, id)
	}

	var successes []Skill
	var errs []UpdateError
	sem := make(chan struct{}, 3)
	var mu sync.Mutex
	var wg sync.WaitGroup

	for _, id := range ids {
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
