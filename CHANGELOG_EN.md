# Changelog

English | [简体中文](https://github.com/sugu6/Agentpack/blob/master/CHANGELOG.md)

This project follows [Keep a Changelog](https://keepachangelog.com/en/1.1.0/) format,
versioned by [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

## [0.2.2] - 2026-07-30

### Features

- **Lite mode**: a new "Lite mode" checkbox in the tray menu hides the main window and proactively returns memory to the OS (`debug.FreeOSMemory` plus `EmptyWorkingSet` working-set trimming on Windows). Unchecking it or clicking "Show main window" restores the window
- Settings page: new "Lite mode" card. The toggle only governs the idle timer (how long before lite mode is entered automatically — default 5 minutes, range 1–120), while the tray menu can always enter or leave lite mode manually. The frontend watches mouse/keyboard activity with a 30-second throttle so the countdown resets on every interaction; clicking "Show main window" or unchecking the tray item stops the timer until the next user activity starts it again

### Changed

- Tray menu item "Show main window" was renamed in the Chinese locale (显示主窗口 → 显示主界面)
- Unified version source to `build/config.yml`, removed `wails.json` (Go embed, CI, and release script all migrated to read version from `build/config.yml`)
- Removed mobile build configs (`build/android/`, `build/ios/`) and Docker cross-compilation configs (`build/docker/`), cleaned up related Taskfile tasks
- README updates: corrected Wails v3 doc links, complete agent variant table with detection methods, download filenames aligned with CI build artifacts, project structure updated

## [0.2.1] - 2026-07-29

### Features

- Pause and resume for update downloads: downloads can be paused, the partial temp file is kept, and resuming continues from the offset via an HTTP `Range` request. If the server ignores `Range` (returns 200 instead of 206), the temp file is discarded and the download restarts from scratch to avoid a corrupted installer
- Paused downloads can be deleted directly, and closing the dialog cleans up the temp file automatically
- The update dialog now shows downloaded bytes, total size and live speed; when the backend speed field is missing, the frontend derives it from the interval between progress events

### Changed

- Downloads no longer launch the installer and quit the app automatically. An "Install now" button is shown instead, and `InstallUpdate` launches the installer and quits only after the user confirms (quitting remains unavoidable since a running exe cannot be replaced by the installer on Windows)
- Consolidated the update dialog into the global `UpdateDialog` component: removed the duplicate dialog embedded in SettingsView, and the "Changelog" button now emits `app:update-available` / `app:show-changelog`. The global dialog gained changelog relative-link rewriting, opening links in the system browser, and the Releases and Close buttons
- The installer filename is shown in the dialog's top-right corner (single line, monospace; the title area shrinks first when width is tight)
- In the paused state, "Resume download" now sits left of "Delete download", and the delete button uses the standard `destructive` variant — same size as the download button, only red
- Route views are wrapped in `KeepAlive`, so switching pages no longer remounts components

### Fixed

- Cancelling a download no longer shows conflicting "Download cancelled" and "Download failed" toasts at once (backend error events are suppressed while cancelling)
- Download speed appeared missing because the whole element was skipped when the value was empty; it now always renders with a `—` placeholder
- Wrong progress percentage when resuming: a 206 response's `Content-Length` only covers the remaining bytes, so the existing offset must be added to get the full file size
- npm detection subprocesses opened a console window on Windows: `npm list` calls now set `SysProcAttr.HideWindow`

## [0.2.0] - 2026-07-29

### Features

- **Wails v3 migration**: Upgraded from Wails v2 to v3 with new Taskfile-based build system (`wails3 task`), supporting cross-platform dev/build/package
- Windows Mica material fix: Added `winbridge.go` to patch v3's `BackgroundTypeTranslucent` background brush issue, achieving same transparent window effect as v2
- Native Windows theme bridge: Runtime theme switching via `DwmSetWindowAttribute(DWMWA_USE_IMMERSIVE_DARK_MODE)` with auto-follow on system theme change (`WM_SETTINGCHANGE` listener)
- Native v3 system tray: Replaced third-party `energye/systray` with Wails v3 native `SystemTray` API, supports language change menu updates
- Infinite scroll loading: Market and Skills pages support auto-loading next page on scroll, replacing manual "load more" buttons
- Market server list cache: Restoring cached homepage results on search clear without re-querying API
- Registry pagination: Official source supports `cursor`-based pagination, `hasMore`/`nextPage` passed to frontend
- Registry tag enhancement: Collecting publisher-provided categories and keywords as search tags
- Command derivation: Auto-derive command from `registryType` when `runtimeHint` is empty (npm→npx -y, pypi→uvx, oci→docker run)
- Registry dedup: Search deduplicates by name preferring `isLatest=true`; browse shows only latest entries
- Skills CDN fallback: When jsDelivr CDN fails to fetch SKILL.md, return fallback data (`Name=directory`) instead of skipping the skill
- Agent list enhancement: Settings page includes `allMergedGroups` with `not_found` agents
- Git env optimization: Set `GIT_TERMINAL_PROMPT=0` to prevent GCM from blocking git operations

### Changed

- Frontend binding migration: from `wailsjs` directory to `bindings/agentpack/` path (ES Module imports), runtime API migrated to `@wailsio/runtime`
- Events API: using `Events.On`/`Events.Off`/`Events.Emit` namespace, `Events.Emit` params wrapped as array
- Service lifecycle: `startup`/`shutdown`/`beforeClose` → `ServiceStartup`/`ServiceShutdown`, window close via `RegisterHook`
- Window creation: v3 uses `application.WebviewWindowOptions` separating window and theme config
- Build system migration: from `wails.json` single config to `Taskfile.yml` + `build/Taskfile.yml` layered architecture
- CI/CD adaptation: GitHub Actions build from `wails build` to `wails3 task` per-platform, Linux deps updated to webkit2gtk-4.1
- Windows NSIS installer: packaging split into `windows:build` + `windows:package` steps
- Settings page layout: removed global padding, fixed header + scrollable content area
- Changelog dialog links: opened via `OpenURL` in system browser, not in WebView
- Version config: maintained both `wails.json` (v2 compat) and `build/config.yml` (v3 native), synced by release.mjs
- Installed card green background opacity reduced from 10% to 5% (`!bg-emerald-500/10` → `!bg-emerald-500/5`)
- Vite build warnings cleaned: `onLog` suppresses third-party `__PURE__` warnings, `settings.ts` dynamic import changed to static import

### Fixed

- `boundAgents` nullable: MCP and Skill `boundAgents` typed as `string[] | null`, frontend adds null checks
- Skills migration scroll offset: Dialog gets `:scroll-root` prop to fix scroll offset on focus restore
- Changelog links: relative paths (`./CHANGELOG.md`) converted to GitHub absolute URLs
- v3 dev port mismatch: `wails3 dev` uses port 9245, Vite dev server port unified
- Skills CDN fallback: SKILL.md fetch failure no longer skips skill, keeps market display complete
- Registry `items: null` defense: frontend `Array.isArray` check prevents `...more.items` spread crash
- Non-search state loadMore syncs baseServers cache
- Registry dedup: same-name server prefers `isLatest=true` version, search also deduplicates
- `registryType` tag added to MarketServer (e.g., npm/pypi)
- Remote server transport: correctly set `streamable-http` type (previously not distinguished from sse)
- Skills page `boundAgents` null causing component crash
- Linux AppImage `.desktop` file path corrected to v3 generated path
- Skills market loading slow: `populateContentHashes` changed from serial to concurrent (5 workers), reducing 50 skills from 50-100s to ~10s
- Missing skill descriptions: `populateContentHashes` now parses SKILL.md frontmatter to fill Name and Description for skills.sh items
- Duplicate CDN requests for GitHub skills: `fetchSkillMeta` computes ContentHash from already-fetched content, avoiding a second CDN request in `populateContentHashes`

### CI

- GitHub Actions build commands migrated to `wails3 task <platform>:build`
- Added `wails3 task windows:package` step for NSIS installer generation
- macOS/Linux build tasks separated into independent steps
- Release script now syncs `build/config.yml` version

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
[Unreleased]: https://github.com/sugu6/Agentpack/compare/v0.2.2...HEAD
[0.2.2]: https://github.com/sugu6/Agentpack/compare/v0.2.1...v0.2.2
[0.2.1]: https://github.com/sugu6/Agentpack/compare/v0.2.0...v0.2.1
