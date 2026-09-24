package jsonbackend

import (
	"agentpack/internal/mcp/backend"
	"agentpack/internal/mcp/types"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// writeFile 是原 mcp 包测试 helper 的本地副本（独立包无法共享）。
func writeFile(t *testing.T, path, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0644); err != nil {
		t.Fatal(err)
	}
}

func TestJsonBackend_PreservesServersContainer(t *testing.T) {
	path := filepath.Join(t.TempDir(), "mcp.json")
	writeFile(t, path, `{"servers":{"old":{"command":"old"}},"editor":"vscode"}`)

	backend := NewBackend("trae")
	err := backend.Write(path, map[string]types.Server{
		"new": {Name: "new", Command: "node", Transport: types.TransportStdio},
	})
	if err != nil {
		t.Fatal(err)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var cfg map[string]json.RawMessage
	if err := json.Unmarshal(data, &cfg); err != nil {
		t.Fatal(err)
	}
	if _, ok := cfg["mcpServers"]; ok {
		t.Fatal("write introduced a second mcpServers container")
	}
	var servers map[string]json.RawMessage
	if err := json.Unmarshal(cfg["servers"], &servers); err != nil {
		t.Fatal(err)
	}
	if _, ok := servers["new"]; !ok {
		t.Fatal("write did not update the existing servers container")
	}
	if _, ok := servers["old"]; ok {
		t.Fatal("write did not replace the existing server set")
	}
}

func TestJsonBackend_SyncsBothContainersWhenBothExist(t *testing.T) {
	path := filepath.Join(t.TempDir(), "mcp.json")
	writeFile(t, path, `{"mcpServers":{"old":{"command":"old"}},"servers":{"legacy":{"command":"legacy"}}}`)

	backend := NewBackend("trae")
	err := backend.Write(path, map[string]types.Server{
		"new": {Name: "new", Command: "node", Transport: types.TransportStdio},
	})
	if err != nil {
		t.Fatal(err)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var cfg map[string]json.RawMessage
	if err := json.Unmarshal(data, &cfg); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"mcpServers", "servers"} {
		var servers map[string]json.RawMessage
		if err := json.Unmarshal(cfg[key], &servers); err != nil {
			t.Fatalf("container %s invalid: %v", key, err)
		}
		if _, ok := servers["new"]; !ok {
			t.Fatalf("container %s was not updated", key)
		}
		if _, ok := servers["old"]; ok {
			t.Fatalf("container %s still has stale entry", key)
		}
		if _, ok := servers["legacy"]; ok {
			t.Fatalf("container %s still has stale entry", key)
		}
	}
}

// TestWriteStandard_OmitsUnsetType 验证空 Transport 且无 ConfigType 时不写出
// "type": ""（相对 HEAD 的 omitempty 行为不回归；CHANGELOG 承诺不写未设置字段）。
func TestWriteStandard_OmitsUnsetType(t *testing.T) {
	path := filepath.Join(t.TempDir(), "mcp.json")
	writeFile(t, path, `{}`)

	backend := NewBackend("claude-code")
	if err := backend.Write(path, map[string]types.Server{
		"no-type": {Name: "no-type", Command: "echo", Args: []string{"hi"}}, // Transport 空、ConfigType 空
	}); err != nil {
		t.Fatal(err)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var cfg struct {
		McpServers map[string]map[string]json.RawMessage `json:"mcpServers"`
	}
	if err := json.Unmarshal(data, &cfg); err != nil {
		t.Fatal(err)
	}
	entry, ok := cfg.McpServers["no-type"]
	if !ok {
		t.Fatalf("entry missing: %s", data)
	}
	if v, ok := entry["type"]; ok {
		t.Errorf("unset transport written as type field (%s); want key omitted:\n%s", v, data)
	}
}

// TestServerDeterministicID_NoPathLeak 验证确定性 ID 不含完整配置路径（L3 回归）。
func TestServerDeterministicID_NoPathLeak(t *testing.T) {
	path := filepath.Join("C:\\Users", "someuser", ".config", "cursor", "mcp.json")
	serverID := backend.ServerDeterministicID("context7", path)
	if serverID == "" {
		t.Fatal("expected non-empty ID")
	}
	// 不泄露完整路径（含用户名目录）
	if strings.Contains(serverID, path) || strings.Contains(serverID, "someuser") {
		t.Errorf("ID must not contain full config path, got %q", serverID)
	}
	// 确定性：同一路径多次生成一致
	if again := backend.ServerDeterministicID("context7", path); again != serverID {
		t.Errorf("ID not deterministic: %q != %q", again, serverID)
	}
	// 不同路径生成不同 ID（同一 name 前缀下）
	other := backend.ServerDeterministicID("context7", filepath.Join("D:\\other", "mcp.json"))
	if other == serverID {
		t.Errorf("expected different IDs for different paths, both %q", other)
	}
}

func TestJsonBackend_OpenCodePreservesRemoteTransports(t *testing.T) {
	for _, transport := range []types.Transport{types.TransportSSE, types.TransportStreamableHTTP} {
		t.Run(string(transport), func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "opencode.json")
			backend := NewBackend("opencode")
			err := backend.Write(path, map[string]types.Server{
				"remote": {Name: "remote", Transport: transport, URL: "https://mcp.example.com/sse"},
			})
			if err != nil {
				t.Fatal(err)
			}

			data, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			var cfg map[string]json.RawMessage
			if err := json.Unmarshal(data, &cfg); err != nil {
				t.Fatal(err)
			}
			var mcpCfg map[string]json.RawMessage
			if err := json.Unmarshal(cfg["mcp"], &mcpCfg); err != nil {
				t.Fatal(err)
			}
			var servers map[string]json.RawMessage
			if err := json.Unmarshal(mcpCfg["servers"], &servers); err != nil {
				t.Fatal(err)
			}
			var remote map[string]any
			if err := json.Unmarshal(servers["remote"], &remote); err != nil {
				t.Fatal(err)
			}
			if remote["type"] != "remote" {
				t.Fatalf("expected OpenCode remote type, got %#v", remote["type"])
			}
			if remote["url"] != "https://mcp.example.com/sse" {
				t.Fatalf("expected remote URL to be preserved, got %#v", remote["url"])
			}
		})
	}
}

