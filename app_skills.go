package main

import (
	"context"
	"fmt"
	"log"
	"path/filepath"
	"sync"
	"time"

	"agentpack/internal/agents"
	"agentpack/internal/market"
	"agentpack/internal/skills"
)

// InstallMarketSkill 从远程仓库 tarball 安装 skill 到指定 agents
func (a *App) InstallMarketSkill(skill market.MarketSkill, agentIDs []string) (skills.Skill, error) {
	if err := a.assertInit(); err != nil {
		return skills.Skill{}, err
	}
	if err := a.beginInFlight(); err != nil {
		return skills.Skill{}, err
	}
	defer a.endInFlight()
	a.storeOpMu.Lock()
	defer a.storeOpMu.Unlock()

	if skill.Directory == "" {
		return skills.Skill{}, fmt.Errorf("skill directory required")
	}
	if skill.RepoOwner == "" || skill.RepoName == "" {
		return skills.Skill{}, fmt.Errorf("skill repo owner/name required")
	}
	branch := skill.RepoBranch
	if branch == "" {
		branch = "main"
	}

	// 构造 tarball URL
	tarballURL := fmt.Sprintf("https://codeload.github.com/%s/%s/tar.gz/refs/heads/%s",
		skill.RepoOwner, skill.RepoName, branch)

	reg, _, _, ss, _, _ := a.snapshot()
	if ss == nil || reg == nil {
		return skills.Skill{}, fmt.Errorf("skills store or registry not initialized")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	input := skills.TarballInstallInput{
		TarballURL: tarballURL,
		Directory:  skill.Directory,
		FullPath:   skill.FullPath, // 传递完整相对路径（如 "skills/pdf"），安装时精准定位
		RepoOwner:  skill.RepoOwner,
		RepoName:   skill.RepoName,
		RepoBranch: branch,
	}
	installed, err := ss.InstallFromTarball(ctx, input, agentIDs, reg)
	if err != nil {
		return skills.Skill{}, err
	}

	// 写入 ~/.agents/.skill-lock.json（兼容 CC Switch 等工具）
	lockEntry := skills.AgentsLockEntry{
		Directory:  skill.Directory,
		Source:     skill.RepoOwner + "/" + skill.RepoName,
		SourceType: "github",
		SourceURL:  "https://github.com/" + skill.RepoOwner + "/" + skill.RepoName,
		SkillPath:  filepath.Join(ss.SSOTDir(), skill.Directory),
		Branch:     branch,
		FullPath:   skill.FullPath,
	}
	if err := skills.WriteAgentsLock(lockEntry); err != nil {
		// 锁文件写入失败不阻断安装，仅记录日志
		log.Printf("warning: write agents lock for %s: %v", skill.Directory, err)
	}

	a.mu.Lock()
	defer a.mu.Unlock()
	a.emitAgentsChangedLocked()
	a.emitLocked("skills:changed", ss.List())
	// 安装成功后异步缓存 Commit SHA 作为更新检测基线
	go func() {
		_ = skills.CacheSkillCommitSHA(installed.ID, input.RepoOwner, input.RepoName,
			input.RepoBranch)
	}()
	return installed, nil
}

// GetSkillRepos 获取当前配置的 GitHub 仓库扫描列表

// CheckSkillUpdates 检查已安装 skills 的远程更新（手动触发）
func (a *App) CheckSkillUpdates() ([]skills.UpdateStatus, error) {
	if err := a.assertInit(); err != nil {
		return nil, err
	}
	if err := a.beginInFlight(); err != nil {
		return nil, err
	}
	defer a.endInFlight()

	_, _, _, ss, _, _ := a.snapshot()
	if ss == nil {
		return nil, fmt.Errorf("skills store not initialized")
	}
	return ss.CheckUpdates(a.registry), nil
}

// UpdateSkill updates a single skill to the latest remote version

// UpdateSkill updates a single skill to the latest remote version
func (a *App) UpdateSkill(skillID string) (skills.Skill, error) {
	if err := a.assertInit(); err != nil {
		return skills.Skill{}, err
	}
	if err := a.beginInFlight(); err != nil {
		return skills.Skill{}, err
	}
	defer a.endInFlight()
	a.storeOpMu.Lock()
	defer a.storeOpMu.Unlock()

	_, _, _, ss, _, _ := a.snapshot()
	if ss == nil {
		return skills.Skill{}, fmt.Errorf("skills store not initialized")
	}
	updated, err := ss.UpdateSkill(skillID, a.registry)
	if err != nil {
		return skills.Skill{}, err
	}

	a.mu.Lock()
	defer a.mu.Unlock()
	a.emitAgentsChangedLocked()
	a.emitLocked("skills:changed", ss.List())
	return updated, nil
}

// UpdateSkills batch-updates multiple skills

// UpdateSkills batch-updates multiple skills
func (a *App) UpdateSkills(skillIDs []string) (skills.UpdateSkillsResult, error) {
	if err := a.assertInit(); err != nil {
		return skills.UpdateSkillsResult{}, err
	}
	if err := a.beginInFlight(); err != nil {
		return skills.UpdateSkillsResult{}, err
	}
	defer a.endInFlight()
	a.storeOpMu.Lock()
	defer a.storeOpMu.Unlock()

	_, _, _, ss, _, _ := a.snapshot()
	if ss == nil {
		return skills.UpdateSkillsResult{}, fmt.Errorf("skills store not initialized")
	}
	result := ss.UpdateSkills(skillIDs, a.registry)

	a.mu.Lock()
	defer a.mu.Unlock()
	a.emitAgentsChangedLocked()
	a.emitLocked("skills:changed", ss.List())
	return result, nil
}

func (a *App) ListSkills() ([]skills.Skill, error) {
	_, _, _, ss, _, _ := a.snapshot()
	if ss == nil {
		return []skills.Skill{}, nil
	}
	return ss.List(), nil
}

func (a *App) ListSkillCapableAgents() ([]*agents.Agent, error) {
	reg, _, _, _, _, _ := a.snapshot()
	if reg == nil {
		return []*agents.Agent{}, nil
	}
	ids := reg.SkillCapableAgentIDs()
	out := make([]*agents.Agent, 0, len(ids))
	for _, id := range ids {
		if ag := reg.Get(id); ag != nil {
			out = append(out, ag)
		}
	}
	return out, nil
}

// AutoAdoptSkills 扫描 agent skill 目录，将未管理 skill 自动纳管到 SSOT。

// AutoAdoptSkills 扫描 agent skill 目录，将未管理 skill 自动纳管到 SSOT。
func (a *App) AutoAdoptSkills() (skills.AdoptionResult, error) {
	if err := a.assertInit(); err != nil {
		return skills.AdoptionResult{}, err
	}
	a.storeOpMu.Lock()
	defer a.storeOpMu.Unlock()
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.closed {
		return skills.AdoptionResult{}, fmt.Errorf("app is shutting down")
	}
	if a.skillsStore == nil {
		return skills.AdoptionResult{}, fmt.Errorf("skills store not initialized")
	}
	result := a.skillsStore.AutoAdopt(a.registry)
	if len(result.Adopted) > 0 || len(result.Conflicts) > 0 {
		a.emitLocked("skills:changed", a.skillsStore.List())
	}
	return result, nil
}

func (a *App) ImportSkillDirectory(path string, agentIDs []string) (skills.Skill, error) {
	if err := a.assertInit(); err != nil {
		return skills.Skill{}, err
	}
	a.storeOpMu.Lock()
	defer a.storeOpMu.Unlock()
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.closed {
		return skills.Skill{}, fmt.Errorf("app is shutting down")
	}
	if a.skillsStore == nil {
		return skills.Skill{}, fmt.Errorf("skills store not initialized")
	}
	sk, err := a.skillsStore.Import(path, agentIDs, a.registry, "", "")
	if err != nil {
		return skills.Skill{}, err
	}
	a.emitLocked("skills:changed", a.skillsStore.List())
	return sk, nil
}

// InstallSkillFromZip 从 zip 文件安装 skill。
// 解压后自动识别含 SKILL.md 的根目录并纳管到 SSOT，同步到指定 agent 目录。

// InstallSkillFromZip 从 zip 文件安装 skill。
// 解压后自动识别含 SKILL.md 的根目录并纳管到 SSOT，同步到指定 agent 目录。
func (a *App) InstallSkillFromZip(zipPath string, agentIDs []string) (skills.Skill, error) {
	if err := a.assertInit(); err != nil {
		return skills.Skill{}, err
	}
	a.storeOpMu.Lock()
	defer a.storeOpMu.Unlock()
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.closed {
		return skills.Skill{}, fmt.Errorf("app is shutting down")
	}
	if a.skillsStore == nil {
		return skills.Skill{}, fmt.Errorf("skills store not initialized")
	}
	sk, err := a.skillsStore.InstallFromZip(zipPath, agentIDs, a.registry)
	if err != nil {
		return skills.Skill{}, err
	}
	a.emitLocked("skills:changed", a.skillsStore.List())
	return sk, nil
}

func (a *App) ToggleSkillAgent(id, agentID string, enabled bool) error {
	if err := a.assertInit(); err != nil {
		return err
	}
	a.storeOpMu.Lock()
	defer a.storeOpMu.Unlock()
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.closed {
		return fmt.Errorf("app is shutting down")
	}
	if a.skillsStore == nil {
		return fmt.Errorf("skills store not initialized")
	}
	if err := a.skillsStore.ToggleAgent(id, agentID, enabled, a.registry); err != nil {
		return err
	}
	a.emitLocked("skills:changed", a.skillsStore.List())
	return nil
}

func (a *App) UninstallSkill(id string) (skills.UninstallResult, error) {
	if err := a.assertInit(); err != nil {
		return skills.UninstallResult{}, err
	}
	a.storeOpMu.Lock()
	defer a.storeOpMu.Unlock()
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.closed {
		return skills.UninstallResult{}, fmt.Errorf("app is shutting down")
	}
	if a.skillsStore == nil {
		return skills.UninstallResult{}, fmt.Errorf("skills store not initialized")
	}
	result, err := a.skillsStore.Uninstall(id, a.registry)
	if err != nil {
		return skills.UninstallResult{}, err
	}
	a.emitLocked("skills:changed", a.skillsStore.List())
	return result, nil
}

func (a *App) ResyncSkills() error {
	if err := a.assertInit(); err != nil {
		return err
	}
	a.storeOpMu.Lock()
	defer a.storeOpMu.Unlock()
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.closed {
		return fmt.Errorf("app is shutting down")
	}
	if a.skillsStore == nil {
		return fmt.Errorf("skills store not initialized")
	}
	return a.skillsStore.Resync(a.registry)
}

// ScanUnmanagedSkills returns skills found in agent directories that are not
// managed by AgentPack (not present in the SSOT directory). Read-only operation.

// ScanUnmanagedSkills returns skills found in agent directories that are not
// managed by AgentPack (not present in the SSOT directory). Read-only operation.
func (a *App) ScanUnmanagedSkills() ([]skills.UnmanagedSkill, error) {
	if err := a.assertInit(); err != nil {
		return nil, err
	}
	a.mu.RLock()
	defer a.mu.RUnlock()
	if a.closed {
		return nil, fmt.Errorf("app is shutting down")
	}
	if a.skillsStore == nil {
		return nil, fmt.Errorf("skills store not initialized")
	}
	return a.skillsStore.ScanUnmanaged(a.registry), nil
}

// PauseDownload 暂停当前正在进行的下载

func (a *App) MigrateSkillStorage(target string) (skills.MigrationResult, error) {
	if err := a.assertInit(); err != nil {
		return skills.MigrationResult{}, err
	}
	a.storeOpMu.Lock()
	defer a.storeOpMu.Unlock()
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.closed {
		return skills.MigrationResult{}, fmt.Errorf("app is shutting down")
	}
	if a.skillsStore == nil {
		return skills.MigrationResult{}, fmt.Errorf("skills store not initialized")
	}
	newDir := skills.ResolveSSOTDir(skills.StorageLocation(target))
	result, err := a.skillsStore.MigrateStorage(newDir, a.registry)
	if err != nil {
		return skills.MigrationResult{}, err
	}
	a.emitLocked("skills:changed", a.skillsStore.List())
	return result, nil
}

// SkillSourceBackfillResult 是从 skills.sh 回填技能来源的结果统计。
type SkillSourceBackfillResult struct {
	Matched    []string `json:"matched"`
	Mismatched []string `json:"mismatched"`
	Unmatched  []string `json:"unmatched"`
	Failed     []string `json:"failed"`
}

// BackfillSkillSources 从 skills.sh 回填缺少仓库来源的技能，使其支持后续更新。
// 仅处理 RepoOwner/RepoName 均为空的技能，已有来源的不覆盖。
// 写入前验证仓库中确实存在同名技能且远程 SKILL.md 与本地一致，
// 防止把不同来源/版本的技能错误关联到仓库。

// BackfillSkillSources 从 skills.sh 回填缺少仓库来源的技能，使其支持后续更新。
// 仅处理 RepoOwner/RepoName 均为空的技能，已有来源的不覆盖。
// 写入前验证仓库中确实存在同名技能且远程 SKILL.md 与本地一致，
// 防止把不同来源/版本的技能错误关联到仓库。
func (a *App) BackfillSkillSources() (SkillSourceBackfillResult, error) {
	if err := a.assertInit(); err != nil {
		return SkillSourceBackfillResult{}, err
	}
	a.mu.RLock()
	closed := a.closed
	var directories []string
	if a.skillsStore != nil {
		for _, sk := range a.skillsStore.List() {
			if sk.RepoOwner == "" && sk.RepoName == "" {
				directories = append(directories, sk.Directory)
			}
		}
	}
	a.mu.RUnlock()
	if closed {
		return SkillSourceBackfillResult{}, fmt.Errorf("app is shutting down")
	}
	if len(directories) == 0 {
		return SkillSourceBackfillResult{}, nil
	}
	ssotDir := a.skillsStore.SSOTDir()

	ctx, cancel := market.ContextWithTimeout(120 * time.Second)
	defer cancel()
	matches, err := market.BackfillSkillSources(ctx, directories)
	if err != nil {
		return SkillSourceBackfillResult{}, err
	}
	verify := func(dir string, cands []market.BackfillCandidate) (market.BackfillCandidate, string, bool, bool) {
		hadNetworkErr := false
		for _, c := range cands {
			fp, ok, verr := skills.VerifySkillSource(ctx, dir, c.Owner, c.Repo, "main",
				filepath.Join(ssotDir, dir))
			if verr != nil {
				hadNetworkErr = true
				continue
			}
			if ok {
				return c, fp, true, false
			}
		}
		return market.BackfillCandidate{}, "", false, hadNetworkErr
	}
	return applyBackfillWithVerification(matches, directories, verify), nil
}

// backfillVerifier 验证候选列表：按序验证（下载量降序），返回首个内容一致的
// 匹配（含 fullPath）。ok=false 且 networkErr=true 表示候选全部因网络失败未验证；
// ok=false 且 networkErr=false 表示候选都验证过但内容不一致。
type backfillVerifier func(dir string, candidates []market.BackfillCandidate) (match market.BackfillCandidate, fullPath string, ok bool, networkErr bool)

// applyBackfillWithVerification 并发验证匹配项并把通过验证的写入 lock。
// 拆分为独立函数便于单元测试（网络查询/验证由调用方注入）。

// applyBackfillWithVerification 并发验证匹配项并把通过验证的写入 lock。
// 拆分为独立函数便于单元测试（网络查询/验证由调用方注入）。
func applyBackfillWithVerification(matches map[string][]market.BackfillCandidate, directories []string, verify backfillVerifier) SkillSourceBackfillResult {
	type verified struct {
		dir        string
		match      market.BackfillCandidate
		fullPath   string
		ok         bool
		networkErr bool
	}
	verifiedMap := make(map[string]verified, len(matches))
	var mu sync.Mutex
	sem := make(chan struct{}, 5)
	var wg sync.WaitGroup
	for dir, cands := range matches {
		dir, cands := dir, cands
		wg.Add(1)
		go func() {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			m, fullPath, ok, networkErr := verify(dir, cands)
			mu.Lock()
			verifiedMap[dir] = verified{dir: dir, match: m, fullPath: fullPath, ok: ok, networkErr: networkErr}
			mu.Unlock()
		}()
	}
	wg.Wait()

	var res SkillSourceBackfillResult
	for _, dir := range directories {
		cands, ok := matches[dir]
		if !ok || len(cands) == 0 {
			res.Unmatched = append(res.Unmatched, dir)
			continue
		}
		v := verifiedMap[dir]
		if v.networkErr && !v.ok {
			res.Failed = append(res.Failed, dir)
			continue
		}
		if !v.ok {
			res.Mismatched = append(res.Mismatched, dir)
			continue
		}
		entry := skills.AgentsLockEntry{
			Directory:  dir,
			Source:     v.match.Owner + "/" + v.match.Repo,
			SourceType: "github",
			SourceURL:  "https://github.com/" + v.match.Owner + "/" + v.match.Repo,
			Branch:     "main",
			FullPath:   v.fullPath,
		}
		if err := skills.WriteAgentsLock(entry); err != nil {
			log.Printf("backfill source for %s: %v", dir, err)
			res.Failed = append(res.Failed, dir)
			continue
		}
		res.Matched = append(res.Matched, dir)
	}
	return res
}
