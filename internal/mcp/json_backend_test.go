package mcp

import (
	"encoding/json"
	"os"
	"path/filepath"
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
