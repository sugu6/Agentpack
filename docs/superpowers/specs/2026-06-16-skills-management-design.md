# Skills 本地管理设计

日期：2026-06-16

## 背景

AgentPack 当前的 `SkillsView.vue` 只保留静态展示，运行时管理逻辑已移除。CC Switch 的实现提供了可复刻的架构：以一个单一真相源目录保存 skill，再按应用启用状态同步到各工具的 skills 目录。

本设计实现本地管理 MVP，不包含 GitHub 远程发现、skills.sh 搜索、自动更新检测和备份恢复 UI。

## 目标

- 使用 SSOT 目录保存 skill 副本，支持两个存储位置：
  - `~/.agentpack/skills/`（默认，AgentPack 专用）
  - `~/.agents/skills/`（统一标准路径，与 CC Switch 等工具共享）
- 用户可在设置中切换存储位置，切换时自动迁移已有 skills。
- 导入本地 skill 目录，要求目录内存在 `SKILL.md`。
- 解析 `SKILL.md` frontmatter 中的 `name` 和 `description`。
- 同步到所有支持 skills 的 enabled/detected Agent。
- 支持每个 skill 对每个 Agent 单独启用或禁用。
- 同步方式与 CC Switch 一致，提供三种选项：
  - **Auto**（默认）：优先 symlink，失败回退 copy
  - **Symlink**：仅使用符号链接
  - **Copy**：仅使用文件复制
- 卸载前备份到 `~/.agentpack/skill-backups/`。
- 将前端 Skills 页面从静态展示改为真实管理界面。

## 非目标

- 不实现 GitHub 仓库发现和下载。
- 不实现 SHA-256 远程更新检测 UI。
- 不实现 skills.sh 公共注册表搜索。
- 不实现备份恢复 UI。
- 不为无明确 skills 目录约定的 Agent 做同步。

## 架构

新增 `internal/skills` 模块，采用和 `internal/mcp` 类似的 Store 模式：内存状态、SQLite 持久化、文件系统同步、回滚和 Wails API 绑定分层处理。

```text
~/.agentpack/skills/<skill>/SKILL.md    ← SSOT 位置 A（默认）
~/.agents/skills/<skill>/SKILL.md       ← SSOT 位置 B（可选）
        │
        ▼
internal/skills.Store
        │
        ├─ skills 表：skill 元数据
        ├─ skill_agent_bindings 表：skill 与 Agent 启用关系
        └─ 同步方式：Auto / Symlink / Copy（用户可配置）
        │
        ▼
~/.claude/skills/<skill>/
~/.codex/skills/<skill>/
~/.config/opencode/skills/<skill>/
```

### 存储位置

| 位置 | 路径 | 说明 |
|------|------|------|
| `agentpack` | `~/.agentpack/skills/` | 默认，AgentPack 专用目录 |
| `unified` | `~/.agents/skills/` | 统一标准路径，可与其他工具（如 CC Switch）共享 |

用户在设置中切换时，`MigrateSkillStorage()` 将已有 skill 文件从旧位置迁移到新位置：
1. 优先使用 `os.Rename`（原子操作，同文件系统时零拷贝）。
2. 跨文件系统时回退到 copy + delete。
3. 迁移完成后更新 Store 的 `ssotDir` 并重新同步所有 Agent 目录。

### 同步方式

| 方式 | 行为 |
|------|------|
| `auto`（默认） | 优先 symlink，失败回退 copy |
| `symlink` | 仅使用符号链接，失败则报错 |
| `copy` | 仅使用文件复制 |

### 支持 Skills 的 Agent

新增能力判断，不支持 skills 的 Agent 返回空目录并被排除。

默认路径：

- Claude Code / Claude Code Desktop：`~/.claude/skills/`
- Codex：`~/.codex/skills/`
- OpenCode / OpenCode Desktop：`~/.config/opencode/skills/`

Cursor、Trae 等没有明确 skills 目录约定的 Agent 不纳入本次同步。

## 数据模型

### 配置扩展

在 `internal/config/config.go` 的 `Settings` 结构体中新增：

```go
type Settings struct {
    // ... 现有字段 ...
    SkillStorage   string `json:"skillStorage"`   // "agentpack" | "unified"
    SkillSyncMethod string `json:"skillSyncMethod"` // "auto" | "symlink" | "copy"
}
```

默认值：
- `SkillStorage`: `"agentpack"`
- `SkillSyncMethod`: `"auto"`

### 数据库表

在 `internal/database/db.go` 的 schema 中新增：

```sql
CREATE TABLE IF NOT EXISTS skills (
  id TEXT PRIMARY KEY,
  name TEXT NOT NULL,
  description TEXT,
  directory TEXT NOT NULL UNIQUE,
  content_hash TEXT,
  installed_at INTEGER NOT NULL,
  updated_at INTEGER NOT NULL
);

CREATE TABLE IF NOT EXISTS skill_agent_bindings (
  skill_id TEXT NOT NULL,
  agent_id TEXT NOT NULL,
  enabled INTEGER NOT NULL,
  synced_at INTEGER,
  PRIMARY KEY (skill_id, agent_id)
);

CREATE INDEX IF NOT EXISTS idx_skill_agent_bindings_agent ON skill_agent_bindings(agent_id);
```

