package skills

import (
	"agentpack/internal/iowriter"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
)

// AgentsLockFile 对应 ~/.agents/.skill-lock.json 的结构
type AgentsLockFile struct {
	Skills map[string]AgentsLockSkill `json:"skills"`
}

// AgentsLockSkill 对应 lock 文件中单个 skill 的信息
type AgentsLockSkill struct {
	Source       string `json:"source"`
	SourceType   string `json:"sourceType"`
	SourceURL    string `json:"sourceUrl"`
	SkillPath    string `json:"skillPath"`
	Branch       string `json:"branch"`
	SourceBranch string `json:"sourceBranch"`
}

// LockRepoInfo 从 lock 文件解析出的仓库信息
type LockRepoInfo struct {
	Owner  string
	Repo   string
	Branch string
}

// ParseAgentsLock 解析 ~/.agents/.skill-lock.json，返回 skill_name -> 仓库信息。
// sourceType == "github" 且 source 非空时解析 Owner/Repo，否则返回空字段存根。
// 文件不存在时返回 nil。
func ParseAgentsLock() map[string]LockRepoInfo {
	homeDir, err := os.UserHomeDir()
	if err != nil {
		return nil
	}
	path := filepath.Join(homeDir, ".agents", ".skill-lock.json")

	data, err := os.ReadFile(path)
	if err != nil {
		return nil
	}

	var lock AgentsLockFile
	if err := json.Unmarshal(data, &lock); err != nil {
		return nil
	}

	result := make(map[string]LockRepoInfo, len(lock.Skills))
	log.Printf("ParseAgentsLock: found %d entries in lock file", len(lock.Skills))
	for name, skill := range lock.Skills {
		if skill.SourceType == "github" && skill.Source != "" {
			owner, repo, ok := splitOwnerRepo(skill.Source)
			if !ok {
				log.Printf("ParseAgentsLock: skip entry %q, cannot split source %q", name, skill.Source)
				continue
			}
			branch := normalizeBranch(skill.Branch)
			if branch == "" {
				branch = normalizeBranch(skill.SourceBranch)
			}
			if branch == "" {
				branch = parseBranchFromURL(skill.SourceURL)
			}
			result[name] = LockRepoInfo{
				Owner:  owner,
				Repo:   repo,
				Branch: branch,
			}
			log.Printf("ParseAgentsLock: entry %q -> owner=%q repo=%q branch=%q", name, owner, repo, branch)
		} else {
			// 存根记录或非 github 源：返回空字段，表示来源未知
			result[name] = LockRepoInfo{}
			log.Printf("ParseAgentsLock: stub entry %q (sourceType=%q, source=%q)", name, skill.SourceType, skill.Source)
		}
	}

	return result
}

func splitOwnerRepo(source string) (owner, repo string, ok bool) {
	parts := strings.SplitN(strings.TrimSpace(source), "/", 2)
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		return "", "", false
	}
	return parts[0], parts[1], true
}

func normalizeBranch(branch string) string {
	b := strings.TrimSpace(branch)
	if b == "" {
		return ""
	}
	return b
}

// parseBranchFromURL 从 GitHub URL 中解析分支名。
// 支持 /tree/<branch>/... 格式、#branch 片段、?branch=xxx / ?ref=xxx 查询参数。
func parseBranchFromURL(sourceURL string) string {
	u := strings.TrimSpace(sourceURL)
	if u == "" {
		return ""
	}

	// https://github.com/owner/repo/tree/<branch>/...
	if _, after, ok := strings.Cut(u, "/tree/"); ok {
		branch := strings.SplitN(after, "/", 2)[0]
		if b := strings.TrimSpace(branch); b != "" {
			return b
		}
	}

	// URL fragment: ...git#branch
	if _, fragment, ok := strings.Cut(u, "#"); ok {
		branch := strings.SplitN(fragment, "&", 2)[0]
		if b := strings.TrimSpace(branch); b != "" {
			return b
		}
	}

	// query: ...?branch=xxx / ?ref=xxx
	if _, query, ok := strings.Cut(u, "?"); ok {
		for _, pair := range strings.Split(query, "&") {
			key, value, ok := strings.Cut(pair, "=")
			if !ok {
				continue
			}
			if key == "branch" || key == "ref" {
				if b := strings.TrimSpace(value); b != "" {
					return b
				}
			}
		}
	}

	return ""
}

// AgentsLockEntry 是写入 ~/.agents/.skill-lock.json 的条目
type AgentsLockEntry struct {
	Directory  string // skill 目录名（= key）
	Source     string // "owner/repo"
	SourceType string // 固定 "github"
	SourceURL  string // GitHub 仓库 URL
	SkillPath  string // SSOT 中的路径
	Branch     string // 分支名
}

