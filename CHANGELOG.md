# 更新日志

[English](https://github.com/sugu6/Agentpack/blob/master/CHANGELOG_EN.md) | 简体中文

本项目遵循 [Keep a Changelog](https://keepachangelog.com/zh-CN/1.1.0/) 格式，
版本号遵循 [语义化版本](https://semver.org/lang/zh-CN/)。

## [Unreleased]

## [0.2.4] - 2026-08-16

> **安装方式**：本次更新改为 per-user 安装（装到当前用户目录 `%LOCALAPPDATA%\Programs`）。应用内点「立即安装」即可直接安装，**无需管理员权限**，并会记住上次安装位置。

### 特性

- **更新下载支持暂停/恢复/取消**：下载中可暂停并保留临时文件，继续下载通过 HTTP `Range` 断点续传；取消下载实时清理临时文件，不再误报失败
- **重启并安装**：下载完成后按钮改为「重启并安装」，点击后关闭应用并启动安装程序完成更新
- **关闭保护**：有任务（下载/更新检查等）进行时阻止窗口关闭，并提示「有任务正在进行」
- **NSIS 记住上次安装位置**：升级/重装时恢复上次安装目录，卸载时清理注册表残留
- **设置失败回滚**：保存设置失败时回滚内存配置并同步恢复自动备份 hook，避免「前端已失败但设置/备份静默失效」的不一致

### 变更

- GitHub 技能仓库列举重构：优先走 jsDelivr data API（flat tree），失败自动回退 GitHub API；默认分支兜底（存储分支 ∪ main/master），避免 CDN 限流或分支名不一致导致技能数量锐减
- 更新检查增加 singleflight + 结果缓存：避免高频请求 GitHub API（未认证 60 次/小时/IP）触发限流
- 备份：默认导出目录与快照表均按 retention 清理旧文件/旧快照，防止无限累积；批量导入/恢复期间挂起自动备份 hook（Suppress），避免逐条也快照把历史手动快照挤出配额
- MCP 受管基线：配置文件单条解析失败时保留基线中该受管条目，等配置修复后下一次正常 Load 再清理，避免管理状态与绑定静默丢失
- GitHub 技能安装分支兜底：逐个候选分支尝试，每个候选独立 90 秒子预算，实际命中分支落库
- CI：Windows 签名顺序修复（build → sign exe → package:signed → sign installer），新增 `windows:package:signed` 任务不对已签名 exe 重建
- 市场请求增加请求 ID（requestId）防竞态：切换搜索/加载更多时丢弃过期响应

### 修复

- MCP 表单编辑时丢弃 `sourceId` / 硬编码 `source:manual`，编辑已纳管服务器会丢失来源
- 设置页保存拆分发起的写入竞态（`ensureLoaded` / 待写队列），避免旧快照回滚并发新增数据
- Market / Skill 市场卡片 URL 与 stars 提示未走 i18n
- Skills 视图遗留死变量导致刷新逻辑判断异常
- 技能来源回填：skill 标识缺少 `skill:` 前缀导致仓库来源映射失败
- 备份回滚（rollbackUpdate）误删保留 Agent 的配置文件
- tar/zip 技能解压未应用权限掩码（`&^ 0022`），可能继承过宽的包内权限
- 应用退出时下载 goroutine 未取消导致 `Downloads` 目录残留 `.downloading` 临时文件
- `.gitignore` 忽略路径错误（`build/bin` → 根目录 `bin/`）
- **安装器改为 per-user 级（免 UAC 提权）**：应用内点「立即安装」即可直接拉起安装窗口，无需管理员权限，0.2.3 应用（`cmd start`）普通权限下也能安装，不再依赖 `ShellExecuteW("runas")` 提权
- **NSIS 记住安装位置改为按用户（HKCU）**：per-user 安装下读写当前用户自己的 HKCU，升级/重装可还原上次安装目录

## [0.2.3] - 2026-08-13

### 特性

- **技能来源自动回填**：启动后后台自动为缺少仓库来源的技能从 skills.sh 查询候选仓库，验证 SKILL.md 内容后补全来源；仅接受目录名一致且内容验证通过的匹配，避免误关联。设置页新增手动回填按钮，成功时通知，失败静默跳过（skip-not-fail）
- **市场页内容级已安装匹配**：以 SKILL.md 内容哈希（与市场侧同一算法）精确识别已安装技能，替代全目录哈希，修复市场页「已安装」状态失真
- **MCP 纳管基线**：重启后仅恢复此前已纳入管理的 MCP 服务器，其他 agent 配置中的 MCP 不再被静默纳入列表；MCP 扫描对话框按归一化 key 合并多来源条目，新发现的服务器默认不勾选，由用户显式选择加入
- **单实例（生产模式）**：改用 Wails v3 官方单实例机制，二次启动会唤醒主窗口（替代自定义 lockfile 实现）
- **Windows 主题同步**：系统深色/浅色切换通过 v3 内置 SystemThemeChanged 事件同步原生标题栏（替代手动解析 WM_SETTINGCHANGE）

### 变更

- MCP 服务器 ID 改为确定性短哈希（name@path 哈希），不再把完整配置路径（含用户目录名）暴露给前端与数据库
- MCP 配置文件单条 entry 解析失败时仅跳过该条并保留其余条目；写操作拒绝整表重写，避免静默删除无法解析的条目
- Trae / Trae CN 更名为 TraeCode / TraeCode CN（Agent 页面显示名、README 支持表格与 MCP 格式提示同步更新；内部 ID 与检测逻辑保持不变）
- TraeCode / TraeCode CN 新品牌配置目录（AppData\TraeCode、TraeCode CN 等）与 macOS/Linux 安装路径纳入检测
- Agent 检测以安装位置为准：配置目录残留不再作为已安装证据；Windows 注册表检测过滤已卸载残留条目（物理存在校验），MSI 安装的条目按已安装处理
- Windows 更新安装直接 CreateProcess 启动安装器（不经 cmd.exe），避免文件名中 & 等元字符被解释
- 网络请求 User-Agent 统一由应用元数据注入当前版本号（不再硬编码旧版本与旧仓库地址）
- CI 改进：wails3 CLI 固定 v3.0.0-beta.8（与 Wails 库同步升级）；release 新增 Windows 二进制签名与 macOS 签名/公证
- Wails v3 升级到 v3.0.0-beta.8：WebView2 初始化超时与消息泵改进、事件有序分发与背压、Windows 10 1809 原生菜单暗色修复、Linux/GTK 窗口修复等
- README：新增 macOS .app / DMG 打包任务与 DMG 外观定制变量说明，项目结构同步移除已删除的 lockfile 模块

### 修复

- 应用关闭竞态：备份关闭先拒绝新任务再等待，避免 WaitGroup misuse panic；关闭流程中不再启动更新下载
- 更新下载加固：1GB 体积上限、30 分钟总时长与响应头超时，防止恶意/异常源写满磁盘或 goroutine 永久阻塞；修复暂停/取消/续传之间的并发竞态；取消下载不再误报「下载失败」
- zip/tar 技能安装路径穿越防护：远程相对路径须通过校验（拒绝反斜杠文件名、.. 段与绝对路径）才能写入本地
- 加密密钥文件损坏时隔离损坏文件并展示启动错误，不再静默换新密钥（避免旧数据永久无法解密）；密钥文件改为原子写入
- 备份导入遇到同 key 已存在的 MCP 服务器时跳过该条而非中止整个导入
- MCP 同名服务器：扫描加入时配置中已存在同 key 条目则「采纳」而非报错；删除时同名但 key 不同的条目不误删
- ~/.agents/.skill-lock.json 与技能更新缓存的读-改-写并发覆盖（加锁串行化与合并写回）
- 设置保存失败时回滚同步方式与存储位置迁移，内存与磁盘配置保持一致
- npm 全局包检测失败结果缓存，避免每次扫描重复触发 30 秒超时
- MCP 表单注释剥离改为状态机实现：字符串内的 // 与 URL 不再被误删；stdio 提交时剥离 URL
- OpenURL 增加 http/https 白名单校验，拒绝危险 scheme；启动错误信息中的路径脱敏为基名
- Agent 页面 MCP 计数改为反映各 Agent 配置文件实际检测到的服务器数量（含未纳入管理的条目，与扫描对话框去重口径一致），修复 IDE（TraeCode）已配置但未纳管的 MCP 计数偏少/检测不到的问题

## [0.2.2] - 2026-07-30

### 特性

- **轻量模式**：托盘菜单新增「轻量模式」复选框，勾选后隐藏主窗口并主动归还内存（`debug.FreeOSMemory` + Windows `EmptyWorkingSet` 压缩工作集），取消勾选或点击「显示主界面」即可恢复
- 设置页新增「轻量模式」卡片：该开关只负责自动计时（空闲多久后自动进入，默认 5 分钟，范围 1–120 分钟），托盘菜单则始终可手动进出轻量模式。前端监听鼠标/键盘活动并按 30 秒节流上报，空闲计时随每次交互重置；点击「显示主界面」或取消托盘勾选会停用计时器，直到下一次用户活动重新拉起

### 变更

- 托盘菜单「显示主窗口」改名为「显示主界面」
- 版本号配置统一到 `build/config.yml`，移除 `wails.json`（Go embed、CI、发版脚本全部迁移到 `build/config.yml` 读取版本）
- 移除移动端构建配置（`build/android/`、`build/ios/`）和 Docker 交叉编译配置（`build/docker/`），清理 Taskfile 中相关任务
- README 更新：Wails v3 文档链接修正、Agent 表格补全变体与检测方式、下载安装文件名与 CI 构建产物对齐、项目结构同步
- Agents 管理统一到 Agents 页面：启用/禁用按钮改为 `Switch`，设置页移除 Agents 管理卡片
- Skills 更新检查优化：「全部更新」按钮替换「检查更新」位置，避免遮挡；单个 skill 更新按钮图标改为向上箭头
- Skills 页面标题与描述布局调整：标题左对齐，描述同行显示，避免英文过长挤占按钮空间
- MCP 页面标题改为 i18n：`{{ t('nav.mcp') }}`
- Skills 更新提示颜色调整：全部为最新时提示为绿色，发现更新时提示为蓝色

### 修复

- Skills 更新基线污染：查询失败的 skill 不再覆盖本地缓存 baseline，避免误报更新
- Skills 默认分支兼容：空分支先查 `main`，失败后自动回退 `master`
- Skills 错误信息细化：同一仓库多个 skill 共享错误信息，保留失败原因映射

## [0.2.1] - 2026-07-29

### 特性

- 更新下载支持暂停与续传：下载中可暂停，暂停后保留已下载的临时文件，继续下载时通过 HTTP `Range` 请求从断点接续；服务器不支持续传（返回 200 而非 206）时自动删除临时文件从头下载，避免产出损坏的安装包
- 暂停状态可直接删除已下载内容，或关闭弹窗自动清理临时文件
- 更新弹窗显示下载字节数、总大小与实时速度，后端速度字段缺失时前端按事件间隔自行推算

### 变更

- 下载完成不再自动启动安装程序并退出应用，改为显示"立即安装"按钮，由用户确认后调用 `InstallUpdate` 启动安装器并退出（Windows 上运行中的 exe 无法被安装器覆盖，退出仍不可避免）
- 更新弹窗统一为全局 `UpdateDialog` 组件：移除 SettingsView 内嵌的重复弹窗，设置页"更新日志"按钮改为派发 `app:update-available` / `app:show-changelog` 事件；全局弹窗补齐 changelog 相对链接转换、链接跳系统浏览器、Releases 与关闭按钮
- 更新弹窗右上角显示安装包文件名（等宽字体单行显示，标题区在宽度不足时优先收缩）
- 暂停状态下按钮顺序调整为"继续下载"在左、"删除下载"在右，删除按钮改用标准 `destructive` 变体，与下载按钮同尺寸仅颜色为红色
- 路由视图启用 `KeepAlive` 缓存，页面切换不再重新挂载组件

### 修复

- 取消下载时不再同时弹出"下载已取消"与"下载失败"两条冲突提示（取消期间抑制后端 error 事件）
- 下载速度为空时整个区域不渲染导致看起来"无速度显示"的问题，现改为始终渲染并以 `—` 占位
- 续传时进度百分比错误：206 响应的 `Content-Length` 仅为剩余字节数，需叠加已下载偏移量才是文件总大小
- npm 检测子进程在 Windows 上弹出命令行窗口：为 `npm list` 调用设置 `SysProcAttr.HideWindow`

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
[Unreleased]: https://github.com/sugu6/Agentpack/compare/v0.2.4...HEAD
[0.2.4]: https://github.com/sugu6/Agentpack/compare/v0.2.3...v0.2.4
[0.2.3]: https://github.com/sugu6/Agentpack/compare/v0.2.2...v0.2.3
[0.2.2]: https://github.com/sugu6/Agentpack/compare/v0.2.1...v0.2.2
[0.2.1]: https://github.com/sugu6/Agentpack/compare/v0.2.0...v0.2.1
