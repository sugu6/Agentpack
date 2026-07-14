# AgentPack Skills 改造计划书

> **⚠️ 历史存档文档**
> 本文档描述的是 2026-06 月 Skills 子系统实现前的规划状态。
> 当前 Skills 系统已完整实现（`internal/skills/`、`app.go` Wails 绑定、`SkillsView.vue` 管理界面均已就绪）。
> 保留此文作为设计思路参考，不反映当前代码状态。

> 基于 CC Switch (tauri-codex-cli) GitHub 源码分析，制定 AgentPack Skills 子系统从"空壳"到"可用"的完整改造方案。

---

## 1. 现状分析

### 1.1 AgentPack 当前状态

| 层次 | 文件 | 状态 |
|------|------|------|
| **前端类型** | `frontend/src/types/index.ts` | `InstalledSkill`、`DiscoverableSkill`、`UnmanagedSkill` 等接口已定义 |
| **前端 Store** | `frontend/src/stores/skills.ts` | Pinia store 骨架完整，但 **所有方法均 throw "not implemented"** |
| **前端 API 层** | `frontend/src/lib/api.ts` | **完全缺失** skills 模块，没有 import wailsjs bindings |
| **前端页面** | `frontend/src/views/SkillsView.vue` | 完整 UI（Tabs: 已安装/可发现/未管理），但依赖空 store |
| **前端组件** | `frontend/src/components/skills/SkillCard.vue` | 完整卡片组件，含开关切换、卸载、更新按钮 |
| **配置** | `internal/config/config.go` | Settings 已有 `SkillStorage` / `SkillSyncMethod` 字段（默认 `unified` / `symlink`） |
| **数据库** | `internal/database/db.go` | **无 skills 表** — 仅有 mcp_servers / mcp_agent_bindings / settings 等 |
| **Go 导出** | `app.go` | **无 skills 相关函数** — 未注册任何 Wails 绑定 |
| **Go 逻辑** | `internal/skills/` | **目录不存在** |

**结论**：前端 UI 和数据模型已 100% 完成，但后端零实现。所有技能操作目前都会抛错。

### 1.2 CC Switch 参考实现

| 文件 | 大小 | 功能 |
|------|------|------|
| `src-tauri/src/services/skill.rs` | 107KB | `SkillService` 核心服务：install/uninstall/toggle_app/scan_unmanaged/import_from_apps/check_updates/update_skill/install_from_zip/migrate_storage |
| `src-tauri/src/database/dao/skills.rs` | — | `installed_skills` / `skill_repos` 表的 DAO CRUD |
| `src-tauri/src/commands/skill.rs` | — | Tauri command handlers（暴露给前端） |
| `src/hooks/useSkills.ts` | — | React hooks 封装 Tauri invoke |
| `src/lib/api/skills.ts` | — | TypeScript API 客户端 |
| `src/components/skills/SkillsPage.tsx` | — | React UI 页面 |

**核心架构模式**：
- **SSOT（单一数据源）**：`~/.cc-switch/skills/` 作为权威存储，同步到各 Agent 目录
- **DAO 层 + Service 层分离**：DAO 负责 CRUD，Service 负责业务逻辑
- **双向同步**：central storage ↔ agent config files
- **Git 仓库源**：支持从 GitHub repo 安装/更新
- **ZIP 本地安装**：支持从本地 zip 文件导入
- **未管理扫描**：检测各 Agent 目录下不属于统一存储的技能

---

## 2. 差距分析

### 2.1 必须实现的功能清单

| # | 功能 | CC Switch 实现 | AgentPack 现状 | 工作量估计 |
|---|------|---------------|---------------|-----------|
| 1 | **数据库表** | `installed_skills` + `skill_repos` | 无表 | 小 |
| 2 | **Skill 模型** | Rust struct | TS 类型已有，Go struct 无 | 小 |
| 3 | **List skills** | DAO query | 全抛错 | 小 |
| 4 | **Install from GitHub** | clone repo → hash → symlink/copy to SSOT | 无 | 大 |
| 5 | **Install from ZIP** | unzip → validate → copy to SSOT | 无 | 中 |
| 6 | **Uninstall** | remove from SSOT + remove from agent configs | 无 | 中 |
| 7 | **Toggle agent binding** | 更新 skills DB 中的 apps JSON + 写 agent config | 无 | 中 |
| 8 | **Discover skills** | scan known GitHub repos for SKILL.md | 无 | 大 |
| 9 | **Scan unmanaged** | 扫描各 agent 目录找孤立技能 | 无 | 中 |
| 10 | **Import unmanaged** | 迁移到 SSOT + 注册到 DB | 无 | 中 |
| 11 | **Check updates** | 对比 content_hash 与 remote | 无 | 中 |
| 12 | **Update skill** | pull latest from remote → re-hash → re-sync | 无 | 大 |
| 13 | **Sync method** | symlink / copy 策略 | 配置字段已有 | 中 |
| 14 | **Storage path** | `~/.cc-switch/skills/` | 配置字段已有 | 小 |
| 15 | **Frontend wiring** | React hooks + API client | 完全缺失 | 中 |

