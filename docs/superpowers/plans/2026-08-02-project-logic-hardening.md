# Project Logic Hardening Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Repair destructive and inconsistent Skill, MCP, settings migration, and backup restore flows with regression coverage.

**Architecture:** Keep the current stores and Wails API boundaries. Add small transaction/rollback helpers at the existing store boundaries, preserve the existing configuration formats, and make state changes commit only after filesystem operations succeed.

**Tech Stack:** Go, Go standard library tests, Vue 3/TypeScript, Wails.

## Global Constraints

- Do not overwrite an existing managed or unmanaged user configuration without an explicit conflict decision.
- Do not report an operation as successful when a required filesystem operation failed.
- Do not change unrelated UI or public behavior.
- Every repaired bug must have a focused regression test that failed before the implementation change.

---

### Task 1: Harden Skill import and update transactions

**Files:**
- Modify: `internal/skills/store.go`
- Modify: `internal/skills/update.go`
- Test: `internal/skills/store_test.go`
- Test: `internal/skills/update_test.go`

- [ ] **Step 1: Add a duplicate-import regression test**

Create an existing SSOT skill and matching store entry, import another directory with the same basename, and assert the original SSOT content remains unchanged and the import returns an error.

- [ ] **Step 2: Run the focused test and confirm it fails**

Run `go test ./internal/skills -run 'TestImport.*Duplicate' -count=1` and verify the current implementation removes the original directory.

- [ ] **Step 3: Add synchronization-failure regression coverage**

Exercise an invalid/unwritable agent target and assert the import returns an error without adding the Skill or bindings.

- [ ] **Step 4: Implement import preflight and rollback**

Check duplicate managed directories before filesystem mutation. Track whether SSOT and agent targets were created by this operation. On any sync failure, remove only those newly created paths and do not update the in-memory store.

- [ ] **Step 5: Add update rollback and live-binding coverage**

Use a failing tarball source and assert the old SSOT content remains. Toggle one binding off before update and assert the disabled target is not recreated.

- [ ] **Step 6: Implement staged update**

Read active agents from `s.bindings`, download/extract into a temporary staging location, validate the staged Skill, then replace the old SSOT directory with a recoverable backup. Restore the backup if installation or synchronization fails.

- [ ] **Step 7: Run the focused Skill tests**

Run `go test ./internal/skills -count=1` and confirm all tests pass.

### Task 2: Make Skill storage migration transactional

**Files:**
- Modify: `app.go`
- Modify: `internal/skills/store.go`
- Modify: `frontend/src/views/SettingsView.vue`
- Test: `internal/skills/store_test.go`
- Test: `app_settings_test.go` if present, otherwise add the smallest package-level regression test possible

- [ ] **Step 1: Add a migration-error regression test**

Force a resync error and assert `MigrationResult.Errors` is surfaced as an error and the configured storage location is not advanced.

- [ ] **Step 2: Run the focused test and confirm it fails**

Run the focused migration test and verify the current implementation returns `nil` despite populated `Errors`.

- [ ] **Step 3: Implement error propagation and commit ordering**

Return an error when migration or resync produces errors. Save settings only after the store migration succeeds. Make the frontend treat returned migration errors as failure and avoid applying the new setting in that case.

- [ ] **Step 4: Run settings and Skill tests**

Run `go test ./internal/skills ./... -count=1` as appropriate and `pnpm check:i18n`.

### Task 3: Repair MCP JSON persistence and conflict handling

**Files:**
- Modify: `internal/mcp/json_backend.go`
- Modify: `internal/mcp/store.go`
- Test: `internal/mcp/json_backend_test.go`
- Test: `internal/mcp/mcp_test.go`

- [ ] **Step 1: Add failing format-preservation tests**

Write a `servers`-format config, write through the backend, and assert it still contains `servers` and not a newly-created authoritative `mcpServers` field. Add OpenCode SSE and Streamable HTTP cases and assert they remain remote.

- [ ] **Step 2: Run the focused tests and confirm they fail**

Run `go test ./internal/mcp -run 'Test.*(Servers|OpenCode|Transport)' -count=1`.

- [ ] **Step 3: Implement container-key preservation and remote transport mapping**

Detect the existing standard container key before writing and use it. Treat all supported remote transports as OpenCode `remote` entries and retain their URL.

- [ ] **Step 4: Add same-name overwrite coverage**

Assert that adding a managed server does not replace a pre-existing unmanaged server with the same name unless the operation explicitly updates that server.

- [ ] **Step 5: Implement conflict rejection and run MCP tests**

Reject ambiguous same-name writes at the store boundary and run `go test ./internal/mcp -count=1`.

### Task 4: Make MCP loading and backup restore consistent

**Files:**
- Modify: `internal/mcp/store.go`
- Modify: `internal/backup/export.go`
- Modify: `app.go`
- Test: `internal/mcp/mcp_test.go`
- Test: `internal/backup/*_test.go`

- [ ] **Step 1: Add partial-load and restore rollback tests**

Assert a valid MCP config remains available when another config is malformed. Add a restore test where the second MCP mutation fails and assert the first mutation is restored.

- [ ] **Step 2: Run the focused tests and confirm they fail**

Run the relevant MCP and backup tests and verify the current all-or-nothing load and sequential restore behavior.

- [ ] **Step 3: Preserve valid loaded entries while reporting load errors**

Commit successfully read configurations to the store, return/report an aggregated load error separately, and keep app readiness semantics explicit.

- [ ] **Step 4: Add restore rollback and state persistence**

Capture pre-restore MCP state using existing backup mechanisms, restore it on mutation failure, and save restored Agent disabled IDs to application config. Route settings application through a shared runtime refresh helper without taking conflicting locks.

- [ ] **Step 5: Run backup/MCP tests**

Run `go test ./internal/mcp ./internal/backup ./... -count=1`.

### Task 5: Full verification

**Files:**
- No implementation files; inspect the final diff and test outputs.

- [ ] **Step 1: Run `go test ./...`**
- [ ] **Step 2: Run `go vet ./...` and record any unrelated remaining warnings**
- [ ] **Step 3: Run `pnpm check:i18n`**
- [ ] **Step 4: Run `pnpm build`**
- [ ] **Step 5: Review `git diff`, confirm no unrelated edits, and report exact verification results**
