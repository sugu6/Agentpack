# Skills 本地管理设计

日期：2026-06-13

## 背景

AgentPack 当前的 `SkillsView.vue` 只保留静态展示，运行时管理逻辑已移除。cc-switch 的实现提供了可复刻的架构：以一个单一真相源目录保存 skill，再按应用启用状态同步到各工具的 skills 目录。

本设计实现本地管理 MVP，不包含 GitHub 远程发现、skills.sh 搜索、自动更新检测、存储位置切换和备份恢复 UI。

## 目标

- 使用 `~/.agentpack/skills/` 作为 SSOT，skill 实体只保存一份。
- 导入本地 skill 目录，要求目录内存在 `SKILL.md`。
- 解析 `SKILL.md` frontmatter 中的 `name` 和 `description`。
- 同步到所有支持 skills 的 enabled/detected Agent。
- 支持每个 skill 对每个 Agent 单独启用或禁用。
- 同步方式为 Auto：优先目录 symlink，失败后回退 copy。
- 卸载前备份到 `~/.agentpack/skill-backups/`。
- 将前端 Skills 页面从静态展示改为真实管理界面。

## 非目标

- 不实现 GitHub 仓库发现和下载。
- 不实现 SHA-256 远程更新检测 UI。
- 不实现 skills.sh 公共注册表搜索。
- 不实现备份恢复 UI。
- 不为无明确 skills 目录约定的 Agent 做同步。

## 已实现但超出原始设计范围的功能

- **`~/.agents/skills` 统一存储位置**：支持在 `~/.agentpack/skills/` 和 `~/.agents/skills/` 之间切换（`StorageUnified`），通过设置页面的存储位置选择器切换，带完整的数据迁移。
- **存储迁移**：`MigrateStorage()` 方法支持将 SSOT 目录迁移到新路径，自动重新同步所有 Agent 绑定。
- **Skills 扫描**：`ScanUnmanaged()` 发现 Agent 目录中未纳管的 Skill，提供导入入口。

## 架构

新增 `internal/skills` 模块，采用和 `internal/mcp` 类似的 Store 模式：内存状态、SQLite 持久化、文件系统同步、回滚和 Wails API 绑定分层处理。

```text
~/.agentpack/skills/<skill>/SKILL.md
        │
        ▼
internal/skills.Store
        │
        ├─ skills 表：skill 元数据
        ├─ skill_agent_bindings 表：skill 与 Agent 启用关系
        └─ Auto 同步：symlink 优先，失败回退 copy
        │
        ▼
~/.claude/skills/<skill>/
~/.codex/skills/<skill>/
~/.config/opencode/skills/<skill>/
```

### 支持的 Agent

新增能力判断，不支持 skills 的 Agent 返回空目录并被排除。

默认路径：

- Claude Code：`~/.claude/skills/`
- Codex：`~/.codex/skills/`
- OpenCode：`~/.config/opencode/skills/`

Cursor、Trae 等没有明确 skills 目录约定的 Agent 不纳入本次同步。

## 数据模型

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

