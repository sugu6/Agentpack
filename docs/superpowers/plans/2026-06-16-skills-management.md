# Skills 本地管理实施计划

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 实现 Skills 本地管理 MVP，包括 SSOT 存储、Agent 绑定、存储位置切换和同步方式配置。

**Architecture:** 新增 `internal/skills` 模块（Store 模式，类似 MCP），修改 Agent 适配器增加 `SkillsDir()`，扩展配置和数据库，改造前端页面。

**Tech Stack:** Go (Wails v2), SQLite, Vue 3, TypeScript, Pinia

---

## File Structure

### 新建文件
- `internal/skills/types.go` — 类型定义
- `internal/skills/manifest.go` — SKILL.md 解析
- `internal/skills/sync.go` — 文件系统同步
- `internal/skills/store.go` — Store 核心
- `frontend/src/stores/skills.ts` — Pinia store

### 修改文件
- `internal/agents/types.go` — Adapter 增加 SkillsDir()
- `internal/agents/claudecode.go` — 实现 SkillsDir()
- `internal/agents/codex.go` — 实现 SkillsDir()
- `internal/agents/opencode.go` — 实现 SkillsDir()
- `internal/agents/cursor.go` — 实现 SkillsDir() 返回 ""
- `internal/agents/trae.go` — 实现 SkillsDir() 返回 ""
- `internal/agents/trae_cn.go` — 实现 SkillsDir() 返回 ""
- `internal/agents/registry.go` — 新增 SkillCapableAgentIDs/AgentSkillsDir
- `internal/config/config.go` — Settings 增加 SkillStorage/SkillSyncMethod
- `internal/database/db.go` — 新增 skills + skill_agent_bindings 表
- `app.go` — 新增 skillsStore 字段和 7 个 Wails 方法
- `frontend/src/types/index.ts` — 新增 Skill 类型
- `frontend/src/lib/api.ts` — 新增 skills API
- `frontend/src/views/SkillsView.vue` — 替换为真实管理页
- `frontend/src/views/SettingsView.vue` — 新增 Skills 配置卡片

---

### Task 1: 后端类型定义

**Files:**
- Create: `internal/skills/types.go`

- [ ] **Step 1: 创建 types.go**

```go
package skills

import "time"

type Skill struct {
	ID          string   `json:"id"`
	Name        string   `json:"name"`
	Description string   `json:"description,omitempty"`
	Directory   string   `json:"directory"`
	ContentHash string   `json:"contentHash,omitempty"`
	BoundAgents []string `json:"boundAgents"`
	InstalledAt string   `json:"installedAt"`
	UpdatedAt   string   `json:"updatedAt"`
}

type SyncMethod string

const (
	SyncMethodAuto    SyncMethod = "auto"
	SyncMethodSymlink SyncMethod = "symlink"
	SyncMethodCopy    SyncMethod = "copy"
)

func (m SyncMethod) Valid() bool {
	switch m {
	case SyncMethodAuto, SyncMethodSymlink, SyncMethodCopy:
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

func nowISO() string {
	return time.Now().UTC().Format(time.RFC3339)
}
```

---

### Task 2: SKILL.md 解析

**Files:**
- Create: `internal/skills/manifest.go`

- [ ] **Step 1: 创建 manifest.go**

```go
package skills

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
)

type SkillMetadata struct {
	Name        string `json:"name,omitempty"`
	Description string `json:"description,omitempty"`
}

func ParseSkillMetadata(content []byte) SkillMetadata {
	text := string(content)
	// Strip BOM
	text = strings.TrimPrefix(text, "\uFEFF")

	if !strings.HasPrefix(text, "---") {
		return SkillMetadata{}
	}

	// Find closing ---
	rest := text[3:]
	// Skip leading newline
	if len(rest) > 0 && (rest[0] == '\n' || rest[0] == '\r') {
		rest = rest[1:]
		if len(rest) > 0 && rest[0] == '\n' {
			rest = rest[1:]
		}
	}

	endIdx := strings.Index(rest, "\n---")
	if endIdx < 0 {
		return SkillMetadata{}
	}

	frontmatter := rest[:endIdx]

	var meta SkillMetadata
	for _, line := range strings.Split(frontmatter, "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "name:") {
			meta.Name = strings.TrimSpace(strings.TrimPrefix(line, "name:"))
			// Strip quotes
			meta.Name = strings.Trim(meta.Name, "\"'")
		} else if strings.HasPrefix(line, "description:") {
			meta.Description = strings.TrimSpace(strings.TrimPrefix(line, "description:"))
			meta.Description = strings.Trim(meta.Description, "\"'")
		}
	}

	return meta
}

func ReadSkillMetadata(dir string) (SkillMetadata, error) {
	data, err := os.ReadFile(filepath.Join(dir, "SKILL.md"))
	if err != nil {
		return SkillMetadata{}, err
	}
	return ParseSkillMetadata(data), nil
}

func HasSkillManifest(dir string) bool {
	info, err := os.Stat(filepath.Join(dir, "SKILL.md"))
	return err == nil && !info.IsDir()
}

func SanitizeDirectoryName(name string) string {
	// Replace unsafe characters with hyphens
	replacer := strings.NewReplacer(
		" ", "-",
		"/", "-",
		"\\", "-",
		":", "-",
		"*", "-",
		"?", "-",
		"\"", "-",
		"<", "-",
		">", "-",
		"|", "-",
	)
	return replacer.Replace(name)
}

func ValidateDirectoryName(name string) error {
	if name == "" {
		return fmt.Errorf("directory name is empty")
	}
	if name == "." || name == ".." {
		return fmt.Errorf("directory name cannot be '.' or '..'")
	}
	if strings.Contains(name, "/") || strings.Contains(name, "\\") {
		return fmt.Errorf("directory name cannot contain path separators")
	}
	if strings.HasPrefix(name, ".") {
		return fmt.Errorf("directory name cannot start with '.'")
	}
	return nil
}
```

