package skills

import (
	"agentpack/internal/agents"
	"agentpack/internal/logger"
	"agentpack/internal/shared"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
)

// Store manages skills using the filesystem as the single source of truth.
// No database persistence — state is inferred from SSOT and agent directories.
type Store struct {
	mu         sync.RWMutex
	importMu   sync.Mutex // 序列化 Import 的文件 I/O 阶段，避免并发导入同名技能浪费磁盘操作
	ssotDir    string
	syncMethod SyncMethod
	skills     map[string]Skill
	bindings   map[string]map[string]bool
}

func NewStore(ssotDir string, syncMethod SyncMethod) *Store {
	return &Store{
		ssotDir:    ssotDir,
		syncMethod: syncMethod,
		skills:     make(map[string]Skill),
		bindings:   make(map[string]map[string]bool),
	}
}

func (s *Store) SetSyncMethod(method SyncMethod) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.syncMethod = method
}

func (s *Store) SetSSOTDir(dir string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.ssotDir = dir
}

func (s *Store) SSOTDir() string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.ssotDir
}

// Load scans the SSOT directory and agent directories to rebuild in-memory state.
func (s *Store) Load(reg *agents.Registry) error {
	// 持有 importMu 以防止与 Import 并发执行。
	// Import 的文件 I/O 阶段（复制到 SSOT）在 mu 锁外执行，
	// 若 Load 同时扫描，可能读到半写入状态的技能目录。
	// importMu 在 mu 之前获取（与 Import 中的顺序一致），不会造成锁顺序反转。
	s.importMu.Lock()
	defer s.importMu.Unlock()

	ssotDir := s.SSOTDir()
	if ssotDir != "" {
		if err := os.MkdirAll(ssotDir, 0755); err != nil {
			return fmt.Errorf("create ssot dir: %w", err)
		}
	}

	skills, bindings, err := s.scanFilesystem(reg)
	if err != nil {
		return fmt.Errorf("scan filesystem: %w", err)
	}

	s.mu.Lock()
	s.skills = skills
	s.bindings = bindings
	s.mu.Unlock()

	// 为所有 SSOT 中缺少锁文件记录的 skill 写入存根记录，
	// 确保锁文件存在且可后续通过市场安装更新为正确的 repo 信息。
	if ssotDir != "" {
		if err := WriteDefaultLockEntries(ssotDir); err != nil {
			log.Printf("warn: write default lock entries: %v", err)
		}
	}

	return nil
}

// scanFilesystem discovers skills from the SSOT directory and infers bindings
// by checking which agent skill directories contain the skill.
func (s *Store) scanFilesystem(reg *agents.Registry) (map[string]Skill, map[string]map[string]bool, error) {
	ssotDir := s.SSOTDir()

	skillsMap := make(map[string]Skill)
	bindings := make(map[string]map[string]bool)

	entries, err := os.ReadDir(ssotDir)
	if err != nil {
		if os.IsNotExist(err) {
			return skillsMap, bindings, nil
		}
		return nil, nil, err
	}

	// 构建去重后的 skillsDir → agentIDs 映射。
	// CLI 与 Desktop 适配器可能共享同一个 SkillsDir（例如 claude-code 和
	// claude-code-desktop 都指向 ~/.claude/skills），若不按目录去重，同一个
	// 物理位置的技能会被推断为绑定到两个 agent，导致前端"重复显示"。
	// 对于共享目录的多个 agent，保留全部 agent ID，但每个物理目录只检查一次。
	type dirEntry struct {
		dir      string
		agentIDs []string
	}
	seenDirs := make(map[string]*dirEntry)
	var uniqueDirs []*dirEntry
	for _, agID := range reg.SkillCapableAgentIDs() {
		agentDir := reg.AgentSkillsDir(agID)
		if agentDir == "" {
			continue
		}
		abs, err := filepath.Abs(agentDir)
		if err != nil {
			abs = agentDir
		}
		if existing, ok := seenDirs[abs]; ok {
			existing.agentIDs = append(existing.agentIDs, agID)
		} else {
			de := &dirEntry{dir: agentDir, agentIDs: []string{agID}}
			seenDirs[abs] = de
			uniqueDirs = append(uniqueDirs, de)
		}
	}

	// 锁文件只解析一次：循环内逐 skill 调用 ParseAgentsLock 会对同一文件
	// 重复读取+JSON 解析（N 个 skill = N 次解析），与 List() 的做法对齐。
	lockData := ParseAgentsLock()

	for _, entry := range entries {
		dirName := entry.Name()
		if strings.HasPrefix(dirName, ".") {
			continue
		}
		skillPath := filepath.Join(ssotDir, dirName)
		// 对于非目录条目（可能是 symlink / Windows Junction），
		// 用 os.Stat 跟踪链接确认目标是否为目录
		if !entry.IsDir() {
			stat, serr := os.Stat(skillPath)
			if serr != nil || !stat.IsDir() {
				continue
			}
		}
		if !HasSkillManifest(skillPath) {
			continue
		}

		meta, err := ReadSkillMetadata(skillPath)
		if err != nil {
			log.Printf("skills load: skip %s, read SKILL.md failed: %v", dirName, err)
			continue
		}

		name := meta.Name
		if name == "" {
			name = dirName
		}

		skillID := "skill:" + dirName
		now := shared.NowRFC3339()

		contentHash, complete := HashDir(skillPath)
		if !complete {
			log.Printf("warning: skill content hash may be incomplete for %s", skillPath)
		}
		skillMdHash, _ := HashSkillMarkdown(skillPath)
		// 从 ~/.agents/.skill-lock.json 读取仓库来源信息（lockData 已在循环外解析一次）
		lockSkill := lockData[dirName]
		logger.Debug("scanFilesystem: skill dir", "dir", dirName, "owner", lockSkill.Owner, "repo", lockSkill.Repo, "branch", lockSkill.Branch)
		sk := Skill{
			ID:          skillID,
			Name:        name,
			Description: meta.Description,
			Directory:   dirName,
			ContentHash: contentHash,
			SkillMdHash: skillMdHash,
			BoundAgents: []string{},
			InstalledAt: now,
			UpdatedAt:   now,
			RepoOwner:   lockSkill.Owner,
			RepoName:    lockSkill.Repo,
			RepoBranch:  lockSkill.Branch,
			FullPath:    lockSkill.FullPath,
		}

		// Infer bindings: check each unique skills directory.
		// 共享同一目录的多个 agent（如 CLI + Desktop）只需检查一次物理路径，
		// 但会将所有共享该目录的 agent ID 都记录为绑定。
		boundAgents := []string{}
		for _, de := range uniqueDirs {
			target := filepath.Join(de.dir, dirName)
			if _, err := os.Lstat(target); err == nil {
				boundAgents = append(boundAgents, de.agentIDs...)
				if bindings[skillID] == nil {
					bindings[skillID] = make(map[string]bool)
				}
				for _, agID := range de.agentIDs {
					bindings[skillID][agID] = true
				}
			}
		}
		sk.BoundAgents = boundAgents
		skillsMap[skillID] = sk
	}

	return skillsMap, bindings, nil
}