// WriteDefaultLockEntries 扫描 SSOT 目录，为所有缺少锁文件记录的 skill 写入存根记录。
// 存根记录的 Source 和 SourceType 为空，表明来源未知（如 TRAE 内置 / 手动安装）。
// 后续从市场安装后，InstallMarketSkill 会通过 WriteAgentsLock 更新为正确的 repo 信息。
func WriteDefaultLockEntries(ssotDir string) error {
	homeDir, err := os.UserHomeDir()
	if err != nil {
		return fmt.Errorf("get home dir: %w", err)
	}
	lockPath := filepath.Join(homeDir, ".agents", ".skill-lock.json")

	// 1. 读取现有 lock 文件
	var lock AgentsLockFile
	data, err := os.ReadFile(lockPath)
	if err != nil {
		if !os.IsNotExist(err) {
			return fmt.Errorf("read lock file: %w", err)
		}
		lock = AgentsLockFile{Skills: make(map[string]AgentsLockSkill)}
	} else {
		if err := json.Unmarshal(data, &lock); err != nil {
			return fmt.Errorf("parse lock file: %w", err)
		}
		if lock.Skills == nil {
			lock.Skills = make(map[string]AgentsLockSkill)
		}
	}

	// 2. 扫描 SSOT 目录
	entries, err := os.ReadDir(ssotDir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return fmt.Errorf("read ssot dir: %w", err)
	}

	// 3. 为缺少记录的 skill 写入存根
	changed := false
	for _, entry := range entries {
		dirName := entry.Name()
		if strings.HasPrefix(dirName, ".") {
			continue
		}
		skillPath := filepath.Join(ssotDir, dirName)
		if !entry.IsDir() {
			stat, serr := os.Stat(skillPath)
			if serr != nil || !stat.IsDir() {
				continue
			}
		}
		if !HasSkillManifest(skillPath) {
			continue
		}
		if _, exists := lock.Skills[dirName]; exists {
			continue
		}
		lock.Skills[dirName] = AgentsLockSkill{
			Source:     "",
			SourceType: "",
			SourceURL:  "",
			SkillPath:  skillPath,
			Branch:     "",
		}
		changed = true
	}

	if !changed {
		return nil
	}

	// 4. 原子写入
	out, err := json.MarshalIndent(lock, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal lock file: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(lockPath), 0700); err != nil {
		return fmt.Errorf("create lock dir: %w", err)
	}
	return iowriter.WriteAtomic(lockPath, out, 0600)
}

// WriteAgentsLock 向 ~/.agents/.skill-lock.json 追加/更新一条 skill 记录
// 保留其他工具已写入的条目，仅更新或新增本应用安装的条目
func WriteAgentsLock(entry AgentsLockEntry) error {
	if entry.Directory == "" {
		return fmt.Errorf("directory is required")
	}

	homeDir, err := os.UserHomeDir()
	if err != nil {
		return fmt.Errorf("get home dir: %w", err)
	}

	lockPath := filepath.Join(homeDir, ".agents", ".skill-lock.json")

	// 1. 读取现有 lock 文件
	var lock AgentsLockFile
	data, err := os.ReadFile(lockPath)
	if err != nil {
		if !os.IsNotExist(err) {
			return fmt.Errorf("read lock file: %w", err)
		}
		// 文件不存在，初始化空的
		lock = AgentsLockFile{Skills: make(map[string]AgentsLockSkill)}
	} else {
		if err := json.Unmarshal(data, &lock); err != nil {
			// 读取现有内容失败时**不覆盖**（避免丢失其他工具的记录）
			return fmt.Errorf("parse existing lock file (not overwriting): %w", err)
		}
		if lock.Skills == nil {
			lock.Skills = make(map[string]AgentsLockSkill)
		}
	}

	// 2. 更新或新增条目
	branch := entry.Branch
	if branch == "" {
		branch = "main"
	}
	lock.Skills[entry.Directory] = AgentsLockSkill{
		Source:       entry.Source,
		SourceType:   entry.SourceType,
		SourceURL:    entry.SourceURL,
		SkillPath:    entry.SkillPath,
		Branch:       branch,
		SourceBranch: branch,
	}

	// 3. 原子写入（权限 0600）
	out, err := json.MarshalIndent(lock, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal lock file: %w", err)
	}

	// 确保目录存在
	if err := os.MkdirAll(filepath.Dir(lockPath), 0700); err != nil {
		return fmt.Errorf("create lock dir: %w", err)
	}

	if err := iowriter.WriteAtomic(lockPath, out, 0600); err != nil {
		return fmt.Errorf("write lock file: %w", err)
	}

	return nil
}
