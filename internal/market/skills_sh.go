package market

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/url"
	"strings"
)

// skillsShAPIBase 是 skills.sh API 的 base URL（可覆盖，便于测试）
var skillsShAPIBase = "https://skills.sh"

// SkillsShFetcher 从 skills.sh 公共 API 搜索 skill 列表
// 对齐 CC Switch 实现：直接调用 https://skills.sh/api/search
type SkillsShFetcher struct {
	hc *HTTPClient
}

// NewSkillsShFetcher 创建 skills.sh API fetcher
func NewSkillsShFetcher() *SkillsShFetcher {
	return &SkillsShFetcher{hc: NewHTTPClient()}
}

func (f *SkillsShFetcher) Source() Source { return SourceSkillsSh }

// skillsShAPIResponse 是 skills.sh /api/search 的原始响应
// 注意：API 命名不一致（searchType 是 camelCase，duration_ms 是 snake_case）
type skillsShAPIResponse struct {
	Query      string             `json:"query"`
	SearchType string             `json:"searchType"`
	Skills     []skillsShAPISkill `json:"skills"`
	Count      int                `json:"count"`
	DurationMS int64              `json:"duration_ms"`
}

// skillsShAPISkill 是 skills.sh API 单个 skill 条目
type skillsShAPISkill struct {
	ID       string `json:"id"`       // "anthropics/skills/pdf"
	SkillID  string `json:"skillId"` // "pdf"
	Name     string `json:"name"`     // "pdf"
	Installs int64  `json:"installs"`
	Source   string `json:"source"`   // "anthropics/skills"
}

// Search 调用 skills.sh /api/search 搜索 skills
// 空查询或短查询（< 2 字符）直接返回空（与 CC Switch 行为一致，避免 API 400 错误）
func (f *SkillsShFetcher) Search(ctx context.Context, opts SearchOptions) (*SearchResultSkills, error) {
	q := strings.TrimSpace(opts.Query)
	// skills.sh API 要求 query >= 2 字符，空查询和短查询直接返回空
	if len(q) < 2 {
		return &SearchResultSkills{Items: []MarketSkill{}, Total: 0, Page: 1}, nil
	}

	limit := opts.PageSize
	if limit <= 0 {
		limit = 30
	}
	offset := 0
	if opts.Page > 1 {
		offset = (opts.Page - 1) * limit
	}

	// 构建 API URL: /api/search?q={query}&limit={limit}&offset={offset}
	u, err := url.Parse(skillsShAPIBase)
	if err != nil {
		return nil, fmt.Errorf("parse skills.sh base: %w", err)
	}
	u.Path = "/api/search"
	qs := u.Query()
	qs.Set("q", q)
	qs.Set("limit", fmt.Sprintf("%d", limit))
	qs.Set("offset", fmt.Sprintf("%d", offset))
	u.RawQuery = qs.Encode()

	resp, err := f.hc.Get(ctx, u.String())
	if err != nil {
		return nil, fmt.Errorf("skills.sh search: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		drainBody(resp.Body)
		return nil, fmt.Errorf("skills.sh search: status %d", resp.StatusCode)
	}

	var apiResp skillsShAPIResponse
	if err := json.NewDecoder(io.LimitReader(resp.Body, 5*1024*1024)).Decode(&apiResp); err != nil {
		return nil, fmt.Errorf("decode skills.sh response: %w", err)
	}

	// 解析每个 skill 条目，过滤非 GitHub 源（source 含 "." 的不是 GitHub owner/repo）
	skills := make([]MarketSkill, 0, len(apiResp.Skills))
	for _, s := range apiResp.Skills {
		// source 字段格式 "owner/repo"
		parts := strings.SplitN(s.Source, "/", 2)
		if len(parts) != 2 {
			continue
		}
		owner, repo := parts[0], parts[1]
		// 过滤非 GitHub 来源（如 "skills.volces.com/..."、含 "." 的域名）
		if strings.Contains(owner, ".") || strings.Contains(repo, ".") {
			continue
		}
		// 校验标识符合法性（防止 URL 注入）
		if !isSafeGitHubIdent(owner) || !isSafeGitHubIdent(repo) {
			continue
		}

		directory := s.SkillID
		if directory == "" {
			directory = s.Name
		}

		// 从 ID（格式 "owner/repo/path"）中提取 fullPath
		// 如 ID="addyosmani/agent-skills/skills/code-review" → fullPath="skills/code-review"
		sourcePrefix := s.Source + "/"
		fullPath := ""
		if strings.HasPrefix(s.ID, sourcePrefix) {
			fullPath = strings.TrimPrefix(s.ID, sourcePrefix)
		}

		skills = append(skills, MarketSkill{
			ID:          fmt.Sprintf("skills-sh:%s", s.ID),
			Name:        s.Name,
			Description: "", // API 不返回 description
			Directory:   directory,
			FullPath:    fullPath,
			Source:      SourceSkillsSh,
			SourceID:    s.ID,
			Installs:    s.Installs,
			RepoOwner:   owner,
			RepoName:    repo,
			RepoBranch:  "", // 留空，安装时由 GitHub fetcher 自动获取默认分支
			ReadmeURL:   fmt.Sprintf("https://github.com/%s/%s", owner, repo),
			UpdatedAt:   "",
		})
	}

	total := apiResp.Count
	if total < len(skills) {
		total = len(skills)
	}

	return &SearchResultSkills{
		Items:   skills,
		Total:   total,
		Page:    opts.Page,
		HasMore: total > offset+limit,
	}, nil
}