func boundAgentsFromMap(bindings map[string]map[string]bool, skillID string) []string {
	agents := bindings[skillID]
	if len(agents) == 0 {
		return []string{}
	}
	out := make([]string, 0, len(agents))
	for agID := range agents {
		out = append(out, agID)
	}
	sort.Strings(out)
	return out
}

func (s *Store) List() []Skill {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]Skill, 0, len(s.skills))
	lockData := ParseAgentsLock()
	for id, sk := range s.skills {
		sk.BoundAgents = copySlice(boundAgentsFromMap(s.bindings, id))
		// 从 lock file 注入仓库来源（兜底：处理 Import 时未传入 repoOwner/repoName 的情况）
		if sk.RepoOwner == "" && lockData != nil {
			if lk, ok := lockData[sk.Directory]; ok {
				sk.RepoOwner = lk.Owner
				sk.RepoName = lk.Repo
				sk.RepoBranch = lk.Branch
				if sk.FullPath == "" {
					sk.FullPath = lk.FullPath
				}
			}
		}
		out = append(out, sk)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

func (s *Store) Get(id string) (Skill, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	sk, ok := s.skills[id]
	if !ok {
		return sk, false
	}
	sk.BoundAgents = copySlice(boundAgentsFromMap(s.bindings, id))
	return sk, true
}

// HasDirectory 检查 store 是否纳管指定目录名的技能。
// 用于 backfill 等后台流程写回前校验：技能可能在后台任务运行期间被卸载，
// 校验失败时跳过写回，避免把已卸载技能的来源重新写入锁文件。
func (s *Store) HasDirectory(dir string) bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	for _, sk := range s.skills {
		if sk.Directory == dir {
			return true
		}
	}
	return false
}

// SetRepoSource 为技能写入仓库来源（内存），用于回填成功后与 lock 文件
// 保持一致。仅写 lock 时 List() 的锁注入能让 UI 看到来源，但
// CheckUpdates/UpdateSkill 直接查内存，来源缺失会跳过更新检测与更新入口。
// 仅当技能尚无来源时写入（回填只处理无来源技能，已有来源不覆盖）。
// 返回 false 表示技能已不存在（回填期间被卸载）。
func (s *Store) SetRepoSource(skillID, owner, repo, branch, fullPath string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	cur, ok := s.skills[skillID]
	if !ok {
		return false
	}
	if cur.RepoOwner == "" && cur.RepoName == "" {
		cur.RepoOwner = owner
		cur.RepoName = repo
		cur.RepoBranch = branch
		cur.FullPath = fullPath
		s.skills[skillID] = cur
	}
	return true
}

func (s *Store) Import(path string, agentIDs []string, reg *agents.Registry, repoOwner, repoName string) (Skill, error) {
	return s.ImportWithDirName(path, filepath.Base(path), agentIDs, reg, repoOwner, repoName)
}

