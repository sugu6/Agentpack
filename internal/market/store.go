package market

import (
	"agentpack/internal/iowriter"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

type Store struct {
	mu            sync.RWMutex
	cacheMu       sync.Mutex
	cacheDir      string
	ttl           time.Duration
	servers       map[Source]ServerFetcher
	skillFetchers map[Source]SkillFetcher
	inFlight      map[string]chan struct{} // 防止并发 fetch 相同 key
	inFlightMu    sync.Mutex
}

// cacheVersion 是缓存键的版本号。
// 当 fetcher 的数据结构或扫描逻辑发生破坏性变更时（如新增字段、修复路径拼接），
// 递增此版本号可使旧缓存文件失效，强制重新拉取最新数据。
const cacheVersion = 4

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
		inFlight:      make(map[string]chan struct{}),
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

func (s *Store) Search(ctx context.Context, source Source, opts SearchOptions) (*SearchResultServers, error) {
	s.mu.RLock()
	f, ok := s.servers[source]
	s.mu.RUnlock()
	if !ok {
		return nil, fmt.Errorf("source not registered: %s", source)
	}

	cacheKey := s.cacheKey("search", source, opts)

	// 先检查缓存（文件 I/O 不持有 cacheMu 锁）
	if cached, ok := s.readCache(cacheKey); ok {
		return cached, nil
	}

	// 检查是否有其他 goroutine 正在 fetch 相同 key
	wait, isFirst := func() (chan struct{}, bool) {
		s.inFlightMu.Lock()
		defer s.inFlightMu.Unlock()
		// 二次检查：readCache 无锁返回与 inFlightMu 获取之间，可能有其他 fetcher 已完成
		if ch, exists := s.inFlight[cacheKey]; exists {
			return ch, false
		}
		ch := make(chan struct{})
		s.inFlight[cacheKey] = ch
		return ch, true
	}()

	if !isFirst {
		// 等待首个 fetch 完成，使用 context 确保不会永久阻塞
		select {
		case <-wait:
			// 读取首个 fetch 写入的缓存
			cached, ok := s.readCache(cacheKey)
			if ok {
				return cached, nil
			}
			// 缓存为空（首个 fetch 失败），返回错误而非触发惊群重试
			return nil, fmt.Errorf("market search: previous fetch failed for %s", cacheKey)
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}

	// 到此 isFirst 必为 true（上面分支均已 return）
	defer s.markInflightDone(cacheKey)
	result, err := f.Search(ctx, opts)
	if err != nil {
		if cached, ok := s.readCacheAnyAge(cacheKey); ok {
			return cached, nil
		}
		return nil, err
	}
	// 即使 context 已超时，如果 fetcher 已成功返回结果，仍然写入缓存并返回
	// 避免因 context 超时丢弃已成功获取的数据（如 GitHub 仓库扫描耗时较长但已返回结果）
	s.writeCache(cacheKey, result)
	return result, nil
}

// markInflightDone 标记 in-flight fetch 完成，通知等待者
func (s *Store) markInflightDone(key string) {
	s.inFlightMu.Lock()
	ch, ok := s.inFlight[key]
	if ok {
		delete(s.inFlight, key)
		close(ch)
	}
	s.inFlightMu.Unlock()
}

// readCache 读取 server 缓存，不持有 cacheMu 锁（文件 I/O 在锁外执行）
func (s *Store) readCache(key string) (*SearchResultServers, bool) {
	path := s.cachePath(key)
	info, err := os.Stat(path)
	if err != nil {
		return nil, false
	}
	if time.Since(info.ModTime()) > s.ttl {
		return nil, false
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, false
	}
	var result SearchResultServers
	if err := json.Unmarshal(data, &result); err != nil {
		_ = os.Remove(path)
		return nil, false
	}
	return &result, true
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

func (s *Store) cacheKey(kind string, source Source, opts SearchOptions) string {
	// cacheVersion 用于在数据结构或扫描逻辑变更后使旧缓存失效
	// 递增此版本号会强制所有用户重新拉取最新数据
	payload := fmt.Sprintf("v%d|%s|%s|%s|%s|%d|%d", cacheVersion, kind, source, opts.Query, opts.Cursor, opts.Page, opts.PageSize)
	sum := sha256.Sum256([]byte(payload))
	return hex.EncodeToString(sum[:16]) + ".json"
}

func (s *Store) cachePath(key string) string {
	return filepath.Join(s.cacheDir, key)
}

func (s *Store) readCacheAnyAge(key string) (*SearchResultServers, bool) {
	path := s.cachePath(key)
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, false
	}
	var result SearchResultServers
	if err := json.Unmarshal(data, &result); err != nil {
		_ = os.Remove(path)
		return nil, false
	}
	return &result, true
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

func (s *Store) writeCache(key string, result *SearchResultServers) {
	s.cacheMu.Lock()
	defer s.cacheMu.Unlock()
	s.writeCacheLocked(key, result, "cache")
}

func (s *Store) CleanCache() (int, error) {
	s.cacheMu.Lock()
	entries, err := os.ReadDir(s.cacheDir)
	s.cacheMu.Unlock()
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
func (s *Store) SearchSkills(ctx context.Context, source Source, opts SearchOptions) (*SearchResultSkills, error) {
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
	wait, isFirst := func() (chan struct{}, bool) {
		s.inFlightMu.Lock()
		defer s.inFlightMu.Unlock()
		if ch, exists := s.inFlight[cacheKey]; exists {
			return ch, false
		}
		ch := make(chan struct{})
		s.inFlight[cacheKey] = ch
		return ch, true
	}()

	if !isFirst {
		select {
		case <-wait:
			cached, ok := s.readSkillCache(cacheKey)
			if ok {
				return cached, nil
			}
			return nil, fmt.Errorf("market skill search: previous fetch failed for %s", cacheKey)
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}

	defer s.markInflightDone(cacheKey)

	result, err := f.Search(ctx, opts)
	if err != nil {
		if cached, ok := s.readSkillCacheAnyAge(cacheKey); ok {
			return cached, nil
		}
		return nil, err
	}
	// 即使 context 已超时，如果 fetcher 已成功返回结果，仍然写入缓存并返回
	// 避免因 context 超时丢弃已成功获取的数据（如 GitHub 仓库扫描耗时较长但已返回结果）
	s.writeSkillCache(cacheKey, result)
	return result, nil
}

// mergedSkillsCacheKey 生成全量合并结果的缓存键
// 包含 query 和来源列表，不包含 page/pageSize，确保不同页共享同一缓存
func (s *Store) mergedSkillsCacheKey(query string, sources []Source) string {
	sourceNames := make([]string, len(sources))
	for i, src := range sources {
		sourceNames[i] = string(src)
	}
	sort.Strings(sourceNames)
	payload := fmt.Sprintf("v%d|skills-merged|%s|%s", cacheVersion, strings.Join(sourceNames, ","), query)
	sum := sha256.Sum256([]byte(payload))
	return hex.EncodeToString(sum[:16]) + ".json"
}

func (s *Store) readMergedSkillsCache(key string) ([]MarketSkill, bool) {
	path := s.cachePath(key)
	info, err := os.Stat(path)
	if err != nil {
		return nil, false
	}
	if time.Since(info.ModTime()) > s.ttl {
		os.Remove(path)
		return nil, false
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, false
	}
	var items []MarketSkill
	if err := json.Unmarshal(data, &items); err != nil {
		_ = os.Remove(path)
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

	// 尝试从全量合并缓存读取
	mergedKey := s.mergedSkillsCacheKey(opts.Query, sources)
	merged, ok := s.readMergedSkillsCache(mergedKey)
	if !ok {
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
				// 全量拉取，传 pageSize=0 让 fetcher 返回所有结果
				fetchOpts := opts
				fetchOpts.PageSize = 0
				result, err := s.SearchSkills(childCtx, src, fetchOpts)
				results[i] = sourceResult{source: src, result: result, err: err}
			}()
		}
		wg.Wait()

		// 合并结果（SourceStatuses 暂未使用，保留 nil 以维持现有行为）
		var allItems []MarketSkill
		for _, r := range results {
			if r.err != nil || r.result == nil {
				continue
			}
			allItems = append(allItems, r.result.Items...)
		}

		// 去重 + 排序
		merged = dedupSkills(allItems)
		sortSkillsByInstalls(merged)

		// 缓存全量合并结果
		s.writeMergedSkillsCache(mergedKey, merged)
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
		SourceStatuses: nil, // 首次加载时已记录，后续分页复用缓存
	}, nil
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

// readSkillCache 读取 skill 缓存，不持有 cacheMu 锁（文件 I/O 在锁外执行）
func (s *Store) readSkillCache(key string) (*SearchResultSkills, bool) {
	path := s.cachePath(key)
	info, err := os.Stat(path)
	if err != nil {
		return nil, false
	}
	if time.Since(info.ModTime()) > s.ttl {
		return nil, false
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, false
	}
	var result SearchResultSkills
	if err := json.Unmarshal(data, &result); err != nil {
		_ = os.Remove(path)
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
		_ = os.Remove(path)
		return nil, false
	}
	return &result, true
}

func (s *Store) writeSkillCache(key string, result *SearchResultSkills) {
	s.cacheMu.Lock()
	defer s.cacheMu.Unlock()
	s.writeCacheLocked(key, result, "skill cache")
}