### 2.2 架构差异

| 维度 | CC Switch (Tauri/Rust) | AgentPack (Wails/Go) |
|------|------------------------|---------------------|
| 运行时 | Rust + Tauri | Go + Wails |
| 命令注册 | `#[tauri::command]` 宏 | `//go:generate` + `wails generate` |
| 数据库 | sqlx (compile-time checked) | database/sql (runtime) |
| Git 操作 | `git2` crate | `github.com/go-git/go-git/v5` |
| 文件系统 | `std::fs` + `walkdir` | `os` + `filepath.WalkDir` |
| 事件系统 | `Emitter` | `wruntime.EventsEmit` |
| 同步策略 | symlink / copy 两种 | 同左（配置项已有） |

---

## 3. 目标架构

```
┌─────────────────────────────────────────────────────┐
│                    Frontend (Vue 3)                   │
│  ┌──────────────┐  ┌──────────┐  ┌───────────────┐ │
│  │ SkillsView   │  │SkillCard │  │ useSkillsStore│ │
│  │ (UI)         │  │ (comp)   │  │ (Pinia)       │ │
│  └──────┬───────┘  └──────────┘  └───────┬───────┘ │
│         │                                 │         │
│         │         api.skills.*            │         │
│         ▼                                 ▼         │
│  ┌─────────────────────────────────────────────┐   │
│  │  frontend/src/lib/api.ts (新增 skills 段)    │   │
│  │  import { ListSkills, InstallSkill, ... }   │   │
│  │    from '../../wailsjs/go/main/App'         │   │
│  └─────────────────────────────────────────────┘   │
└──────────────────────┬──────────────────────────────┘
                       │ Wails bridge
                       ▼
┌─────────────────────────────────────────────────────┐
│                   Backend (Go)                        │
│                                                       │
│  app.go                                               │
│  ├── ListSkills() []Skill                            │
│  ├── InstallSkillFromRepo(owner, name, branch)       │
│  ├── InstallSkillFromZip(filePath)                   │
│  ├── UninstallSkill(id)                              │
│  ├── ToggleSkillAgent(id, agentID, enabled)          │
│  ├── DiscoverSkills() []DiscoverableSkill            │
│  ├── ScanUnmanaged() []UnmanagedSkill                │
│  ├── ImportUnmanaged(selections) []Skill             │
│  ├── CheckUpdates() []UpdateInfo                     │
│  └── UpdateSkill(id)                                 │
│                                                       │
│  internal/skills/service.go  ← 业务逻辑层             │
│  internal/skills/model.go    ← 数据类型               │
│  internal/skills/git.go      ← Git 操作               │
│  internal/skills/sync.go     ← 同步策略 (symlink/copy)│
│  internal/skills/discover.go ← 仓库发现               │
│                                                       │
│  internal/database/db.go                                │
│  └── 新增: installed_skills, skill_repos 表            │
└───────────────────────────────────────────────────────┘
                       │
                       ▼
┌─────────────────────────────────────────────────────┐
│                   Storage                             │
│                                                       │
│  ~/.agentpack/skills/          ← SSOT 统一存储         │
│  ├── skill-abc123/            ← 每个技能一个目录       │
│  │   ├── SKILL.md            │
│  │   └── ...                 │
│  └── skill-def456/                                   │
│                                                       │
│  ~/.claude/CLAUDE.md           ← Agent 配置           │
│  ~/.cursor/rules/              ← Agent 规则           │
│  ... (其他 Agent 目录)                               │
└─────────────────────────────────────────────────────┘
```

---

## 4. 详细设计方案

### 4.1 数据库 Schema