// ImportWithDirName 与 Import 相同，但允许调用方显式指定 skill 的目录名。
// 当源目录本身来自解压临时目录（zip/tarball 根目录直接含 SKILL.md）时，
// filepath.Base(path) 是随机临时目录名，不能作为 SSOT 中的技能目录名。
func (s *Store) ImportWithDirName(path, dirName string, agentIDs []string, reg *agents.Registry, repoOwner, repoName string) (Skill, error) {
	// Validate source path
	info, err := os.Stat(path)
	if err != nil {
		return Skill{}, fmt.Errorf("source path not accessible: %w", err)
	}
	if !info.IsDir() {
		return Skill{}, fmt.Errorf("source path is not a directory")
	}
	if !HasSkillManifest(path) {
		return Skill{}, fmt.Errorf("source directory does not contain SKILL.md")
	}

	// Require at least one agent
	if len(agentIDs) == 0 {
		return Skill{}, fmt.Errorf("at least one agent required")
	}

	// Parse metadata
	meta, err := ReadSkillMetadata(path)
	if err != nil {
		return Skill{}, fmt.Errorf("read SKILL.md: %w", err)
	}

	if err := ValidateDirectoryName(dirName); err != nil {
		return Skill{}, fmt.Errorf("invalid directory name %q: %w", dirName, err)
	}

	// Validate agent IDs
	capableIDs := reg.SkillCapableAgentIDs()
	if err := validateSkillAgentIDs(agentIDs, capableIDs); err != nil {
		return Skill{}, err
	}

	ssotDir := s.SSOTDir()
	if ssotDir == "" {
		return Skill{}, fmt.Errorf("SSOT directory not configured")
	}
	skillID := "skill:" + dirName
	now := shared.NowRFC3339()

	// 串行化 Import 的临界区，避免并发导入同名技能浪费文件 I/O。
	// importMu 在 mu 之前获取、之后释放，确保不会造成锁顺序反转。
	s.importMu.Lock()
	defer s.importMu.Unlock()

	// Reject duplicate managed directories before touching the existing SSOT
	// directory. Importing a duplicate must never delete or overwrite the
	// already managed skill.
	dest := filepath.Join(ssotDir, dirName)
	destExists := false
	if _, statErr := os.Stat(dest); statErr == nil {
		destExists = true
	} else if !os.IsNotExist(statErr) {
		return Skill{}, fmt.Errorf("stat destination: %w", statErr)
	}
	// 磁盘上不存在同名目录时允许替换（如技能目录被外部删除、内存仍保留记录）；
	// 否则同名即拒绝，防止覆盖已有托管技能。
	allowReplace := !destExists
	s.mu.RLock()
	for _, existing := range s.skills {
		if existing.Directory == dirName && !allowReplace {
			s.mu.RUnlock()
			return Skill{}, fmt.Errorf("skill with directory %q already exists (id: %s)", dirName, existing.ID)
		}
	}
	s.mu.RUnlock()

	// 获取 syncMethod（不在此时检查重复，仅在最终写入阶段检查一次以避免 TOCTOU）
	var method SyncMethod
	s.mu.RLock()
	method = s.syncMethod
	s.mu.RUnlock()

	// Copy to SSOT (filesystem I/O — outside the lock)
	// 走到这里时保证：目标不存在（正常安装/目录被外部删除后重装），
	// 或目标存在但未被 store 纳管（残留目录：此前解析失败被跳过、或安装中断残留，
	// 内存重复检查已确认无同名托管技能）。残留目录不能沿用旧内容
	//（可能是损坏状态），先移除再用源内容替换。
	copiedDest := false
	if destExists {
		if err := RemovePath(dest); err != nil {
			return Skill{}, fmt.Errorf("remove stale destination: %w", err)
		}
	}
	if err := os.MkdirAll(ssotDir, 0755); err != nil {
		return Skill{}, fmt.Errorf("create ssot dir: %w", err)
	}
	copiedDest = true
	if err := copyDirRecursive(path, dest); err != nil {
		_ = RemovePath(dest)
		return Skill{}, fmt.Errorf("copy to ssot: %w", err)
	}

	// Compute hash (filesystem I/O — outside the lock)
	contentHash, complete := HashDir(dest)
	if !complete {
		log.Printf("warning: skill content hash may be incomplete for %s", dest)
	}
	skillMdHash, _ := HashSkillMarkdown(dest)

	// Sync to agent directories (filesystem I/O — outside the lock). Existing
	// targets are moved aside first so a failed import can restore them.
	type syncRollback struct {
		target string
		backup string
	}
	var rollbackDir string
	var rollbackEntries []syncRollback
	rollback := func() {
		for i := len(rollbackEntries) - 1; i >= 0; i-- {
			entry := rollbackEntries[i]
			_ = RemovePath(entry.target)
			if entry.backup != "" {
				if err := os.Rename(entry.backup, entry.target); err != nil {
					log.Printf("import rollback: restore %s: %v", entry.target, err)
				}
			}
		}
		if rollbackDir != "" {
			_ = RemovePath(rollbackDir)
		}
		if copiedDest {
			_ = RemovePath(dest)
		}
	}
	defer func() {
		if rollbackDir != "" {
			_ = RemovePath(rollbackDir)
		}
	}()

	var syncErrs []string
	for _, agID := range agentIDs {
		agentDir := reg.AgentSkillsDir(agID)
		if agentDir == "" {
			continue
		}
		target := filepath.Join(agentDir, dirName)
		entry := syncRollback{target: target}
		if _, statErr := os.Lstat(target); statErr == nil {
			if rollbackDir == "" {
				rollbackDir, err = os.MkdirTemp("", "skill-import-rollback-")
				if err != nil {
					rollback()
					return Skill{}, fmt.Errorf("create import rollback dir: %w", err)
				}
			}
			entry.backup = filepath.Join(rollbackDir, fmt.Sprintf("target-%d", len(rollbackEntries)))
			if err := os.Rename(target, entry.backup); err != nil {
				rollback()
				return Skill{}, fmt.Errorf("backup existing agent skill %s: %w", target, err)
			}
		}
		rollbackEntries = append(rollbackEntries, entry)
		if err := SyncToAgentDir(dest, target, method); err != nil {
			syncErrs = append(syncErrs, fmt.Sprintf("agent %s: %v", agID, err))
		}
	}
	if len(syncErrs) > 0 {
		rollback()
		return Skill{}, fmt.Errorf("sync skill %s failed: %s", dirName, strings.Join(syncErrs, "; "))
	}

	// Re-acquire the lock to update in-memory state.
	// Re-check for duplicates: another Import may have added the same skill concurrently.
	var sk Skill
	s.mu.Lock()
	defer s.mu.Unlock()

	for _, existing := range s.skills {
		if existing.Directory == dirName {
			if allowReplace {
				break
			}
			// Another state-changing operation added the skill while the file
			// I/O was in progress. Roll back only this import's changes; never
			// remove the existing managed SSOT directory.
			rollback()
			return Skill{}, fmt.Errorf("skill with directory %q already exists (id: %s)", dirName, existing.ID)
		}
	}

	name := meta.Name
	if name == "" {
		name = dirName
	}

	sk = Skill{
		ID:          skillID,
		Name:        name,
		Description: meta.Description,
		Directory:   dirName,
		ContentHash: contentHash,
		SkillMdHash: skillMdHash,
		BoundAgents: copySlice(agentIDs),
		InstalledAt: now,
		UpdatedAt:   now,
		RepoOwner:   repoOwner,
		RepoName:    repoName,
	}
	// 从 ~/.agents/.skill-lock.json 注入仓库来源（若存在），覆盖空值
	if repo, ok := ParseAgentsLock()[dirName]; ok {
		if sk.RepoOwner == "" {
			sk.RepoOwner = repo.Owner
		}
		if sk.RepoName == "" {
			sk.RepoName = repo.Repo
		}
		if sk.RepoBranch == "" {
			sk.RepoBranch = repo.Branch
		}
	}

	s.skills[skillID] = sk
	for _, agID := range agentIDs {
		s.recordBindingLocked(skillID, agID)
	}
	return sk, nil
}