func TestJsonBackend_OpenCodePreservesHeadersAndDisabled(t *testing.T) {
	path := filepath.Join(t.TempDir(), "opencode.json")
	writeFile(t, path, `{"mcp":{"servers":{"api":{"type":"remote","url":"https://mcp.example.com","headers":{"Authorization":"Bearer tok"},"enabled":false}}}}`)

	backend := NewBackend("opencode")
	servers, err := backend.Read(path)
	if err != nil {
		t.Fatal(err)
	}
	s, ok := servers["api"]
	if !ok {
		t.Fatal("server not read")
	}
	if s.Headers["Authorization"] != "Bearer tok" {
		t.Fatalf("headers dropped on read: %#v", s.Headers)
	}
	if s.Enabled == nil || *s.Enabled {
		t.Fatalf("enabled=false not preserved on read: %#v", s.Enabled)
	}

	// 写回后 round-trip 一致：headers 保留，enabled=false 不被撤销
	if err := backend.Write(path, map[string]types.Server{"api": s}); err != nil {
		t.Fatal(err)
	}
	servers2, err := backend.Read(path)
	if err != nil {
		t.Fatal(err)
	}
	s2 := servers2["api"]
	if s2.Headers["Authorization"] != "Bearer tok" {
		t.Fatalf("headers dropped on write: %#v", s2.Headers)
	}
	if s2.Enabled == nil || *s2.Enabled {
		t.Fatalf("enabled=false not preserved on write: %#v", s2.Enabled)
	}
}

