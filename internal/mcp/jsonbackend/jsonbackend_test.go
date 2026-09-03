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