func (s *Store) ToggleAgent(skillID, agentID string, enabled bool, reg *agents.Registry) error {
	// 持 importMu 串行化 SSOT/agent 目录文件 I/O，与 Import/Uninstall 互斥，
	// 防止并发读取到被半写入的目录；锁序保持 importMu 先于 mu。
	s.importMu.Lock()
	defer s.importMu.Unlock()
	capableIDs := reg.SkillCapableAgentIDs()
	if err := validateSkillAgentIDs([]string{agentID}, capableIDs); err != nil {
		return err
	}

	// Under the lock: check skill exists, get paths, check idempotency.
	// Release the lock before performing filesystem I/O.
	var ssotPath, target string
	var method SyncMethod
	s.mu.Lock()
	sk, ok := s.skills[skillID]
	if !ok {
		s.mu.Unlock()
		return fmt.Errorf("skill %s not found", skillID)
	}

	agentDir := reg.AgentSkillsDir(agentID)
	if agentDir == "" {
		s.mu.Unlock()
		return fmt.Errorf("agent %s does not support skills", agentID)
	}

	ssotPath = filepath.Join(s.ssotDir, sk.Directory)
	target = filepath.Join(agentDir, sk.Directory)
	method = s.syncMethod

	currentlyBound := s.bindings[skillID][agentID]

	// Idempotent: no-op if state already matches
	if enabled == currentlyBound {
		s.mu.Unlock()
		return nil
	}
	s.mu.Unlock()

	// Perform filesystem I/O outside the lock
	if enabled {
		// Enable: sync from SSOT to agent dir
		if err := SyncToAgentDir(ssotPath, target, method); err != nil {
			return fmt.Errorf("sync to agent: %w", err)
		}
	} else {
		// Disable: remove from agent dir
		if err := RemovePath(target); err != nil {
			return fmt.Errorf("remove from agent: %w", err)
		}
	}

	// Re-acquire the lock to update bindings
	s.mu.Lock()
	defer s.mu.Unlock()
	// 重新检查 skill 是否仍存在：在锁释放期间可能已被 Uninstall 删除。
	// 若已删除，清理刚执行的文件 I/O（移除刚同步的 agent 目录），避免留下孤儿文件。
	if _, ok := s.skills[skillID]; !ok {
		if enabled {
			// 刚同步了文件但 skill 已被卸载，回滚清理
			if removeErr := RemovePath(target); removeErr != nil {
				log.Printf("toggle cleanup: failed to remove orphan target %s: %v", target, removeErr)
			}
		}
		return nil
	}
	if enabled {
		s.recordBindingLocked(skillID, agentID)
	} else {
		if s.bindings[skillID] != nil {
			delete(s.bindings[skillID], agentID)
		}
	}

	return nil
}