在 `internal/database/db.go` 的 `schema` 常量中追加：

```sql
CREATE TABLE IF NOT EXISTS installed_skills (
  id TEXT PRIMARY KEY,
  name TEXT NOT NULL,
  description TEXT,
  directory TEXT NOT NULL,          -- SSOT 中的目录路径
  repo_owner TEXT,                  -- GitHub owner (可选)
  repo_name TEXT,                   -- GitHub repo name (可选)
  repo_branch TEXT DEFAULT 'main',  -- 分支名
  content_hash TEXT,                -- SHA256 of SKILL.md content
  source_url TEXT,                  -- 安装来源 URL
  apps TEXT NOT NULL DEFAULT '{}',  -- JSON: SkillApps {claude, codex, ...}
  installed_at INTEGER NOT NULL,
  updated_at INTEGER NOT NULL
);

CREATE TABLE IF NOT EXISTS skill_repos (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  owner TEXT NOT NULL UNIQUE,
  name TEXT NOT NULL UNIQUE,
  branch TEXT DEFAULT 'main',
  enabled INTEGER NOT NULL DEFAULT 1,
  last_checked INTEGER,
  last_sync INTEGER
);

CREATE INDEX IF NOT EXISTS idx_installed_skills_repo 
  ON installed_skills(repo_owner, repo_name);
```

**字段说明**：
- `directory`：指向 SSOT 中的物理路径（如 `~/.agentpack/skills/skill-abc123`）
- `content_hash`：用于检测更新的完整性校验（SKILL.md 的 SHA256）
- `apps`：JSON 编码的 `SkillApps`，记录该技能启用了哪些 Agent
- `repo_owner/repo_name/repo_branch`：Git 源信息，用于更新

### 4.2 Go 模型 (`internal/skills/model.go`)

```go
package skills

// SkillApps mirrors frontend/types SkillApps
type SkillApps struct {
	Claude        bool `json:"claude"`
	ClaudeDesktop bool `json:"claudeDesktop,omitempty"`
	Codex         bool `json:"codex"`
	Gemini        bool `json:"gemini,omitempty"`
	Opencode      bool `json:"opencode"`
	Openclaw      bool `json:"openclaw,omitempty"`
	Cursor        bool `json:"cursor"`
	Trae          bool `json:"trae"`
	TraeCn        bool `json:"traeCn,omitempty"`
}

// InstalledSkill 对应数据库 installed_skills 表
type InstalledSkill struct {
	ID           string   `json:"id"`
	Name         string   `json:"name"`
	Description  string   `json:"description,omitempty"`
	Directory    string   `json:"directory"`
	RepoOwner    *string  `json:"repoOwner,omitempty"`
	RepoName     *string  `json:"repoName,omitempty"`
	RepoBranch   string   `json:"repoBranch,omitempty"`
	ContentHash  string   `json:"contentHash,omitempty"`
	SourceURL    string   `json:"sourceUrl,omitempty"`
	Apps         SkillApps `json:"apps"`
	InstalledAt  string   `json:"installedAt"`
	UpdatedAt    string   `json:"updatedAt"`
}

// DiscoverableSkill 可发现的技能（来自远程仓库）
type DiscoverableSkill struct {
	Key         string `json:"key"`
	Name        string `json:"name"`
	Description string `json:"description"`
	Directory   string `json:"directory"`
	ReadmeURL   string `json:"readmeUrl,omitempty"`
	RepoOwner   string `json:"repoOwner"`
	RepoName    string `json:"repoName"`
	RepoBranch  string `json:"repoBranch"`
}

// UnmanagedSkill 未管理的技能（存在于 Agent 目录但不在 SSOT）
type UnmanagedSkill struct {
	Directory   string   `json:"directory"`
	Name        string   `json:"name"`
	Description string   `json:"description,omitempty"`
	FoundIn     []string `json:"foundIn"`  // 在哪些 Agent 中发现
	Path        string   `json:"path"`     // 实际路径
}

// UpdateInfo 更新信息
type UpdateInfo struct {
	ID         string `json:"id"`
	Name       string `json:"name"`
	CurrentHash string `json:"currentHash,omitempty"`
	RemoteHash  string `json:"remoteHash"`
}
```

### 4.3 Go 服务层 (`internal/skills/service.go`)