Go 对外类型：

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
```

## 后端文件设计

### `internal/skills/types.go`

定义：

- `Skill`
- `ImportOptions`
- `UninstallResult`
- `SyncMethod`

### `internal/skills/manifest.go`

职责：解析和校验 `SKILL.md`。

规则：

- 支持 YAML frontmatter 中的 `name` 和 `description`。
- 缺少 frontmatter 时，`name` 回退为目录名。
- `description` 可为空。

### `internal/skills/sync.go`

职责：文件系统同步。

包含：

- `syncToAgentDir(source, dest, method)`
- `createSymlink`
- `copyDirAtomic`
- `removePath`
- `isSymlink`
- `isSymlinkToSSOT`
- `hashDir`

同步策略：

1. 校验 source 是目录并包含 `SKILL.md`。
2. 创建目标父目录。
3. 如果目标存在或是 symlink，先移除。
4. Auto 模式先尝试 symlink。
5. symlink 失败后，复制到临时目录，再 rename 到目标目录。

### `internal/skills/store.go`

核心 API：

```go
func NewStore() *Store
func (s *Store) Load(reg *agents.Registry) error
func (s *Store) List() []Skill
func (s *Store) Import(path string, agentIDs []string, reg *agents.Registry) (Skill, error)
func (s *Store) ToggleAgent(skillID, agentID string, enabled bool, reg *agents.Registry) error
func (s *Store) Uninstall(skillID string, reg *agents.Registry) (UninstallResult, error)
func (s *Store) Resync(reg *agents.Registry) error
```

职责：

- 从 DB 读取 skills 与 bindings。
- 维护内存快照。
- 将导入的 skill 复制到 SSOT。
- 按 bindings 同步到 Agent skills 目录。
- 卸载时备份并删除。
- 同步 DB 与文件系统状态。

## 后端修改设计

### `internal/agents/types.go`

`Adapter` 增加：

```go
SkillsDir() string
```

不支持 skills 的 adapter 返回空字符串。

### `internal/agents/*.go`

为支持的 Agent 实现 `SkillsDir()`：

- Claude Code：`~/.claude/skills`
- Codex：`~/.codex/skills`
- OpenCode：`~/.config/opencode/skills`

### `internal/agents/registry.go`

新增能力查询：

```go
func SkillCapableAgentIDs(reg *Registry) []string
func AgentSkillsDir(agentID string) string
```

这些函数只返回状态为 enabled/detected 且 skills 目录非空的 Agent。

### `app.go`

新增字段：

```go
skillsStore *skills.Store
```

初始化时：

1. 创建 store。
2. 调用 `Load(a.registry)`。
3. 在 Agent 重扫后同步更新 skills store。

新增 Wails 方法：

```go
func (a *App) ListSkills() ([]skills.Skill, error)
func (a *App) ListSkillCapableAgents() ([]*agents.Agent, error)
func (a *App) ImportSkillDirectory(path string, agentIDs []string) (skills.Skill, error)
func (a *App) ToggleSkillAgent(id, agentID string, enabled bool) error
func (a *App) UninstallSkill(id string) (skills.UninstallResult, error)
func (a *App) ResyncSkills() error
```

## 前端设计

### `frontend/src/types/index.ts`

增加 `Skill` 类型。

### `frontend/src/lib/api.ts`

增加 skills API 包装。

### `frontend/src/stores/skills.ts`

Pinia store 状态：

- `skills`
- `skillCapableAgents`
- `loading`

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

## 错误处理

- 导入目录必须存在且包含 `SKILL.md`。
- skill directory 必须是安全单段名称，不允许路径遍历。
- 同名 skill 已存在时拒绝导入。
- Agent 不存在、未启用或不支持 skills 时拒绝绑定。
- 同步前校验 source 中 `SKILL.md` 存在，避免误删或覆盖非 skill 目录。
- copy 使用临时目录 + rename，避免半写入目标目录。
- 卸载前先备份；如果 DB 删除失败，保留备份路径并返回错误。

## 测试计划

后端测试：

- `SKILL.md` frontmatter 解析。
- 无 frontmatter 时 name 回退为目录名。
- 不安全目录名被拒绝。
- 导入复制到 SSOT 并写入 DB。
- Auto 同步在 symlink 不可用时回退 copy。
- Toggle 启用/禁用后目标 Agent 目录变化正确。
- 卸载会备份、删除 SSOT、删除绑定。
- 不支持 skills 的 Agent 不出现在可绑定列表。

验证命令：

```bash
rtk go test ./...
cd frontend && rtk pnpm vue-tsc --noEmit
cd frontend && rtk pnpm build
```

UI 改动后应启动应用或前端开发服务器做手动验证；如果本地环境无法启动，需要明确说明未完成 UI 人工验证。

## 参考

- cc-switch repository: `https://github.com/farion1231/cc-switch`
- cc-switch Skills doc: `docs/user-manual/zh/3-extensions/3.3-skills.md`
- cc-switch key files: `src-tauri/src/services/skill.rs`, `src-tauri/src/commands/skill.rs`, `src-tauri/src/database/dao/skills.rs`
