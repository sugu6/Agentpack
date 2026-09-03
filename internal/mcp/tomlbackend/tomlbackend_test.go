package tomlbackend

import (
	"agentpack/internal/mcp/types"
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
func TestTomlBackend_ReadWrite(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.toml")

	backend := NewBackend()
	in := map[string]types.Server{
		"alpha": {
			ID:        "alpha-id",
			Name:      "alpha",
			Command:   "echo",
			Args:      []string{"hello"},
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
	got := out["alpha"]
	if got.Command != "echo" {
		t.Errorf("expected echo, got %q", got.Command)
	}
	if len(got.Args) != 1 || got.Args[0] != "hello" {
		t.Errorf("expected [hello], got %v", got.Args)
	}
}

func TestTomlBackend_WriteRejectsInvalidExistingTOML(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.toml")
	original := "model = \"gpt-4\n"
	writeFile(t, path, original)

	backend := NewBackend()
	err := backend.Write(path, map[string]types.Server{
		"alpha": {Name: "alpha", Command: "echo", Transport: types.TransportStdio},
	})
	if err == nil {
		t.Fatal("expected invalid TOML error")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != original {
		t.Fatalf("expected original file to remain unchanged, got %q", string(data))
	}
}

func TestTomlBackend_ReadWriteWithEnv(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.toml")

	backend := NewBackend()
	in := map[string]types.Server{
		"myserver": {
			ID:        "myserver-id",
			Name:      "myserver",
			Command:   "npx",
			Args:      []string{"-y", "pkg"},
			Env:       map[string]string{"API_KEY": "secret123"},
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
	got, ok := out["myserver"]
	if !ok {
		t.Fatal("expected myserver")
	}
	if got.Env["API_KEY"] != "secret123" {
		t.Errorf("expected API_KEY=secret123, got %q", got.Env["API_KEY"])
	}
}

func TestTomlBackend_CodexArrayFormat(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.toml")

	// Codex uses [[mcp_servers]] array format
	writeFile(t, path, `
model = "gpt-4"

[[mcp_servers]]
name = "my-server"
command = "npx"
args = ["-y", "@mcp/server-fs", "/tmp"]

[[mcp_servers]]
name = "another"
command = "python"
args = ["server.py"]
`)

	backend := NewBackend()
	out, err := backend.Read(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(out) != 2 {
		t.Fatalf("expected 2 servers, got %d", len(out))
	}
	got, ok := out["my-server"]
	if !ok {
		t.Fatal("expected my-server")
	}
	if got.Command != "npx" {
		t.Errorf("expected npx, got %q", got.Command)
	}
	if len(got.Args) != 3 || got.Args[0] != "-y" {
		t.Errorf("expected [-y, @mcp/server-fs, /tmp], got %v", got.Args)
	}
	if _, ok := out["another"]; !ok {
		t.Error("expected another server")
	}
}

func TestTomlBackend_CodexWriteRead(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.toml")

	writeFile(t, path, `model = "gpt-4"`)

	backend := NewBackend()
	in := map[string]types.Server{
		"my-server": {
			Name:      "my-server",
			Command:   "npx",
			Args:      []string{"-y", "@mcp/server-fs"},
			Env:       map[string]string{"KEY": "val"},
			Transport: types.TransportStdio,
		},
	}
	if err := backend.Write(path, in); err != nil {
		t.Fatal(err)
	}

	// Verify model field preserved
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	body := string(data)
	if !containsStr(body, `model`) {
		t.Error("expected model field preserved")
	}

	// Read back
	out, err := backend.Read(path)
	if err != nil {
		t.Fatal(err)
	}
	got, ok := out["my-server"]
	if !ok {
		t.Fatal("expected my-server")
	}
	if got.Command != "npx" {
		t.Errorf("expected npx, got %q", got.Command)
	}
	if got.Env["KEY"] != "val" {
		t.Errorf("expected KEY=val, got %q", got.Env["KEY"])
	}
}

func TestTomlBackend_CodexTableFormatReadWrite(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.toml")

	// Codex [mcp_servers.NAME] table format with type field
	writeFile(t, path, `
model = "gpt-4"

[mcp_servers]

[mcp_servers.fetch]
type = "stdio"
command = "uvx"
args = ["mcp-server-fetch"]

[mcp_servers.memory]
type = "stdio"
command = "cmd"
args = ["/c", "npx", "-y", "@modelcontextprotocol/server-memory"]

[mcp_servers.sequential-thinking]
type = "stdio"
command = "cmd"
args = ["/c", "npx", "-y", "@modelcontextprotocol/server-sequential-thinking"]

[mcp_servers.context7]
type = "stdio"
command = "cmd"
args = ["/c", "npx", "-y", "@upstash/context7-mcp"]
`)

	backend := NewBackend()
	out, err := backend.Read(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(out) != 4 {
		t.Fatalf("expected 4 servers, got %d", len(out))
	}

	// 验证 fetch (无 cmd /c 包装)
	fetch, ok := out["fetch"]
	if !ok {
		t.Fatal("expected fetch server")
	}
	if fetch.Command != "uvx" {
		t.Errorf("fetch: expected command=uvx, got %q", fetch.Command)
	}
	if fetch.ConfigType != "stdio" {
		t.Errorf("fetch: expected configType=stdio, got %q", fetch.ConfigType)
	}

	// 验证 context7 (cmd /c 包装)
	c7, ok := out["context7"]
	if !ok {
		t.Fatal("expected context7 server")
	}
	if c7.Command != "cmd" {
		t.Errorf("context7: expected command=cmd, got %q", c7.Command)
	}
	if len(c7.Args) != 4 || c7.Args[0] != "/c" {
		t.Errorf("context7: expected args=[/c, npx, -y, @upstash/context7-mcp], got %v", c7.Args)
	}
	if c7.ConfigType != "stdio" {
		t.Errorf("context7: expected configType=stdio, got %q", c7.ConfigType)
	}

	// 写回并验证保留 type 字段和 table 格式
	if err := backend.Write(path, out); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	body := string(data)
	if !containsStr(body, `type = "stdio"`) {
		t.Error("expected type = \"stdio\" in output")
	}
	if !containsStr(body, `[mcp_servers.fetch]`) {
		t.Error("expected [mcp_servers.fetch] table format")
	}
	if !containsStr(body, `model`) {
		t.Error("expected model field preserved")
	}
	// 确认不是数组格式
	if containsStr(body, `[[mcp_servers]]`) {
		t.Error("expected table format, not array format")
	}

	// 重新读取验证往返一致
	out2, err := backend.Read(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(out2) != 4 {
		t.Fatalf("round-trip: expected 4 servers, got %d", len(out2))
	}
	if out2["context7"].ConfigType != "stdio" {
		t.Errorf("round-trip: expected configType=stdio, got %q", out2["context7"].ConfigType)
	}
}

func TestTomlBackend_TableFormatQuotesRegistryStyleName(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.toml")
	writeFile(t, path, `
model = "gpt-4"

[mcp_servers]

[mcp_servers.existing]
type = "stdio"
command = "echo"
`)

	backend := NewBackend()
	in := map[string]types.Server{
		"io.example/filesystem": {
			Name:      "io.example/filesystem",
			Command:   "npx",
			Args:      []string{"-y", "@example/filesystem"},
			Transport: types.TransportStdio,
		},
	}
	if err := backend.Write(path, in); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	body := string(data)
	if !containsStr(body, `[mcp_servers."io.example/filesystem"]`) {
		t.Fatalf("expected quoted table key for registry-style name, got:\n%s", body)
	}
	out, err := backend.Read(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := out["io.example/filesystem"]; !ok {
		t.Fatalf("expected registry-style name after round trip, got %v", out)
	}
}

func TestTomlBackend_CodexArrayFormatWithTypeField(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.toml")

	// [[mcp_servers]] array format also uses type field
	writeFile(t, path, `
model = "gpt-4"

[[mcp_servers]]
name = "my-server"
type = "stdio"
command = "npx"
args = ["-y", "@mcp/server-fs"]
`)
	backend := NewBackend()
	out, err := backend.Read(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(out) != 1 {
		t.Fatalf("expected 1 server, got %d", len(out))
	}
	got, ok := out["my-server"]
	if !ok {
		t.Fatal("expected my-server")
	}
	if got.ConfigType != "stdio" {
		t.Errorf("expected configType=stdio, got %q", got.ConfigType)
	}

	// 写回保留 type 字段
	if err := backend.Write(path, out); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	body := string(data)
	if !containsStr(body, `type = "stdio"`) {
		t.Error("expected type = \"stdio\" in array format output")
	}
}

// containsStr 是原 mcp 包测试 helper 的本地副本（独立包无法共享）。
func containsStr(s, sub string) bool {
	return strings.Contains(s, sub)
}