```go
package skills

import (
	"agentpack/internal/agents"
	"agentpack/internal/database"
	"agentpack/internal/iowriter"
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// Service 技能服务主入口
type Service struct {
	ssotDir string              // 统一存储根目录 (~/.agentpack/skills/)
	registry *agents.Registry // Agent 注册表
	mu      sync.Mutex
}

func NewService(ssotDir string, reg *agents.Registry) *Service {
	return &Service{
		ssotDir:  ssotDir,
		registry: reg,
	}
}

// --- 核心方法签名 ---

// List 列出所有已安装技能
func (s *Service) List() ([]InstalledSkill, error)

// InstallFromRepo 从 GitHub 仓库安装
func (s *Service) InstallFromRepo(owner, name, branch string) (*InstalledSkill, error)

// InstallFromZip 从本地 ZIP 文件安装
func (s *Service) InstallFromZip(zipPath string) (*InstalledSkill, error)

// Uninstall 卸载技能（从 SSOT + Agent 配置中移除）
func (s *Service) Uninstall(id string) error

// ToggleAgent 切换技能对某个 Agent 的绑定
func (s *Service) ToggleAgent(skillID, agentID string, enabled bool) error

// Discover 从配置的仓库发现可用技能
func (s *Service) Discover() ([]DiscoverableSkill, error)

// ScanUnmanaged 扫描各 Agent 目录中的未管理技能
func (s *Service) ScanUnmanaged() ([]UnmanagedSkill, error)

// ImportUnmanaged 将未管理技能导入 SSOT
func (s *Service) ImportUnmanaged(selections []ImportSelection) ([]InstalledSkill, error)

// ImportSelection 导入选择
type ImportSelection struct {
	Directory string   `json:"directory"`
	Apps      SkillApps `json:"apps"`
}

// CheckUpdates 检查所有技能的更新
func (s *Service) CheckUpdates() ([]UpdateInfo, error)

// Update 更新单个技能
func (s *Service) Update(id string) error
```

### 4.4 关键子系统设计

#### 4.4.1 Git 克隆与存储 (`git.go`)

```go
package skills

import (
	"github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing/object"
	// ...
)

// cloneRepo 将 GitHub repo 的指定分支克隆到目标目录
func cloneRepo(owner, name, branch, dest string) (string, error) {
	// URL: https://github.com/{owner}/{name}.git
	// 使用 go-git 库，支持进度回调
	// 返回克隆后的工作树路径
}

// computeHash 计算目录内 SKILL.md 的 SHA256
func computeHash(path string) (string, error) {
	data, err := os.ReadFile(filepath.Join(path, "SKILL.md"))
	if err != nil {
		return "", err
	}
	h := sha256.Sum256(data)
	return fmt.Sprintf("%x", h[:8]), // 截断到 8 字节 hex
}
```

#### 4.4.2 同步策略 (`sync.go`)

支持两种同步模式（由 `config.Settings.SkillSyncMethod` 控制）：

```go
const (
	SyncMethodSymlink = "symlink"
	SyncMethodCopy    = "copy"
)

// SyncToAgent 将技能同步到指定 Agent 的配置中
func (s *Service) SyncToAgent(skillDir, agentID string, enabled bool) error {
	agent := s.registry.Get(agentID)
	if agent == nil || agent.ConfigPath == "" {
		return fmt.Errorf("agent %s not found", agentID)
	}

	switch config.Settings.SkillSyncMethod {
	case SyncMethodSymlink:
		return s.createSymlink(skillDir, agent.ConfigPath)
	case SyncMethodCopy:
		return s.copyToAgent(skillDir, agent.ConfigPath)
	default:
		return s.createSymlink(skillDir, agent.ConfigPath) // 默认 symlink
	}
}
```

**注意**：AgentPack 中技能同步到 Agent 的方式取决于 Agent 的配置格式。与 MCP Server 不同，Skills 通常是通过 `SKILL.md` 文件被 Agent 自动读取的，所以同步方式可能是：
- **Claude Code**：复制到 `~/.claude/SKILLS/` 或写入 `CLAUDE.md` 的 SKILLS 段
- **Cursor/Trae**：复制到项目的 `.cursor/rules/` 或等效目录
- **Codex/OpenCode**：写入各自的 skill 配置路径

实际实现时需要调研各 Agent 的具体技能加载机制。

#### 4.4.3 仓库发现 (`discover.go`)

