# Project Logic Hardening Design

## Goal

Repair the highest-risk state-consistency bugs found during the project audit without introducing a broad architectural rewrite.

## Scope

### Skill lifecycle

- Reject duplicate directory imports before touching the existing SSOT directory.
- Make import synchronization all-or-nothing: failed agent synchronization must not create managed bindings.
- Update from a staged download/extraction and preserve the old version if the new version cannot be installed.
- Derive update targets from the live binding map rather than the stale serialized `BoundAgents` field.
- Treat storage migration errors as operation failures and save the new setting only after migration succeeds.

### MCP configuration

- Preserve the detected standard JSON container key (`servers` or `mcpServers`) when writing.
- Preserve OpenCode remote MCPs for HTTP, SSE, and Streamable HTTP transports.
- Refuse to overwrite an unmanaged same-name server in an agent configuration.
- Allow valid configurations to load even when another agent configuration is malformed, while retaining the load error for reporting.

### Backup restore

- Persist restored agent disabled state.
- Route restored settings through the same runtime-refresh path used by normal settings changes where possible.
- Roll back MCP mutations already applied by a restore if a later mutation fails.

## Design choices

The implementation will use focused helpers and existing store-level backup/restore facilities instead of introducing a new transaction framework. File replacement will use temporary directories and explicit rollback. Existing APIs will remain compatible unless an error result must be surfaced to prevent false success.

## Error handling

Any failed filesystem synchronization, migration, or restore mutation must return an error and leave the managed state consistent with the pre-operation state. Partial results will be reported only when the operation is intentionally best-effort; the scoped Skill and MCP mutations are all-or-nothing.

## Verification

Add regression tests for every repaired behavior, run the focused Go packages first, then `go test ./...`, `go vet ./...`, frontend i18n checks, and the production frontend build.
