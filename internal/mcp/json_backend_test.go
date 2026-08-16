package mcp

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestJsonBackend_PreservesServersContainer(t *testing.T) {
	path := filepath.Join(t.TempDir(), "mcp.json")
	writeFile(t, path, `{"servers":{"old":{"command":"old"}},"editor":"vscode"}`)

	backend := NewJsonBackend("trae")
	err := backend.Write(path, map[string]Server{
		"new": {Name: "new", Command: "node", Transport: TransportStdio},
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

	backend := NewJsonBackend("trae")
	err := backend.Write(path, map[string]Server{
		"new": {Name: "new", Command: "node", Transport: TransportStdio},
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
	serverID := serverDeterministicID("context7", path)
	if serverID == "" {
		t.Fatal("expected non-empty ID")
	}
	// 不泄露完整路径（含用户名目录）
	if strings.Contains(serverID, path) || strings.Contains(serverID, "someuser") {
		t.Errorf("ID must not contain full config path, got %q", serverID)
	}
	// 确定性：同一路径多次生成一致
	if again := serverDeterministicID("context7", path); again != serverID {
		t.Errorf("ID not deterministic: %q != %q", again, serverID)
	}
	// 不同路径生成不同 ID（同一 name 前缀下）
	other := serverDeterministicID("context7", filepath.Join("D:\\other", "mcp.json"))
	if other == serverID {
		t.Errorf("expected different IDs for different paths, both %q", other)
	}
}

func TestJsonBackend_OpenCodePreservesRemoteTransports(t *testing.T) {
	for _, transport := range []Transport{TransportSSE, TransportStreamableHTTP} {
		t.Run(string(transport), func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "opencode.json")
			backend := NewJsonBackend("opencode")
			err := backend.Write(path, map[string]Server{
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

	backend := NewJsonBackend("opencode")
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
	if err := backend.Write(path, map[string]Server{"api": s}); err != nil {
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

	backend := NewJsonBackend("claude-code")
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
