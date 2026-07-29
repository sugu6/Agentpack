# 更新日志

[English](https://github.com/sugu6/Agentpack/blob/master/CHANGELOG_EN.md) | 简体中文

本项目遵循 [Keep a Changelog](https://keepachangelog.com/zh-CN/1.1.0/) 格式，
版本号遵循 [语义化版本](https://semver.org/lang/zh-CN/)。

## [Unreleased]

## [0.2.0] - 2026-07-29

### 特性

- **Wails v3 迁移**：项目从 Wails v2 升级到 v3，采用全新的 Taskfile 构建系统（`wails3 task`），支持跨平台开发/构建/打包
- Windows Mica 材料效果修复：通过 `winbridge.go` 修补 v3 的 `BackgroundTypeTranslucent` 背景画刷问题，实现与 v2 完全一致的透明窗口效果
- Windows 原生主题桥接：通过 `DwmSetWindowAttribute(DWMWA_USE_IMMERSIVE_DARK_MODE)` 实现运行时主题切换和系统主题自动跟随（`WM_SETTINGCHANGE` 监听）
- v3 原生系统托盘：替换第三方 `energye/systray`，使用 Wails v3 原生 `SystemTray` API，支持语言切换时更新托盘文案
- 前端无限滚动加载：Market 和 Skills 页面支持滚动到底部自动加载下一页，替代手动"加载更多"按钮
- 市场服务器列表缓存：清空搜索框时直接恢复缓存的首页结果，不再重复调用 API
- Registry 分页支持：Official 来源支持 `cursor` 分页查询，`hasMore`/`nextPage` 传递给前端
- Registry 标签增强：收集 Publisher 提供的 categories 和 keywords 作为搜索标签
- 命令派生优化：当 `runtimeHint` 为空时，根据 `registryType` 自动派生命令（npm→npx -y, pypi→uvx, oci→docker run）
- Skills CDN 降级：jsDelivr CDN 拉取 SKILL.md 失败时返回降级数据（`Name=directory`），不再跳过该 skill，解决 CDN 限流导致 skills 数量锐减的问题
- Agent 列表增强：设置页面新增 `allMergedGroups`，包含 `not_found` agent 的完整分组
- Git 命令环境优化：设置 `GIT_TERMINAL_PROMPT=0` 防止 GCM 弹窗拦截 git 操作

### 变更

- 前端绑定机制变更：从 `wailsjs` 目录迁移到 `bindings/agentpack/` 路径（ES Module 导入），运行时 API 迁移到 `@wailsio/runtime`
- 事件系统 API 变更：使用 `Events.On`/`Events.Off`/`Events.Emit` 命名空间，`Events.Emit` 参数改为数组包装
- 服务生命周期变更：`startup`/`shutdown`/`beforeClose` 改为 `ServiceStartup`/`ServiceShutdown`，窗口关闭通过 `RegisterHook` 拦截
- 窗口创建方式变更：v3 使用 `application.WebviewWindowOptions` 分离窗口和主题配置
- 构建系统迁移：从 `wails.json` 单一配置迁移到 `Taskfile.yml` + `build/Taskfile.yml` 分层架构
- CI/CD 适配：GitHub Actions 构建命令从 `wails build` 改为 `wails3 task` 平台特定任务，Linux 依赖更新为 webkit2gtk-4.1
- Windows NSIS 安装包：打包流程拆分为 `windows:build` + `windows:package` 两个独立步骤
- 设置页面布局：移除全局 padding，使用固定头部 + 可滚动内容区域
- 更新日志弹窗链接：点击时通过 `OpenURL` 在系统浏览器打开，不再在 WebView 内跳转
- 版本号配置：同时维护 `wails.json`（v2 兼容）和 `build/config.yml`（v3 原生），由 release.mjs 同步更新
- 已安装卡片绿色背景透明度从 10% 降低到 5%（`!bg-emerald-500/10` → `!bg-emerald-500/5`）
- Vite 构建警告清理：`onLog` 抑制第三方库 `__PURE__` 警告，`settings.ts` 动态导入改为静态导入

### 修复

- `boundAgents` 可为空：MCP 和 Skill 的 `boundAgents` 字段类型为 `string[] | null`，前端添加空检查
- Skills 目录迁移后页面滚动错位：为 Dialog 添加 `:scroll-root` 属性指定滚动容器，修复焦点恢复导致的滚动偏移
- 更新日志中文/英文链接：修复相对路径（`./CHANGELOG.md`）未转换为 GitHub 绝对 URL 的问题
- v3 开发模式端口不匹配：`wails3 dev` 使用 `9245` 端口，Vite 开发服务器端口统一配置
- 市场 Skill 卡片 CDN 降级：SKILL.md 拉取失败时不跳过 skill，保持市场中完整显示
- Registry `items: null` 防御：前端增加 `Array.isArray` 防御，避免空响应导致 `...more.items` 展开崩溃
- 非搜索状态下 loadMore 同步更新 baseServers 缓存
- Registry 去重优化：同名 server 优先使用 `isLatest=true` 版本，搜索模式下也执行去重
- `registryType` 标签：将 registryType（如 npm/pypi）作为标签添加到 MarketServer
- 远程服务器 transport：正确设置 `streamable-http` 类型（之前未区分 streamable-http 和 sse）
- Skills 页面 `boundAgents` 空值导致组件崩溃
- Linux AppImage 构建 `.desktop` 文件路径修正为 v3 生成路径
- Skills 市场加载缓慢：`populateContentHashes` 串行逐个从 CDN 拉取 SKILL.md 改为并发 5 路，50 个 skill 从 50-100 秒降至 ~10 秒
- Skills 描述缺失：`populateContentHashes` 拉取 SKILL.md 后同时解析 frontmatter 补全 skills.sh 来源缺失的 Name 和 Description
- GitHub 来源 skill 重复 CDN 请求：`fetchSkillMeta` 中直接从已拉取内容计算 ContentHash，避免后续 `populateContentHashes` 再次请求

### CI

- GitHub Actions 构建命令迁移到 `wails3 task <platform>:build`
- 新增 `wails3 task windows:package` 步骤生成 NSIS 安装包
- macOS/Linux 构建任务分离为独立步骤
- Release 脚本支持同步更新 `build/config.yml` 版本号

## [0.1.2] - 2026-07-15

### 特性

- Sidebar Skills 导航项添加数量角标，显示已安装数量
- 发现新版本时自动弹出更新日志弹窗，可直接在弹窗内下载安装包，不再需要到页面底部下载
- 新增 Release CI workflow：在 GitHub Actions 输入版本号即可自动更新版本号、转换 CHANGELOG、打 tag 并触发打包

### 变更

- Agent 类型标签（CLI / Desktop / IDE / Config）不再走 i18n 翻译，直接使用英文硬编码
- 删除 `en.json` / `zh-CN.json` 中 `agents.variant` 的 i18n 键
- 自动检查更新改为进入设置页时触发（每个会话仅一次），避免刚开软件就弹更新提示
- 移除下载完成的"打开文件"按钮（下载完成会自动安装，按钮已无用）
- 清理孤立的 `OpenDownloadedFile` 后端方法和前端 API 绑定
- 弹窗关闭按钮（X）恢复为原始简洁样式（`opacity-70` + `hover:opacity-100`），移除错误添加的边框

### 修复

- `UpdateStatus` 结构体缺少 `LocalHash` 字段，补全以便前端展示本地哈希
- 测试用例恢复对 `config.DefaultGitHubProxy` 的保存和重置，防止全局变量修改污染其他测试
- Windows 检测到 Linux 安装包的问题：`matchPlatformAsset` 使用了下划线（`windows_amd64`）但 release asset 名称使用连字符（`windows-amd64`），改回连字符匹配；同时处理 macOS 别名（`darwin` → `macos`）及 OS-only 兜底逻辑
- 下载路径从临时目录改到系统默认 Downloads 文件夹（支持 XDG 规范），XDG_DOWNLOAD_DIR 优先级高于 `~/Downloads`
- Windows 自动安装改用 `cmd /c start` 完全脱离父进程，避免应用退出后子进程被终止；增加 UAC 提权支持
- 下载完成先写入 `.downloading` 临时文件，成功后再重命名为正式文件名，防止并发下载冲突
- 下载完成后等待 1 秒再退出应用，确保安装程序启动完毕
- macOS 下载增加 `XDG_DOWNLOAD_DIR` 环境变量支持
- 市场 MCP 查看弹窗关闭后列表滚动位置偏移：reka-ui Dialog 关闭时的焦点还原会触发浏览器滚动，改为保存并恢复滚动位置
- `autoUpdateChecked` 变量误放在 `<script setup>` 内导致每次组件重新挂载都重置，移到独立 `<script>` 块实现真正的模块级持久化
- Release workflow 的 `${{ inputs.version }}` 直接插值到 shell 存在注入风险，改用 env 变量传参 + `[[ =~ ]]` 整串匹配
- CHANGELOG 底部 compare 链接的 repo URL 错误指向 `JetBrains/AgentPack`，由 release 脚本自动修正为 `sugu6/Agentpack`
- 中文 CHANGELOG 的 [0.1.0] 节存在未翻译的英文条目，已全部翻译为中文

## [0.1.1] - 2026-07-15

### 特性

- 中英文双语支持，设置页可切换语言（中文 / English / 跟随系统）
- 默认跟随系统语言，不支持的语言回退到英文
- 前端 UI + 后端用户可见字符串全量国际化
- 检查更新支持 GitHub 代理（`https://gh-proxy.com/`），解决中国地区无法直连 GitHub 的问题
- 应用内下载安装包，显示下载进度、速度与百分比
- 自动匹配当前平台（GOOS_GOARCH）的安装包
- 启动后自动检查更新（延迟 5 秒）
- 前端版本号改为从后端 API 获取（`GetAppVersion()`）
- 更新日志弹窗支持 Markdown 渲染
- GitHub API 限流友好提示："请求过于频繁，请稍后再试"
- Skills 更新检测：修复首次检查不显示更新、目录回退 Bug、硬编码缓存路径

### 变更

- 窗口关闭行为：默认"最小化到托盘"，"询问"选项移除。新增"不再提醒"复选框
- 更新消息改用 Sonner Toast 在屏幕正上方显示，版本号使用圆角边框突出展示
- 设置页面窗口行为卡片：Tabs 居中，复选框显示在 Tabs 下方

### 修复

- TitleBar 标题与 Sidebar 不一致（`Agent` → `Agents`，`MCP Servers` → `MCP`）
- 切换到英文后切回"跟随系统"语言不生效（`resolveLanguage("")` 误读 localStorage 缓存）
- Skills 页面英文副标题换行显示
- 检查更新通过 `gh-proxy.com` 代理调用 GitHub API 导致 403 限流，永远拿不到 release 数据（API 调用改为直连，下载仍走代理）
- 检查更新 toast 在限流/网络错误时误显"已是最新版本"，现改为如实显示后端 message
- `githubRelease` 结构体缺少 `assets` 字段，导致下载 URL 无法传递到前端
- 点 X 弹窗勾选"不再提醒"时未同步保存 `windowNoRemind` 设置
- `StartDownloadUpdate` 缺少 HTTP 状态码检查和进度事件通知
- 下载 URL 未走代理
- GitHub 代理 URL 拼接错误（双重 https）
- 缺失 `config.DefaultGitHubProxy` 常量

### 持续集成

- CI 增加 i18n 键集一致性校验
- CI 不再用 git-cliff 自动生成 CHANGELOG.md，改为从手动维护的 CHANGELOG.md 提取 release notes

## [0.1.0] - 2026-07-14

AgentPack 的初始版本，一款面向 AI 编码工具的统一 MCP / Skills / Agent 管理桌面应用。

### 特性

- 新增 ARM 平台构建支持，修复右键菜单调试行为

### 修复

- 在 pnpm-workspace.yaml 中添加 packages 字段以修复构建问题
- 取消跟踪生成的 wailsjs/bindings 目录以修复 CI
- 为 Wails v2 安装 libwebkit2gtk-4.0-dev 而非 4.1
- 通过 choco 安装 NSIS 以生成 Windows 安装包
- 将 NSIS 添加到 GITHUB_PATH，确保 wails build 能找到 makensis

### 持续集成

- 用 macos-latest 上的 darwin/universal 构建替代 macos-13 intel 构建

[0.1.2]: https://github.com/sugu6/Agentpack/compare/v0.1.1...v0.1.2
[0.1.1]: https://github.com/sugu6/Agentpack/compare/v0.1.0...v0.1.1
[0.1.0]: https://github.com/sugu6/Agentpack/releases/tag/v0.1.0
[Unreleased]: https://github.com/sugu6/Agentpack/compare/v0.1.2...HEAD
