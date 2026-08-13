package market

import (
	"context"
	"log"
	"path/filepath"
	"sort"
	"sync"
	"time"
)

// BackfillCandidate 是 skills.sh 搜索结果中的一个候选仓库。
// 最终是否写入由内容验证决定（App 层按下载量降序验证）。
type BackfillCandidate struct {
	Owner    string
	Repo     string
	Installs int64
}

// maxBackfillCandidates 是每个目录返回的最大候选数（防止内容验证开销过大）。
const maxBackfillCandidates = 10

// AcceptBackfillMatch 判断验证通过的远端定位（fullPath）是否可写入锁文件。
// 仅接受远端目录名与本地目录名一致的匹配：内容回退匹配（目录名不同）会把
// 本地目录名关联到仓库，市场页上同仓库同名但内容不同的条目会被误判为「已安装」；
// 目录名一致且内容已验证的匹配不存在误报路径。
// fullPath 为空（仓库根级技能）也不接受——市场条目没有对应的可匹配目录。
func AcceptBackfillMatch(dir, fullPath string) bool {
	return fullPath != "" && filepath.Base(fullPath) == dir
}

// BackfillSkillSources 为每个目录名查询 skills.sh，返回候选仓库列表：
// 仅 GitHub 来源（域名已过滤），按下载量（installs）降序。
// 名字不再要求精确匹配——由调用方按内容验证决定最终写入。
// 单个查询失败不阻断其他目录。
func BackfillSkillSources(ctx context.Context, directories []string) (map[string][]BackfillCandidate, error) {
	out := make(map[string][]BackfillCandidate)
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
			var cands []BackfillCandidate
			for i := range res.Items {
				item := &res.Items[i]
				if item.RepoOwner == "" || item.RepoName == "" {
					continue
				}
				cands = append(cands, BackfillCandidate{Owner: item.RepoOwner, Repo: item.RepoName, Installs: item.Installs})
			}
			if len(cands) == 0 {
				return
			}
			sort.Slice(cands, func(i, j int) bool { return cands[i].Installs > cands[j].Installs })
			if len(cands) > maxBackfillCandidates {
				cands = cands[:maxBackfillCandidates]
			}
			mu.Lock()
			out[dir] = cands
			mu.Unlock()
		}()
	}
	wg.Wait()
	return out, nil
}
