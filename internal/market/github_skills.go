package market

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/url"
	"sort"
	"strings"
	"sync"
	"time"
)

// RepoRef 表示一个可扫描的 GitHub 仓库引用
type RepoRef struct {
	Owner  string `json:"owner"`
	Name   string `json:"name"`
	Branch string `json:"branch"`
}

// 包级可覆盖 base URL，便于测试
// 使用 jsDelivr CDN 替代 raw.githubusercontent.com，因为后者在中国网络环境下经常不可达
// jsDelivr CDN 在中国有节点，速度快且无 GitHub 限流
var (
	githubAPIBase = "https://api.github.com"
	githubRawBase = "https://cdn.jsdelivr.net/gh"
)

// GitHubSkillFetcher 扫描配置的 GitHub 仓库列表，解析含 SKILL.md 的子目录
type GitHubSkillFetcher struct {
	hc     *HTTPClient
	getter func() []RepoRef // 注入：返回当前配置的仓库列表
}

// NewGitHubSkillFetcher 创建 GitHub 仓库扫描 fetcher
// getter 返回当前配置的仓库列表（由 App 层从 config 注入）
func NewGitHubSkillFetcher(getter func() []RepoRef) *GitHubSkillFetcher {
	if getter == nil {
		getter = func() []RepoRef { return nil }
	}
	return &GitHubSkillFetcher{
		hc:     NewHTTPClient(),
		getter: getter,
	}
}

func (f *GitHubSkillFetcher) Source() Source { return SourceGitHub }

// githubTreeResponse 是 GitHub Trees API 的响应结构
type githubTreeResponse struct {
	SHA       string           `json:"sha"`
	URL       string           `json:"url"`
	Tree      []githubTreeItem `json:"tree"`
	Truncated bool             `json:"truncated"`
}

type githubTreeItem struct {
	Path string `json:"path"`
	Mode string `json:"mode"`
	Type string `json:"type"` // "blob" | "tree" | "commit"
	SHA  string `json:"sha"`
	URL  string `json:"url"`
}

// Search 扫描所有配置的 GitHub 仓库，返回含 SKILL.md 的 skill 列表
func (f *GitHubSkillFetcher) Search(ctx context.Context, opts SearchOptions) (*SearchResultSkills, error) {
	repos := f.getter()
	log.Printf("github skills: getter returned %d repos: %+v", len(repos), repos)
	if len(repos) == 0 {
		return &SearchResultSkills{Items: []MarketSkill{}, Total: 0, Page: 1}, nil
	}

	normalizePaging(&opts)

	// 并行扫描所有仓库，每个仓库使用独立的 context 超时
	// 避免一个大仓库（如 ComposioHQ/awesome-claude-skills 含上百个 skills）拖垮其他仓库
	type repoResult struct {
		repo   RepoRef
		skills []MarketSkill
		err    error
	}
	results := make([]repoResult, len(repos))
	var wg sync.WaitGroup
	wg.Add(len(repos))

	for i, repo := range repos {
		i, repo := i, repo
		go func() {
			defer wg.Done()
			// 每个仓库独立 60 秒超时，互不影响
			repoCtx, cancel := context.WithTimeout(ctx, 60*time.Second)
			defer cancel()
			skills, err := f.scanRepo(repoCtx, repo)
			results[i] = repoResult{repo: repo, skills: skills, err: err}
		}()
	}
	wg.Wait()

	// 合并结果
	var allSkills []MarketSkill
	for _, r := range results {
		if r.err != nil {
			// 单仓库失败不阻断其他
			log.Printf("github skills: repo %s/%s branch=%s failed: %v", r.repo.Owner, r.repo.Name, r.repo.Branch, r.err)
			continue
		}
		log.Printf("github skills: repo %s/%s branch=%s returned %d skills", r.repo.Owner, r.repo.Name, r.repo.Branch, len(r.skills))
		allSkills = append(allSkills, r.skills...)
	}

	// 按 query 过滤
	if q := strings.TrimSpace(opts.Query); q != "" {
		filtered := allSkills[:0]
		for _, s := range allSkills {
			if strings.Contains(strings.ToLower(s.Name), strings.ToLower(q)) ||
				strings.Contains(strings.ToLower(s.Description), strings.ToLower(q)) ||
				strings.Contains(strings.ToLower(s.Directory), strings.ToLower(q)) {
				filtered = append(filtered, s)
			}
		}
		allSkills = filtered
	}

	total := len(allSkills)
	// 不分页，直接返回全部（仓库扫描结果通常 < 100 条）
	result := &SearchResultSkills{
		Items:   allSkills,
		Total:   total,
		Page:    1,
		HasMore: false,
	}
	return result, nil
}

