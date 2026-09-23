package skills

import (
	"os"
	"path/filepath"
	"sort"
	"strings"

	"agentpack/internal/agents"
)

type copyClass struct {
	kind      ConflictKind // "" = 无冲突
	localHash string
}

// classifyCopyEntry 判定 agent 目录中一个已存在条目与 SSOT 的关系。
// copy 模式下与 SSOT 全同的普通目录是正常投影（D5），不产冲突；
// symlink 模式下同状态是散装残留（plain_same）。
func classifyCopyEntry(method SyncMethod, ssotPath, target, ssotHash string, ssotComplete bool) copyClass {
	info, err := os.Lstat(target)
	if err != nil {
		return copyClass{}
	}
	if info.Mode()&os.ModeSymlink != 0 {
		if _, err := os.Stat(target); err != nil {
			return copyClass{kind: ConflictBrokenLink}
		}
		link, err := os.Readlink(target)
		if err != nil {
			return copyClass{kind: ConflictWrongTarget}
		}
		if !filepath.IsAbs(link) {
			link = filepath.Join(filepath.Dir(target), link)
		}
		a, aerr := filepath.Abs(link)
		b, berr := filepath.Abs(ssotPath)
		if aerr != nil || berr != nil || filepath.Clean(a) != filepath.Clean(b) {
			return copyClass{kind: ConflictWrongTarget}
		}
		return copyClass{}
	}
	localHash, complete := HashDir(target)
	if ssotComplete && complete && ssotHash != "" && localHash == ssotHash {
		if method == SyncMethodCopy {
			return copyClass{}
		}
		return copyClass{kind: ConflictPlainSame, localHash: localHash}
	}
	return copyClass{kind: ConflictPlainDiff, localHash: localHash}
}

// ScanConflicts 对账：分类 (SSOT 技能 × agent 目录) 中已存在的条目，
// 外加一轮原始目录扫描找孤儿死链。只读，不修改任何文件。
// 结果按 (kind, directory, path) 排序，多次调用顺序稳定。
func (s *Store) ScanConflicts(reg *agents.Registry) []ReconcileItem {
	s.mu.RLock()
	skills := make([]Skill, 0, len(s.skills))
	for _, sk := range s.skills {
		skills = append(skills, sk)
	}
	ssotDir := s.ssotDir
	method := s.syncMethod
	s.mu.RUnlock()

	acks := ReadConflictAcks(ssotDir)
	ssotDirs := make(map[string]bool, len(skills))
	for _, sk := range skills {
		ssotDirs[sk.Directory] = true
	}
	ssotHash := make(map[string]string, len(skills))
	ssotOK := make(map[string]bool, len(skills))
	for _, sk := range skills {
		h, ok := HashDir(filepath.Join(ssotDir, sk.Directory))
		ssotHash[sk.ID], ssotOK[sk.ID] = h, ok
	}

	var items []ReconcileItem
	for _, sd := range scanAgentSkillDirs(reg.SkillCapableAgentIDs(), reg.AgentSkillsDir) {
		for _, sk := range skills {
			target := filepath.Join(sd.dir, sk.Directory)
			cl := classifyCopyEntry(method, filepath.Join(ssotDir, sk.Directory), target, ssotHash[sk.ID], ssotOK[sk.ID])
			if cl.kind == "" {
				continue
			}
			items = append(items, ReconcileItem{
				Kind:         cl.kind,
				SkillID:      sk.ID,
				Directory:    sk.Directory,
				Path:         target,
				AgentIDs:     append([]string(nil), sd.agentIDs...),
				SSOTHash:     ssotHash[sk.ID],
				LocalHash:    cl.localHash,
				Acknowledged: acks[conflictKey(sk.Directory, target)].Matches(ssotHash[sk.ID], cl.localHash),
			})
		}
		// 孤儿死链：scanSkillEntries 会跳过死链，必须原始 ReadDir
		entries, err := os.ReadDir(sd.dir)
		if err != nil {
			continue
		}
		for _, entry := range entries {
			name := entry.Name()
			if strings.HasPrefix(name, ".") || ssotDirs[name] {
				continue
			}
			if entry.Type()&os.ModeSymlink == 0 {
				continue
			}
			path := filepath.Join(sd.dir, name)
			if _, err := os.Stat(path); err == nil {
				continue
			}
			items = append(items, ReconcileItem{
				Kind:      ConflictOrphanLink,
				Directory: name,
				Path:      path,
				AgentIDs:  append([]string(nil), sd.agentIDs...),
			})
		}
	}

	sort.Slice(items, func(i, j int) bool {
		if items[i].Kind != items[j].Kind {
			return items[i].Kind < items[j].Kind
		}
		if items[i].Directory != items[j].Directory {
			return items[i].Directory < items[j].Directory
		}
		return items[i].Path < items[j].Path
	})
	return items
}
