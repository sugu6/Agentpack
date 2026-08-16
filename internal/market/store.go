package market

import (
	"agentpack/internal/iowriter"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

// inflightResult 是 singleflight 等待者收到的结果。
// 通道必须携带结果而非仅通知：首者"成功但空结果"不写缓存（v17 语义），
// 等待者醒来后回读缓存必然 miss，若只看通道关闭会误报
// "previous fetch failed"，连锁导致 SearchAllSkills 把该 source 标记为
// error、全量 merged 缓存不写。直接传结果消除误报。
type inflightResult struct {
	result *SearchResultSkills
	err    error
}

type Store struct {
	mu            sync.RWMutex
	cacheMu       sync.Mutex
	cacheDir      string
	ttl           time.Duration
	servers       map[Source]ServerFetcher
	skillFetchers map[Source]SkillFetcher
	inFlight      map[string]chan inflightResult // 防止并发 fetch 相同 key
	inFlightMu    sync.Mutex
}

// cacheVersion 是缓存键的版本号。
// 当 fetcher 的数据结构或扫描逻辑发生破坏性变更时（如新增字段、修复路径拼接），
// 递增此版本号可使旧缓存文件失效，强制重新拉取最新数据。
//
// v11: 修复 github_skills.go 中 fetchSkillMeta CDN 失败时跳过整个 skill 的 bug。
//
//	旧缓存（v10）中 anthropics/skills 仓库只有 1 个 canvas-design skill，
//	其他 17 个因 jsDelivr CDN 限流被跳过。升级后强制重新拉取，所有 skill 都会出现。
//
// v15: MCP servers 搜索改回 API cursor 分页(放弃全量缓存)。
//
//	registry API 每页 20 秒(网络瓶颈),全量加载几千个 server 需要几十分钟,
//	后台预取永远完不成,导致 Transport 筛选只能用 100 条数据,LoadMore 到 100 条就停。
//	新方案:Search 直接透传给 fetcher(API cursor 分页),Transport 筛选改到前端做
//	(前端从已加载的 items 累积过滤),LoadMore 用 API cursor 一定能继续加载。
//
// v16: GitHub fetcher 在 fetchSkillMeta 中直接计算 ContentHash，避免后续重复 CDN 请求。
//
//	populateContentHashes 改为并发 5 路 + 同时补全 skills.sh 来源缺失的 Name/Description。
//	旧缓存缺少 ContentHash 和 Description，需强制刷新。
//
// v17: 空结果不写缓存（SearchSkills/merged 缓存均跳过），并强制失效
//
//	v16 及更早版本在网络故障/代理 403 时写入的空结果缓存（市场页 24h 空白）。
const cacheVersion = 17

type ServerFetcher interface {
	Source() Source
	Search(ctx context.Context, opts SearchOptions) (*SearchResultServers, error)
}

func NewStore(cacheDir string) *Store {
	if cacheDir == "" {
		home, _ := os.UserHomeDir()
		cacheDir = filepath.Join(home, ".agentpack", "market")
	}
	return &Store{
		cacheDir:      cacheDir,
		ttl:           24 * time.Hour,
		servers:       make(map[Source]ServerFetcher),
		skillFetchers: make(map[Source]SkillFetcher),
		inFlight:      make(map[string]chan inflightResult),
	}
}

func (s *Store) RegisterServer(f ServerFetcher) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.servers[f.Source()] = f
}

func (s *Store) Sources() []Source {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]Source, 0, len(s.servers))
	for src := range s.servers {
		out = append(out, src)
	}
	return out
}

// Search 直接透传给 fetcher,使用 registry API 的 cursor 分页。
// Transport 筛选由前端累积式过滤(从已加载的 items 过滤),后端不处理。
// 这是因为 registry API 不支持 transport 筛选参数,而全量缓存方案在网络瓶颈下
// (每页 20 秒,全量加载几千个 server 需要几十分钟)不可行。
func (s *Store) Search(ctx context.Context, source Source, opts SearchOptions) (*SearchResultServers, error) {
	s.mu.RLock()
	f, ok := s.servers[source]
	s.mu.RUnlock()
	if !ok {
		return nil, fmt.Errorf("source not found: %s", source)
	}
	return f.Search(ctx, opts)
}

