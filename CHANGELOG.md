# 更新日志

本项目遵循 [Keep a Changelog](https://keepachangelog.com/zh-CN/1.1.0/) 格式，
版本号遵循 [语义化版本](https://semver.org/lang/zh-CN/)。

## [Unreleased]

## [0.1.0] - 2026-07-14

### 首个公开版本

AgentPack —— 统一管理 AI 编码工具的 MCP / Skills / Agent 配置的跨平台桌面应用。

#### Agent 管理
- 自动检测已安装的 AI 编码工具：Claude Code、Codex、Cursor、OpenCode、Trae（含国内版）
- 支持 CLI / Desktop / IDE 多种变体检测
- 单个 Agent 启用/禁用，状态持久化
- `AgentToggleButton` 可复用组件，统一各视图中 Agent 启用/禁用交互

#### MCP 服务器管理
- MCP 服务器增删改查，支持 JSON / TOML 配置格式
- 多 Agent 绑定，一键将 MCP 服务器应用到多个 Agent
- 一键扫描已安装 Agent 的 MCP 配置
- 环境变量加密存储（EncryptEnv / DecryptEnv）

#### Skills 管理
- Skills 安装、卸载、更新检查
- 支持从 GitHub 仓库扫描 Skills（可配置仓库列表，支持增删改）
- 支持 ZIP 包导入安装
- 支持 symlink / copy 两种同步方式
- 支持 agentpack / unified 两种存储模式
- 文件系统作为单一数据源，避免 DB 与文件状态不一致

#### 市场浏览
- 集成多个技能市场：Smithery、Official Registry、skills.sh
- 搜索与浏览 MCP 服务器和 Skills
- 一键安装市场资源到指定 Agent
- 带版本化缓存键，避免旧结果阻塞代码修复

#### 配置与备份
- 配置导入/导出，支持在多设备间迁移
- 自动备份与手动备份，支持保留数量配置
- 备份快照管理与恢复
- 导入时可选择应用范围（Agent 状态 / 设置 / MCP / Skills）

#### 系统托盘
- 系统托盘（右键展开菜单），包含显示窗口和退出

#### 窗口行为
- `WindowAction` 设置：关闭窗口时退出 / 最小化到托盘 / 询问
- `beforeClose` 拦截，关闭前弹窗确认
- `Quit` / `HideWindow` / `ShowWindow` 方法

#### 更新检查
- 内置版本检查，调用 GitHub Releases API
- 应用内查看更新日志，支持跳转下载页
- 版本号通过 `go:embed wails.json` 编译时嵌入
- 语义化版本比较

#### 用户界面
- Vue 3 + TypeScript + Tailwind CSS + shadcn/vue
- 暗色 / 亮色 / 系统主题跟随
- Windows Mica 原生半透明效果（Wails v2.12 BackdropType）
- 统一 Select 等控件暗色模式适配
- 状态栏、侧边栏、标题栏布局
- 确认对话框与 Toast 通知

#### 跨平台
- Windows（amd64）
- macOS（Intel / Apple Silicon）
- Linux（amd64，含 AppImage）

#### 安全性
- 路径遍历防护（ExportToFile / TomlBackend 校验路径在安全基目录内）
- 命令字段危险 shell 元字符校验（validateCommand）
- WriteAtomic 使用随机临时文件名防止符号链接攻击
- zip 炸弹防护（单文件 10MB / 总计 50MB / 文件数 1000）
- symlink 目标校验，确保在源目录内
- 事务语义保证（WithTransaction 使用 committed 标志）

#### 持续集成
- GitHub Actions 多平台构建（Windows / macOS / Linux）
- tag 推送自动生成 CHANGELOG.md 并提交回仓库
- 自动创建 GitHub Release，附加更新日志与构建产物

---

# Changelog (English)

This project follows the [Keep a Changelog](https://keepachangelog.com/en/1.1.0/) format,
and adheres to [Semantic Versioning](https://semver.org/).

## [Unreleased]

## [0.1.0] - 2026-07-14

### First Public Release

AgentPack — A cross-platform desktop app for unified management of MCP / Skills / Agent configurations across AI coding tools.

#### Agent Management
- Auto-detect installed AI coding tools: Claude Code, Codex, Cursor, OpenCode, Trae (including CN variant)
- Support CLI / Desktop / IDE variant detection
- Enable/disable individual agents with persisted state
- Reusable `AgentToggleButton` component for consistent agent toggle interactions across views

#### MCP Server Management
- Full CRUD for MCP servers, supporting JSON / TOML config formats
- Multi-agent binding — apply an MCP server to multiple agents at once
- One-click scan of installed agents' MCP configurations
- Encrypted environment variable storage (EncryptEnv / DecryptEnv)

#### Skills Management
- Install, uninstall, and check for skill updates
- Scan skills from configurable GitHub repositories (add / edit / remove repos)
- Import and install from ZIP archives
- Symlink or copy sync methods
- Agentpack or unified storage modes
- File system as the single source of truth — no DB/file state inconsistency

#### Marketplace
- Integrated skill markets: Smithery, Official Registry, skills.sh
- Search and browse MCP servers and Skills
- One-click install of marketplace resources to specified agents
- Versioned cache keys to prevent stale results from blocking code fixes

#### Config & Backup
- Config import/export for migration across devices
- Auto and manual backups with retention count configuration
- Backup snapshot management and restoration
- Selective import scope (Agent status / Settings / MCP / Skills)

#### System Tray
- System tray (right-click to expand menu) with show window and quit

#### Window Behavior
- `WindowAction` setting: exit / minimize to tray / ask on close
- `beforeClose` interception with confirmation dialog
- `Quit` / `HideWindow` / `ShowWindow` methods

#### Update Check
- Built-in version check via GitHub Releases API
- In-app changelog viewer with download page link
- Version embedded at compile time via `go:embed wails.json`
- Semantic version comparison

#### User Interface
- Vue 3 + TypeScript + Tailwind CSS + shadcn/vue
- Dark / Light / System theme following
- Windows Mica native translucent effect (Wails v2.12 BackdropType)
- Unified dark mode adaptation for Select and other controls
- Status bar, sidebar, and title bar layout
- Confirmation dialogs and toast notifications

#### Cross-Platform
- Windows (amd64)
- macOS (Intel / Apple Silicon)
- Linux (amd64, with AppImage)

#### Security
- Path traversal protection (ExportToFile / TomlBackend validate paths within a secure base directory)
- Dangerous shell metacharacter validation for command fields (validateCommand)
- WriteAtomic uses random temporary filenames to prevent symlink attacks
- Zip bomb protection (10MB per file / 50MB total / 1000 file count)
- Symlink target validation to ensure containment within the source directory
- Transaction semantics (WithTransaction uses a committed flag)

#### Continuous Integration
- GitHub Actions multi-platform builds (Windows / macOS / Linux)
- Auto-generate CHANGELOG.md on tag push and commit back to the repo
- Auto-create GitHub Release with changelog and build artifacts attached
