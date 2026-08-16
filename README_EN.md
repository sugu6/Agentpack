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

AgentPack is a cross-platform desktop application built with [Wails v3](https://v3.wails.io)
(Go + Vue 3 + TypeScript) for unified management of MCP servers, Skills, and Agent
configurations across various AI coding tools.

Supported agents:

| Agent | Variant | Config Format |
| --- | --- | --- |
| Claude Code | CLI / Desktop | JSON |
| Codex (OpenAI) | CLI / Desktop | TOML |
| Cursor | IDE | JSON |
| OpenCode | CLI / Desktop | JSON |
| TraeCode | IDE | JSON |
| TraeCode CN | IDE | JSON |

> Desktop variants are detected on Windows only (via registry / UWP package detection); CLI variants via npm global packages or PATH commands; IDE variants via registry / application directory detection.

## Features

- **Agent Management**: Auto-detect installed AI coding tools; enable/disable individual agents
- **MCP Server Management**: Full CRUD for MCP servers with multi-agent binding and one-click scan
- **Skills Management**: Install, uninstall, check updates; scan from GitHub repos and import from ZIP
- **Marketplace**: Integrated Official Registry, skills.sh, GitHub skill marketplaces with infinite scroll
- **Config Import/Export**: Backup configurations and migrate across devices
- **System Tray**: Wails v3 native tray with language change menu updates
- **Lite Mode**: One-click tray toggle to hide window and free memory; supports auto-entry on idle (configurable 1–120 min)
- **Auto Update Check**: Built-in version check via GitHub Releases with pause/resume download
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
- [Wails3 CLI](https://v3.wails.io/quick-start/installation)

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
# Windows build (produces bin/AgentPack.exe)
wails3 task windows:build

# macOS build (produces bin/AgentPack)
wails3 task darwin:build

# macOS Universal build (Intel + Apple Silicon merged)
wails3 task darwin:build:universal

# Linux build (produces bin/AgentPack)
wails3 task linux:build

# Windows NSIS installer (depends on windows:build)
wails3 task windows:package

# macOS packaging (run on macOS; depends on darwin:build)
wails3 task darwin:package          # produces .app bundle
wails3 task darwin:package:universal  # package .app with the universal binary

# Linux packaging (produces AppImage / deb / rpm / Arch packages)
wails3 task linux:package
```

Build artifacts are located in `bin/`.

## Project Structure

```
AgentPack/
├── app.go                 # Wails app entry, methods exposed to frontend
├── main.go                # Program entry
├── tray.go                # System tray implementation
├── lite.go                # Lite mode core logic (idle timer, memory release)
├── update.go              # Update check (GitHub Releases API)
├── winbridge.go           # Windows theme bridge (Mica / dark mode)
├── winbridge_stub.go      # Stub for non-Windows platforms
├── devmode_dev.go         # Dev mode configuration
├── devmode_prod.go        # Production mode configuration
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
│   └── linux/             # Linux build resources
└── scripts/               # Build and release scripts
```

## Download & Install

Visit the [Releases page](https://github.com/sugu6/AgentPack/releases) to download the installer for your platform:

**Windows**
- `AgentPack-{version}-windows-amd64.zip` — Portable
- `AgentPack-{version}-windows-amd64-installer.exe` — NSIS installer
- `AgentPack-{version}-windows-arm64.zip` — ARM64 portable
- `AgentPack-{version}-windows-arm64-installer.exe` — ARM64 NSIS installer

**macOS**
- `AgentPack-{version}-macos-universal.dmg` — Universal (Intel + Apple Silicon)
- `AgentPack-{version}-macos-universal.zip` — Portable

**Linux**
- `AgentPack-{version}-linux-amd64.tar.gz` — Portable
- `AgentPack-{version}-linux-amd64.AppImage` — AppImage
- `AgentPack-{version}-linux-arm64.tar.gz` — ARM64 portable
- `AgentPack-{version}-linux-arm64.AppImage` — ARM64 AppImage

> macOS users: if you see an "unverified developer" warning on first launch, go to
> "System Settings → Privacy & Security" and click "Open Anyway".

## License

This project is open-sourced under the [MIT License](./LICENSE).

Copyright © 2026 sugu6