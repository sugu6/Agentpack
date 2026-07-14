package skills

type Skill struct {
	ID          string   `json:"id"`
	Name        string   `json:"name"`
	Description string   `json:"description,omitempty"`
	Directory   string   `json:"directory"`
	ContentHash string   `json:"contentHash,omitempty"`
	BoundAgents []string `json:"boundAgents"`
	InstalledAt string   `json:"installedAt"`
	UpdatedAt   string   `json:"updatedAt"`
	// 仓库来源信息（从 ~/.agents/.skill-lock.json 解析，用于更新检测）
	RepoOwner  string `json:"repoOwner,omitempty"`
	RepoName   string `json:"repoName,omitempty"`
	RepoBranch string `json:"repoBranch,omitempty"`
}

type SyncMethod string

const (
	SyncMethodSymlink SyncMethod = "symlink"
	SyncMethodCopy    SyncMethod = "copy"
)

func (m SyncMethod) Valid() bool {
	switch m {
	case SyncMethodSymlink, SyncMethodCopy:
		return true
	}
	return false
}

type StorageLocation string

const (
	StorageAgentpack StorageLocation = "agentpack"
	StorageUnified   StorageLocation = "unified"
)

func (l StorageLocation) Valid() bool {
	switch l {
	case StorageAgentpack, StorageUnified:
		return true
	}
	return false
}

type UninstallResult struct {
	ID         string `json:"id"`
	BackupPath string `json:"backupPath,omitempty"`
}

type MigrationResult struct {
	Migrated int      `json:"migrated"`
	Errors   []string `json:"errors,omitempty"`
}

// UnmanagedSkill represents a skill found in an agent's skills directory
// that is not managed by AgentPack (not in SSOT).
type UnmanagedSkill struct {
	AgentID    string   `json:"agentId"`
	Directory  string   `json:"directory"`
	Path       string   `json:"path"`
	Name       string   `json:"name,omitempty"`
	FoundIn    []string `json:"foundIn,omitempty"`
}

// AdoptionResult 是 AutoAdopt 的返回结果
type AdoptionResult struct {
	Adopted   []AdoptedSkill  `json:"adopted"`
	Conflicts []SkillConflict `json:"conflicts"`
	Errors    []string        `json:"errors"`
}

// AdoptedSkill 记录一个成功纳管的 skill 来源
type AdoptedSkill struct {
	Directory string   `json:"directory"`
	AgentIDs  []string `json:"agentIds"`
}

// SkillConflict 记录 SSOT 已存在同名、用 SSOT 覆盖 agent 目录的冲突
type SkillConflict struct {
	Directory string   `json:"directory"`
	AgentIDs  []string `json:"agentIds"`
}

// UpdateStatus 表示已安装 skill 与远端的差异
type UpdateStatus struct {
	SkillID    string `json:"skillId"`
	Directory  string `json:"directory"`
	LocalHash  string `json:"localHash"`
	RemoteHash string `json:"remoteHash"`
	HasUpdate  bool   `json:"hasUpdate"`
	CheckedAt  string `json:"checkedAt"`
	Error      string `json:"error,omitempty"` // 检查失败时的错误信息
}
