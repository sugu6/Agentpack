package market

import "context"

type Source string

const (
	SourceOfficial Source = "official"
	SourceGitHub   Source = "github"
	SourceLocal    Source = "local"
	SourceSkillsSh Source = "skills-sh"
	SourceSmithery Source = "smithery"
)

type MarketServer struct {
	ID          string            `json:"id"`
	Name        string            `json:"name"`
	Title       string            `json:"title,omitempty"`
	Description string            `json:"description"`
	Homepage    string            `json:"homepage,omitempty"`
	Docs        string            `json:"docs,omitempty"`
	Tags        []string          `json:"tags"`
	Transport   string            `json:"transport,omitempty"`
	Command     string            `json:"command,omitempty"`
	Args        []string          `json:"args,omitempty"`
	Env         map[string]string `json:"env,omitempty"`
	URL         string            `json:"url,omitempty"`
	Source      Source            `json:"source"`
	SourceID    string            `json:"sourceId"`
	Installs    int               `json:"installs,omitempty"`
	Stars       int               `json:"stars,omitempty"`
	UpdatedAt   string            `json:"updatedAt"`
	// Smithery 特有字段（仅 SourceSmithery 时填充，用于前端筛选）
	BySmithery bool `json:"bySmithery,omitempty"` // 是否由 Smithery 官方管理
	IsDeployed bool `json:"isDeployed,omitempty"`
	IsVerified bool `json:"isVerified,omitempty"`
	IsRemote   bool `json:"isRemote,omitempty"` // 是否为远程托管（http 型）
	// Official 特有字段（仅 SourceOfficial 时填充，用于前端筛选）
	Registry string `json:"registry,omitempty"` // 包注册表：npm / pypi / docker / oci
}

type SearchOptions struct {
	Query    string `json:"query"`
	Source   Source `json:"source"`
	Page     int    `json:"page"`
	PageSize int    `json:"pageSize"`
	Cursor   string `json:"cursor"`
}

type SearchResultServers struct {
	Items    []MarketServer `json:"items"`
	Total    int            `json:"total"`
	Page     int            `json:"page"`
	HasMore  bool           `json:"hasMore"`
	NextPage string         `json:"nextPage,omitempty"`
}

type InstallServerOptions struct {
	Server MarketServer      `json:"server"`
	Agents []string          `json:"agents"`
	Env    map[string]string `json:"env,omitempty"`
}

// MarketSkill 是市场中可发现的 Skill（区别于 MarketServer）
type MarketSkill struct {
	ID          string `json:"id"`          // 唯一 ID（前端 key + 锁文件记录）
	Name        string `json:"name"`        // 显示名（SKILL.md frontmatter.name）
	Description string `json:"description"`
	Directory   string `json:"directory"`   // 安装目录名（= slug / 仓库名 / SKILL.md 父目录的最后一段）
	FullPath    string `json:"fullPath,omitempty"` // SKILL.md 所在目录的完整相对路径（如 "skills/pdf"，根目录为空）— 用于安装时精准定位
	Source      Source `json:"source"`      // "github" | "skills-sh"
	SourceID    string `json:"sourceId"`    // skills.sh 的 id 或 GitHub 仓库的 owner/repo
	Installs    int64  `json:"installs"`    // 下载量（skills.sh 有值，GitHub 仓库扫描为 0）
	RepoOwner   string `json:"repoOwner"`
	RepoName    string `json:"repoName"`
	RepoBranch  string `json:"repoBranch"` // 默认 "main"
	ReadmeURL   string `json:"readmeUrl,omitempty"` // GitHub 仓库 README 直链
	UpdatedAt   string `json:"updatedAt"`
}

// SearchResultSkills 是 Skill 搜索结果
type SearchResultSkills struct {
	Items    []MarketSkill `json:"items"`
	Total    int           `json:"total"`
	Page     int           `json:"page"`
	HasMore  bool          `json:"hasMore"`
	NextPage string        `json:"nextPage,omitempty"`
	// SourceStatuses 记录每个来源的搜索状态（成功/失败/跳过）
	// 供前端展示哪些来源有结果、哪些出错
	SourceStatuses []SourceStatus `json:"sourceStatuses,omitempty"`
}

// SourceStatus 记录单个来源的搜索状态
type SourceStatus struct {
	Source Source `json:"source"`
	// Status: "ok" | "error" | "skipped" | "empty"
	Status  string `json:"status"`
	Count   int    `json:"count"`
	Error   string `json:"error,omitempty"`
}

// SkillFetcher 是 Skill 来源 fetcher 的接口（与 ServerFetcher 平行）
type SkillFetcher interface {
	Source() Source
	Search(ctx context.Context, opts SearchOptions) (*SearchResultSkills, error)
}

// InstallSkillOptions 是安装 Skill 的参数
type InstallSkillOptions struct {
	Skill  MarketSkill `json:"skill"`
	Agents []string    `json:"agents"`
}
