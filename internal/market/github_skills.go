package market

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/url"
	"sort"
	"strings"
	"sync"
	"time"

	"agentpack/internal/config"
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
	// jsDelivr Data API：返回包文件树（structure=flat 为递归列表），
	// 与 GitHub Trees API 同构。中国网络可达、无未认证限流（api.github.com
	// 未认证 60 次/小时，配置几十个仓库时必然触发 403 RateLimit）。
	jsDelivrDataBase = "https://data.jsdelivr.com"
)

// jsDelivrDefaultBranchAlias 是 jsDelivr 对 gh 包的默认分支别名：
// 请求 @master 时，若仓库不存在 master 分支，jsDelivr 自动解析为仓库的
// 默认分支（实测 anthropics/skills@master 返回默认分支 main 的文件树）。
// 因此未配置分支时无需调用 GitHub API 即可获取默认分支的文件树。
// 该别名只在扫描与 SKILL.md 拉取链路使用；安装侧 InstallMarketSkill 会
// 枚举 main/master 兜底，别名不会导致安装 404。
const jsDelivrDefaultBranchAlias = "master"

// githubURL 构造 GitHub API URL，按需套用应用层代理 DefaultGitHubProxy。
// 与 update.go/skills 的代理策略一致；仅在 https URL 上套用，测试中
// 覆盖 githubAPIBase 为本地 http:// 服务器时不受影响。
func githubURL(path string) string {
	u := githubAPIBase + path
	if strings.HasPrefix(u, "https://") {
		if p := strings.TrimSpace(config.DefaultGitHubProxy); p != "" && !strings.HasPrefix(u, p) {
			// TrimSuffix 防尾斜杠代理配置拼出 // 双斜杠路径，
			// 部分代理服务器对双斜杠返回 404
			p = strings.TrimSuffix(p, "/")
			u = p + strings.TrimPrefix(strings.TrimPrefix(u, "https://"), "http://")
		}
	}
	return u
}

