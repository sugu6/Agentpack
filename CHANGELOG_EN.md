# Changelog

English | [简体中文](./CHANGELOG.md)

This project follows [Keep a Changelog](https://keepachangelog.com/en/1.1.0/) format,
versioned by [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

## [0.1.2] - 2026-07-15

### Features

- Sidebar Skills nav item now shows a badge with the installed skill count
- Auto-prompt update changelog dialog when a new version is found, with in-dialog download button
- New Release CI workflow: enter a version number in GitHub Actions to auto-bump versions, transform CHANGELOG, create tag and trigger packaging

### Changed

- Agent variant labels (CLI / Desktop / IDE / Config) no longer use i18n translation, now hardcoded in English
- Removed `agents.variant` i18n keys from `en.json` / `zh-CN.json`
- Auto update check moved to Settings page entry (once per session), no longer triggers on app startup
- Removed "Open file" button after download completes (download auto-installs, button was unused)
- Cleaned up orphaned `OpenDownloadedFile` backend method and frontend API binding
- Dialog close button (X) reverted to original minimal style (`opacity-70` + `hover:opacity-100`), removed incorrectly added borders

### Fixed

- Added missing `LocalHash` field to `UpdateStatus` struct for frontend local hash display
- Tests restored save/restore of `config.DefaultGitHubProxy` to prevent global state pollution across tests
- Windows incorrectly matching Linux packages: `matchPlatformAsset` used underscores (`windows_amd64`) but release assets use hyphens (`windows-amd64`), switched to hyphen-based matching; also added macOS alias (`darwin` → `macos`) and OS-only fallback logic
- Download path changed from temp dir to system Downloads folder (XDG compliant), `XDG_DOWNLOAD_DIR` takes priority over `~/Downloads`
- Windows auto-install now uses `cmd /c start` to fully detach child process, preventing installer termination on app exit; added UAC elevation support
- Download writes to `.downloading` temp file first, then renames to final name on success, preventing concurrent download conflicts
- Wait 1 second after download before quitting to ensure installer starts
- Added `XDG_DOWNLOAD_DIR` env var support for macOS downloads
- Market MCP detail dialog close caused list scroll position offset: reka-ui Dialog's focus restoration triggers browser scroll, now saves and restores scroll position
- `autoUpdateChecked` variable was inside `<script setup>`, resetting on every component remount; moved to a separate `<script>` block for true module-level persistence
- Release workflow's `${{ inputs.version }}` direct shell interpolation was an injection risk; switched to env var passing + `[[ =~ ]]` whole-string matching
- CHANGELOG footer compare links pointed to wrong repo `JetBrains/AgentPack`; release script now auto-fixes to `sugu6/Agentpack`
- Chinese CHANGELOG [0.1.0] section had untranslated English entries; all translated to Chinese

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
- Check update routing GitHub API through `gh-proxy.com` caused 403 rate limiting (shared proxy IP), never fetching release data (API calls now direct-connect; downloads still proxied)
- Check update toast falsely showing "latest version" on rate-limit/network errors; now displays backend message
- Missing `assets` field in `githubRelease` struct, download URL not passed to frontend
- "Don't remind" checkbox in close dialog not saving `windowNoRemind`
- `StartDownloadUpdate` missing HTTP status check and progress events
- Download URL not going through proxy
- GitHub proxy URL concatenation error (double https)
- Missing `config.DefaultGitHubProxy` constant

### CI

- Added i18n key consistency check to CI workflow
- CI no longer auto-generates CHANGELOG.md with git-cliff; release notes extracted from manually maintained CHANGELOG.md

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

[0.1.2]: https://github.com/sugu6/Agentpack/compare/v0.1.1...v0.1.2
[0.1.1]: https://github.com/sugu6/Agentpack/compare/v0.1.0...v0.1.1
[0.1.0]: https://github.com/sugu6/Agentpack/releases/tag/v0.1.0
[Unreleased]: https://github.com/sugu6/Agentpack/compare/v0.1.2...HEAD