// markInflightDone 标记 in-flight fetch 完成，通知等待者
// close 后等待者收到零值 inflightResult；首者正常路径会先发送结果再 close，
// panic 等异常退出时零值兜底（result==nil 时等待者回读缓存或报错）。
func (s *Store) markInflightDone(key string) {
	s.inFlightMu.Lock()
	ch, ok := s.inFlight[key]
	if ok {
		delete(s.inFlight, key)
		close(ch)
	}
	s.inFlightMu.Unlock()
}

func (s *Store) GetServer(ctx context.Context, source Source, sourceID string) (*MarketServer, error) {
	s.mu.RLock()
	f, ok := s.servers[source]
	s.mu.RUnlock()
	if !ok {
		return nil, fmt.Errorf("source not registered: %s", source)
	}
	if getter, supports := f.(interface {
		Get(ctx context.Context, sourceID string) (*MarketServer, error)
	}); supports {
		return getter.Get(ctx, sourceID)
	}
	return nil, fmt.Errorf("source %q (%T) does not support GetServer", source, f)
}

func ContextWithTimeout(d time.Duration) (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.Background(), d)
}

// cacheKey 生成 skill 搜索的缓存键(含 query/cursor/page 等分页参数)
// 注意:server 搜索直接透传给 fetcher(API cursor 分页),不使用此缓存键
func (s *Store) cacheKey(kind string, source Source, opts SearchOptions) string {
	// cacheVersion 用于在数据结构或扫描逻辑变更后使旧缓存失效
	// 递增此版本号会强制所有用户重新拉取最新数据
	// 用 JSON 数组承载各段：分隔符拼接（如 "a|b" 与 ("a","b")）会产生
	// 歧义碰撞，不同查询误命中同一缓存文件
	payload, _ := json.Marshal([]any{cacheVersion, kind, source, opts.Query, opts.Cursor, opts.Page, opts.PageSize})
	sum := sha256.Sum256(payload)
	return hex.EncodeToString(sum[:16]) + ".json"
}

func (s *Store) cachePath(key string) string {
	return filepath.Join(s.cacheDir, key)
}

func (s *Store) writeCacheLocked(key string, payload any, logPrefix string) {
	if err := os.MkdirAll(s.cacheDir, 0700); err != nil {
		log.Printf("%s mkdir: %v", logPrefix, err)
		return
	}
	data, err := json.Marshal(payload)
	if err != nil {
		log.Printf("%s marshal: %v", logPrefix, err)
		return
	}
	if err := iowriter.WriteAtomic(s.cachePath(key), data, 0600); err != nil {
		log.Printf("%s write: %v", logPrefix, err)
	}
}

func (s *Store) CleanCache() (int, error) {
	// 锁保持在整个操作期间：与 ClearAllCache 一致，防止并发写入在
	// ReadDir 和 Remove 之间插入新缓存文件后被误删。
	s.cacheMu.Lock()
	defer s.cacheMu.Unlock()
	entries, err := os.ReadDir(s.cacheDir)
	if err != nil {
		return 0, err
	}
	var toRemove []string
	now := time.Now()
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		info, err := entry.Info()
		if err != nil {
			continue
		}
		if now.Sub(info.ModTime()) > s.ttl {
			toRemove = append(toRemove, s.cachePath(entry.Name()))
		}
	}
	removed := 0
	for _, path := range toRemove {
		if err := os.Remove(path); err == nil {
			removed++
		}
	}
	return removed, nil
}

