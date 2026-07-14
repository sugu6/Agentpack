package skills

import (
	"agentpack/internal/agents"
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
		// 从 ~/.agents/.skill-lock.json 读取仓库来源信息
		lockSkill := ParseAgentsLock()[dirName]
		log.Printf("scanFilesystem: dir=%q, lockSkill={Owner:%q, Repo:%q, Branch:%q}", dirName, lockSkill.Owner, lockSkill.Repo, lockSkill.Branch)
		sk := Skill{
			ID:          skillID,
			Name:        name,
			Description: meta.Description,
			Directory:   dirName,
			ContentHash: contentHash,
			BoundAgents: []string{},
			InstalledAt: now,
			UpdatedAt:   now,
			RepoOwner:   lockSkill.Owner,
			RepoName:    lockSkill.Repo,
			RepoBranch:  lockSkill.Branch,
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
	log.Printf("List: lockData has %d entries", len(lockData))
	for id, sk := range s.skills {
		sk.BoundAgents = copySlice(boundAgentsFromMap(s.bindings, id))
		// 从 lock file 注入仓库来源（兜底：处理 Import 时未传入 repoOwner/repoName 的情况）
		if sk.RepoOwner == "" && lockData != nil {
			if lk, ok := lockData[sk.Directory]; ok {
				sk.RepoOwner = lk.Owner
				sk.RepoName = lk.Repo
				sk.RepoBranch = lk.Branch
				log.Printf("List: injected repo info for skill %q (dir=%q): owner=%q repo=%q", id, sk.Directory, lk.Owner, lk.Repo)
			} else {
				log.Printf("List: skill %q (dir=%q) has empty RepoOwner and NOT found in lockData", id, sk.Directory)
			}
		}
		log.Printf("List: skill %q dir=%q repoOwner=%q repoName=%q", id, sk.Directory, sk.RepoOwner, sk.RepoName)
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
		return Skill{}, false
	}
	sk.BoundAgents = copySlice(boundAgentsFromMap(s.bindings, id))
	return sk, true
}

func (s *Store) Import(path string, agentIDs []string, reg *agents.Registry, repoOwner, repoName string) (Skill, error) {
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

	dirName := filepath.Base(path)
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

	// 获取 syncMethod（不在此时检查重复，仅在最终写入阶段检查一次以避免 TOCTOU）
	var method SyncMethod
	s.mu.Lock()
	method = s.syncMethod
	s.mu.Unlock()

	// Copy to SSOT (filesystem I/O — outside the lock)
	dest := filepath.Join(ssotDir, dirName)
	info, err = os.Stat(dest)
	if err != nil {
		if !os.IsNotExist(err) {
			return Skill{}, fmt.Errorf("stat destination: %w", err)
		}
		// dest doesn't exist, proceed with copy
		if err := os.MkdirAll(ssotDir, 0755); err != nil {
			return Skill{}, fmt.Errorf("create ssot dir: %w", err)
		}
		if err := copyDirRecursive(path, dest); err != nil {
			return Skill{}, fmt.Errorf("copy to ssot: %w", err)
		}
	}

	// Compute hash (filesystem I/O — outside the lock)
	contentHash, complete := HashDir(dest)
	if !complete {
		log.Printf("warning: skill content hash may be incomplete for %s", dest)
	}

	// Sync to agent directories (filesystem I/O — outside the lock)
	for _, agID := range agentIDs {
		agentDir := reg.AgentSkillsDir(agID)
		if agentDir == "" {
			continue
		}
		target := filepath.Join(agentDir, dirName)
		if err := SyncToAgentDir(dest, target, method); err != nil {
			log.Printf("sync skill %s to agent %s: %v", dirName, agID, err)
		}
	}

	// Re-acquire the lock to update in-memory state.
	// Re-check for duplicates: another Import may have added the same skill concurrently.
	var sk Skill
	s.mu.Lock()
	defer s.mu.Unlock()

	for _, existing := range s.skills {
		if existing.Directory == dirName {
			// 清理已复制到 SSOT 的目录，避免孤立文件残留
			if removeErr := RemovePath(dest); removeErr != nil {
				log.Printf("import cleanup: failed to remove duplicate ssot dir %s: %v", dest, removeErr)
			}
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
	// Backup
	backupPath, backupErr := BackupSkillDir(ssotDir, backupDir, dirName)
	if backupErr != nil {
		log.Printf("backup skill before uninstall: %v", backupErr)
	}
	result.BackupPath = backupPath

	// Remove from all agent directories
	for _, t := range agentTargets {
		if err := RemovePath(t.target); err != nil {
			log.Printf("remove skill %s from agent %s: %v", dirName, t.agID, err)
		}
	}

	// Remove from SSOT
	if err := RemovePath(ssotPath); err != nil {
		log.Printf("remove skill from ssot: %v", err)
		// Verify if SSOT still exists; if so, return error to preserve consistency
		if _, statErr := os.Stat(ssotPath); statErr == nil {
			return result, fmt.Errorf("failed to remove skill from ssot: %w", err)
		}
	}

	// Re-acquire the lock to delete from in-memory state
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.skills, skillID)
	delete(s.bindings, skillID)

	result.ID = skillID
	return result, nil
}

// Resync re-syncs bound skills from SSOT to agent directories.
// Only fixes missing or broken links — does NOT delete user's unmanaged skills
// in agent directories. Unmanaged skills are preserved for manual import.
func (s *Store) Resync(reg *agents.Registry) error {
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
		now := shared.NowRFC3339()
		sk, exists := s.skills[skillID]
		if !exists {
			sk = Skill{
				ID:          skillID,
				Name:        name,
				Description: meta.Description,
				Directory:   ap.dirName,
				ContentHash: contentHash,
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

	migrated, errs := MigrateSSOTDir(oldDir, targetDir)

	s.mu.Lock()
	s.ssotDir = targetDir
	s.mu.Unlock()

	// Resync all skills to agent dirs with new SSOT path
	if err := s.Resync(reg); err != nil {
		// Resync 失败，回滚：将文件从 targetDir 移回 oldDir，并恢复 ssotDir 指针
		log.Printf("migrate storage: resync failed, rolling back: %v", err)
		if rollbackMigrated, rollbackErrs := MigrateSSOTDir(targetDir, oldDir); rollbackErrs != nil {
			errs = append(errs, rollbackErrs...)
		} else {
			_ = rollbackMigrated
		}
		s.mu.Lock()
		s.ssotDir = oldDir
		s.mu.Unlock()
		errs = append(errs, fmt.Sprintf("resync after migration: %v", err))
		// 重新加载以恢复与文件系统一致的状态
		if loadErr := s.Load(reg); loadErr != nil {
			errs = append(errs, fmt.Sprintf("reload after rollback: %v", loadErr))
		}
	}

	return MigrationResult{
		Migrated: migrated,
		Errors:   errs,
	}, nil
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
