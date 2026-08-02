package main

import (
	"fmt"
	"log"
	"time"

	"agentpack/internal/config"
	"agentpack/internal/market"
)

func (a *App) SearchMarketServers(source, query, cursor string, pageSize int) (*market.SearchResultServers, error) {
	_, _, mks, _, _, _ := a.snapshot()
	if mks == nil {
		return nil, fmt.Errorf("market store not initialized")
	}
	ctx, cancel := market.ContextWithTimeout(120 * time.Second)
	defer cancel()
	return mks.Search(ctx, market.Source(source), market.SearchOptions{
		Query:    query,
		PageSize: pageSize,
		Cursor:   cursor,
	})
}

func (a *App) GetMarketServer(source, sourceID string) (market.MarketServer, error) {
	_, _, mks, _, _, _ := a.snapshot()
	if mks == nil {
		return market.MarketServer{}, fmt.Errorf("market store not initialized")
	}
	ctx, cancel := market.ContextWithTimeout(15 * time.Second)
	defer cancel()
	srv, err := mks.GetServer(ctx, market.Source(source), sourceID)
	if err != nil {
		return market.MarketServer{}, err
	}
	return *srv, nil
}

// SearchMarketSkills 搜索市场中的 Skills，合并所有来源并按下载量排序
// SearchMarketSkills 搜索市场 skills
// source 参数："" 表示搜索全部启用的来源，"github" 仅 GitHub 仓库，"skills-sh" 仅 skills.sh
// page 从 1 开始，支持分页（无限滚动）
func (a *App) SearchMarketSkills(query string, pageSize int, page int, source string) (*market.SearchResultSkills, error) {
	_, _, mks, _, _, _ := a.snapshot()
	if mks == nil {
		return nil, fmt.Errorf("market store not initialized")
	}
	// 在锁内读取配置，避免数据竞争
	var enabledSources []market.Source
	a.mu.RLock()
	if a.cfg != nil && a.cfg.Settings.MarketSources != nil {
		if ms, ok := a.cfg.Settings.MarketSources["github"]; ok && ms.Enabled {
			enabledSources = append(enabledSources, market.SourceGitHub)
		}
		if ms, ok := a.cfg.Settings.MarketSources["skills-sh"]; ok && ms.Enabled {
			enabledSources = append(enabledSources, market.SourceSkillsSh)
		}
	}
	a.mu.RUnlock()
	// 前端指定了来源时，只搜索该来源
	if source != "" {
		var filtered []market.Source
		for _, s := range enabledSources {
			if string(s) == source {
				filtered = append(filtered, s)
			}
		}
		enabledSources = filtered
	}
	log.Printf("SearchMarketSkills: query=%q pageSize=%d page=%d source=%q enabledSources=%v", query, pageSize, page, source, enabledSources)
	// Skills 搜索可能需要扫描多个 GitHub 仓库（每个仓库含多个 SKILL.md），超时设长一些
	ctx, cancel := market.ContextWithTimeout(120 * time.Second)
	defer cancel()
	result, err := mks.SearchAllSkills(ctx, market.SearchOptions{
		Query:    query,
		PageSize: pageSize,
		Page:     page,
	}, enabledSources)
	if err != nil {
		log.Printf("SearchMarketSkills: SearchAllSkills error: %v", err)
		return nil, err
	}
	log.Printf("SearchMarketSkills: result total=%d items=%d hasMore=%v nextPage=%q", result.Total, len(result.Items), result.HasMore, result.NextPage)
	return result, nil
}

// InstallMarketSkill 从远程仓库 tarball 安装 skill 到指定 agents

// GetSkillRepos 获取当前配置的 GitHub 仓库扫描列表
func (a *App) GetSkillRepos() ([]config.SkillRepo, error) {
	a.mu.RLock()
	defer a.mu.RUnlock()
	if a.cfg == nil {
		return nil, nil
	}
	// 返回副本，避免外部修改
	out := make([]config.SkillRepo, len(a.cfg.Settings.SkillRepos))
	copy(out, a.cfg.Settings.SkillRepos)
	return out, nil
}

// AddSkillRepo 添加一个 GitHub 仓库到扫描列表

