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

	// 写回并验证 table 格式；最新 Codex schema 无 type 键，
	// 写路径不再输出（transport 由 command/url 隐式确定），避免触发
	// "unrecognized configuration settings" 警告。
	if err := backend.Write(path, out); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	body := string(data)
	if containsStr(body, `type = `) {
		t.Error("type key must not be written (unrecognized by current Codex)")
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
	if out2["context7"].Transport != types.TransportStdio {
		t.Errorf("round-trip: expected transport=stdio, got %q", out2["context7"].Transport)
	}
	if out2["context7"].Command != "cmd" {
		t.Errorf("round-trip: expected command=cmd, got %q", out2["context7"].Command)
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

	// 写回不再输出 type 键（最新 Codex schema 不识别该键）
	if err := backend.Write(path, out); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	body := string(data)
	if containsStr(body, `type = "stdio"`) {
		t.Error("type key must not be written in array format output")
	}
	// 条目仍可往返
	out2, err := backend.Read(path)
	if err != nil {
		t.Fatal(err)
	}
	if out2["my-server"].Transport != types.TransportStdio {
		t.Errorf("round-trip: expected transport=stdio, got %q", out2["my-server"].Transport)
	}
}

// TestTomlBackend_LatestCodexKeys 验证最新 Codex schema 键的读写：
// http_headers / tool_timeout_sec / enabled 显式建模，
// env_vars / startup_timeout_sec 等未建模键经 Extra 原样保留。
func TestTomlBackend_LatestCodexKeys(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.toml")

	writeFile(t, path, `
[mcp_servers.appmanaged]
command = "uvx"
args = ["mcp-server-fetch"]
env_vars = ["CODEX_WINDOWS_REGISTERED_CORE"]
startup_timeout_sec = 120
enabled = false
http_headers = { "Authorization" = "Bearer token123" }
tool_timeout_sec = 30
`)

	backend := NewBackend()
	out, err := backend.Read(path)
	if err != nil {
		t.Fatal(err)
	}
	srv, ok := out["appmanaged"]
	if !ok {
		t.Fatal("expected appmanaged server")
	}
	if srv.Headers["Authorization"] != "Bearer token123" {
		t.Errorf("expected http_headers parsed, got %v", srv.Headers)
	}
	if srv.Timeout != 30 {
		t.Errorf("expected timeout=30 from tool_timeout_sec, got %d", srv.Timeout)
	}
	if srv.Enabled == nil || *srv.Enabled != false {
		t.Errorf("expected enabled=false, got %v", srv.Enabled)
	}
	if got := srv.Extra["env_vars"]; got == nil {
		t.Error("expected env_vars preserved in Extra")
	} else if list, ok := got.([]any); !ok || len(list) != 1 {
		t.Errorf("unexpected env_vars shape: %#v", got)
	}
	if _, ok := srv.Extra["startup_timeout_sec"]; !ok {
		t.Error("expected startup_timeout_sec preserved in Extra")
	}

	if err := backend.Write(path, out); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	body := string(data)
	if containsStr(body, `type = `) {
		t.Error("type key must not be written")
	}
	if !containsStr(body, `http_headers = `) {
		t.Error("expected http_headers in output")
	}
	if !containsStr(body, `tool_timeout_sec = 30`) {
		t.Error("expected tool_timeout_sec = 30 in output")
	}
	if !containsStr(body, `enabled = false`) {
		t.Error("expected enabled = false in output")
	}
	if !containsStr(body, `env_vars = `) {
		t.Error("expected env_vars preserved in output")
	}
	if !containsStr(body, `startup_timeout_sec = 120`) {
		t.Error("expected startup_timeout_sec preserved in output")
	}

	// 二次往返：Extra 键持续保留
	out2, err := backend.Read(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := out2["appmanaged"].Extra["env_vars"]; !ok {
		t.Error("env_vars lost on second round-trip")
	}
	if out2["appmanaged"].Headers["Authorization"] != "Bearer token123" {
		t.Errorf("http_headers lost on second round-trip: %v", out2["appmanaged"].Headers)
	}
}

// TestTomlBackend_LegacyKeysMigrate 验证旧版键（headers/timeout）读入后
// 按最新 schema 键（http_headers/tool_timeout_sec）写回。
func TestTomlBackend_LegacyKeysMigrate(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.toml")

	writeFile(t, path, `
[mcp_servers.remote]
url = "https://example.com/mcp"
headers = { "X-Api-Key" = "legacy" }
timeout = 45
`)

	backend := NewBackend()
	out, err := backend.Read(path)
	if err != nil {
		t.Fatal(err)
	}
	srv := out["remote"]
	if srv.Headers["X-Api-Key"] != "legacy" {
		t.Errorf("expected legacy headers parsed, got %v", srv.Headers)
	}
	if srv.Timeout != 45 {
		t.Errorf("expected timeout=45, got %d", srv.Timeout)
	}
	if srv.Transport != types.TransportHTTP {
		t.Errorf("expected http transport, got %q", srv.Transport)
	}

	if err := backend.Write(path, out); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	body := string(data)
	if !containsStr(body, `http_headers = `) {
		t.Error("expected http_headers (latest key) in output")
	}
	if !containsStr(body, `tool_timeout_sec = 45`) {
		t.Error("expected tool_timeout_sec (latest key) in output")
	}
	if containsStr(body, "\nheaders = ") {
		t.Error("legacy headers key must not be written")
	}
	if containsStr(body, "\ntimeout = ") {
		t.Error("legacy timeout key must not be written")
	}
}

// containsStr 是原 mcp 包测试 helper 的本地副本（独立包无法共享）。
func containsStr(s, sub string) bool {
	return strings.Contains(s, sub)
}
