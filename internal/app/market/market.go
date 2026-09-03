// Package market 提供市场安装的纯逻辑：把市场 MCP Server / Skill 落到
// 本地 store（含字段归一化、分支兜底枚举、agents lock 写入）。不持有 App
// 状态，依赖通过参数传入；锁编排与事件触发由根目录 app.go 负责。
package market

import (
	"context"
	"fmt"
	"log"
	"path/filepath"
	"time"

	"agentpack/internal/agents"
	"agentpack/internal/market"
	"agentpack/internal/mcp"
	"agentpack/internal/skills"
)

// InstallServer 把市场 server 归一化后加入 MCP store。
func InstallServer(mcps *mcp.Store, reg *agents.Registry, server market.MarketServer, agentIDs []string) (mcp.Server, error) {
	if server.Name == "" {
		return mcp.Server{}, fmt.Errorf("server name required")
	}
	if server.Command == "" && server.URL == "" {
		return mcp.Server{}, fmt.Errorf("server must have command or url")
	}
	env := server.Env
	if env == nil {
		env = map[string]string{}
	}
	transport := server.Transport
	if transport == "" {
		transport = "stdio"
	}
	return mcps.Add(mcp.Server{
		Name:        server.Name,
		Description: server.Description,
		Command:     server.Command,
		Args:        server.Args,
		Env:         env,
		Transport:   mcp.Transport(transport),
		URL:         server.URL,
		Source:      string(server.Source),
		SourceID:    server.SourceID,
	}, agentIDs, reg)
}

// InstallSkill 从远程仓库 tarball 安装 skill 到指定 agents。
// 分支兜底枚举：{存储分支} ∪ {main, master} 去重，依次尝试直到成功，
// 成功后以实际命中的分支写入 ~/.agents/.skill-lock.json。
func InstallSkill(ss *skills.Store, reg *agents.Registry, skill market.MarketSkill, agentIDs []string) (skills.Skill, error) {
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

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	// 分支兜底枚举：{存储分支} ∪ {main, master} 去重。
	// 场景：扫描侧通过 jsDelivr @master 别名（=仓库默认分支）解析出的分支
	// 存为 "master"，而真实默认分支可能是 main（别名只用于扫描，不保证
	// 与仓库实际分支名一致）；反之 master-only 仓库在默认 main 404 时需重试。
	attempts := []string{branch, "main", "master"}
	seen := map[string]bool{}
	var installed skills.Skill
	var lastErr error
	for _, cand := range attempts {
		if cand == "" || seen[cand] {
			continue
		}
		seen[cand] = true
		tarballURL := fmt.Sprintf("https://codeload.github.com/%s/%s/tar.gz/refs/heads/%s",
			skill.RepoOwner, skill.RepoName, cand)
		input := skills.TarballInstallInput{
			TarballURL: tarballURL,
			Directory:  skill.Directory,
			FullPath:   skill.FullPath, // 传递完整相对路径（如 "skills/pdf"），安装时精准定位
			RepoOwner:  skill.RepoOwner,
			RepoName:   skill.RepoName,
			RepoBranch: cand,
		}
		// 5 分钟总预算共享给多个候选分支时，第一个失败的分支可能耗尽整个
		// 预算，后续候选直接因 ctx 超时失败。为每个候选派生独立子预算
		// （90 秒/候选，3 候选共 270 秒，不超过 5 分钟总预算）。
		childCtx, cancel := context.WithTimeout(ctx, 90*time.Second)
		installed, lastErr = ss.InstallFromTarball(childCtx, input, agentIDs, reg)
		cancel()
		if lastErr == nil {
			branch = cand
			skill.RepoBranch = cand
			break
		}
	}
	if lastErr != nil {
		return skills.Skill{}, lastErr
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
	return installed, nil
}