func (s *Store) Uninstall(skillID string, reg *agents.Registry) (UninstallResult, error) {
	// 持 importMu 串行化 SSOT 文件 I/O，与 Import/UpdateSkill 互斥，
	// 防止删除 SSOT 目录时被并发 Import 写入；锁序保持 importMu 先于 mu。
	s.importMu.Lock()
	defer s.importMu.Unlock()
	var result UninstallResult

	type agentTarget struct {
		agID   string
		target string
	}

	// Under the lock: get skill info and build the list of agent targets.
	// Release the lock before performing filesystem I/O.
	var dirName, ssotDir, ssotPath, backupDir string
	var agentTargets []agentTarget
	s.mu.Lock()
	sk, ok := s.skills[skillID]
	if !ok {
		s.mu.Unlock()
		return UninstallResult{}, fmt.Errorf("skill %s not found", skillID)
	}
	dirName = sk.Directory
	ssotDir = s.ssotDir
	ssotPath = filepath.Join(ssotDir, dirName)
	backupDir = filepath.Join(filepath.Dir(ssotDir), "skill-backups")
	for agID := range s.bindings[skillID] {
		agentDir := reg.AgentSkillsDir(agID)
		if agentDir == "" {
			continue
		}
		agentTargets = append(agentTargets, agentTarget{
			agID:   agID,
			target: filepath.Join(agentDir, dirName),
		})
	}
	s.mu.Unlock()

	// Perform filesystem I/O outside the lock
	// 汇总全部错误，避免只暴露最后一个；用户看到"ssot 删除失败"时
	// 也能知道 agent 目录残留情况。
	var errs []string

	// Backup：备份失败时降级为"警告并继续"，而不是中止卸载——
	// 备份目录不可写（权限/磁盘满/被占用）时中止会让技能永远无法卸载，
	// 用户没有任何绕过手段。降级后本地修改会丢失，通过 errs 汇总
	// 让前端提示"已卸载但备份失败"。
	backupPath, backupErr := BackupSkillDir(ssotDir, backupDir, dirName)
	if backupErr != nil {
		errs = append(errs, fmt.Sprintf("backup skill %s before uninstall: %v", dirName, backupErr))
	} else if backupPath != "" {
		result.BackupPath = backupPath
	}

	// Remove from all agent directories
	for _, t := range agentTargets {
		if err := RemovePath(t.target); err != nil {
			errs = append(errs, fmt.Sprintf("remove skill %s from agent %s: %v", dirName, t.agID, err))
		}
	}

	// Remove from SSOT
	if err := RemovePath(ssotPath); err != nil {
		// Verify if SSOT still exists; if so, return error to preserve consistency
		if _, statErr := os.Stat(ssotPath); statErr == nil {
			return result, fmt.Errorf("failed to remove skill from ssot: %w", err)
		}
		errs = append(errs, fmt.Sprintf("remove skill %s from ssot (path already gone, retry succeeded): %v", dirName, err))
	}
	// 清理 ~/.agents/.skill-lock.json 中的旧仓库来源记录，
	// 避免重装同名技能时被残留的 RepoOwner/RepoName 错误关联。
	if err := RemoveAgentsLockEntry(dirName); err != nil {
		errs = append(errs, fmt.Sprintf("remove skill %s from agents lock: %v", dirName, err))
	}

	// Re-acquire the lock to delete from in-memory state
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.skills, skillID)
	delete(s.bindings, skillID)

	result.ID = skillID
	if len(errs) > 0 {
		return result, fmt.Errorf("skill %s uninstalled, but cleanup incomplete: %s",
			skillID, strings.Join(errs, "; "))
	}
	return result, nil
}

// Resync re-syncs bound skills from SSOT to agent directories.
// Only fixes missing or broken links — does NOT delete user's unmanaged skills
// in agent directories. Unmanaged skills are preserved for manual import.
func (s *Store) Resync(reg *agents.Registry) error {
	// 持 importMu 串行化 agent 目录文件 I/O，与 Import/Uninstall 互斥，
	// 防止把半写入的 SSOT 内容同步到 agent；锁序保持 importMu 先于 mu。
	s.importMu.Lock()
	defer s.importMu.Unlock()
	type syncJob struct {
		ssot   string
		target string
		name   string
		agID   string
	}

	var syncJobs []syncJob
	var method SyncMethod
	capableIDs := reg.SkillCapableAgentIDs()

	s.mu.RLock()
	method = s.syncMethod
	ssotDir := s.ssotDir

	// 1. Collect pending sync entries (defer filesystem I/O until lock released)
	type pendingSync struct {
		skillID string
		dir     string
		agents  []string
	}
	var pending []pendingSync
	for skillID, agentBindings := range s.bindings {
		sk, ok := s.skills[skillID]
		if !ok {
			continue
		}
		agents := make([]string, 0, len(agentBindings))
		for agID := range agentBindings {
			agents = append(agents, agID)
		}
		pending = append(pending, pendingSync{skillID: skillID, dir: sk.Directory, agents: agents})
	}
	s.mu.RUnlock()

	// Build sync jobs (filesystem I/O — outside the lock)
	// 按 target 路径去重：CLI/Desktop 共享 SkillsDir 时，同一物理路径只需同步一次。
	seenTargets := make(map[string]bool)
	for _, p := range pending {
		ssotPath := filepath.Join(ssotDir, p.dir)
		if !HasSkillManifest(ssotPath) {
			continue
		}
		for _, agID := range p.agents {
			agentDir := reg.AgentSkillsDir(agID)
			if agentDir == "" {
				continue
			}
			target := filepath.Join(agentDir, p.dir)
			if seenTargets[target] {
				continue
			}
			seenTargets[target] = true
			syncJobs = append(syncJobs, syncJob{
				ssot:   ssotPath,
				target: target,
				name:   p.dir,
				agID:   agID,
			})
		}
	}

	_ = capableIDs // 不再扫描 agent 目录清理"孤儿"——用户自己的未纳管 skills 应保留

	// 2. Execute sync jobs (re-sync bound skills: only fix missing/broken links)
	var syncErrs []string
	for _, job := range syncJobs {
		needSync := false

		// Check if target is missing
		info, err := os.Lstat(job.target)
		if err != nil {
			needSync = true
		} else if info.Mode()&os.ModeSymlink != 0 {
			// Symlink: check if it's broken
			if _, err := os.Stat(job.target); err != nil {
				needSync = true // broken symlink
			}
		}

		if needSync {
			if err := SyncToAgentDir(job.ssot, job.target, method); err != nil {
				log.Printf("resync skill %s to agent %s: %v", job.name, job.agID, err)
				syncErrs = append(syncErrs, fmt.Sprintf("sync %s to %s: %v", job.name, job.agID, err))
			}
		}
	}

	if len(syncErrs) > 0 {
		return fmt.Errorf("resync completed with %d errors: %s", len(syncErrs), strings.Join(syncErrs, "; "))
	}
	return nil
}