```go
// 预设可发现的技能仓库列表
var defaultRepos = []struct {
	Owner string
	Name  string
}{
	{"anthropic", "cookbook"},
	{"coderabbitai", "skill-template"},
	// ... 更多社区技能仓库
}

func (s *Service) Discover() ([]DiscoverableSkill, error) {
	var results []DiscoverableSkill
	for _, repo := range defaultRepos {
		skills, err := s.discoverFromRepo(repo.Owner, repo.Name, repo.Branch)
		if err != nil {
			log.Printf("discover %s/%s: %v", repo.Owner, repo.Name, err)
			continue
		}
		results = append(results, skills...)
	}
	return results, nil
}

// discoverFromRepo 扫描 GitHub repo 的 skills/ 目录，找出包含 SKILL.md 的子目录
func (s *Service) discoverFromRepo(owner, name, branch string) ([]DiscoverableSkill, error) {
	// 通过 GitHub API 获取 skills/ 目录下的文件列表
	// 筛选出包含 SKILL.md 的子目录
	// 返回 DiscoverableSkill 列表
}
```

#### 4.4.4 未管理扫描 (`unmanaged.go`)

```go
func (s *Service) ScanUnmanaged() ([]UnmanagedSkill, error) {
	// 1. 获取所有已启用 Agent 的目录列表
	// 2. 在每个 Agent 的配置目录中搜索 SKILL.md 或 skills/ 子目录
	// 3. 对找到的每个技能目录，检查是否已注册到 installed_skills 表
	// 4. 未注册的即为"未管理"
	// 5. 解析 SKILL.md 提取 name/description
}
```

### 4.5 Wails 绑定 (`app.go` 新增)

在 `app.go` 中添加 `skillService` 字段和导出方法：

```go
type App struct {
	// ... 现有字段 ...
	skillService *skills.Service
}

func (a *App) startup(ctx context.Context) {
	// ... 现有初始化代码 ...

	// 初始化 Skills 服务
	skotDir := filepath.Join(config.AgentPackDir(), "skills")
	if err := os.MkdirAll(skotDir, 0700); err != nil {
		addErr("create skills dir", err)
	}
	a.skillService = skills.NewService(skotDir, a.registry)
}

// Wails 导出的技能方法
func (a *App) ListSkills() ([]skills.InstalledSkill, error) {
	if a.skillService == nil {
		return []skills.InstalledSkill{}, nil
	}
	return a.skillService.List()
}

func (a *App) InstallSkillFromRepo(owner, name, branch string) (*skills.InstalledSkill, error) {
	if err := a.assertInit(); err != nil {
		return nil, err
	}
	if err := a.beginInFlight(); err != nil {
		return nil, err
	}
	defer a.endInFlight()
	return a.skillService.InstallFromRepo(owner, name, branch)
}

func (a *App) InstallSkillFromZip(zipPath string) (*skills.InstalledSkill, error) {
	if err := a.assertInit(); err != nil {
		return nil, err
	}
	if err := a.beginInFlight(); err != nil {
		return nil, err
	}
	defer a.endInFlight()
	return a.skillService.InstallFromZip(zipPath)
}

func (a *App) UninstallSkill(id string) error {
	if err := a.assertInit(); err != nil {
		return err
	}
	return a.skillService.Uninstall(id)
}

func (a *App) ToggleSkillAgent(skillID, agentID string, enabled bool) error {
	if err := a.assertInit(); err != nil {
		return err
	}
	return a.skillService.ToggleAgent(skillID, agentID, enabled)
}

func (a *App) DiscoverSkills() ([]skills.DiscoverableSkill, error) {
	if a.skillService == nil {
		return []skills.DiscoverableSkill{}, nil
	}
	return a.skillService.Discover()
}

func (a *App) ScanUnmanagedSkills() ([]skills.UnmanagedSkill, error) {
	if a.skillService == nil {
		return []skills.UnmanagedSkill{}, nil
	}
	return a.skillService.ScanUnmanaged()
}

func (a *App) ImportUnmanagedSkills(selections []skills.ImportSelection) ([]skills.InstalledSkill, error) {
	if a.skillService == nil {
		return []skills.InstalledSkill{}, nil
	}
	return a.skillService.ImportUnmanaged(selections)
}

func (a *App) CheckSkillUpdates() ([]skills.UpdateInfo, error) {
	if a.skillService == nil {
		return []skills.UpdateInfo{}, nil
	}
	return a.skillService.CheckUpdates()
}

func (a *App) UpdateSkill(id string) error {
	if err := a.assertInit(); err != nil {
		return err
	}
	return a.skillService.Update(id)
}
```