// GitHubSkillFetcher 扫描配置的 GitHub 仓库列表，解析含 SKILL.md 的子目录
type GitHubSkillFetcher struct {
	hc     *HTTPClient      // 带环境代理（GitHub API 回退路径优先）
	direct *HTTPClient      // 直连（jsDelivr data API/CDN 主链路优先）
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
		direct: NewHTTPClientNoProxy(),
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

// jsDelivrTreeResponse 是 jsDelivr Data API 的响应结构
// GET /v1/packages/gh/{owner}/{repo}@{branch}?structure=flat
// flat 结构返回递归文件列表（最多 3000 个，超出置 truncated=true）
type jsDelivrTreeResponse struct {
	Name      string `json:"name"`
	Version   string `json:"version"`
	Default   string `json:"default"`
	Truncated bool   `json:"truncated"`
	Files     []struct {
		Name string `json:"name"` // 以 / 开头的相对路径，如 "/skills/pdf/SKILL.md"
		Size int64  `json:"size"`
	} `json:"files"`
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
	// 确保 items 非 nil,nil slice 会被 JSON 序列化为 null,导致前端 `...more.items` 崩溃
	if allSkills == nil {
		allSkills = []MarketSkill{}
	}
	// 不分页，直接返回全部（仓库扫描结果通常 < 100 条）
	result := &SearchResultSkills{
		Items:   allSkills,
		Total:   total,
		Page:    1,
		HasMore: false,
	}
	return result, nil
}

// escapeBranch 对分支名做整段路径编码。url.PathEscape 不转义 "/"，分支名
// 含 "/"（如 feature/foo）时 URL 中会多出一个路径段，服务端解析为错误路径
// （GitHub Trees 变 /trees/feature/foo，jsDelivr 变 @feature/foo）。
func escapeBranch(b string) string {
	return strings.ReplaceAll(url.PathEscape(b), "/", "%2F")
}

// fetchTreePaths 获取仓库递归文件树并确定扫描分支。
//
// 主链路使用 jsDelivr Data API（structure=flat 返回递归文件列表）：
//   - 不受 GitHub API 未认证限流影响（api.github.com 限 60 次/小时，配置几十个
//     仓库时必然 403），中国网络下可达且无需代理；
//   - 未配置分支时用 @master 别名，jsDelivr 自动解析为仓库默认分支，
//     无需额外一次 API 调用。
//
// jsDelivr 失败（仓库不存在、CDN 故障）时回退 GitHub Trees API（含默认分支
// 解析与 404 重试）。返回的 branch 用于 SKILL.md 拉取与安装字段。
func (f *GitHubSkillFetcher) fetchTreePaths(ctx context.Context, repo RepoRef) ([]string, string, error) {
	branch := repo.Branch
	alias := branch
	if alias == "" {
		alias = jsDelivrDefaultBranchAlias
	}

	paths, err := f.fetchTreeViaJSDelivr(ctx, repo.Owner, repo.Name, alias)
	if err == nil {
		if branch == "" {
			// 注意：@master 别名 ≠ 真实默认分支——仓库真实存在 master 分支
			// （默认 main）时别名解析到的是 master 内容，直接落库会让安装
			// 枚举 [master, main] 优先命中 master，安装的不是用户预期的
			// 默认分支内容。尝试解析真实默认分支落库；GitHub API 不可达
			// （未认证限流/网络）时保持空串，由安装侧枚举（main 优先）决定。
			defCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
			defer cancel()
			if def, derr := f.fetchDefaultBranch(defCtx, repo.Owner, repo.Name); derr == nil && def != "" {
				branch = def
			}
			// branch 保持空串：SKILL.md 内容拉取仍用别名（见 scanRepo），
			// 落库分支交给安装枚举处理。
		}
		return paths, branch, nil
	}
	log.Printf("github skills: jsdelivr tree failed for %s/%s@%s: %v, falling back to GitHub API",
		repo.Owner, repo.Name, alias, err)

	// ---- GitHub Trees API 回退路径 ----
	if branch == "" {
		def, err := f.fetchDefaultBranch(ctx, repo.Owner, repo.Name)
		if err != nil {
			return nil, "", fmt.Errorf("fetch default branch for %s/%s: %w", repo.Owner, repo.Name, err)
		}
		branch = def
	}

	treeURL := githubURL(fmt.Sprintf("/repos/%s/%s/git/trees/%s?recursive=1",
		url.PathEscape(repo.Owner), url.PathEscape(repo.Name), escapeBranch(branch)))

	resp, err := f.hc.GetWithFallback(ctx, treeURL, f.direct)
	if err != nil {
		return nil, "", fmt.Errorf("github tree fetch: %w", err)
	}

	// branch 404 时，尝试获取默认分支并重试一次
	if resp.StatusCode == 404 {
		drainBody(resp.Body)
		resp.Body.Close()

		def, err := f.fetchDefaultBranch(ctx, repo.Owner, repo.Name)
		if err != nil {
			return nil, "", fmt.Errorf("repo %s/%s branch %q not found and failed to get default branch: %w",
				repo.Owner, repo.Name, branch, err)
		}
		branch = def
		treeURL = githubURL(fmt.Sprintf("/repos/%s/%s/git/trees/%s?recursive=1",
			url.PathEscape(repo.Owner), url.PathEscape(repo.Name), escapeBranch(branch)))
		resp, err = f.hc.GetWithFallback(ctx, treeURL, f.direct)
		if err != nil {
			return nil, "", fmt.Errorf("github tree fetch (retry): %w", err)
		}
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		drainBody(resp.Body)
		return nil, "", fmt.Errorf("github tree: status %d", resp.StatusCode)
	}

	var treeResp githubTreeResponse
	if err := json.NewDecoder(io.LimitReader(resp.Body, 5*1024*1024)).Decode(&treeResp); err != nil {
		return nil, "", fmt.Errorf("github tree decode: %w", err)
	}
	// Trees API 的 Truncated=true 表示目录树被截断（超大仓库），
	// 返回错误让上层跳过该仓库，避免静默返回不完整的 skill 列表误导用户。
	if treeResp.Truncated {
		log.Printf("github skills: tree for %s/%s truncated (too large), skipping repo", repo.Owner, repo.Name)
		return nil, "", fmt.Errorf("tree truncated for %s/%s", repo.Owner, repo.Name)
	}

	paths = make([]string, 0, len(treeResp.Tree))
	for _, item := range treeResp.Tree {
		if item.Type == "blob" {
			paths = append(paths, item.Path)
		}
	}
	return paths, branch, nil
}

// fetchTreeViaJSDelivr 通过 jsDelivr Data API 获取仓库递归文件列表。
// branch 为空时使用 @master 别名（jsDelivr 自动解析为仓库默认分支）。
// 直连优先：部分环境代理对 jsDelivr 域名未放行返回 403，直连通常可达。
func (f *GitHubSkillFetcher) fetchTreeViaJSDelivr(ctx context.Context, owner, name, branch string) ([]string, error) {
	u := fmt.Sprintf("%s/v1/packages/gh/%s/%s@%s?structure=flat",
		jsDelivrDataBase, url.PathEscape(owner), url.PathEscape(name), escapeBranch(branch))

	resp, err := f.direct.GetWithFallback(ctx, u, f.hc)
	if err != nil {
		return nil, fmt.Errorf("jsdelivr tree fetch: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		drainBody(resp.Body)
		return nil, fmt.Errorf("jsdelivr tree: status %d", resp.StatusCode)
	}

	var treeResp jsDelivrTreeResponse
	if err := json.NewDecoder(io.LimitReader(resp.Body, 5*1024*1024)).Decode(&treeResp); err != nil {
		return nil, fmt.Errorf("jsdelivr tree decode: %w", err)
	}
	// flat 结构最多返回 3000 个文件，truncated=true 表示被截断（超大仓库），
	// 与 GitHub Trees API 的 Truncated 同语义：跳过仓库，避免静默返回不完整列表。
	if treeResp.Truncated {
		log.Printf("github skills: jsdelivr tree for %s/%s truncated (too large), skipping repo", owner, name)
		return nil, fmt.Errorf("jsdelivr tree truncated for %s/%s", owner, name)
	}

	paths := make([]string, 0, len(treeResp.Files))
	for _, file := range treeResp.Files {
		// data API 的文件路径以 / 开头（如 "/skills/pdf/SKILL.md"）
		paths = append(paths, strings.TrimPrefix(file.Name, "/"))
	}
	return paths, nil
}

// parseSkillDirs 从递归文件列表中找出 skills/ 目录下所有 SKILL.md 的父目录。
// 约定：只扫描仓库下 skills/<name>/SKILL.md
//   - directory: SKILL.md 所在目录的最后一段（用于显示和去重 key）
//   - fullPath:  SKILL.md 所在目录的完整相对路径（如 "skills/pdf"，用于拼接 raw URL）
//
// 例: skills/pdf/SKILL.md ✓ | SKILL.md ✗ (根目录) | template/SKILL.md ✗ | composio-skills/foo/SKILL.md ✗
func parseSkillDirs(paths []string) map[string]skillDirInfo {
	skillDirs := make(map[string]skillDirInfo) // 按 directory 去重
	for _, p := range paths {
		if !strings.HasPrefix(p, "skills/") || !strings.HasSuffix(p, "/SKILL.md") {
			continue
		}
		pathParts := strings.Split(p, "/")
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
	return skillDirs
}

// skillDirInfo 描述一个 skill 目录
type skillDirInfo struct {
	directory string // 最后一段，如 "pdf"
	fullPath  string // 完整相对路径，如 "skills/pdf"
}

func (f *GitHubSkillFetcher) scanRepo(ctx context.Context, repo RepoRef) ([]MarketSkill, error) {
	if err := validateRepoRef(repo); err != nil {
		return nil, fmt.Errorf("invalid repo %s/%s: %w", repo.Owner, repo.Name, err)
	}

	// 获取递归文件树并确定分支（jsDelivr Data API 主链路 + GitHub API 回退）
	paths, branch, err := f.fetchTreePaths(ctx, repo)
	if err != nil {
		return nil, err
	}

	// 2. 找出 skills/ 目录下所有 SKILL.md 的父目录
	//    约定：只扫描仓库下 skills/<name>/SKILL.md
	//    - directory: SKILL.md 所在目录的最后一段（用于显示和去重 key）
	//    - fullPath:  SKILL.md 所在目录的完整相对路径（如 "skills/pdf"，用于拼接 raw URL）
	skillDirs := parseSkillDirs(paths)

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
	// 内容拉取分支：branch 未解析出真实默认分支时用 jsDelivr 默认分支别名
	//（@master 别名能覆盖 main/master 默认分支仓库），避免空分支导致
	// SKILL.md 全部拉取失败、卡片降级为目录名。落库分支仍用解析后的
	// branch（空则交给安装枚举决定，见下方修正）。
	contentBranch := branch
	if contentBranch == "" {
		contentBranch = jsDelivrDefaultBranchAlias
	}
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

			// fetchSkillMeta 内部对 CDN 失败做降级处理，始终返回有效 MarketSkill
			// 避免 jsDelivr 限流/网络抖动导致整个 skill 从市场消失
			skill := f.fetchSkillMeta(ctx, repo, contentBranch, item.directory, item.fullPath)
			mu.Lock()
			skills = append(skills, skill)
			mu.Unlock()
		}()
	}
	wg.Wait()

	// 修正落库分支：fetchSkillMeta 用内容拉取分支（可能为 @master 别名），
	// 落库必须是解析后的真实分支（空则安装枚举 [main, master] 决定），
	// 否则别名被误当真实分支（双分支仓库安装到 master 内容）。
	for i := range skills {
		skills[i].RepoBranch = branch
	}

	return skills, nil
}

// fetchSkillMeta 获取单个 skill 的 SKILL.md 并解析 frontmatter
// directory: SKILL.md 所在目录的最后一段（用于显示）
// fullPath: SKILL.md 所在目录的完整相对路径（如 "skills/pdf"，根目录为空）
// 使用 jsDelivr CDN URL 格式: https://cdn.jsdelivr.net/gh/{owner}/{repo}@{branch}/{path}/SKILL.md
//
// 重要：即使 SKILL.md 拉取失败（CDN 限流、网络抖动、5xx 等），也返回降级 MarketSkill
// （Name=directory，Description=""），而不是返回错误让调用方跳过该 skill。
// 原因：SKILL.md 内容仅用于卡片展示（名称/描述），安装流程使用 GitHub tarball API +
// Directory/FullPath 字段定位 skill 目录，完全不依赖 SKILL.md 内容。
// 之前的实现：CDN 失败 → fetchSkillMeta 返回 error → scanRepo 跳过该 skill →
// 用户看到 skills 数量大幅减少（实测 anthropics/skills 18 个 skill 只有 1 个能显示）。
// 修复后：CDN 失败 → 降级返回（Name=directory，Description=""）→ skill 仍出现在市场中。
func (f *GitHubSkillFetcher) fetchSkillMeta(ctx context.Context, repo RepoRef, branch, directory, fullPath string) MarketSkill {
	// 拼接 jsDelivr CDN URL
	// 格式: https://cdn.jsdelivr.net/gh/{owner}/{repo}@{branch}/{path}/SKILL.md
	// fullPath 为空时表示 SKILL.md 在根目录
	var rawURL string
	if fullPath == "" {
		rawURL = fmt.Sprintf("%s/%s/%s@%s/SKILL.md",
			githubRawBase, url.PathEscape(repo.Owner), url.PathEscape(repo.Name), escapeBranch(branch))
	} else {
		// 路径段需要按 / 分段后逐段 PathEscape，避免整体编码把 / 也编码掉
		segments := strings.Split(fullPath, "/")
		for i, seg := range segments {
			segments[i] = url.PathEscape(seg)
		}
		rawURL = fmt.Sprintf("%s/%s/%s@%s/%s/SKILL.md",
			githubRawBase, url.PathEscape(repo.Owner), url.PathEscape(repo.Name), escapeBranch(branch), strings.Join(segments, "/"))
	}

	resp, err := f.direct.GetWithFallback(ctx, rawURL, f.hc)
	if err != nil {
		// 网络错误：降级返回，不跳过 skill
		log.Printf("github skills: fetch SKILL.md for %s/%s/%s failed, using fallback: %v", repo.Owner, repo.Name, directory, err)
		return fallbackSkill(repo, branch, directory, fullPath)
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		drainBody(resp.Body)
		// 非 200（如 404/429/5xx）：降级返回，不跳过 skill
		log.Printf("github skills: SKILL.md for %s/%s/%s returned status %d, using fallback", repo.Owner, repo.Name, directory, resp.StatusCode)
		return fallbackSkill(repo, branch, directory, fullPath)
	}

	// 限制读取 256KB（SKILL.md 通常 < 10KB）
	data, err := io.ReadAll(io.LimitReader(resp.Body, 256*1024))
	if err != nil {
		log.Printf("github skills: read SKILL.md for %s/%s/%s failed, using fallback: %v", repo.Owner, repo.Name, directory, err)
		return fallbackSkill(repo, branch, directory, fullPath)
	}

	meta := parseSkillFrontmatter(data)
	name := meta.Name
	if name == "" {
		name = directory
	}

	// 从已拉取的 SKILL.md 内容计算 ContentHash，避免后续 populateContentHashes 再次从 CDN 拉取
	normalized := bytes.ReplaceAll(data, []byte{'\r', '\n'}, []byte{'\n'})
	h := sha256.Sum256(normalized)
	contentHash := hex.EncodeToString(h[:])

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
		ContentHash: contentHash,
	}
}

// fallbackSkill 返回 SKILL.md 拉取失败时的降级 MarketSkill
// Name 使用 directory（最后一段路径），Description 为空
// 安装流程不依赖这些字段，仍可正常安装
func fallbackSkill(repo RepoRef, branch, directory, fullPath string) MarketSkill {
	return MarketSkill{
		ID:          fmt.Sprintf("github:%s/%s/%s", repo.Owner, repo.Name, directory),
		Name:        directory,
		Description: "",
		Directory:   directory,
		FullPath:    fullPath,
		Source:      SourceGitHub,
		SourceID:    fmt.Sprintf("%s/%s", repo.Owner, repo.Name),
		Installs:    0,
		RepoOwner:   repo.Owner,
		RepoName:    repo.Name,
		RepoBranch:  branch,
		ReadmeURL:   fmt.Sprintf("https://github.com/%s/%s", repo.Owner, repo.Name),
		UpdatedAt:   "",
	}
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
	repoURL := githubURL(fmt.Sprintf("/repos/%s/%s",
		url.PathEscape(owner), url.PathEscape(name)))

	resp, err := f.hc.GetWithFallback(ctx, repoURL, f.direct)
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