// ScanUnmanaged finds skills in agent directories that are not managed by
// AgentPack (not present in the SSOT directory). Only scans agent-specific
// directories, excluding already-managed skills.
func (s *Store) ScanUnmanaged(reg *agents.Registry) []UnmanagedSkill {
	s.mu.RLock()
	ssotDirs := make(map[string]bool, len(s.skills))
	for _, sk := range s.skills {
		ssotDirs[sk.Directory] = true
	}
	s.mu.RUnlock()

	// 扫描各 agent 的 skills 目录，按目录名聚合
	type aggregated struct {
		foundIn  []string
		agentIDs []string
	}
	agg := make(map[string]*aggregated)
	for _, sd := range scanAgentSkillDirs(reg.SkillCapableAgentIDs(), reg.AgentSkillsDir) {
		for _, dirName := range sd.entries {
			skillPath := filepath.Join(sd.dir, dirName)
			a, ok := agg[dirName]
			if !ok {
				a = &aggregated{}
				agg[dirName] = a
			}
			a.foundIn = append(a.foundIn, skillPath)
			a.agentIDs = append(a.agentIDs, sd.agentIDs...)
		}
	}

	// 过滤已纳管的，构造结果
	var unmanaged []UnmanagedSkill
	for dirName, a := range agg {
		if ssotDirs[dirName] {
			continue
		}
		name := dirName
		if len(a.foundIn) > 0 {
			if meta, err := ReadSkillMetadata(a.foundIn[0]); err == nil && meta.Name != "" {
				name = meta.Name
			}
		}
		for _, agID := range a.agentIDs {
			unmanaged = append(unmanaged, UnmanagedSkill{
				AgentID:   agID,
				Directory: dirName,
				Path:      a.foundIn[0],
				Name:      name,
				FoundIn:   copySlice(a.foundIn),
			})
		}
	}
	return unmanaged
}

// agentDirScan 表示一个去重后的 agent skill 目录及其包含的 skill 子目录名列表
type agentDirScan struct {
	dir      string
	agentIDs []string
	entries  []string
}

// scanSkillEntries 扫描 dir 下的 skill 子目录名，跳过隐藏目录，
// 只收录含 SKILL.md 的子目录（含指向目录的符号链接 / Windows Junction）。
func scanSkillEntries(dir string) []string {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	var names []string
	for _, entry := range entries {
		dirName := entry.Name()
		if strings.HasPrefix(dirName, ".") {
			continue
		}
		skillPath := filepath.Join(dir, dirName)
		// 对于非目录条目（可能是 symlink / Windows Junction），
		// 用 os.Stat 跟踪链接确认目标是否为目录
		if !entry.IsDir() {
			stat, serr := os.Stat(skillPath)
			if serr != nil || !stat.IsDir() {
				continue
			}
		}
		if !HasSkillManifest(skillPath) {
			continue
		}
		names = append(names, dirName)
	}
	return names
}

// scanAgentSkillDirs 遍历所有 skill-capable agent 的 skills 目录，
// 按绝对路径去重（CLI/Desktop 共享目录只扫描一次），返回每个目录及其 skill 子目录名。
// 只收录含 SKILL.md 的子目录，跳过隐藏目录。
// dirResolver 接收 agent ID 返回其 skills 目录路径（空字符串表示不支持）。
func scanAgentSkillDirs(capableIDs []string, dirResolver func(agentID string) string) []*agentDirScan {
	seen := make(map[string]*agentDirScan)
	var uniqueDirs []*agentDirScan
	for _, agID := range capableIDs {
		agentDir := dirResolver(agID)
		if agentDir == "" {
			continue
		}
		abs, err := filepath.Abs(agentDir)
		if err != nil {
			abs = agentDir
		}
		if existing, ok := seen[abs]; ok {
			existing.agentIDs = append(existing.agentIDs, agID)
		} else {
			sd := &agentDirScan{dir: agentDir, agentIDs: []string{agID}}
			seen[abs] = sd
			uniqueDirs = append(uniqueDirs, sd)
		}
	}

	for _, sd := range uniqueDirs {
		sd.entries = scanSkillEntries(sd.dir)
	}

	return uniqueDirs
}