func TestJsonBackend_StandardPreservesHeaders(t *testing.T) {
	path := filepath.Join(t.TempDir(), "mcp.json")
	writeFile(t, path, `{"mcpServers":{"sse":{"type":"sse","url":"https://mcp.example.com","headers":{"X-Auth":"secret"}}}}`)

	backend := NewBackend("claude-code")
	servers, err := backend.Read(path)
	if err != nil {
		t.Fatal(err)
	}
	if s := servers["sse"]; s.Headers["X-Auth"] != "secret" {
		t.Fatalf("headers dropped on read: %#v", s.Headers)
	}
	if err := backend.Write(path, servers); err != nil {
		t.Fatal(err)
	}
	servers2, err := backend.Read(path)
	if err != nil {
		t.Fatal(err)
	}
	if s2 := servers2["sse"]; s2.Headers["X-Auth"] != "secret" {
		t.Fatalf("headers dropped on write: %#v", s2.Headers)
	}
}

func TestJsonBackend_ReadWrite(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "mcp.json")

	backend := NewBackend("claude-code")
	servers, err := backend.Read(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(servers) != 0 {
		t.Fatalf("expected empty, got %d", len(servers))
	}

	in := map[string]types.Server{
		"github": {
			ID:        "test-id",
			Name:      "github",
			Command:   "npx",
			Args:      []string{"-y", "@mcp/server-github"},
			Env:       map[string]string{"GITHUB_TOKEN": "abc"},
			Transport: types.TransportStdio,
		},
	}
	if err := backend.Write(path, in); err != nil {
		t.Fatal(err)
	}

	out, err := backend.Read(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(out) != 1 {
		t.Fatalf("expected 1, got %d", len(out))
	}
	got := out["github"]
	if got.Command != "npx" {
		t.Errorf("expected npx, got %q", got.Command)
	}
	if got.Env["GITHUB_TOKEN"] != "abc" {
		t.Errorf("expected token abc, got %q", got.Env["GITHUB_TOKEN"])
	}
	if got.ID == "" {
		t.Error("expected stable ID after read")
	}
}

func TestJsonBackend_WriteRejectsInvalidExistingJSON(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "mcp.json")
	original := `{"model":`
	writeFile(t, path, original)

	backend := NewBackend("claude-code")
	err := backend.Write(path, map[string]types.Server{
		"github": {Name: "github", Command: "npx", Transport: types.TransportStdio},
	})
	if err == nil {
		t.Fatal("expected invalid JSON error")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != original {
		t.Fatalf("expected original file to remain unchanged, got %q", string(data))
	}
}

func TestJsonBackend_ReadOpenCodeFlatFormat(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "opencode.json")

	// OpenCode flat format: servers directly under "mcp", command is an array
	writeFile(t, path, `{
		"$schema": "https://opencode.ai/config.json",
		"mcp": {
			"context7": {
				"command": ["cmd", "/c", "npx", "-y", "@upstash/context7-mcp"],
				"enabled": true,
				"type": "local"
			},
			"fetch": {
				"command": ["uvx", "mcp-server-fetch"],
				"enabled": true,
				"type": "local"
			},
			"memory": {
				"command": ["cmd", "/c", "npx", "-y", "@modelcontextprotocol/server-memory"],
				"enabled": true,
				"type": "local"
			}
		}
	}`)

	backend := NewBackend("opencode")
	servers, err := backend.Read(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(servers) != 3 {
		t.Fatalf("expected 3 servers, got %d", len(servers))
	}

	// context7: cmd /c npx → should be normalized to cmd + ["/c", "npx", "-y", "@upstash/context7-mcp"]
	c7, ok := servers["context7"]
	if !ok {
		t.Fatal("expected context7 server")
	}
	if c7.Command != "cmd" {
		t.Errorf("context7: expected command=cmd, got %q", c7.Command)
	}
	if len(c7.Args) != 4 || c7.Args[0] != "/c" {
		t.Errorf("context7: expected args=[/c, npx, -y, @upstash/context7-mcp], got %v", c7.Args)
	}

	// fetch: uvx (no wrapper)
	fetch, ok := servers["fetch"]
	if !ok {
		t.Fatal("expected fetch server")
	}
	if fetch.Command != "uvx" {
		t.Errorf("fetch: expected command=uvx, got %q", fetch.Command)
	}
	if len(fetch.Args) != 1 || fetch.Args[0] != "mcp-server-fetch" {
		t.Errorf("fetch: expected args=[mcp-server-fetch], got %v", fetch.Args)
	}

	// memory: cmd /c npx
	mem, ok := servers["memory"]
	if !ok {
		t.Fatal("expected memory server")
	}
	if mem.Command != "cmd" {
		t.Errorf("memory: expected command=cmd, got %q", mem.Command)
	}
	if len(mem.Args) != 4 || mem.Args[1] != "npx" {
		t.Errorf("memory: unexpected args: %v", mem.Args)
	}
}

func TestJsonBackend_OpenCodeFlatFormatWriteRoundTrip(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "opencode.json")

	// Write initial config in flat format
	writeFile(t, path, `{
		"$schema": "https://opencode.ai/config.json",
		"mcp": {
			"context7": {
				"command": ["npx", "-y", "@upstash/context7-mcp"],
				"enabled": true,
				"type": "local"
			}
		}
	}`)

	backend := NewBackend("opencode")

	// Read should detect flat format
	servers, err := backend.Read(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(servers) != 1 {
		t.Fatalf("expected 1 server, got %d", len(servers))
	}

	// Write back a new server
	in := map[string]types.Server{
		"fetch": {Name: "fetch", Command: "uvx", Args: []string{"mcp-server-fetch"}, Transport: types.TransportStdio},
	}
	if err := backend.Write(path, in); err != nil {
		t.Fatal(err)
	}

	// Verify file still uses flat mcp format (not mcp.servers)
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	body := string(data)
	if containsStr(body, `"mcp"`) && containsStr(body, `"servers"`) {
		// Make sure it's NOT using nested mcp.servers format for the servers themselves
		// The flat format has "mcp": { "fetch": {...} } not "mcp": { "servers": { "fetch": {...} } }
		var cfg map[string]json.RawMessage
		if err := json.Unmarshal(data, &cfg); err != nil {
			t.Fatal(err)
		}
		mcpRaw, ok := cfg["mcp"]
		if !ok {
			t.Fatal("expected mcp key")
		}
		var mcp map[string]json.RawMessage
		if err := json.Unmarshal(mcpRaw, &mcp); err != nil {
			t.Fatal(err)
		}
		if _, hasServers := mcp["servers"]; hasServers {
			t.Error("expected flat mcp format, got nested mcp.servers format")
		}
		if _, hasFetch := mcp["fetch"]; !hasFetch {
			t.Error("expected fetch key directly under mcp")
		}
	}
	// Also verify $schema preserved
	if !containsStr(body, `"$schema"`) {
		t.Error("expected $schema preserved")
	}
}

func TestJsonBackend_ReadOpenCodeNestedFormat(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "opencode.json")

	// OpenCode uses nested "mcp" → "servers" format with command as array
	writeFile(t, path, `{
		"mcp": {
			"servers": {
				"github": {
					"type": "local",
					"command": ["npx", "-y", "@mcp/server-github"],
					"environment": {"TOKEN": "xyz"}
				},
				"filesystem": {
					"type": "local",
					"command": ["npx", "-y", "@mcp/server-fs"]
				}
			}
		}
	}`)

	backend := NewBackend("opencode")
	servers, err := backend.Read(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(servers) != 2 {
		t.Fatalf("expected 2 servers, got %d", len(servers))
	}
	got, ok := servers["github"]
	if !ok {
		t.Fatal("expected github server")
	}
	if got.Command != "npx" {
		t.Errorf("expected npx, got %q", got.Command)
	}
	if len(got.Args) != 2 || got.Args[0] != "-y" {
		t.Errorf("expected args [-y, @mcp/server-github], got %v", got.Args)
	}
	if got.Env["TOKEN"] != "xyz" {
		t.Errorf("expected token xyz, got %q", got.Env["TOKEN"])
	}
	if _, ok := servers["filesystem"]; !ok {
		t.Error("expected filesystem server")
	}
}

func TestJsonBackend_ReadTopLevelMcpServers(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "claude.json")

	// Claude Code / Cursor use top-level "mcpServers"
	writeFile(t, path, `{
		"mcpServers": {
			"github": {
				"command": "npx",
				"args": ["-y", "@mcp/server-github"]
			}
		}
	}`)

	backend := NewBackend("claude-code")
	servers, err := backend.Read(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(servers) != 1 {
		t.Fatalf("expected 1 server, got %d", len(servers))
	}
	if _, ok := servers["github"]; !ok {
		t.Error("expected github server")
	}
}

func TestJsonBackend_ReadEmptyConfig(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "empty.json")
	writeFile(t, path, `{}`)

	backend := NewBackend("claude-code")
	servers, err := backend.Read(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(servers) != 0 {
		t.Errorf("expected 0 servers, got %d", len(servers))
	}
}

func TestJsonBackend_WritePreservesNonMcpFields(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")

	// Write config with extra fields
	writeFile(t, path, `{
		"theme": "dark",
		"mcpServers": {
			"old": {"command": "echo"}
		},
		"version": 2
	}`)

	backend := NewBackend("claude-code")
	servers := map[string]types.Server{
		"new": {Name: "new", Command: "npx", Args: []string{"-y", "pkg"}, Transport: types.TransportStdio},
	}
	if err := backend.Write(path, servers); err != nil {
		t.Fatal(err)
	}

	// Read back and verify non-mcpServers fields preserved
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	body := string(data)
	if !containsStr(body, `"theme"`) {
		t.Error("expected theme field preserved")
	}
	if !containsStr(body, `"version"`) {
		t.Error("expected version field preserved")
	}

	// Verify MCP servers updated
	out, err := backend.Read(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := out["new"]; !ok {
		t.Error("expected new server")
	}
	if _, ok := out["old"]; ok {
		t.Error("expected old server removed")
	}
}

func TestJsonBackend_OpenCodeWriteRead(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "opencode.json")

	writeFile(t, path, `{"$schema":"https://opencode.ai/config.json"}`)

	backend := NewBackend("opencode")
	in := map[string]types.Server{
		"github": {
			Name:      "github",
			Command:   "npx",
			Args:      []string{"-y", "@mcp/server-github"},
			Env:       map[string]string{"TOKEN": "abc"},
			Transport: types.TransportStdio,
		},
	}
	if err := backend.Write(path, in); err != nil {
		t.Fatal(err)
	}

	// Verify the file has mcp.servers nested structure
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	body := string(data)
	if !containsStr(body, `"mcp"`) {
		t.Error("expected mcp key in output")
	}
	if !containsStr(body, `"servers"`) {
		t.Error("expected servers key in output")
	}
	if !containsStr(body, `"$schema"`) {
		t.Error("expected $schema preserved")
	}

	// Read back and verify
	out, err := backend.Read(path)
	if err != nil {
		t.Fatal(err)
	}
	got, ok := out["github"]
	if !ok {
		t.Fatal("expected github server")
	}
	if got.Command != "npx" {
		t.Errorf("expected npx, got %q", got.Command)
	}
	if got.Env["TOKEN"] != "abc" {
		t.Errorf("expected TOKEN=abc, got %q", got.Env["TOKEN"])
	}
}

func TestJsonBackend_ClaudeCodeTypeFieldPreserved(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "claude.json")

	// Claude Code 使用 "type": "stdio" 而非 "transport"
	writeFile(t, path, `{
		"mcpServers": {
			"context7": {
				"type": "stdio",
				"command": "cmd",
				"args": ["/c", "npx", "-y", "@upstash/context7-mcp"]
			},
			"fetch": {
				"type": "stdio",
				"command": "uvx",
				"args": ["mcp-server-fetch"]
			}
		}
	}`)

	backend := NewBackend("claude-code")
	servers, err := backend.Read(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(servers) != 2 {
		t.Fatalf("expected 2 servers, got %d", len(servers))
	}

	// 验证 ConfigType 被保留
	c7, ok := servers["context7"]
	if !ok {
		t.Fatal("expected context7 server")
	}
	if c7.ConfigType != "stdio" {
		t.Errorf("context7: expected configType=stdio, got %q", c7.ConfigType)
	}
	if c7.Transport != types.TransportStdio {
		t.Errorf("context7: expected transport=stdio, got %q", c7.Transport)
	}

	// 写回并验证 type 字段保留
	if err := backend.Write(path, servers); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	body := string(data)
	if !containsStr(body, `"type"`) {
		t.Error("expected 'type' field in output")
	}
	if containsStr(body, `"transport"`) {
		t.Error("expected no 'transport' field in output (Claude Code uses 'type')")
	}

	// 重新读取验证往返一致
	out, err := backend.Read(path)
	if err != nil {
		t.Fatal(err)
	}
	if out["context7"].ConfigType != "stdio" {
		t.Errorf("round-trip: expected configType=stdio, got %q", out["context7"].ConfigType)
	}
}

func TestJsonBackend_CursorNoTypeField(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "cursor.json")

	// Cursor 不使用 type 字段
	writeFile(t, path, `{
		"mcpServers": {
			"github": {
				"command": "npx",
				"args": ["-y", "@mcp/server-github"]
			}
		}
	}`)

	backend := NewBackend("cursor")
	servers, err := backend.Read(path)
	if err != nil {
		t.Fatal(err)
	}
	gh, ok := servers["github"]
	if !ok {
		t.Fatal("expected github server")
	}
	if gh.ConfigType != "" {
		t.Errorf("cursor: expected empty configType, got %q", gh.ConfigType)
	}

	// 写回时应自动推导 type 字段
	if err := backend.Write(path, servers); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	body := string(data)
	if !containsStr(body, `"type"`) {
		t.Error("expected 'type' field to be derived from transport for Cursor write-back")
	}
}

