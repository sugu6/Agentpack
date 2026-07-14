# 更新日志

[English](./CHANGELOG_EN.md) | 简体中文

本项目遵循 [Keep a Changelog](https://keepachangelog.com/zh-CN/1.1.0/) 格式，
版本号遵循 [语义化版本](https://semver.org/lang/zh-CN/)。

## [Unreleased]

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

- 添加 ARM 平台构建支持并修复右键菜单调试行为

### 修复

- Add packages field to pnpm-workspace.yaml to fix build
- Stop tracking generated wailsjs/bindings dirs to fix CI
- Install libwebkit2gtk-4.0-dev for Wails v2 instead of 4.1
- Install NSIS via choco for Windows installer generation
- Add NSIS to GITHUB_PATH so makensis is found by wails build

### 持续集成

- Replace macos-13 intel build with darwin/universal on macos-latest