// AutoAdopt 扫描 agent skill 目录，将未管理 skill 纳管到 SSOT。
//   - 未在 SSOT 中的：复制到 SSOT 并按 syncMethod 同步回 agent 目录
//   - SSOT 已有同名的：用 SSOT 版本覆盖 agent 目录（用户选择"SSOT 覆盖"策略）
//
// 文件 I/O 在锁外执行，与 Import/Resync 模式一致。
func (s *Store) AutoAdopt(reg *agents.Registry) AdoptionResult {
	return s.autoAdoptWith(reg.SkillCapableAgentIDs(), reg.AgentSkillsDir)
}

// autoAdoptWith 是 AutoAdopt 的可测试核心，接收 capableIDs 和 dirResolver 注入。
func (s *Store) autoAdoptWith(capableIDs []string, dirResolver func(agentID string) string) AdoptionResult {
	ssotDir := s.SSOTDir()
	if ssotDir == "" {
		return AdoptionResult{Errors: []string{"SSOT directory not configured"}}
	}

	// 解析 ~/.agents/.skill-lock.json 一次，纳管时写入 skill 记录用于后续更新检测
	lockInfo := ParseAgentsLock()

	// 1. 收集 SSOT 已有目录名集合（读锁）
	s.mu.RLock()
	ssotDirs := make(map[string]bool, len(s.skills))
	for _, sk := range s.skills {
		ssotDirs[sk.Directory] = true
	}
	method := s.syncMethod
	s.mu.RUnlock()

	uniqueDirs := scanAgentSkillDirs(capableIDs, dirResolver)

	type adoptJob struct {
		dirName  string
		srcPath  string // agent 目录中的源路径
		agentIDs []string
		conflict bool // true = SSOT 已有同名，需覆盖 agent 目录
	}
	var jobs []adoptJob
	for _, sd := range uniqueDirs {
		for _, dirName := range sd.entries {
			jobs = append(jobs, adoptJob{
				dirName:  dirName,
				srcPath:  filepath.Join(sd.dir, dirName),
				agentIDs: append([]string(nil), sd.agentIDs...),
				conflict: ssotDirs[dirName],
			})
		}
	}

	// 2. 执行纳管/覆盖（文件 I/O 在锁外）
	// 整个文件 I/O 阶段持 importMu，与 Import/Uninstall/UpdateSkill 串行化：
	// 否则并发时可能读到 Import 半写入的 SSOT 目录，或与 Uninstall 的删除互相逆转。
	s.importMu.Lock()
	defer s.importMu.Unlock()
	result := AdoptionResult{}
	type applied struct {
		dirName  string
		agentIDs []string
		conflict bool
	}
	var appliedList []applied

	for _, job := range jobs {
		dest := filepath.Join(ssotDir, job.dirName)
		if !job.conflict {
			// 纳管：复制 agent 目录的 skill 到 SSOT
			if err := os.MkdirAll(ssotDir, 0755); err != nil {
				result.Errors = append(result.Errors, fmt.Sprintf("create ssot dir: %v", err))
				continue
			}
			// 若 SSOT 中意外已存在（并发纳管），跳过复制
			if _, err := os.Lstat(dest); err == nil {
				// 并发场景：当作冲突处理，用 SSOT 覆盖 agent 目录
				job.conflict = true
			} else if err := copyDirRecursive(job.srcPath, dest); err != nil {
				result.Errors = append(result.Errors, fmt.Sprintf("adopt %s: copy to ssot: %v", job.dirName, err))
				// 复制失败时清理半写入的 dest：SKILL.md 若已复制成功，
				// 残留目录会在下次 Load 被当作合法技能纳管（scanFilesystem
				// 只检查 HasSkillManifest），采纳半写入内容。
				if rmErr := RemovePath(dest); rmErr != nil {
					result.Errors = append(result.Errors, fmt.Sprintf("adopt %s: cleanup partial dest: %v", job.dirName, rmErr))
				}
				continue
			}
		}

		// 用 SSOT 版本同步到所有相关 agent 目录（冲突时覆盖，纳管时建立同步副本）
		for _, agID := range job.agentIDs {
			agentDir := dirResolver(agID)
			if agentDir == "" {
				continue
			}
			target := filepath.Join(agentDir, job.dirName)
			// 纳管场景下 srcPath 即该 agent 自己的目录，跳过自同步
			if !job.conflict && target == job.srcPath {
				// 需要先把原目录移除，再按 syncMethod 重建为 symlink/copy
				if err := RemovePath(target); err != nil {
					result.Errors = append(result.Errors, fmt.Sprintf("adopt %s: remove original from %s: %v", job.dirName, agID, err))
					continue
				}
			}
			if err := SyncToAgentDir(dest, target, method); err != nil {
				result.Errors = append(result.Errors, fmt.Sprintf("adopt %s: sync to %s: %v", job.dirName, agID, err))
				continue
			}
		}

		appliedList = append(appliedList, applied{
			dirName:  job.dirName,
			agentIDs: job.agentIDs,
			conflict: job.conflict,
		})
	}

	// 3. 加写锁更新 in-memory 状态
	s.mu.Lock()
	for _, ap := range appliedList {
		skillID := "skill:" + ap.dirName
		// 读取 SSOT 中的元数据
		skillPath := filepath.Join(ssotDir, ap.dirName)
		meta, err := ReadSkillMetadata(skillPath)
		if err != nil {
			result.Errors = append(result.Errors, fmt.Sprintf("adopt %s: read metadata: %v", ap.dirName, err))
			continue
		}
		name := meta.Name
		if name == "" {
			name = ap.dirName
		}
		contentHash, complete := HashDir(skillPath)
		if !complete {
			log.Printf("warning: skill content hash may be incomplete for %s", skillPath)
		}
		skillMdHash, _ := HashSkillMarkdown(skillPath)
		now := shared.NowRFC3339()
		sk, exists := s.skills[skillID]
		if !exists {
			sk = Skill{
				ID:          skillID,
				Name:        name,
				Description: meta.Description,
				Directory:   ap.dirName,
				ContentHash: contentHash,
				SkillMdHash: skillMdHash,
				InstalledAt: now,
			}
			// 从 ~/.agents/.skill-lock.json 注入仓库来源（若存在）
			if repo, ok := lockInfo[ap.dirName]; ok {
				sk.RepoOwner = repo.Owner
				sk.RepoName = repo.Repo
				sk.RepoBranch = repo.Branch
			}
		}
		sk.UpdatedAt = now
		sk.ContentHash = contentHash
		sk.SkillMdHash = skillMdHash
		sk.BoundAgents = copySlice(ap.agentIDs)
		s.skills[skillID] = sk
		for _, agID := range ap.agentIDs {
			s.recordBindingLocked(skillID, agID)
		}

		if ap.conflict {
			result.Conflicts = append(result.Conflicts, SkillConflict{
				Directory: ap.dirName,
				AgentIDs:  copySlice(ap.agentIDs),
			})
		} else {
			result.Adopted = append(result.Adopted, AdoptedSkill{
				Directory: ap.dirName,
				AgentIDs:  copySlice(ap.agentIDs),
			})
		}
	}
	s.mu.Unlock()

	return result
}

