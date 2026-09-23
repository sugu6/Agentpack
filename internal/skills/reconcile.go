package skills

import (
	"fmt"
	"log"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"agentpack/internal/agents"
	"agentpack/internal/shared"
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

// lookupSkillPaths 取技能的 SSOT 路径与同步方式（读锁快照）。
func (s *Store) lookupSkillPaths(skillID string) (Skill, string, SyncMethod, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	sk, ok := s.skills[skillID]
	if !ok {
		return Skill{}, "", "", fmt.Errorf("skill %s not found", skillID)
	}
	return sk, filepath.Join(s.ssotDir, sk.Directory), s.syncMethod, nil
}

// resolveAgentSkillPath 校验 sourcePath 必须等于 dirName 在某个
// skill-capable agent 目录下的条目；返回与 scan 相同构造法的规范路径。
func resolveAgentSkillPath(sourcePath, dirName string, reg *agents.Registry) (string, error) {
	abs, err := filepath.Abs(sourcePath)
	if err != nil {
		return "", err
	}
	abs = filepath.Clean(abs)
	for _, id := range reg.SkillCapableAgentIDs() {
		d := reg.AgentSkillsDir(id)
		if d == "" {
			continue
		}
		candidate := filepath.Join(d, dirName)
		cAbs, err := filepath.Abs(candidate)
		if err != nil {
			continue
		}
		if abs == filepath.Clean(cAbs) {
			return candidate, nil
		}
	}
	return "", fmt.Errorf("path %q is not entry %q of any agent skills directory", sourcePath, dirName)
}

// ConvertSkillCopyToLink 将 agent 目录中的条目转换为指向 SSOT 的正确投影。
// 字节守卫：普通目录必须与 SSOT 全树哈希完全一致（两侧均完整），否则拒绝——
// 内容分叉必须先经 AdoptSkillCopy / OverwriteSkillCopyFromSSOT 显式处置。
func (s *Store) ConvertSkillCopyToLink(skillID, sourcePath string, reg *agents.Registry) error {
	s.importMu.Lock()
	defer s.importMu.Unlock()

	sk, ssotPath, method, err := s.lookupSkillPaths(skillID)
	if err != nil {
		return err
	}
	target, err := resolveAgentSkillPath(sourcePath, sk.Directory, reg)
	if err != nil {
		return err
	}
	info, lerr := os.Lstat(target)
	if lerr != nil {
		return fmt.Errorf("entry %q not found: %w", target, lerr)
	}
	if info.Mode()&os.ModeSymlink == 0 {
		ssotHash, ssotOK := HashDir(ssotPath)
		localHash, localOK := HashDir(target)
		if !ssotOK || !localOK || ssotHash == "" || localHash != ssotHash {
			return fmt.Errorf("local copy of %q differs from SSOT; choose adopt or overwrite first", sk.Directory)
		}
	}
	if err := RemovePath(target); err != nil {
		return fmt.Errorf("remove entry: %w", err)
	}
	if err := SyncToAgentDir(ssotPath, target, method); err != nil {
		return fmt.Errorf("create projection: %w", err)
	}
	return nil
}

// KeepSkillFork 记录一次内容分叉的"保留"决定（指纹级 ack，
// 任一侧后续再漂移会重新出现在对账面板）。
func (s *Store) KeepSkillFork(skillID, sourcePath string, reg *agents.Registry) error {
	s.importMu.Lock()
	defer s.importMu.Unlock()

	sk, ssotPath, _, err := s.lookupSkillPaths(skillID)
	if err != nil {
		return err
	}
	target, err := resolveAgentSkillPath(sourcePath, sk.Directory, reg)
	if err != nil {
		return err
	}
	info, lerr := os.Lstat(target)
	if lerr != nil {
		return fmt.Errorf("entry %q not found: %w", target, lerr)
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("entry %q is a link, nothing to keep", target)
	}
	ssotHash, ssotOK := HashDir(ssotPath)
	localHash, localOK := HashDir(target)
	if ssotOK && localOK && localHash == ssotHash {
		return fmt.Errorf("contents of %q are identical to SSOT; nothing to keep", sk.Directory)
	}
	ack := ConflictAck{
		SSOTHash:  ssotHash,
		LocalHash: localHash,
		CheckedAt: shared.NowRFC3339(),
	}
	return WriteConflictAck(s.ssotDir, conflictKey(sk.Directory, target), ack)
}

// OverwriteSkillCopyFromSSOT 用 SSOT 内容覆盖 agent 目录中的条目并重建投影。
// 分叉三选中的「SSOT 覆盖」显式授权路径：本地分叉内容会被删除。
func (s *Store) OverwriteSkillCopyFromSSOT(skillID, sourcePath string, reg *agents.Registry) error {
	s.importMu.Lock()
	defer s.importMu.Unlock()

	sk, ssotPath, method, err := s.lookupSkillPaths(skillID)
	if err != nil {
		return err
	}
	target, err := resolveAgentSkillPath(sourcePath, sk.Directory, reg)
	if err != nil {
		return err
	}
	if _, err := os.Lstat(target); err != nil {
		return fmt.Errorf("entry %q not found: %w", target, err)
	}
	_ = DeleteConflictAck(s.ssotDir, conflictKey(sk.Directory, target))
	if err := RemovePath(target); err != nil {
		return fmt.Errorf("remove entry: %w", err)
	}
	if err := SyncToAgentDir(ssotPath, target, method); err != nil {
		return fmt.Errorf("create projection: %w", err)
	}
	return nil
}

// AdoptSkillCopy 用 agent 目录中的散装副本内容覆盖 SSOT（分叉三选之「本地收编」）。
// 覆盖前备份原 SSOT 到 skill-backups；备份失败即中止，绝不丢数据。
func (s *Store) AdoptSkillCopy(skillID, sourcePath string, reg *agents.Registry) (Skill, error) {
	s.importMu.Lock()
	defer s.importMu.Unlock()

	sk, ssotPath, method, err := s.lookupSkillPaths(skillID)
	if err != nil {
		return Skill{}, err
	}
	target, err := resolveAgentSkillPath(sourcePath, sk.Directory, reg)
	if err != nil {
		return Skill{}, err
	}
	info, lerr := os.Lstat(target)
	if lerr != nil {
		return Skill{}, fmt.Errorf("entry %q not found: %w", target, lerr)
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return Skill{}, fmt.Errorf("entry %q is a link, nothing to adopt", target)
	}
	if !HasSkillManifest(target) {
		return Skill{}, fmt.Errorf("entry %q has no SKILL.md", target)
	}
	ssotHash, ssotOK := HashDir(ssotPath)
	localHash, localOK := HashDir(target)
	if ssotOK && localOK && localHash == ssotHash {
		return Skill{}, fmt.Errorf("contents of %q are identical to SSOT; use convert instead", sk.Directory)
	}
	backupDir := filepath.Join(filepath.Dir(s.ssotDir), "skill-backups")
	backupPath, backupErr := BackupSkillDir(s.ssotDir, backupDir, sk.Directory)
	if backupErr != nil {
		return Skill{}, fmt.Errorf("backup SSOT before adopt (aborted): %w", backupErr)
	}
	if err := RemovePath(ssotPath); err != nil {
		return Skill{}, fmt.Errorf("remove SSOT (backup at %s): %w", backupPath, err)
	}
	if err := copyDirRecursive(target, ssotPath); err != nil {
		if backupPath != "" {
			if berr := copyDirRecursive(backupPath, ssotPath); berr != nil {
				log.Printf("adopt: restore from backup %s failed: %v", backupPath, berr)
			}
		}
		return Skill{}, fmt.Errorf("copy local into SSOT: %w", err)
	}
	_ = DeleteConflictAck(s.ssotDir, conflictKey(sk.Directory, target))
	refreshed, err := s.refreshSkillAfterFileUpdate(skillID, ssotPath)
	if err != nil {
		return Skill{}, err
	}
	if err := SyncToAgentDir(ssotPath, target, method); err != nil {
		return refreshed, fmt.Errorf("SSOT adopted, but projection refresh failed: %w", err)
	}
	return refreshed, nil
}

// CleanOrphanSkillLinks 删除「目标已死且 SSOT 无同名技能」的孤儿死链。
// 只删死链：活链、普通目录、SSOT 同名条目一律不动。
// 返回实际删除的路径（按路径排序）；部分失败时已删清单照常返回。
func (s *Store) CleanOrphanSkillLinks(reg *agents.Registry) ([]string, error) {
	s.mu.RLock()
	ssotDirs := make(map[string]bool, len(s.skills))
	for _, sk := range s.skills {
		ssotDirs[sk.Directory] = true
	}
	s.mu.RUnlock()

	var removed []string
	var errs []string
	for _, sd := range scanAgentSkillDirs(reg.SkillCapableAgentIDs(), reg.AgentSkillsDir) {
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
			if err := RemovePath(path); err != nil {
				errs = append(errs, fmt.Sprintf("%s: %v", path, err))
				continue
			}
			removed = append(removed, path)
		}
	}
	sort.Strings(removed)
	if len(errs) > 0 {
		return removed, fmt.Errorf("clean orphan links: %s", strings.Join(errs, "; "))
	}
	return removed, nil
}