### Go 对外类型

```go
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

type StorageLocation string

const (
    StorageAgentpack StorageLocation = "agentpack"
    StorageUnified   StorageLocation = "unified"
)

type ImportOptions struct {
    Path     string   `json:"path"`
    AgentIDs []string `json:"agentIDs"`
}

type UninstallResult struct {
    ID          string `json:"id"`
    BackupPath  string `json:"backupPath,omitempty"`
}

type MigrationResult struct {
    Migrated int      `json:"migrated"`
    Errors   []string `json:"errors,omitempty"`
}
```

## 后端文件设计

### `internal/skills/types.go`

定义：

- `Skill`
- `SyncMethod` / `StorageLocation` 常量
- `ImportOptions`
- `UninstallResult`
- `MigrationResult`

### `internal/skills/manifest.go`

职责：解析和校验 `SKILL.md`。

规则：

- 支持 YAML frontmatter 中的 `name` 和 `description`。
- 缺少 frontmatter 时，`name` 回退为目录名。
- `description` 可为空。

### `internal/skills/sync.go`

职责：文件系统同步。

包含：

- `syncToAgentDir(source, dest, method SyncMethod)` — 根据 SyncMethod 选择同步方式
- `createSymlink(source, dest)`
- `copyDirAtomic(source, dest)` — 复制到临时目录再 rename
- `removePath(path)`
- `isSymlink(path) bool`
- `isSymlinkToSSOT(path, ssotDir) bool`
- `hashDir(path) string`

同步策略（按 SyncMethod）：

1. 校验 source 是目录并包含 `SKILL.md`。
2. 创建目标父目录。
3. 如果目标存在或是 symlink，先移除。
4. **Auto**：先尝试 symlink，失败后 copy。
5. **Symlink**：仅尝试 symlink，失败返回错误。
6. **Copy**：复制到临时目录，再 rename 到目标目录。

### `internal/skills/store.go`

核心 API：

```go
func NewStore(ssotDir string, syncMethod SyncMethod) *Store
func (s *Store) SetSyncMethod(method SyncMethod)
func (s *Store) SetSSOTDir(dir string)
func (s *Store) Load(reg *agents.Registry) error
func (s *Store) List() []Skill
func (s *Store) Import(path string, agentIDs []string, reg *agents.Registry) (Skill, error)
func (s *Store) ToggleAgent(skillID, agentID string, enabled bool, reg *agents.Registry) error
func (s *Store) Uninstall(skillID string, reg *agents.Registry) (UninstallResult, error)
func (s *Store) Resync(reg *agents.Registry) error
func (s *Store) MigrateStorage(targetDir string, reg *agents.Registry) (MigrationResult, error)
```

职责：

- 从 DB 读取 skills 与 bindings。
- 维护内存快照。
- 将导入的 skill 复制到 SSOT。
- 按 bindings 和 SyncMethod 同步到 Agent skills 目录。
- 卸载时备份并删除。
- 同步 DB 与文件系统状态。
- 存储位置迁移时移动文件并重新同步。

### 存储位置解析

```go
func ResolveSSOTDir(location StorageLocation) string {
    home, _ := os.UserHomeDir()
    switch location {
    case StorageUnified:
        return filepath.Join(home, ".agents", "skills")
    default: // StorageAgentpack
        return filepath.Join(home, ".agentpack", "skills")
    }
}
```

## 后端修改设计

### `internal/agents/types.go`

`Adapter` 增加：

```go
SkillsDir() string
```

不支持 skills 的 adapter 返回空字符串。

### `internal/agents/*.go`

为支持的 Agent 实现 `SkillsDir()`：

- Claude Code / Claude Code Desktop：`~/.claude/skills`
- Codex：`~/.codex/skills`
- OpenCode / OpenCode Desktop：`~/.config/opencode/skills`

其他 Agent（Cursor、Trae、Trae CN）返回空字符串。

### `internal/agents/registry.go`

新增能力查询：

```go
func SkillCapableAgentIDs(reg *Registry) []string
func AgentSkillsDir(agentID string) string
```

这些函数只返回状态为 enabled/detected 且 skills 目录非空的 Agent。

### `internal/config/config.go`

`Settings` 新增字段：

```go
SkillStorage    string `json:"skillStorage"`    // "agentpack" | "unified"
SkillSyncMethod string `json:"skillSyncMethod"` // "auto" | "symlink" | "copy"
```

默认值填充到 `DefaultSettings()` 和 `Load()` 中。

### `app.go`

新增字段：

```go
skillsStore *skills.Store
```

初始化时：

1. 根据 `cfg.Settings.SkillStorage` 解析 SSOT 目录。
2. 根据 `cfg.Settings.SkillSyncMethod` 创建 store。
3. 调用 `Load(a.registry)`。
4. 在 Agent 重扫后同步更新 skills store。

新增 Wails 方法：

