# Changelog

English | [简体中文](./CHANGELOG.md)

This project follows [Keep a Changelog](https://keepachangelog.com/en/1.1.0/) format,
versioned by [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

## [0.1.1] - 2026-07-15

### Features

- Bilingual (Chinese / English) support with language switcher in Settings (Chinese / English / Follow system)
- Default follows system language; unsupported languages fall back to English
- Full i18n for frontend UI and backend user-visible strings
- GitHub proxy (`https://gh-proxy.com/`) for check update, fixes China access to GitHub
- In-app download with progress, speed, and percentage display
- Automatic platform asset matching by GOOS_GOARCH
- Auto-check for updates on startup (5-second delay)
- Frontend version fetched from backend API (`GetAppVersion()`)
- Markdown rendering for changelog dialog
- Friendly rate-limit message: "GitHub API 请求过于频繁，请稍后再试"
- Skills update detection: fix first-check-no-update, directory fallback bug, hardcoded cache path

### Changed

- Window close behavior: default "minimize to tray", removed "ask" option. Added "don't remind" checkbox
- Update messages now use Sonner Toast at top-center, version displayed with rounded border badge
- Settings window behavior card: Tabs centered, checkbox below Tabs

### Fixed

- TitleBar titles inconsistent with Sidebar (`Agent` → `Agents`, `MCP Servers` → `MCP`)
- Switching back to "Follow system" from English not working (`resolveLanguage("")` was reading localStorage cache)
- Skills page English subtitle wrapping to multiple lines
- Missing `assets` field in `githubRelease` struct, download URL not passed to frontend
- "Don't remind" checkbox in close dialog not saving `windowNoRemind`
- `StartDownloadUpdate` missing HTTP status check and progress events
- Download URL not going through proxy
- GitHub proxy URL concatenation error (double https)
- Missing `config.DefaultGitHubProxy` constant

### CI

- Added i18n key consistency check to CI workflow

## [0.1.0] - 2026-07-14

Initial release of AgentPack — a unified MCP / Skills / Agent management desktop application for AI coding tools.

### Features

- ARM platform build support and right-click menu debug behavior fix

### Fixed

- Add packages field to pnpm-workspace.yaml to fix build
- Stop tracking generated wailsjs/bindings dirs to fix CI
- Install libwebkit2gtk-4.0-dev for Wails v2 instead of 4.1
- Install NSIS via choco for Windows installer generation
- Add NSIS to GITHUB_PATH so makensis is found by wails build

### CI

- Replace macos-13 intel build with darwin/universal on macos-latest