// scanRepo 扫描单个仓库，返回所有含 SKILL.md 的 skill
func (f *GitHubSkillFetcher) scanRepo(ctx context.Context, repo RepoRef) ([]MarketSkill, error) {
	if err := validateRepoRef(repo); err != nil {
		return nil, fmt.Errorf("invalid repo %s/%s: %w", repo.Owner, repo.Name, err)
	}

	branch := repo.Branch
	// branch 为空时，调用 GitHub API 获取默认分支
	if branch == "" {
		def, err := f.fetchDefaultBranch(ctx, repo.Owner, repo.Name)
		if err != nil {
			return nil, fmt.Errorf("fetch default branch for %s/%s: %w", repo.Owner, repo.Name, err)
		}
		branch = def
	}

	// 1. 调用 Trees API 获取递归目录树
	treeURL := fmt.Sprintf("%s/repos/%s/%s/git/trees/%s?recursive=1",
		githubAPIBase, url.PathEscape(repo.Owner), url.PathEscape(repo.Name), url.PathEscape(branch))

	resp, err := f.hc.Get(ctx, treeURL)
	if err != nil {
		return nil, fmt.Errorf("github tree fetch: %w", err)
	}

	// branch 404 时，尝试获取默认分支并重试一次
	if resp.StatusCode == 404 {
		drainBody(resp.Body)
		resp.Body.Close()

		def, err := f.fetchDefaultBranch(ctx, repo.Owner, repo.Name)
		if err != nil {
			return nil, fmt.Errorf("repo %s/%s branch %q not found and failed to get default branch: %w",
				repo.Owner, repo.Name, branch, err)
		}
		branch = def
		treeURL = fmt.Sprintf("%s/repos/%s/%s/git/trees/%s?recursive=1",
			githubAPIBase, url.PathEscape(repo.Owner), url.PathEscape(repo.Name), url.PathEscape(branch))
		resp, err = f.hc.Get(ctx, treeURL)
		if err != nil {
			return nil, fmt.Errorf("github tree fetch (retry): %w", err)
		}
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		drainBody(resp.Body)
		return nil, fmt.Errorf("github tree: status %d", resp.StatusCode)
	}

	var treeResp githubTreeResponse
	if err := json.NewDecoder(io.LimitReader(resp.Body, 5*1024*1024)).Decode(&treeResp); err != nil {
		return nil, fmt.Errorf("github tree decode: %w", err)
	}

	// 2. 找出 skills/ 目录下所有 SKILL.md 的父目录
	//    约定：只扫描仓库下 skills/<name>/SKILL.md
	//    - directory: SKILL.md 所在目录的最后一段（用于显示和去重 key）
	//    - fullPath:  SKILL.md 所在目录的完整相对路径（如 "skills/pdf"，用于拼接 raw URL）
	type skillDirInfo struct {
		directory string // 最后一段，如 "pdf"
		fullPath  string // 完整相对路径，如 "skills/pdf"
	}
	skillDirs := make(map[string]skillDirInfo) // 按 directory 去重
	for _, item := range treeResp.Tree {
		if item.Type != "blob" {
			continue
		}
		// 只处理 skills/ 目录下的 SKILL.md
		// 例: skills/pdf/SKILL.md ✓
		//     skills/webdiscovery/SKILL.md ✓
		//     SKILL.md ✗ (根目录)
		//     template/SKILL.md ✗
		//     composio-skills/foo/SKILL.md ✗
		if !strings.HasPrefix(item.Path, "skills/") || !strings.HasSuffix(item.Path, "/SKILL.md") {
			continue
		}
		pathParts := strings.Split(item.Path, "/")
		if len(pathParts) < 3 {
			continue // skills/SKILL.md 不符合 skills/<name>/SKILL.md 结构
		}
		// directory = SKILL.md 所在目录的最后一段
		dir := pathParts[len(pathParts)-2]
		// fullPath = path 去掉最后一段 (SKILL.md)
		fullPath := strings.Join(pathParts[:len(pathParts)-1], "/")
		// 同名 directory 只保留第一次（避免不同子路径的同名目录冲突）
		if _, exists := skillDirs[dir]; !exists {
			skillDirs[dir] = skillDirInfo{directory: dir, fullPath: fullPath}
		}
	}

	if len(skillDirs) == 0 {
		return nil, nil
	}

	// 限制每仓库最多扫描的 SKILL.md 数量，避免大仓库（如 ComposioHQ/awesome-claude-skills 含 200+ skills）
	// 拖垮整体扫描超时。anthropics/skills 只有 18 个，不受影响。
	const maxSkillsPerRepo = 50
	if len(skillDirs) > maxSkillsPerRepo {
		log.Printf("github skills: %s/%s has %d SKILL.md, truncating to %d", repo.Owner, repo.Name, len(skillDirs), maxSkillsPerRepo)
		// 按 key 排序后截断，保证截断结果确定（不依赖 map 随机遍历顺序）
		keys := make([]string, 0, len(skillDirs))
		for k := range skillDirs {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for i := maxSkillsPerRepo; i < len(keys); i++ {
			delete(skillDirs, keys[i])
		}
	}

	// 3. 并行获取每个 skill 的 SKILL.md 内容（限制并发 5）
	skillPaths := make([]skillDirInfo, 0, len(skillDirs))
	for _, info := range skillDirs {
		skillPaths = append(skillPaths, info)
	}

	var mu sync.Mutex
	skills := make([]MarketSkill, 0, len(skillPaths))

	sem := make(chan struct{}, 5) // 并发限制
	var wg sync.WaitGroup
	wg.Add(len(skillPaths))

	for _, item := range skillPaths {
		item := item
		go func() {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()

			skill, err := f.fetchSkillMeta(ctx, repo, branch, item.directory, item.fullPath)
			if err != nil {
				// 记录失败日志，方便调试（不影响其他 skill 的扫描）
				log.Printf("github skills: skip %s/%s/%s: %v", repo.Owner, repo.Name, item.directory, err)
				return
			}
			mu.Lock()
			skills = append(skills, skill)
			mu.Unlock()
		}()
	}
	wg.Wait()

	return skills, nil
}

// fetchSkillMeta 获取单个 skill 的 SKILL.md 并解析 frontmatter
// directory: SKILL.md 所在目录的最后一段（用于显示）
// fullPath: SKILL.md 所在目录的完整相对路径（如 "skills/pdf"，根目录为空）
// 使用 jsDelivr CDN URL 格式: https://cdn.jsdelivr.net/gh/{owner}/{repo}@{branch}/{path}/SKILL.md
func (f *GitHubSkillFetcher) fetchSkillMeta(ctx context.Context, repo RepoRef, branch, directory, fullPath string) (MarketSkill, error) {
	// 拼接 jsDelivr CDN URL
	// 格式: https://cdn.jsdelivr.net/gh/{owner}/{repo}@{branch}/{path}/SKILL.md
	// fullPath 为空时表示 SKILL.md 在根目录
	var rawURL string
	if fullPath == "" {
		rawURL = fmt.Sprintf("%s/%s/%s@%s/SKILL.md",
			githubRawBase, url.PathEscape(repo.Owner), url.PathEscape(repo.Name), url.PathEscape(branch))
	} else {
		// 路径段需要按 / 分段后逐段 PathEscape，避免整体编码把 / 也编码掉
		segments := strings.Split(fullPath, "/")
		for i, seg := range segments {
			segments[i] = url.PathEscape(seg)
		}
		rawURL = fmt.Sprintf("%s/%s/%s@%s/%s/SKILL.md",
			githubRawBase, url.PathEscape(repo.Owner), url.PathEscape(repo.Name), url.PathEscape(branch), strings.Join(segments, "/"))
	}

	resp, err := f.hc.Get(ctx, rawURL)
	if err != nil {
		return MarketSkill{}, fmt.Errorf("fetch SKILL.md for %s: %w", directory, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		drainBody(resp.Body)
		return MarketSkill{}, fmt.Errorf("SKILL.md for %s: status %d", directory, resp.StatusCode)
	}

	// 限制读取 256KB（SKILL.md 通常 < 10KB）
	data, err := io.ReadAll(io.LimitReader(resp.Body, 256*1024))
	if err != nil {
		return MarketSkill{}, fmt.Errorf("read SKILL.md for %s: %w", directory, err)
	}

	meta := parseSkillFrontmatter(data)
	name := meta.Name
	if name == "" {
		name = directory
	}

	return MarketSkill{
		ID:          fmt.Sprintf("github:%s/%s/%s", repo.Owner, repo.Name, directory),
		Name:        name,
		Description: meta.Description,
		Directory:   directory,
		FullPath:    fullPath, // 保存完整相对路径，安装时用于精准定位 tarball 中的 skill 目录
		Source:      SourceGitHub,
		SourceID:    fmt.Sprintf("%s/%s", repo.Owner, repo.Name),
		Installs:    0, // GitHub 仓库扫描无下载量
		RepoOwner:   repo.Owner,
		RepoName:    repo.Name,
		RepoBranch:  branch,
		ReadmeURL:   fmt.Sprintf("https://github.com/%s/%s", repo.Owner, repo.Name),
		UpdatedAt:   "",
	}, nil
}

// skillMeta 是 SKILL.md frontmatter 的解析结果
type skillMeta struct {
	Name        string
	Description string
}

// parseSkillFrontmatter 解析 SKILL.md 的 YAML frontmatter
// 仅提取 name 和 description 两个字段
func parseSkillFrontmatter(content []byte) skillMeta {
	text := string(content)
	text = strings.TrimPrefix(text, "\uFEFF")

	if !strings.HasPrefix(text, "---") {
		return skillMeta{}
	}

	rest := text[3:]
	// 跳过首行换行
	if len(rest) > 0 && (rest[0] == '\n' || rest[0] == '\r') {
		rest = rest[1:]
		if len(rest) > 0 && rest[0] == '\n' {
			rest = rest[1:]
		}
	}

	endIdx := strings.Index(rest, "\n---")
	if endIdx < 0 {
		return skillMeta{}
	}

	frontmatter := rest[:endIdx]
	var meta skillMeta
	inDescription := false
	var descLines []string

	for _, line := range strings.Split(frontmatter, "\n") {
		trimmed := strings.TrimSpace(line)

		if inDescription {
			// 多行 description 的延续行
			if trimmed == "" || strings.HasPrefix(line, " ") || strings.HasPrefix(line, "\t") {
				descLines = append(descLines, trimmed)
				continue
			}
			inDescription = false
		}

		if !strings.Contains(line, ":") {
			continue
		}

		parts := strings.SplitN(line, ":", 2)
		key := strings.TrimSpace(parts[0])
		val := ""
		if len(parts) > 1 {
			val = parts[1]
		}

		switch strings.ToLower(key) {
		case "name":
			meta.Name = strings.Trim(strings.TrimSpace(val), "\"'")
		case "description":
			desc := strings.TrimSpace(val)
			if desc != "" {
				meta.Description = strings.Trim(desc, "\"'")
			} else {
				inDescription = true
			}
		}
	}

	if inDescription && len(descLines) > 0 {
		meta.Description = strings.Join(descLines, " ")
	}

	return meta
}

// validateRepoRef 校验仓库引用的合法性，防止 URL 注入
func validateRepoRef(repo RepoRef) error {
	if repo.Owner == "" || repo.Name == "" {
		return fmt.Errorf("owner and name are required")
	}
	if !isSafeGitHubIdent(repo.Owner) {
		return fmt.Errorf("invalid owner: %s", repo.Owner)
	}
	if !isSafeGitHubIdent(repo.Name) {
		return fmt.Errorf("invalid name: %s", repo.Name)
	}
	if repo.Branch != "" && !isSafeBranchName(repo.Branch) {
		return fmt.Errorf("invalid branch: %s", repo.Branch)
	}
	return nil
}

// fetchDefaultBranch 调用 GitHub API 获取仓库的默认分支
// GET /repos/{owner}/{name} → default_branch 字段
func (f *GitHubSkillFetcher) fetchDefaultBranch(ctx context.Context, owner, name string) (string, error) {
	repoURL := fmt.Sprintf("%s/repos/%s/%s",
		githubAPIBase, url.PathEscape(owner), url.PathEscape(name))

	resp, err := f.hc.Get(ctx, repoURL)
	if err != nil {
		return "", fmt.Errorf("fetch repo info: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		drainBody(resp.Body)
		return "", fmt.Errorf("repo %s/%s: status %d", owner, name, resp.StatusCode)
	}

	var repoInfo struct {
		DefaultBranch string `json:"default_branch"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 64*1024)).Decode(&repoInfo); err != nil {
		return "", fmt.Errorf("decode repo info: %w", err)
	}
	if repoInfo.DefaultBranch == "" {
		return "", fmt.Errorf("default_branch is empty")
	}
	return repoInfo.DefaultBranch, nil
}

// isSafeGitHubIdent 校验 owner/name 是否仅含 [a-zA-Z0-9._-]
func isSafeGitHubIdent(s string) bool {
	if s == "" || len(s) > 100 {
		return false
	}
	for _, r := range s {
		if !((r >= 'a' && r <= 'z') ||
			(r >= 'A' && r <= 'Z') ||
			(r >= '0' && r <= '9') ||
			r == '.' || r == '_' || r == '-') {
			return false
		}
	}
	return true
}

// isSafeBranchName 校验分支名是否仅含安全字符
func isSafeBranchName(s string) bool {
	if s == "" || len(s) > 200 {
		return false
	}
	for _, r := range s {
		if !((r >= 'a' && r <= 'z') ||
			(r >= 'A' && r <= 'Z') ||
			(r >= '0' && r <= '9') ||
			r == '.' || r == '_' || r == '-' || r == '/') {
			return false
		}
	}
	return true
}