```go
func (a *App) ListSkills() ([]skills.Skill, error)
func (a *App) ListSkillCapableAgents() ([]*agents.Agent, error)
func (a *App) ImportSkillDirectory(path string, agentIDs []string) (skills.Skill, error)
func (a *App) ToggleSkillAgent(id, agentID string, enabled bool) error
func (a *App) UninstallSkill(id string) (skills.UninstallResult, error)
func (a *App) ResyncSkills() error
func (a *App) MigrateSkillStorage(target string) (skills.MigrationResult, error)
```

`UpdateSettings` 中需检测 `SkillStorage` 和 `SkillSyncMethod` 变更：
- `SkillSyncMethod` 变更：调用 `skillsStore.SetSyncMethod()` 并 `Resync()`。
- `SkillStorage` 变更：调用 `skillsStore.MigrateStorage()` 迁移文件。

## 前端设计

### `frontend/src/types/index.ts`

增加 `Skill` 类型和 `AppSettings` 扩展：

```typescript
export interface Skill {
  id: string
  name: string
  description?: string
  directory: string
  contentHash?: string
  boundAgents: string[]
  installedAt: string
  updatedAt: string
}

export interface AppSettings {
  // ... 现有字段 ...
  skillStorage: 'agentpack' | 'unified'
  skillSyncMethod: 'auto' | 'symlink' | 'copy'
}
```

### `frontend/src/lib/api.ts`

增加 skills API 包装：

```typescript
skills: {
  list: () => toPlainObject(await ListSkills()),
  listCapableAgents: () => toPlainObject(await ListSkillCapableAgents()),
  importDirectory: (path: string, agentIDs: string[]) => ImportSkillDirectory(path, agentIDs),
  toggleAgent: (id: string, agentID: string, enabled: boolean) => ToggleSkillAgent(id, agentID, enabled),
  uninstall: (id: string) => UninstallSkill(id),
  resync: () => ResyncSkills(),
  migrateStorage: (target: string) => MigrateSkillStorage(target),
}
```

### `frontend/src/stores/skills.ts`

Pinia store 状态：

- `skills: Skill[]`
- `skillCapableAgents: Agent[]`
- `loading: boolean`

动作：

- `load()`
- `importSkill(path, agentIDs)`
- `toggleAgent(skillId, agentId, enabled)`
- `uninstall(skillId)`
- `resync()`

### `frontend/src/views/SkillsView.vue`

替换静态展示为真实管理页：

- 顶部统计：已安装 skills 数、支持 skills 的 Agent 数。
- 空状态：提供导入入口。
- Skill 卡片：显示 name、description、directory、bound agents。
- 每个支持 Agent 显示一个 Switch。
- 提供重新同步和卸载操作。

### `frontend/src/views/SettingsView.vue`

在设置页面新增 Skills 配置卡片：

**存储位置**：
- 单选：`~/.agentpack/skills/`（AgentPack 专用）| `~/.agents/skills/`（统一标准）
- 切换时弹出确认对话框，提示将迁移已有 skills

**同步方式**：
- 单选：Auto（推荐）| Symlink | Copy
- 每个选项附带说明文字

## 错误处理

- 导入目录必须存在且包含 `SKILL.md`。
- skill directory 必须是安全单段名称，不允许路径遍历。
- 同名 skill 已存在时拒绝导入。
- Agent 不存在、未启用或不支持 skills 时拒绝绑定。
- 同步前校验 source 中 `SKILL.md` 存在，避免误删或覆盖非 skill 目录。
- copy 使用临时目录 + rename，避免半写入目标目录。
- 卸载前先备份；如果 DB 删除失败，保留备份路径并返回错误。
- Symlink 模式下创建失败时返回明确错误（不回退 copy）。
- 存储迁移过程中如果 copy 失败，保留已迁移的部分并返回错误列表。

## 测试计划

后端测试：

- `SKILL.md` frontmatter 解析。
- 无 frontmatter 时 name 回退为目录名。
- 不安全目录名被拒绝。
- 导入复制到 SSOT 并写入 DB。
- Auto 同步在 symlink 不可用时回退 copy。
- Symlink 模式在 symlink 失败时返回错误。
- Copy 模式使用原子替换。
- Toggle 启用/禁用后目标 Agent 目录变化正确。
- 卸载会备份、删除 SSOT、删除绑定。
- 不支持 skills 的 Agent 不出现在可绑定列表。
- 存储位置迁移正确移动文件并更新 SSOT 目录。
- 同步方式变更后重新同步使用新方式。

验证命令：

```bash
go test ./...
cd frontend && pnpm vue-tsc --noEmit
cd frontend && pnpm build
```

UI 改动后应启动应用或前端开发服务器做手动验证；如果本地环境无法启动，需要明确说明未完成 UI 人工验证。

## 参考

- cc-switch repository: `https://github.com/farion1231/cc-switch`
- cc-switch Skills doc: `docs/user-manual/zh/3-extensions/3.3-skills.md`
- cc-switch key files: `src-tauri/src/services/skill.rs`, `src-tauri/src/commands/skill.rs`, `src-tauri/src/database/dao/skills.rs`
