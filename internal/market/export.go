// Package market 对外导出市场搜索与管理。实现按功能拆分为子包：
//
//	types/      Source / MarketServer / MarketSkill / SearchOptions / 搜索接口（共享数据与契约）
//	util/       通用工具（DrainBody / NormalizePaging / ReadErrorSnippet / IsStatusErr）
//	httpclient/ HTTP 客户端（代理兜底 / 重试）
//	registry/   MCP registry 官方源 fetcher
//	github/     GitHub 仓库 skill fetcher
//	skillsh/    skills.sh API skill fetcher
//	store/      Store 主逻辑（多源聚合搜索 / 缓存）
//	backfill/   技能来源自动回填
//
// 本包通过类型别名 re-export 子包类型，使外部引用（app.go / internal/app 等）
// 继续使用 market.Store / market.MarketServer / market.BackfillCandidate 等，无需改动。
package market

import (
	"context"
	"time"

	"agentpack/internal/market/backfill"
	"agentpack/internal/market/github"
	"agentpack/internal/market/registry"
	"agentpack/internal/market/skillsh"
	"agentpack/internal/market/store"
	"agentpack/internal/market/types"
)

// 类型别名：保持对外 API 稳定。
type (
	Source               = types.Source
	MarketServer         = types.MarketServer
	SearchOptions        = types.SearchOptions
	SearchResultServers  = types.SearchResultServers
	InstallServerOptions = types.InstallServerOptions
	MarketSkill          = types.MarketSkill
	SearchResultSkills   = types.SearchResultSkills
	SourceStatus         = types.SourceStatus
	SkillFetcher         = types.SkillFetcher
	InstallSkillOptions  = types.InstallSkillOptions
	BackfillCandidate    = types.BackfillCandidate
	ServerFetcher        = types.ServerFetcher

	Store              = store.Store
	RepoRef            = github.RepoRef
	GitHubSkillFetcher = github.GitHubSkillFetcher
	SkillsShFetcher    = skillsh.SkillsShFetcher
	RegistryFetcher    = registry.RegistryFetcher
)

// 常量别名：Source 值。
const (
	SourceOfficial = types.SourceOfficial
	SourceGitHub   = types.SourceGitHub
	SourceLocal    = types.SourceLocal
	SourceSkillsSh = types.SourceSkillsSh
)

// NewStore 创建市场 Store。
func NewStore(cacheDir string) *Store { return store.NewStore(cacheDir) }

// ContextWithTimeout 创建带超时的市场搜索上下文。
func ContextWithTimeout(d time.Duration) (context.Context, context.CancelFunc) {
	return store.ContextWithTimeout(d)
}

// NewRegistryFetcher 创建 MCP registry 官方源 fetcher。
func NewRegistryFetcher() *RegistryFetcher { return registry.NewRegistryFetcher() }

// NewGitHubSkillFetcher 创建 GitHub 仓库 skill fetcher。
func NewGitHubSkillFetcher(getter func() []RepoRef) *GitHubSkillFetcher {
	return github.NewGitHubSkillFetcher(getter)
}

// NewSkillsShFetcher 创建 skills.sh API skill fetcher。
func NewSkillsShFetcher() *SkillsShFetcher { return skillsh.NewSkillsShFetcher() }

// BackfillSkillSources 从 skills.sh 候选回填技能来源。
func BackfillSkillSources(ctx context.Context, directories []string) (map[string][]BackfillCandidate, error) {
	return backfill.BackfillSkillSources(ctx, directories)
}

// AcceptBackfillMatch 判断回填候选与本地技能内容是否一致。
func AcceptBackfillMatch(dir, fullPath string) bool {
	return backfill.AcceptBackfillMatch(dir, fullPath)
}