### 4.6 前端 API 层 (`api.ts` 新增)

```typescript
import {
  ListSkills,
  InstallSkillFromRepo,
  InstallSkillFromZip,
  UninstallSkill,
  ToggleSkillAgent,
  DiscoverSkills,
  ScanUnmanagedSkills,
  ImportUnmanagedSkills,
  CheckSkillUpdates,
  UpdateSkill,
} from '../../wailsjs/go/main/App'

// 在 api 对象中新增 skills 段：
export const api = {
  // ... 现有 sections ...

  skills: {
    list: async () => toPlainObject(await ListSkills()),
    installFromRepo: (owner: string, name: string, branch = 'main') =>
      InstallSkillFromRepo(owner, name, branch),
    installFromZip: (zipPath: string) => InstallSkillFromZip(zipPath),
    uninstall: (id: string) => UninstallSkill(id),
    toggleAgent: (skillId: string, agentId: string, enabled: boolean) =>
      ToggleSkillAgent(skillId, agentId, enabled),
    discover: async () => toPlainObject(await DiscoverSkills()),
    scanUnmanaged: async () => toPlainObject(await ScanUnmanagedSkills()),
    importUnmanaged: (selections: any[]) =>
      ImportUnmanagedSkills(toPlainObject(selections)),
    checkUpdates: async () => toPlainObject(await CheckSkillUpdates()),
    update: (id: string) => UpdateSkill(id),
  },
}
```

### 4.7 前端 Store 改造 (`skills.ts`)

将现有的 throw-only 实现替换为真实 API 调用：

```typescript
async function fetch() {
  loading.value = true
  error.value = null
  try {
    items.value = await api.skills.list()
  } catch (e: any) {
    error.value = e?.message || '加载技能列表失败'
    throw e
  } finally {
    loading.value = false
  }
}

async function discover() {
  discovering.value = true
  try {
    discoverable.value = await api.skills.discover()
  } catch (e: any) {
    error.value = e?.message || '发现技能失败'
    throw e
  } finally {
    discovering.value = false
  }
}

async function install(skill: DiscoverableSkill) {
  await api.skills.installFromRepo(skill.repoOwner, skill.repoName, skill.repoBranch)
  await fetch() // 刷新列表
}

async function uninstall(id: string) {
  await api.skills.uninstall(id)
  await fetch()
}

async function toggleAgent(id: string, agentId: string, enabled: boolean) {
  await api.skills.toggleAgent(id, agentId, enabled)
  await fetch()
}

async function scanUnmanaged() {
  unmanaged.value = await api.skills.scanUnmanaged()
}

async function importSelections(selections: any[]) {
  const imported = await api.skills.importUnmanaged(selections)
  await fetch()
  return imported
}

async function checkUpdates() {
  checking.value = true
  try {
    updates.value = await api.skills.checkUpdates()
  } catch (e: any) {
    error.value = e?.message || '检查更新失败'
    throw e
  } finally {
    checking.value = false
  }
}

async function updateSkill(id: string) {
  await api.skills.update(id)
  await fetch()
}
```

---

## 5. 实施阶段

### Phase 1: 基础框架（1-2 天）

**目标**：能列出空技能列表，数据库就绪

| 文件 | 变更 |
|------|------|
| `internal/database/db.go` | 追加 `installed_skills` + `skill_repos` DDL |
| `internal/skills/model.go` | 新建，定义 `InstalledSkill` 等结构体 |
| `internal/skills/service.go` | 新建，实现 `List()` 方法 |
| `app.go` | 新增 `skillService` 字段 + `ListSkills()` 绑定 |
| `frontend/src/lib/api.ts` | 新增 `skills.list` 调用 |
| `frontend/src/stores/skills.ts` | 修复 `fetch()` 调用真实 API |

**验收标准**：打开 SkillsView 页面能看到空列表，不报错。

### Phase 2: 安装/卸载核心（2-3 天）

**目标**：能从 GitHub 安装技能，能卸载