// containsStr 是原 mcp 包测试 helper 的本地副本（独立包无法共享）。
func containsStr(s, sub string) bool {
	return strings.Contains(s, sub)
}

// TestJsonBackend_OpenCodeEnabledOverrideEntry 验证官方 schema 的最小覆盖
// 条目 {"enabled": bool}（用于启用/禁用组织远端提供的 MCP server）：
// 按合法条目读入（不标记 partial），重写时原样保留。
func TestJsonBackend_OpenCodeEnabledOverrideEntry(t *testing.T) {
	path := filepath.Join(t.TempDir(), "opencode.json")
	writeFile(t, path, `{"mcp":{"org-server":{"enabled":true}}}`)

	backend := NewBackend("opencode")
	servers, err := backend.Read(path)
	if err != nil {
		t.Fatalf("enabled-only override entry must not mark partial: %v", err)
	}
	srv, ok := servers["org-server"]
	if !ok {
		t.Fatal("expected org-server entry")
	}
	if srv.Enabled == nil || !*srv.Enabled {
		t.Fatalf("expected enabled=true, got %v", srv.Enabled)
	}

	if err := backend.Write(path, servers); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	body := string(data)
	if !containsStr(body, `"org-server"`) || (!containsStr(body, `"enabled":true`) && !containsStr(body, `"enabled": true`)) {
		t.Errorf("override entry lost on rewrite:\n%s", body)
	}
	if containsStr(body, `"command"`) || containsStr(body, `"url"`) {
		t.Errorf("override entry must be written as {\"enabled\": ...} only:\n%s", body)
	}

	// 二次往返
	out2, err := backend.Read(path)
	if err != nil {
		t.Fatal(err)
	}
	if out2["org-server"].Enabled == nil || !*out2["org-server"].Enabled {
		t.Error("override entry lost on second round-trip")
	}
}