注意：需要添加 `"fmt"` 到 import。

---

### Task 3: 文件系统同步

**Files:**
- Create: `internal/skills/sync.go`

- [ ] **Step 1: 创建 sync.go**

实现 `syncToAgentDir`, `createSymlink`, `copyDirAtomic`, `removePath`, `isSymlink`, `hashDir`, `ResolveSSOTDir`, `MigrateSSOTDir` 等函数。

关键逻辑：
- `syncToAgentDir(source, dest, method)` 根据 SyncMethod 选择同步方式
- Auto 模式：先尝试 symlink，失败回退 copy
- Symlink 模式：仅 symlink，失败报错
- Copy 模式：复制到临时目录再 rename
- `ResolveSSOTDir(location)` 根据存储位置返回路径
- `MigrateSSOTDir(oldDir, newDir)` 迁移文件

---

### Task 4: 数据库 Schema

**Files:**
- Modify: `internal/database/db.go`

- [ ] **Step 1: 在 schema 中新增 skills 和 skill_agent_bindings 表**

在 `createTables` SQL 中追加两个 CREATE TABLE 语句。

---

### Task 5: Agent 适配器 SkillsDir()

**Files:**
- Modify: `internal/agents/types.go`
- Modify: `internal/agents/claudecode.go`
- Modify: `internal/agents/codex.go`
- Modify: `internal/agents/opencode.go`
- Modify: `internal/agents/cursor.go`
- Modify: `internal/agents/trae.go`
- Modify: `internal/agents/trae_cn.go`
- Modify: `internal/agents/registry.go`

- [ ] **Step 1: 在 Adapter 接口增加 SkillsDir() string**

- [ ] **Step 2: 为每个适配器实现 SkillsDir()**

- [ ] **Step 3: 在 registry.go 新增 SkillCapableAgentIDs 和 AgentSkillsDir**

---

### Task 6: 配置扩展

**Files:**
- Modify: `internal/config/config.go`

- [ ] **Step 1: Settings 增加 SkillStorage 和 SkillSyncMethod 字段**

- [ ] **Step 2: 在 DefaultSettings 和 Load 中填充默认值**

---

### Task 7: Skills Store 核心

**Files:**
- Create: `internal/skills/store.go`

- [ ] **Step 1: 创建 store.go**

实现 `NewStore`, `Load`, `List`, `Import`, `ToggleAgent`, `Uninstall`, `Resync`, `MigrateStorage`, `SetSyncMethod`, `SetSSOTDir`。

核心模式参考 MCP Store：
- 内存状态：`skills map[string]Skill` + `bindings map[string]map[string]bool`
- SQLite 持久化：`syncDBFromSnapshot`
- 文件系统同步：调用 sync.go 中的函数
- 回滚机制

---

### Task 8: App.go 集成

**Files:**
- Modify: `app.go`

- [ ] **Step 1: 新增 skillsStore 字段**

- [ ] **Step 2: 在 startup 中初始化 skillsStore**

- [ ] **Step 3: 新增 7 个 Wails 绑定方法**

- [ ] **Step 4: 在 UpdateSettings 中检测 SkillStorage/SkillSyncMethod 变更**

- [ ] **Step 5: 在 RescanAgents 中同步更新 skillsStore**

---

### Task 9: 前端类型和 API

**Files:**
- Modify: `frontend/src/types/index.ts`
- Modify: `frontend/src/lib/api.ts`

- [ ] **Step 1: 在 types/index.ts 新增 Skill 类型**

- [ ] **Step 2: 在 api.ts 新增 skills API 段**

---

### Task 10: 前端 Pinia Store

**Files:**
- Create: `frontend/src/stores/skills.ts`

- [ ] **Step 1: 创建 skills.ts Pinia store**

---

### Task 11: SkillsView 页面

**Files:**
- Modify: `frontend/src/views/SkillsView.vue`

- [ ] **Step 1: 替换静态占位为真实管理页面**

---

### Task 12: SettingsView Skills 配置

**Files:**
- Modify: `frontend/src/views/SettingsView.vue`

- [ ] **Step 1: 新增 Skills 配置卡片（存储位置 + 同步方式）**

---

### Task 13: 编译验证

- [ ] **Step 1: 运行 go build 验证后端编译**
- [ ] **Step 2: 运行 vue-tsc --noEmit 验证前端类型**
- [ ] **Step 3: 运行 pnpm build 验证前端构建**
