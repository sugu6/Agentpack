package market

import (
	"context"
	"log"
	"strings"
	"sync"
	"time"
)

// BackfillMatch 是 skills.sh 搜索出的最优匹配（下载量最大）。
type BackfillMatch struct {
	Owner    string
	Repo     string
	Installs int64
}

// BackfillSkillSources 为每个目录名查询 skills.sh，返回 skillId/name 精确匹配、
// 下载量（installs）最大的条目。无精确匹配或全部查询失败时不返回该目录。
// 单个查询失败不阻断其他目录。
func BackfillSkillSources(ctx context.Context, directories []string) (map[string]BackfillMatch, error) {
	out := make(map[string]BackfillMatch)
	if len(directories) == 0 {
		return out, nil
	}
	fetcher := NewSkillsShFetcher()
	sem := make(chan struct{}, 5)
	var wg sync.WaitGroup
	var mu sync.Mutex
	for _, dir := range directories {
		dir := dir
		wg.Add(1)
		go func() {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			qctx, cancel := context.WithTimeout(ctx, 15*time.Second)
			defer cancel()
			res, err := fetcher.Search(qctx, SearchOptions{Query: dir, PageSize: 20})
			if err != nil {
				log.Printf("skills.sh backfill search for %q: %v", dir, err)
				return
			}
			var best *MarketSkill
			for i := range res.Items {
				item := &res.Items[i]
				if !strings.EqualFold(item.Directory, dir) && !strings.EqualFold(item.Name, dir) {
					continue
				}
				if best == nil || item.Installs > best.Installs {
					best = item
				}
			}
			if best == nil || best.RepoOwner == "" || best.RepoName == "" {
				return
			}
			mu.Lock()
			out[dir] = BackfillMatch{Owner: best.RepoOwner, Repo: best.RepoName, Installs: best.Installs}
			mu.Unlock()
		}()
	}
	wg.Wait()
	return out, nil
}