| 文件 | 变更 |
|------|------|
| `internal/skills/git.go` | 新建，`cloneRepo()` / `computeHash()` |
| `internal/skills/service.go` | 实现 `InstallFromRepo()` / `Uninstall()` |
| `internal/skills/sync.go` | 新建，`SyncToAgent()` 同步逻辑 |
| `app.go` | 新增 `InstallSkillFromRepo()` / `UninstallSkill()` 绑定 |
| `frontend/src/lib/api.ts` | 新增 install/uninstall 调用 |
| `frontend/src/stores/skills.ts` | 实现 install/uninstall |

**验收标准**：
- 从预设仓库安装一个技能 → SSOT 中出现目录 + SKILL.md
- 卸载后目录被清除 + Agent 配置被清理
- 列表正确刷新

### Phase 3: 发现/更新/未管理（2-3 天）

**目标**：完整功能闭环

| 文件 | 变更 |
|------|------|
| `internal/skills/discover.go` | 新建，GitHub API 扫描 |
| `internal/skills/unmanaged.go` | 新建，Agent 目录扫描 |
| `internal/skills/service.go` | 实现 `Discover()` / `ScanUnmanaged()` / `ImportUnmanaged()` / `CheckUpdates()` / `Update()` |
| `app.go` | 新增对应 Wails 绑定 |
| `frontend/src/lib/api.ts` | 新增 discover/import/checkUpdates/update 调用 |
| `frontend/src/stores/skills.ts` | 实现对应方法 |

**验收标准**：
- "发现"标签页能列出预设仓库中的技能
- "未管理"标签页能检测到 Agent 目录中的孤立技能
- "检查更新"能识别有更新的技能
- "全部导入"能将未管理技能迁入 SSOT

### Phase 4: ZIP 安装 + 打磨（1 天）

| 文件 | 变更 |
|------|------|
| `internal/skills/service.go` | 实现 `InstallFromZip()` |
| `app.go` | 新增 `InstallSkillFromZip()` 绑定 |
| `frontend` | 添加 ZIP 上传 UI（如有需要） |

---

## 6. 与 MCP 系统的协同设计

AgentPack 的 Skills 系统与 MCP 系统共享以下基础设施：

| 共享资源 | MCP 使用方式 | Skills 使用方式 |
|----------|-------------|----------------|
| `agents.Registry` | 读取 Agent 配置路径 | 写入 SKILL.md 到 Agent 目录 |
| `database.WithTransaction` | 持久化 MCP 配置 | 持久化技能元数据 |
| `iowriter.WriteAtomic` | 原子写入 agent config | 原子写入 SKILL.md |
| `isSafeAgentConfigPath` | 验证写路径安全 | 复用同一验证 |
| `rollback` 模式 | Add/Update/Remove 回滚 | 安装/卸载时回滚 |

**关键区别**：
- MCP 系统修改的是 Agent 的 **JSON/TOML 配置文件**（结构化数据）
- Skills 系统是操作 **独立文件**（SKILL.md）→ 更简单，不需要解析 Agent 配置格式

---

## 7. 风险与注意事项

### 7.1 跨平台问题