// TestJsonBackend_OpenCodeExtraWhitelist 验证严格 schema（additionalProperties:
// false）下的 Extra 写回策略：官方支持的 oauth 键保留，未知键不写出。
func TestJsonBackend_OpenCodeExtraWhitelist(t *testing.T) {
	path := filepath.Join(t.TempDir(), "opencode.json")
	writeFile(t, path, `{
		"mcp": {
			"remote-srv": {
				"type": "remote",
				"url": "https://mcp.example.com",
				"oauth": {"clientId": "cid"},
				"bogus-unknown-key": 1
			}
		}
	}`)

	backend := NewBackend("opencode")
	servers, err := backend.Read(path)
	if err != nil {
		t.Fatal(err)
	}
	srv := servers["remote-srv"]
	if _, ok := srv.Extra["oauth"]; !ok {
		t.Fatalf("oauth not captured in Extra: %#v", srv.Extra)
	}

	if err := backend.Write(path, servers); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	body := string(data)
	if !containsStr(body, `"oauth"`) {
		t.Errorf("official oauth key lost on rewrite:\n%s", body)
	}
	if containsStr(body, `"bogus-unknown-key"`) {
		t.Errorf("unknown key must not be written back under strict opencode schema:\n%s", body)
	}
}

// TestJsonBackend_TraePreservesDisabled 验证 Trae 的 "disabled" 键在整表
// 重写时原样保留。此前该键未被建模，一次 install/uninstall 重写就会把
// 用户在 Trae 里手动禁用的服务器重新启用。
func TestJsonBackend_TraePreservesDisabled(t *testing.T) {
	path := filepath.Join(t.TempDir(), "mcp.json")
	writeFile(t, path, `{
		"mcpServers": {
			"MySQL": {
				"command": "npx",
				"args": ["-y", "@f4ww4z/mcp-mysql-server"],
				"type": "stdio",
				"disabled": true
			}
		}
	}`)

	backend := NewBackend("trae")
	servers, err := backend.Read(path)
	if err != nil {
		t.Fatal(err)
	}
	srv, ok := servers["MySQL"]
	if !ok {
		t.Fatal("expected MySQL server")
	}
	if _, ok := srv.Extra["disabled"]; !ok {
		t.Fatalf("disabled key not captured in Extra: %#v", srv.Extra)
	}

	// 模拟 install 流程：读入全量服务器后整表重写
	if err := backend.Write(path, servers); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !containsStr(string(data), `"disabled":true`) && !containsStr(string(data), `"disabled": true`) {
		t.Errorf("disabled key lost on rewrite:\n%s", string(data))
	}

	out2, err := backend.Read(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := out2["MySQL"].Extra["disabled"]; !ok {
		t.Error("disabled key lost on second round-trip")
	}
}

// TestJsonBackend_ClaudePreservesOAuthAndHeadersHelper 验证 Claude Code
// 最新文档的 oauth / headersHelper / alwaysLoad 键在重写时原样保留。
func TestJsonBackend_ClaudePreservesOAuthAndHeadersHelper(t *testing.T) {
	path := filepath.Join(t.TempDir(), "claude.json")
	writeFile(t, path, `{
		"mcpServers": {
			"remote": {
				"type": "http",
				"url": "https://mcp.example.com/mcp",
				"oauth": {"clientId": "cid", "callbackPort": 8080},
				"headersHelper": "/opt/bin/get-headers.sh",
				"alwaysLoad": true,
				"timeout": 60000
			}
		}
	}`)

	backend := NewBackend("claude-code")
	servers, err := backend.Read(path)
	if err != nil {
		t.Fatal(err)
	}
	srv := servers["remote"]
	if srv.Timeout != 60000 {
		t.Errorf("expected timeout=60000, got %d", srv.Timeout)
	}
	for _, k := range []string{"oauth", "headersHelper", "alwaysLoad"} {
		if _, ok := srv.Extra[k]; !ok {
			t.Errorf("key %q not captured in Extra: %#v", k, srv.Extra)
		}
	}

	if err := backend.Write(path, servers); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	body := string(data)
	for _, k := range []string{`"oauth"`, `"headersHelper"`, `"alwaysLoad"`, `"timeout"`} {
		if !containsStr(body, k) {
			t.Errorf("key %s lost on rewrite:\n%s", k, body)
		}
	}
	if !containsStr(body, "60000") {
		t.Errorf("timeout value lost on rewrite:\n%s", body)
	}
}

// TestJsonBackend_CursorPreservesEnvFileAndAuth 验证 Cursor 最新文档的
// envFile / auth 键在重写时原样保留。
func TestJsonBackend_CursorPreservesEnvFileAndAuth(t *testing.T) {
	path := filepath.Join(t.TempDir(), "mcp.json")
	writeFile(t, path, `{
		"mcpServers": {
			"local-srv": {
				"type": "stdio",
				"command": "node",
				"envFile": ".env",
				"env": {"API_KEY": "k"}
			},
			"oauth-srv": {
				"url": "https://api.example.com/mcp",
				"auth": {"CLIENT_ID": "id", "scopes": ["read"]}
			}
		}
	}`)

	backend := NewBackend("cursor")
	servers, err := backend.Read(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := servers["local-srv"].Extra["envFile"]; !ok {
		t.Errorf("envFile not captured in Extra: %#v", servers["local-srv"].Extra)
	}
	if _, ok := servers["oauth-srv"].Extra["auth"]; !ok {
		t.Errorf("auth not captured in Extra: %#v", servers["oauth-srv"].Extra)
	}

	if err := backend.Write(path, servers); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	body := string(data)
	if !containsStr(body, `"envFile":".env"`) && !containsStr(body, `"envFile": ".env"`) {
		t.Errorf("envFile lost on rewrite:\n%s", body)
	}
	if !containsStr(body, `"auth"`) {
		t.Errorf("auth lost on rewrite:\n%s", body)
	}
}

// TestJsonBackend_StandardOmitsEmptyFields 验证写出条目不再包含
// "command": "" / "args": null 等空值噪声键（各 agent 最新文档均省略
// 未设置的字段）。
func TestJsonBackend_StandardOmitsEmptyFields(t *testing.T) {
	path := filepath.Join(t.TempDir(), "claude.json")

	backend := NewBackend("claude-code")
	in := map[string]types.Server{
		"remote": {Name: "remote", URL: "https://mcp.example.com", Transport: types.TransportHTTP},
		"stdio":  {Name: "stdio", Command: "npx", Transport: types.TransportStdio},
	}
	if err := backend.Write(path, in); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var cfg struct {
		McpServers map[string]map[string]any `json:"mcpServers"`
	}
	if err := json.Unmarshal(data, &cfg); err != nil {
		t.Fatal(err)
	}
	remote := cfg.McpServers["remote"]
	if _, has := remote["command"]; has {
		t.Errorf("remote entry should omit empty command: %v", remote)
	}
	if _, has := remote["args"]; has {
		t.Errorf("remote entry should omit null args: %v", remote)
	}
	stdioSrv := cfg.McpServers["stdio"]
	if _, has := stdioSrv["args"]; has {
		t.Errorf("stdio entry without args should omit args key: %v", stdioSrv)
	}
	if remote["type"] != "http" {
		t.Errorf("expected type=http derived from transport, got %v", remote["type"])
	}
}

// TestJsonBackend_OpenCodeCwdAndOAuth 验证最新 opencode schema 的
// local cwd 与 remote oauth 键的读写。
func TestJsonBackend_OpenCodeCwdAndOAuth(t *testing.T) {
	path := filepath.Join(t.TempDir(), "opencode.json")
	writeFile(t, path, `{
		"mcp": {
			"local-srv": {
				"type": "local",
				"command": ["node", "server.js"],
				"cwd": "subdir"
			},
			"remote-srv": {
				"type": "remote",
				"url": "https://mcp.example.com",
				"oauth": {"clientId": "cid", "callbackPort": 19876}
			}
		}
	}`)

	backend := NewBackend("opencode")
	servers, err := backend.Read(path)
	if err != nil {
		t.Fatal(err)
	}
	local := servers["local-srv"]
	if local.Cwd != "subdir" {
		t.Errorf("expected cwd=subdir, got %q", local.Cwd)
	}
	remote, ok := servers["remote-srv"]
	if !ok {
		t.Fatal("expected remote-srv")
	}
	if _, has := remote.Extra["oauth"]; !has {
		t.Fatalf("oauth not captured in Extra: %#v", remote.Extra)
	}

	if err := backend.Write(path, servers); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	body := string(data)
	if !containsStr(body, `"cwd":"subdir"`) && !containsStr(body, `"cwd": "subdir"`) {
		t.Errorf("cwd lost on rewrite:\n%s", body)
	}
	if !containsStr(body, `"oauth"`) {
		t.Errorf("oauth lost on rewrite:\n%s", body)
	}

	// 二次往返
	out2, err := backend.Read(path)
	if err != nil {
		t.Fatal(err)
	}
	if out2["local-srv"].Cwd != "subdir" {
		t.Errorf("cwd lost on second round-trip: %q", out2["local-srv"].Cwd)
	}
}