// AddSkillRepo 添加一个 GitHub 仓库到扫描列表
func (a *App) AddSkillRepo(repo config.SkillRepo) error {
	if err := a.assertInit(); err != nil {
		return err
	}
	// 与 UpdateSettings / ToggleAgent 等整份 config 保存操作串行化，
	// 避免并发 Save 时互相覆盖（丢失对方的变更）。
	a.storeOpMu.Lock()
	defer a.storeOpMu.Unlock()
	if repo.Owner == "" || repo.Name == "" {
		return fmt.Errorf("repo owner and name required")
	}
	if repo.Branch == "" {
		repo.Branch = "main"
	}

	a.mu.Lock()
	defer a.mu.Unlock()
	if a.cfg == nil {
		return fmt.Errorf("config not loaded")
	}
	// 检查重复
	for _, r := range a.cfg.Settings.SkillRepos {
		if r.Owner == repo.Owner && r.Name == repo.Name {
			return fmt.Errorf("repo %s/%s already exists", repo.Owner, repo.Name)
		}
	}
	a.cfg.Settings.SkillRepos = append(a.cfg.Settings.SkillRepos, repo)
	// 保存配置
	if err := config.Save(a.cfg); err != nil {
		return fmt.Errorf("save config: %w", err)
	}
	// 清理 skills 市场缓存，确保下次搜索能扫描到新添加的仓库
	if a.marketStore != nil {
		n, err := a.marketStore.ClearAllCache()
		log.Printf("AddSkillRepo: cleared %d cache files, err=%v", n, err)
	}
	return nil
}

// RemoveSkillRepo 从扫描列表移除一个 GitHub 仓库

// RemoveSkillRepo 从扫描列表移除一个 GitHub 仓库
func (a *App) RemoveSkillRepo(repo config.SkillRepo) error {
	if err := a.assertInit(); err != nil {
		return err
	}
	a.storeOpMu.Lock()
	defer a.storeOpMu.Unlock()

	a.mu.Lock()
	defer a.mu.Unlock()
	if a.cfg == nil {
		return fmt.Errorf("config not loaded")
	}
	// 查找并删除
	found := false
	updated := a.cfg.Settings.SkillRepos[:0]
	for _, r := range a.cfg.Settings.SkillRepos {
		if r.Owner == repo.Owner && r.Name == repo.Name {
			found = true
			continue
		}
		updated = append(updated, r)
	}
	if !found {
		return fmt.Errorf("repo %s/%s not found", repo.Owner, repo.Name)
	}
	a.cfg.Settings.SkillRepos = updated
	// 保存配置
	if err := config.Save(a.cfg); err != nil {
		return fmt.Errorf("save config: %w", err)
	}
	// 清理 skills 市场缓存，确保下次搜索不再包含已移除的仓库
	if a.marketStore != nil {
		n, err := a.marketStore.ClearAllCache()
		log.Printf("RemoveSkillRepo: cleared %d cache files, err=%v", n, err)
	}
	return nil
}

// UpdateSkillRepo 修改一个已配置的 GitHub 仓库扫描条目
// original 用于定位原条目(按 Owner+Name 匹配),updated 为新值(整体替换)

// UpdateSkillRepo 修改一个已配置的 GitHub 仓库扫描条目
// original 用于定位原条目(按 Owner+Name 匹配),updated 为新值(整体替换)
func (a *App) UpdateSkillRepo(original, updated config.SkillRepo) error {
	if err := a.assertInit(); err != nil {
		return err
	}
	a.storeOpMu.Lock()
	defer a.storeOpMu.Unlock()
	if original.Owner == "" || original.Name == "" {
		return fmt.Errorf("original repo owner and name required")
	}
	if updated.Owner == "" || updated.Name == "" {
		return fmt.Errorf("updated repo owner and name required")
	}
	if updated.Branch == "" {
		updated.Branch = "main"
	}

	a.mu.Lock()
	defer a.mu.Unlock()
	if a.cfg == nil {
		return fmt.Errorf("config not loaded")
	}

	// 1. 定位原条目
	origIdx := -1
	for i, r := range a.cfg.Settings.SkillRepos {
		if r.Owner == original.Owner && r.Name == original.Name {
			origIdx = i
			break
		}
	}
	if origIdx == -1 {
		return fmt.Errorf("repo %s/%s not found", original.Owner, original.Name)
	}

	// 2. 若 owner/name 发生变化,检查与其他条目冲突
	if !(updated.Owner == original.Owner && updated.Name == original.Name) {
		for i, r := range a.cfg.Settings.SkillRepos {
			if i == origIdx {
				continue
			}
			if r.Owner == updated.Owner && r.Name == updated.Name {
				return fmt.Errorf("repo %s/%s already exists", updated.Owner, updated.Name)
			}
		}
	}

	// 3. 原地替换
	a.cfg.Settings.SkillRepos[origIdx] = updated

	// 4. 保存配置
	if err := config.Save(a.cfg); err != nil {
		return fmt.Errorf("save config: %w", err)
	}

	// 5. 清理 skills 市场缓存,确保下次搜索扫描到新仓库
	if a.marketStore != nil {
		n, err := a.marketStore.ClearAllCache()
		log.Printf("UpdateSkillRepo: cleared %d cache files, err=%v", n, err)
	}
	return nil
}

// CheckSkillUpdates 检查已安装 skills 的远程更新（手动触发）
