// Package skillbackfill 提供技能来源自动回填的纯逻辑：从 skills.sh 候选
// 并发验证并写入 lock，结果统计。不依赖 App 状态，可独立测试。
// 编排（锁/事件/内存状态）由根目录 app.go 负责。
package skillbackfill

import (
	"context"
	"log"
	"path/filepath"
	"sync"

	"agentpack/internal/market"
	"agentpack/internal/skills"
)

// Result 是从 skills.sh 回填技能来源的结果统计。
type Result struct {
	Matched    []string `json:"matched"`
	Mismatched []string `json:"mismatched"`
	Unmatched  []string `json:"unmatched"`
	Failed     []string `json:"failed"`
}

// Verifier 验证候选列表：按序验证（下载量降序），返回首个内容一致的
// 匹配（含 fullPath 与实际匹配分支）。ok=false 且 networkErr=true 表示候选
// 全部因网络失败未验证；ok=false 且 networkErr=false 表示候选都验证过但内容不一致。
type Verifier func(dir string, candidates []market.BackfillCandidate) (match market.BackfillCandidate, fullPath, branch string, ok bool, networkErr bool)

// ApplyWithVerification 并发验证匹配项并把通过验证的写入 lock。
// 拆分为独立函数便于单元测试（网络查询/验证由调用方注入）。
// stillExists 在写 lock 前校验技能仍被 store 纳管：回填验证期间（后台最多
// 120s 窗口）用户可能已卸载该技能，跳过写回避免锁文件残留已删除技能的来源。
// applyMemory 在写 lock 后同步内存来源（storeOpMu 下用当前 store 重新校验）：
// 返回 false 时回滚 lock 条目并静默跳过，防止"写 lock 后、落内存前"卸载
// 的窗口残留条目，同时保证 CheckUpdates/UpdateSkill 入口立即可用。
func ApplyWithVerification(matches map[string][]market.BackfillCandidate, directories []string, verify Verifier, stillExists func(dir string) bool, applyMemory func(dir string, entry skills.AgentsLockEntry) bool, ssotDirs ...string) Result {
	type verified struct {
		dir        string
		match      market.BackfillCandidate
		fullPath   string
		branch     string
		ok         bool
		networkErr bool
	}
	verifiedMap := make(map[string]verified, len(matches))
	var mu sync.Mutex
	sem := make(chan struct{}, 5)
	var wg sync.WaitGroup
	for dir, cands := range matches {
		wg.Add(1)
		go func() {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			m, fullPath, branch, ok, networkErr := verify(dir, cands)
			mu.Lock()
			verifiedMap[dir] = verified{dir: dir, match: m, fullPath: fullPath, branch: branch, ok: ok, networkErr: networkErr}
			mu.Unlock()
		}()
	}
	wg.Wait()

	var res Result
	for _, dir := range directories {
		cands, ok := matches[dir]
		if !ok || len(cands) == 0 {
			res.Unmatched = append(res.Unmatched, dir)
			continue
		}
		v := verifiedMap[dir]
		if v.networkErr && !v.ok {
			res.Failed = append(res.Failed, dir)
			continue
		}
		if !v.ok {
			res.Mismatched = append(res.Mismatched, dir)
			continue
		}
		if stillExists != nil && !stillExists(dir) {
			// 技能已被卸载：静默跳过（不写入锁文件，也不计入失败统计）
			log.Printf("backfill: skip %s: skill was uninstalled during verification", dir)
			continue
		}
		entry := skills.AgentsLockEntry{
			Directory:  dir,
			Source:     v.match.Owner + "/" + v.match.Repo,
			SourceType: "github",
			SourceURL:  "https://github.com/" + v.match.Owner + "/" + v.match.Repo,
			Branch:     v.branch,
			FullPath:   v.fullPath,
		}
		if len(ssotDirs) > 0 && ssotDirs[0] != "" {
			entry.SkillPath = filepath.Join(ssotDirs[0], dir)
		}
		if err := skills.WriteAgentsLock(entry); err != nil {
			log.Printf("backfill source for %s: %v", dir, err)
			res.Failed = append(res.Failed, dir)
			continue
		}
		if applyMemory != nil && !applyMemory(dir, entry) {
			// 技能在"写 lock 后、落内存前"被卸载（或应用关闭）：回滚 lock
			// 条目并静默跳过，不把已卸载技能的来源留在锁文件。
			log.Printf("backfill: %s: skill gone or app closing after lock write, rolling back", dir)
			if rerr := skills.RemoveAgentsLockEntry(dir); rerr != nil {
				log.Printf("backfill: rollback lock entry for %s: %v", dir, rerr)
			}
			continue
		}
		res.Matched = append(res.Matched, dir)
	}
	return res
}

// VerifyCandidate 验证单个回填候选：先按 main 分支，查询失败时
// 追加一次 master 尝试（master 默认分支的仓库），与安装/更新侧的分支
// 兜底行为保持一致，避免 master-only 仓库的来源回填永久无果。
// 返回匹配成功时实际使用的分支（写入 lock，后续更新检测按此分支进行）。
func VerifyCandidate(ctx context.Context, ssotDir, dir string, c market.BackfillCandidate) (fullPath, branch string, ok bool, err error) {
	localDir := filepath.Join(ssotDir, dir)
	fp, ok, err := skills.VerifySkillSource(ctx, dir, c.Owner, c.Repo, "main", localDir)
	if err == nil {
		return fp, "main", ok, nil
	}
	fp2, ok2, err2 := skills.VerifySkillSource(ctx, dir, c.Owner, c.Repo, "master", localDir)
	if err2 != nil {
		return fp, "", ok, err
	}
	return fp2, "master", ok2, nil
}