func (s *Store) MigrateStorage(targetDir string, reg *agents.Registry) (MigrationResult, error) {
	oldDir := s.SSOTDir()
	if oldDir == targetDir {
		return MigrationResult{}, nil
	}

	// 迁移是破坏性操作（移动整个 SSOT 目录）。MigrateSSOTDir 执行期间持
	// importMu 与 Import/Uninstall/UpdateSkill 的目录文件 I/O 互斥，防止
	// 并发 Import 正往旧目录复制新文件时被 os.Rename 移动后写失数据。
	// 注意不能在整个函数体持 importMu：后续 Resync 内部同样要取 importMu。
	migrateLocked := func(src, dst string) (int, []string) {
		s.importMu.Lock()
		defer s.importMu.Unlock()
		return MigrateSSOTDir(src, dst)
	}

	migrated, errs := migrateLocked(oldDir, targetDir)

	s.mu.Lock()
	s.ssotDir = targetDir
	s.mu.Unlock()

	rollbackMigration := func() {
		if rollbackMigrated, rollbackErrs := migrateLocked(targetDir, oldDir); rollbackErrs != nil {
			errs = append(errs, rollbackErrs...)
		} else {
			_ = rollbackMigrated
		}
		s.mu.Lock()
		s.ssotDir = oldDir
		s.mu.Unlock()
		if loadErr := s.Load(reg); loadErr != nil {
			errs = append(errs, fmt.Sprintf("reload after rollback: %v", loadErr))
		}
	}

	// MigrateSSOTDir can move some entries and fail on others. Roll back the
	// successful moves as well; otherwise the configured and actual SSOT roots
	// can diverge even before agent resync begins.
	if len(errs) > 0 {
		rollbackMigration()
		result := MigrationResult{Migrated: migrated, Errors: errs}
		return result, fmt.Errorf("migrate storage completed with %d errors: %s", len(errs), strings.Join(errs, "; "))
	}

	// Resync all skills to agent dirs with new SSOT path
	if err := s.Resync(reg); err != nil {
		// Resync 失败，回滚：将文件从 targetDir 移回 oldDir，并恢复 ssotDir 指针
		log.Printf("migrate storage: resync failed, rolling back: %v", err)
		errs = append(errs, fmt.Sprintf("resync after migration: %v", err))
		rollbackMigration()
	}

	result := MigrationResult{
		Migrated: migrated,
		Errors:   errs,
	}
	if len(errs) > 0 {
		return result, fmt.Errorf("migrate storage completed with %d errors: %s", len(errs), strings.Join(errs, "; "))
	}
	return result, nil
}

func (s *Store) recordBindingLocked(skillID, agentID string) {
	if s.bindings[skillID] == nil {
		s.bindings[skillID] = make(map[string]bool)
	}
	s.bindings[skillID][agentID] = true
}

func validateSkillAgentIDs(agentIDs []string, capableIDs []string) error {
	capableSet := make(map[string]bool, len(capableIDs))
	for _, id := range capableIDs {
		capableSet[id] = true
	}
	var invalid []string
	for _, id := range agentIDs {
		if !capableSet[id] {
			invalid = append(invalid, id)
		}
	}
	if len(invalid) > 0 {
		return fmt.Errorf("agent(s) not skill-capable or not active: %s", strings.Join(invalid, ", "))
	}
	return nil
}

func copySlice(src []string) []string {
	return shared.CopySlice(src)
}