| 风险 | 影响 | 缓解措施 |
|------|------|---------|
| Symlink 权限 | Windows 需要管理员或 Developer Mode | 提供 `copy` 模式作为 fallback；优先使用 `copy` 默认 |
| 路径分隔符 | Windows `\` vs Unix `/` | 全部使用 `filepath.Join` |
| Git 依赖 | 克隆 GitHub 需要 git CLI 或 go-git | 使用 `go-git` 纯 Go 实现，无外部依赖 |

### 7.2 安全性

- **ZIP 安装**：需要限制解压深度（防 zip slip 攻击），限制文件类型（仅接受 `.md` / `.txt` / `.json` 等）
- **GitHub 源**：验证克隆内容的 content_hash，防止中间人篡改
- **写路径验证**：复用现有的 `isSafeAgentConfigPath` 确保不写到系统目录

### 7.3 性能

- **Discover**：GitHub API 有速率限制（未认证 60/h），需要缓存结果 + 增量检查
- **ScanUnmanaged**：递归搜索 Agent 目录可能较慢，考虑异步执行 + 进度提示
- **CheckUpdates**：逐个对比 remote hash，可考虑并行检查

---

## 8. 文件清单

### 新建文件

| 路径 | 说明 |
|------|------|
| `internal/skills/model.go` | 数据类型定义 |
| `internal/skills/service.go` | 核心业务逻辑 |
| `internal/skills/git.go` | Git 克隆/拉取操作 |
| `internal/skills/sync.go` | 同步策略实现 |
| `internal/skills/discover.go` | 仓库发现逻辑 |
| `internal/skills/unmanaged.go` | 未管理扫描逻辑 |
| `internal/skills/zip.go` | ZIP 安装逻辑 |

### 修改文件

| 路径 | 变更 |
|------|------|
| `internal/database/db.go` | 追加 skills 表 DDL |
| `app.go` | 新增 skillService + 9 个 Wails 绑定方法 |
| `frontend/src/lib/api.ts` | 新增 skills API 段 |
| `frontend/src/stores/skills.ts` | 实现所有空方法 |

---

## 9. 后续扩展（Phase 5+）

- **自定义仓库源**：允许用户添加自己的 GitHub 仓库作为技能源
- **技能市场**：集成到现有的 Market 系统，统一搜索/安装体验
- **技能编辑器**：直接在 App 中编辑 SKILL.md
- **版本历史**：保留技能的旧版本快照，支持回滚
- **依赖管理**：技能之间的依赖关系（如 A 技能需要先安装 B 技能）

---

## 附录 A: CC Switch 关键实现参考

### A.1 SkillService::install 核心流程

```rust
// CC Switch (Rust) 伪代码
pub async fn install(&self, repo: &str, branch: Option<String>) -> Result<Skill> {
    let temp_dir = tempfile::tempdir()?;
    let repo_url = format!("https://github.com/{}/{}.git", repo, repo);
    
    // 1. 临时目录克隆
    let work_tree = git2::Repository::clone(&repo_url, temp_dir.path().to_str().unwrap())?;
    
    // 2. 定位 SKILL.md
    let skill_dir = find_skill_directory(temp_dir.path())?;
    
    // 3. 计算 hash
    let hash = compute_hash(&skill_dir)?;
    
    // 4. 复制到 SSOT
    let target_dir = self.ssot_dir.join(format!("skill-{}", hash));
    fs_extra::dir::copy(&skill_dir, &self.ssot_dir, &options)?;
    
    // 5. 写入 DB
    self.dao.insert_skill(InstalledSkill {
        id: uuid::Uuid::new_v4().to_string(),
        name: skill.name,
        directory: target_dir.to_string_lossy().to_string(),
        content_hash: hash,
        // ...
    })?;
    
    // 6. 同步到启用的 Agent
    self.sync_to_enabled_agents(&target_dir)?;
    
    Ok(skill)
}
```

### A.2 SkillService::toggle_app 核心流程

```rust
// 切换某个 Agent 对技能的启用
pub async fn toggle_app(&self, skill_id: &str, app: &str, enabled: bool) -> Result<()> {
    // 1. 读取技能记录
    let mut skill = self.dao.get_skill(skill_id)?;
    
    // 2. 更新 apps JSON
    let mut apps: SkillApps = serde_json::from_str(&skill.apps)?;
    match app {
        "claude" => apps.claude = enabled,
        "codex" => apps.codex = enabled,
        // ...
    }
    skill.apps = serde_json::to_string(&apps)?;
    
    // 3. 持久化
    self.dao.update_skill(&skill)?;
    
    // 4. 同步文件系统
    if enabled {
        self.sync_skill_to_app(&skill, app)?;
    } else {
        self.unsync_skill_from_app(&skill, app)?;
    }
    
    Ok(())
}
```

---

## 附录 B: 各 Agent 技能加载机制参考

| Agent | 技能加载方式 | 同步目标 |
|-------|------------|---------|
| **Claude Code** | 读取 `~/.claude/SKILLS.md` 或 `~/.claude/skills/` 目录 | SSOT → `~/.claude/` |
| **Claude Desktop** | 读取 `~/.claude/SKILLS.md` | 同上 |
| **Cursor** | 读取项目 `.cursor/rules/` 或全局规则 | SSOT → Cursor rules 目录 |
| **Trae** | 类似 Cursor，规则目录 | SSOT → Trae rules 目录 |
| **Codex** | 读取 `~/.codex/skills/` 或 `SKILL.md` | SSOT → `~/.codex/` |
| **OpenCode** | 读取 `~/.opencode/skills/` | SSOT → `~/.opencode/` |

> **注意**：以上机制基于常见实践推断，实际实现前需要针对每个 Agent 验证具体路径和格式。

---

*文档生成日期: 2026-06-15*
*基于 CC Switch commit 分析，源码仓库: github.com/aurexjs/tauri-codex-cli*
