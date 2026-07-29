# AgentPack

<p align="center">
  <img src="build/appicon.png" width="128" alt="AgentPack Logo" />
</p>

<p align="center">
  <strong>Unified management of MCP / Skills / Agent configurations for AI coding tools</strong>
</p>

<p align="center">
  English | <a href="./README.md">简体中文</a>
  · <a href="./CHANGELOG_EN.md">Changelog</a>
  · <a href="./LICENSE">License: MIT</a>
</p>

---

## Introduction

AgentPack is a cross-platform desktop application built with [Wails v3](https://wails.io)
(Go + Vue 3 + TypeScript) for unified management of MCP servers, Skills, and Agent
configurations across various AI coding tools.

Supported agents:

| Agent | Type | Config Format |
| --- | --- | --- |
| Claude Code | CLI | JSON |
| Codex | CLI | TOML |
| Cursor | IDE | JSON |
| OpenCode | CLI / Desktop | JSON |
| Trae | IDE / CN | JSON |

## Features

- **Agent Management**: Auto-detect installed AI coding tools; enable/disable individual agents
- **MCP Server Management**: Full CRUD for MCP servers with multi-agent binding and one-click scan
- **Skills Management**: Install, uninstall, check updates; scan from GitHub repos and import from ZIP
- **Marketplace**: Integrated Official Registry, skills.sh, GitHub skill marketplaces with infinite scroll
- **Config Import/Export**: Backup configurations and migrate across devices
- **System Tray**: Wails v3 native tray with language change menu updates
- **Auto Update Check**: Built-in version check via GitHub Releases with changelog preview
- **i18n**: Built-in Chinese/English toggle, defaults to system language
- **Cross-Platform**: Windows, macOS (Intel / Apple Silicon), Linux

## Tech Stack

- **Backend**: Go 1.25+, Wails v3
- **Frontend**: Vue 3, TypeScript, Vite, Tailwind CSS, shadcn/vue
- **Database**: SQLite (modernc.org/sqlite, pure Go)
- **Icons**: Phosphor Icons

## Requirements

- [Go](https://go.dev/dl/) 1.25 or higher
- [Node.js](https://nodejs.org/) 20+
- [pnpm](https://pnpm.io/) 9+
- [Wails3 CLI](https://wails.io/docs/next/gettingstarted/installation)

**Platform-specific:**

- **Windows**: [WebView2 Runtime](https://developer.microsoft.com/microsoft-edge/webview2/)
- **macOS**: Xcode Command Line Tools
- **Linux**: `libgtk-3-dev libwebkit2gtk-4.1-dev pkg-config libfuse2`

## Quick Start

### 1. Clone the repository

```bash
git clone https://github.com/sugu6/AgentPack.git
cd AgentPack
```

### 2. Install dependencies

```bash
# Install Wails3 CLI (if not yet installed)
go install github.com/wailsapp/wails/v3/cmd/wails3@latest

# Install frontend dependencies
cd frontend && pnpm install && cd ..
```

### 3. Run in development mode

```bash
wails3 dev
```

Development mode starts the Vite dev server with frontend hot-reload.
The backend dev server runs at `http://localhost:9245` for browser-based Go method debugging.

### 4. Build for production

```bash
# Windows build
wails3 task windows:build

# macOS build
wails3 task darwin:build

# Linux build
wails3 task linux:build

# Generate Windows NSIS installer
wails3 task windows:package
```

Build artifacts are located in `build/bin/`.

## Project Structure

```
AgentPack/
├── app.go                 # Wails app entry, methods exposed to frontend
├── main.go                # Program entry
├── tray.go                # System tray implementation
├── update.go              # Update check (GitHub Releases API)
├── winbridge.go           # Windows theme bridge (Mica / dark mode)
├── devmode_dev.go         # Dev mode configuration
├── devmode_prod.go        # Production mode configuration
├── wails.json             # Wails project config (v2 compat)
├── Taskfile.yml           # Wails v3 build task definitions
├── CHANGELOG.md           # Changelog (Chinese)
├── CHANGELOG_EN.md        # Changelog (English)
├── internal/              # Backend business logic
│   ├── agents/            # Agent detection and management
│   ├── backup/            # Backup and import/export
│   ├── config/            # Configuration management
│   ├── crypto/            # Environment variable encryption
│   ├── database/          # SQLite database
│   ├── dbutil/            # Database utility functions
│   ├── i18n/              # Internationalization (zh-CN / en)
│   ├── iowriter/          # Atomic file writer
│   ├── lockfile/          # Cross-platform file locking
│   ├── logger/            # Logging utility
│   ├── market/            # Skill marketplace (Official / skills.sh / GitHub)
│   ├── mcp/               # MCP server storage
│   ├── shared/            # Shared utility functions
│   ├── skills/            # Skills management and update check
│   └── win32/             # Windows-specific implementation
├── frontend/              # Vue 3 frontend
│   ├── src/
│   │   ├── views/         # Pages (Agents / MCP / Skills / Market / Settings)
│   │   ├── components/    # UI components
│   │   ├── stores/        # Pinia state management
│   │   ├── lib/api.ts     # Wails binding wrapper
│   │   └── composables/   # Composition functions
│   └── bindings/          # Wails v3 auto-generated bindings
├── build/                 # Build resources per platform (v3 Taskfile layout)
│   ├── config.yml         # Wails v3 build config
│   ├── windows/           # Windows installer resources
│   ├── darwin/            # macOS build resources
│   ├── linux/             # Linux build resources
│   └── docker/            # Cross-compilation Docker images
└── scripts/               # Build and release scripts
```

## Download & Install

Visit the [Releases page](https://github.com/sugu6/AgentPack/releases) to download the installer for your platform:

- **Windows**: `AgentPack-windows-amd64.zip` or `AgentPack-windows-amd64-installer.exe` (NSIS installer)
- **macOS (Intel)**: `AgentPack-macos-intel.dmg`
- **macOS (Apple Silicon)**: `AgentPack-macos-arm64.dmg`
- **Linux**: `AgentPack-linux-amd64.tar.gz` or `AppImage`

> macOS users: if you see an "unverified developer" warning on first launch, go to
> "System Settings → Privacy & Security" and click "Open Anyway".

## License

This project is open-sourced under the [MIT License](./LICENSE).

Copyright © 2026 sugu6