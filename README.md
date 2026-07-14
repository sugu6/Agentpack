# AgentPack

<p align="center">
  <strong>统一管理 AI 编码工具的 MCP / Skills / Agent 配置</strong>
</p>

<p align="center">
  <a href="./LICENSE">License: MIT</a> ·
  <a href="./CHANGELOG.md">更新日志</a>
</p>

---

## 简介

AgentPack 是一个基于 [Wails v2](https://wails.io)（Go + Vue 3 + TypeScript）构建的跨平台桌面应用，
用于统一管理各类 AI 编码工具的 MCP 服务器、Skills 和 Agent 配置。

支持检测并管理以下 Agent：

| Agent | 类型 | 配置格式 |
| --- | --- | --- |
| Claude Code | CLI | JSON |
| Codex | CLI | TOML |
| Cursor | IDE | JSON |
| OpenCode | CLI / Desktop | JSON |
| Trae | IDE / CN | JSON |

## 功能特性

- **Agent 管理**：自动检测已安装的 AI 编码工具，支持启用/禁用单个 Agent
- **MCP 服务器管理**：增删改查 MCP 服务器，支持多 Agent 绑定与一键扫描
- **Skills 管理**：安装、卸载、更新检查，支持从 GitHub 仓库扫描与 ZIP 导入
- **市场浏览**：集成 Smithery、Official Registry、skills.sh 多个技能市场
- **配置导入/导出**：支持配置备份与在多设备间迁移
- **系统托盘**：轻量模式，窗口隐藏到托盘时暂停后台扫描，降低资源占用
- **自动更新检查**：内置版本检查，对接 GitHub Releases，支持查看更新日志
- **跨平台**：支持 Windows、macOS（Intel / Apple Silicon）、Linux

## 技术栈

- **后端**：Go 1.25+、Wails v2.12
- **前端**：Vue 3、TypeScript、Vite、Tailwind CSS、reka UI
- **数据库**：SQLite（modernc.org/sqlite 纯 Go 实现）
- **图标**：Phosphor Icons

## 环境要求

- [Go](https://go.dev/dl/) 1.25 或更高版本
- [Node.js](https://nodejs.org/) 20+
- [pnpm](https://pnpm.io/) 9+
- [Wails CLI](https://wails.io/docs/gettingstarted/installation) v2.12.0

**平台额外依赖：**

- **Windows**：需要 [WebView2 Runtime](https://developer.microsoft.com/microsoft-edge/webview2/)
- **macOS**：Xcode Command Line Tools
- **Linux**：`libgtk-3-dev libwebkit2gtk-4.1-dev pkg-config libfuse2`

## 快速开始

### 1. 克隆仓库

```bash
git clone https://github.com/sugu6/AgentPack.git
cd AgentPack
```

### 2. 安装依赖

```bash
# 安装 Wails CLI（若尚未安装）
go install github.com/wailsapp/wails/v2/cmd/wails@v2.12.0

# 安装前端依赖
cd frontend && pnpm install && cd ..
```

### 3. 开发模式运行

```bash
wails dev
```

开发模式下会启动 Vite 开发服务器，提供前端热重载。
同时后端 dev server 运行在 `http://localhost:34115`，可在浏览器中调试 Go 方法。

### 4. 构建生产版本

```bash
wails build -clean
```

构建产物位于 `build/bin/` 目录下。

## 项目结构

```
AgentPack/
├── app.go                 # Wails 应用主入口，暴露给前端的方法
├── main.go                # 程序入口
├── tray.go                # 系统托盘实现
├── update.go              # 更新检查（GitHub Releases API）
├── wails.json             # Wails 项目配置
├── cliff.toml             # git-cliff 更新日志生成配置
├── CHANGELOG.md           # 更新日志
├── internal/              # 后端业务逻辑
│   ├── agents/            # Agent 检测与管理
│   ├── backup/            # 备份与导入/导出
│   ├── config/            # 配置管理
│   ├── crypto/            # 环境变量加密
│   ├── database/          # SQLite 数据库
│   ├── market/            # 技能市场（Smithery / Official / skills.sh）
│   ├── mcp/               # MCP 服务器存储
│   └── skills/            # Skills 管理与更新检查
├── frontend/              # Vue 3 前端
│   ├── src/
│   │   ├── views/         # 页面（Agents / MCP / Skills / Market / Settings）
│   │   ├── components/    # UI 组件
│   │   ├── stores/        # Pinia 状态管理
│   │   ├── lib/api.ts     # Wails 绑定封装
│   │   └── composables/   # 组合式函数
│   └── wailsjs/           # Wails 自动生成的绑定
└── build/                 # 各平台构建资源
```

## 下载安装

前往 [Releases 页面](https://github.com/sugu6/AgentPack/releases) 下载对应平台的安装包：

- **Windows**：`AgentPack-windows-amd64.zip`
- **macOS（Intel）**：`AgentPack-macos-intel.dmg`
- **macOS（Apple Silicon）**：`AgentPack-macos-arm64.dmg`
- **Linux**：`AgentPack-linux-amd64.tar.gz` 或 `AppImage`

> macOS 用户首次打开若提示无法验证开发者，请在「系统设置 → 隐私与安全性」中点击「仍要打开」。

## 开源许可

本项目基于 [MIT License](./LICENSE) 开源。

Copyright © 2026 sugu6