// ClearAllCache 清理所有缓存文件（不论是否过期）
// 用于配置变更（如添加/删除 skills 仓库）后强制下次搜索重新拉取数据
// 锁保持在整个操作期间，防止并发写入在 ReadDir 和 Remove 之间插入新缓存文件
func (s *Store) ClearAllCache() (int, error) {
	s.cacheMu.Lock()
	defer s.cacheMu.Unlock()
	entries, err := os.ReadDir(s.cacheDir)
	if err != nil {
		return 0, err
	}
	removed := 0
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		if err := os.Remove(s.cachePath(entry.Name())); err == nil {
			removed++
		}
	}
	return removed, nil
}

// --- Skill 搜索支持 ---

func (s *Store) RegisterSkillFetcher(f SkillFetcher) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.skillFetchers[f.Source()] = f
}

func (s *Store) SkillSources() []Source {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]Source, 0, len(s.skillFetchers))
	for src := range s.skillFetchers {
		out = append(out, src)
	}
	return out
}

// SearchSkills 从单个 source 搜索 skills（带缓存 + 单飞 + 降级读旧缓存）
func (s *Store) SearchSkills(ctx context.Context, source Source, opts SearchOptions) (res *SearchResultSkills, err error) {
	s.mu.RLock()
	f, ok := s.skillFetchers[source]
	s.mu.RUnlock()
	if !ok {
		return nil, fmt.Errorf("skill source not registered: %s", source)
	}

	cacheKey := s.cacheKey("search-skills", source, opts)

	// 先检查缓存（文件 I/O 不持有 cacheMu 锁）
	if cached, ok := s.readSkillCache(cacheKey); ok {
		return cached, nil
	}

	// 检查是否有其他 goroutine 正在 fetch 相同 key
	wait, isFirst := func() (chan inflightResult, bool) {
		s.inFlightMu.Lock()
		defer s.inFlightMu.Unlock()
		if ch, exists := s.inFlight[cacheKey]; exists {
			return ch, false
		}
		ch := make(chan inflightResult, 1)
		s.inFlight[cacheKey] = ch
		return ch, true
	}()

	if !isFirst {
		select {
		case fr := <-wait:
			if fr.err != nil {
				// 首者失败；降级读任意年龄旧缓存，避免等待者重复请求
				if cached, ok := s.readSkillCacheAnyAge(cacheKey); ok {
					return cached, nil
				}
				return nil, fr.err
			}
			// 首者成功（含"成功但空结果"——空结果不写缓存，fr 直接携带）
			if fr.result == nil {
				// 首者异常退出（panic 等）：回读缓存兜底
				if cached, ok := s.readSkillCache(cacheKey); ok {
					return cached, nil
				}
				return nil, fmt.Errorf("market skill search: previous fetch did not complete for %s", cacheKey)
			}
			return fr.result, nil
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}

	defer func() {
		// 先发送结果再标记完成：markInflightDone 会 close 通道，
		// 发送必须在 close 之前（close 后再发送 panic）
		// 携带结果通知等待者（缓冲 1 保证不阻塞）；defer 中读取命名返回值，
		// 所有 return 路径（成功/降级缓存/失败）都正确传递给等待者
		wait <- inflightResult{result: res, err: err}
		s.markInflightDone(cacheKey)
	}()

	res, err = f.Search(ctx, opts)
	if err != nil {
		if cached, ok := s.readSkillCacheAnyAge(cacheKey); ok {
			return cached, nil
		}
		return nil, err
	}
	// 即使 context 已超时，如果 fetcher 已成功返回结果，仍然写入缓存并返回
	// 避免因 context 超时丢弃已成功获取的数据（如 GitHub 仓库扫描耗时较长但已返回结果）
	//
	// 空结果不写缓存：网络故障/代理 403 会产出空结果，缓存后 24 小时内
	// （TTL）市场页一直空白且不再重试。空结果下次重新扫描，成功后再缓存。
	if res.Total > 0 {
		s.writeSkillCache(cacheKey, res)
	}
	return res, nil
}

// mergedSkillsCacheKey 生成全量合并结果的缓存键
// 包含 query 和来源列表，不包含 page/pageSize，确保不同页共享同一缓存
func (s *Store) mergedSkillsCacheKey(query string, sources []Source) string {
	sourceNames := make([]string, len(sources))
	for i, src := range sources {
		sourceNames[i] = string(src)
	}
	sort.Strings(sourceNames)
	// JSON 数组承载：来源列表与 query 的 | 拼接存在歧义碰撞
	payload, _ := json.Marshal([]any{cacheVersion, "skills-merged", sourceNames, query})
	sum := sha256.Sum256(payload)
	return hex.EncodeToString(sum[:16]) + ".json"
}

func (s *Store) readMergedSkillsCache(key string) ([]MarketSkill, bool) {
	path := s.cachePath(key)
	info, err := os.Stat(path)
	if err != nil {
		return nil, false
	}
	if time.Since(info.ModTime()) > s.ttl {
		s.removeCacheFile(key)
		return nil, false
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, false
	}
	var items []MarketSkill
	if err := json.Unmarshal(data, &items); err != nil {
		s.removeCacheFile(key)
		return nil, false
	}
	return items, true
}

func (s *Store) writeMergedSkillsCache(key string, items []MarketSkill) {
	s.cacheMu.Lock()
	defer s.cacheMu.Unlock()
	s.writeCacheLocked(key, items, "merged skills cache")
}

// SearchAllSkills 并行调用指定的 SkillFetcher，合并去重后按 Installs 降序排序
// onlySources 指定要搜索的来源列表（nil 表示搜索全部已注册来源）
// 任一 source 失败不阻断其他，失败的 source 状态记录在 SourceStatuses 中
// 支持分页：opts.Page 和 opts.PageSize 控制分页，返回 HasMore 和 NextPage
func (s *Store) SearchAllSkills(ctx context.Context, opts SearchOptions, onlySources []Source) (*SearchResultSkills, error) {
	s.mu.RLock()
	// 按 onlySources 过滤；若 onlySources 为 nil，搜索全部已注册来源
	var sources []Source
	if len(onlySources) > 0 {
		allowed := make(map[Source]bool, len(onlySources))
		for _, src := range onlySources {
			allowed[src] = true
		}
		for src := range s.skillFetchers {
			if allowed[src] {
				sources = append(sources, src)
			}
		}
	} else {
		for src := range s.skillFetchers {
			sources = append(sources, src)
		}
	}
	s.mu.RUnlock()

	if len(sources) == 0 {
		return &SearchResultSkills{Items: []MarketSkill{}, Total: 0, Page: 1}, nil
	}

	pageSize := opts.PageSize
	if pageSize <= 0 {
		pageSize = 30
	}
	page := opts.Page
	if page < 1 {
		page = 1
	}

	// 缓存命中时各来源状态未知，不重复上报；首次拉取（含部分失败）时已填充
	var mergedStatuses []SourceStatus
	mergedKey := s.mergedSkillsCacheKey(opts.Query, sources)
	merged, ok := s.readMergedSkillsCache(mergedKey)
	if !ok {
		// merged 全量缓存同样需要 singleflight：并发首轮请求（同一查询
		// 不同 page）各自拉取全部来源并翻页，请求量与并发数线性放大；
		// 共享同一次拉取结果后分页。
		wait, isFirst := func() (chan inflightResult, bool) {
			s.inFlightMu.Lock()
			defer s.inFlightMu.Unlock()
			if ch, exists := s.inFlight[mergedKey]; exists {
				return ch, false
			}
			ch := make(chan inflightResult, 1)
			s.inFlight[mergedKey] = ch
			return ch, true
		}()

		if !isFirst {
			select {
			case fr := <-wait:
				if fr.err != nil {
					return nil, fr.err
				}
				if fr.result == nil || len(fr.result.Items) == 0 {
					// 首者异常退出或拉取空结果：回读缓存兜底
					if cached, ok := s.readMergedSkillsCache(mergedKey); ok {
						merged = cached
					} else if fr.result != nil {
						merged = fr.result.Items
					} else {
						return nil, fmt.Errorf("market skill search: previous merged fetch did not complete")
					}
				} else {
					merged = fr.result.Items
					mergedStatuses = fr.result.SourceStatuses
				}
			case <-ctx.Done():
				return nil, ctx.Err()
			}
		} else {
			// 缓存未命中，并行拉取所有来源
			type sourceResult struct {
				source Source
				result *SearchResultSkills
				err    error
			}
			results := make([]sourceResult, len(sources))
			var wg sync.WaitGroup
			wg.Add(len(sources))

			for i, src := range sources {
				i, src := i, src
				go func() {
					defer wg.Done()
					childCtx, cancel := context.WithTimeout(ctx, 90*time.Second)
					defer cancel()
					// 全量拉取：先取第一页，若 HasMore 则循环翻页补齐，
					// 否则单页上限（如 skills.sh 的 100 条）会截断合并结果。
					// GitHub fetcher 不分页（始终返回全部且 HasMore=false），循环自然终止。
					const maxFetchPages = 50 // 上限保护，防止异常 API 返回无限 HasMore
					fetchOpts := opts
					fetchOpts.PageSize = 0
					var all []MarketSkill
					for page := 1; page <= maxFetchPages; page++ {
						fetchOpts.Page = page
						result, err := s.SearchSkills(childCtx, src, fetchOpts)
						if err != nil {
							results[i] = sourceResult{source: src, err: err}
							return
						}
						all = append(all, result.Items...)
						if !result.HasMore {
							break
						}
					}
					results[i] = sourceResult{
						source: src,
						result: &SearchResultSkills{Items: all, Total: len(all), Page: 1},
					}
				}()
			}
			wg.Wait()

			// 合并结果，同时收集各来源状态
			var allItems []MarketSkill
			statuses := make([]SourceStatus, 0, len(results))
			allOK := true
			for _, r := range results {
				if r.err != nil || r.result == nil {
					allOK = false
					// r.err 可能为 nil（goroutine 在写入 results[i] 前 panic，
					// 留下零值 sourceResult），直接 .Error() 会再触发 panic
					msg := "source fetch returned no result"
					if r.err != nil {
						msg = r.err.Error()
					}
					statuses = append(statuses, SourceStatus{Source: r.source, Status: "error", Error: msg})
					continue
				}
				status := "ok"
				if len(r.result.Items) == 0 {
					status = "empty"
				}
				statuses = append(statuses, SourceStatus{Source: r.source, Status: status, Count: len(r.result.Items)})
				allItems = append(allItems, r.result.Items...)
			}

			// 为 skills.sh 来源（无 ContentHash）的条目补全内容指纹
			// 从 GitHub CDN 拉取 SKILL.md 计算 SHA256，后续缓存后不再重复请求
			populateContentHashes(ctx, allItems)

			// 去重 + 排序
			merged = dedupSkills(allItems)
			sortSkillsByInstalls(merged)

			// 仅当全部来源成功且合并结果非空时才写全量合并缓存：
			// 1) 有来源失败仍写入 → 失败源的缺失会被固化在缓存里 24h（TTL），
			//    期间用户刷新/翻页都看不到该来源的结果且无任何提示；
			// 2) 合并结果为空（网络故障/代理 403 时 fetcher 可能"成功但空"）
			//    → 空结果缓存会让市场页 24h 空白且不再重试。
			// 两种情况都跳过缓存，下次请求重新拉取。
			if allOK && len(merged) > 0 {
				s.writeMergedSkillsCache(mergedKey, merged)
			}
			mergedStatuses = statuses
			// 先发送结果再标记完成（close 后再发送会 panic）
			wait <- inflightResult{
				result: &SearchResultSkills{Items: merged, Total: len(merged), SourceStatuses: statuses},
			}
			s.markInflightDone(mergedKey)
		}
	}

	total := len(merged)
	start := (page - 1) * pageSize
	if start > total {
		start = total
	}
	end := start + pageSize
	if end > total {
		end = total
	}
	pageItems := merged[start:end]
	// 确保 pageItems 非 nil(nil slice 会被 JSON 序列化为 null,导致前端 `...more.items` 崩溃)
	if pageItems == nil {
		pageItems = []MarketSkill{}
	}

	nextPage := ""
	if end < total {
		nextPage = fmt.Sprintf("%d", page+1)
	}

	return &SearchResultSkills{
		Items:          pageItems,
		Total:          total,
		Page:           page,
		HasMore:        end < total,
		NextPage:       nextPage,
		SourceStatuses: mergedStatuses,
	}, nil
}

// populateContentHashes 为缺少 ContentHash 的 MarketSkill 补全内容指纹。
// 从 GitHub CDN 拉取 SKILL.md 计算 SHA256，并发执行（最多 5 路并行），
// 避免串行逐个拉取造成 50+ skill 等待 50-100 秒。
// 同时补全 skills.sh 来源缺少的 Name 和 Description（从 SKILL.md frontmatter 解析）。
// 不阻断整体流程，拉取失败时保持原有值不变（降级到元数据匹配）。
func populateContentHashes(ctx context.Context, items []MarketSkill) {
	type task struct {
		idx      int
		owner    string
		repo     string
		segments []string // FullPath 各段（已 PathEscape）
		// skills.sh 条目没有权威分支信息：先猜 main（大多数仓库），
		// 404 再试 master（旧仓库）。未配置 RepoBranch 的 GitHub 条目
		// 是扫描时的边缘状态，同语义猜测。猜测全部失败时该条目保持
		// 无 ContentHash，仅降级到元数据匹配，不阻断整体流程。
		branches []string
	}
	var tasks []task
	for i, item := range items {
		if item.ContentHash != "" || item.RepoOwner == "" || item.RepoName == "" {
			continue
		}
		branches := []string{item.RepoBranch}
		if item.RepoBranch == "" {
			branches = []string{"main", "master"}
		}
		var segments []string
		if item.FullPath != "" {
			segments = strings.Split(item.FullPath, "/")
			for j, seg := range segments {
				segments[j] = url.PathEscape(seg)
			}
		}
		tasks = append(tasks, task{
			idx:      i,
			owner:    url.PathEscape(item.RepoOwner),
			repo:     url.PathEscape(item.RepoName),
			segments: segments,
			branches: branches,
		})
	}
	if len(tasks) == 0 {
		return
	}

	// 客户端在函数级复用：NewHTTPClient 每次调用都会新建完整 Transport
	// （连接池 + 代理探测），100 个任务 = 200 个全新 Transport，
	// 既浪费连接又无法复用 keep-alive。http.Client 并发安全，可共享。
	hc := NewHTTPClientNoProxy()
	proxyHc := NewHTTPClient()

	const maxConcurrency = 5
	sem := make(chan struct{}, maxConcurrency)
	var wg sync.WaitGroup
	wg.Add(len(tasks))
	for _, t := range tasks {
		t := t
		go func() {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			for _, branch := range t.branches {
				var rawURL string
				if len(t.segments) == 0 {
					rawURL = fmt.Sprintf("%s/%s/%s@%s/SKILL.md",
						githubRawBase, t.owner, t.repo, url.PathEscape(branch))
				} else {
					rawURL = fmt.Sprintf("%s/%s/%s@%s/%s/SKILL.md",
						githubRawBase, t.owner, t.repo, url.PathEscape(branch), strings.Join(t.segments, "/"))
				}
				childCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
				// CDN 直连优先（jsDelivr 在中国有节点），代理未放行时兜底直连/代理
				resp, err := hc.GetWithFallback(childCtx, rawURL, proxyHc)
				cancel()
				if err != nil {
					log.Printf("populateContentHash: fetch SKILL.md for %s: %v", rawURL, err)
					continue
				}
				if resp.StatusCode != 200 {
					// 404（分支猜测失败）静默尝试下一个分支；其他状态码
					// 也不值得刷日志——该条目保持无 ContentHash 降级即可
					drainBody(resp.Body)
					resp.Body.Close()
					continue
				}
				data, err := io.ReadAll(io.LimitReader(resp.Body, 256*1024))
				resp.Body.Close()
				if err != nil {
					continue
				}
				normalized := bytes.ReplaceAll(data, []byte{'\r', '\n'}, []byte{'\n'})
				h := sha256.Sum256(normalized)
				items[t.idx].ContentHash = hex.EncodeToString(h[:])
				// 补全 skills.sh 来源缺失的 Name 和 Description
				meta := parseSkillFrontmatter(data)
				if items[t.idx].Name == "" || items[t.idx].Name == items[t.idx].Directory {
					if meta.Name != "" {
						items[t.idx].Name = meta.Name
					}
				}
				if items[t.idx].Description == "" && meta.Description != "" {
					items[t.idx].Description = meta.Description
				}
				break
			}
		}()
	}
	wg.Wait()
}

// dedupSkills 按 owner/repo + directory 去重
// 优先保留 skills.sh 条目（Installs > 0），其次保留首次出现的条目
func dedupSkills(items []MarketSkill) []MarketSkill {
	// key = repoOwner/repoName/directory
	// 第一遍：找 skills.sh 条目（Installs > 0 且 Source == skills-sh）
	seen := make(map[string]int) // key -> index in result
	var result []MarketSkill

	for _, item := range items {
		key := item.RepoOwner + "/" + item.RepoName + "/" + item.Directory
		if idx, exists := seen[key]; exists {
			// 已存在：若当前条目是 skills.sh 且已有的是 GitHub，替换
			if item.Source == SourceSkillsSh && result[idx].Source == SourceGitHub {
				result[idx] = item
			}
			continue
		}
		seen[key] = len(result)
		result = append(result, item)
	}
	return result
}

// sortSkillsByInstalls 按 Installs 降序排序（稳定排序）
func sortSkillsByInstalls(items []MarketSkill) {
	// 使用简单的插入排序（数据量通常 < 200）
	for i := 1; i < len(items); i++ {
		for j := i; j > 0 && items[j-1].Installs < items[j].Installs; j-- {
			items[j-1], items[j] = items[j], items[j-1]
		}
	}
}

// removeCacheFile 删除缓存文件。删除必须持 cacheMu：读取路径在锁外做
// 文件 I/O，写入路径持锁写原子文件（WriteAtomic 先写临时文件再 rename）；
// 锁外删除可能与并发写入者竞态——写入者刚写好新文件就被误删，
// 或者删除发生时写入者正写临时文件、删除后 rename 复活旧数据。
func (s *Store) removeCacheFile(key string) {
	s.cacheMu.Lock()
	defer s.cacheMu.Unlock()
	_ = os.Remove(s.cachePath(key))
}

// readSkillCache 读取 skill 缓存，不持有 cacheMu 锁（文件 I/O 在锁外执行）
func (s *Store) readSkillCache(key string) (*SearchResultSkills, bool) {
	path := s.cachePath(key)
	info, err := os.Stat(path)
	if err != nil {
		return nil, false
	}
	if time.Since(info.ModTime()) > s.ttl {
		// 过期缓存顺手清理，避免残留文件积累
		// （CacheKey 含 page/cursor，长期使用文件只增不减）
		s.removeCacheFile(key)
		return nil, false
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, false
	}
	var result SearchResultSkills
	if err := json.Unmarshal(data, &result); err != nil {
		s.removeCacheFile(key)
		return nil, false
	}
	return &result, true
}

func (s *Store) readSkillCacheAnyAge(key string) (*SearchResultSkills, bool) {
	path := s.cachePath(key)
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, false
	}
	var result SearchResultSkills
	if err := json.Unmarshal(data, &result); err != nil {
		s.removeCacheFile(key)
		return nil, false
	}
	return &result, true
}

func (s *Store) writeSkillCache(key string, result *SearchResultSkills) {
	s.cacheMu.Lock()
	defer s.cacheMu.Unlock()
	s.writeCacheLocked(key, result, "skill cache")
}
