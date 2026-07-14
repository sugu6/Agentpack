package mcp

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"agentpack/internal/agents"
	"agentpack/internal/database"
)

// realTestRequired 判断是否运行真实集成测试。
// 设置环境变量 AGENTPACK_REALNET=1 启用，默认跳过。
func realTestRequired(t *testing.T) {
	t.Helper()
	if os.Getenv("AGENTPACK_REALNET") != "1" {
		t.Skip("skipping real integration test; set AGENTPACK_REALNET=1 to run")
	}
}

// TestReal_AddAndRemoveMCPServer 真实安装一个 MCP server 到 claude-code
// 的配置文件（~/.claude.json），验证配置写入正确后卸载并验证移除。
// 全程在临时 HOME + 临时数据库中操作，不影响真实环境。
func TestReal_AddAndRemoveMCPServer(t *testing.T) {
	realTestRequired(t)

	// 设置临时 HOME，使 ConfigPath 指向临时目录，且 isSafeAgentConfigPath 通过
	tmpHome := t.TempDir()
	origHome := os.Getenv("HOME")
	origUserProfile := os.Getenv("USERPROFILE")
	origHomeDrive := os.Getenv("HOMEDRIVE")
	origHomePath := os.Getenv("HOMEPATH")
	os.Setenv("HOME", tmpHome)
	os.Setenv("USERPROFILE", tmpHome)
	os.Setenv("HOMEDRIVE", "")
	os.Setenv("HOMEPATH", "")
	agents.ResetSkillDirCacheForTesting()
	defer func() {
		os.Setenv("HOME", origHome)
		os.Setenv("USERPROFILE", origUserProfile)
		os.Setenv("HOMEDRIVE", origHomeDrive)
		os.Setenv("HOMEPATH", origHomePath)
		agents.ResetSkillDirCacheForTesting()
	}()

	// 初始化临时数据库（mcp.Store.Load/Add/Remove 依赖 database）
	dbPath := filepath.Join(t.TempDir(), "agentpack.db")
	if err := database.Init(dbPath); err != nil {
		t.Fatalf("database init: %v", err)
	}
	defer database.Close()

	// 构造 registry，注册 claude-code agent
	configPath := filepath.Join(tmpHome, ".claude.json")
	reg := agents.NewRegistry()
	reg.Register(agents.Agent{
		ID:           "claude-code",
		Name:         "Claude Code",
		Type:         agents.TypeClaudeCode,
		Status:       agents.StatusEnabled,
		ConfigPath:   configPath,
		ConfigFormat: agents.FormatJSON,
	})

	// 初始化 mcp store 并加载
	store := NewStore()
	if err := store.Load(reg); err != nil {
		t.Fatalf("mcp store load: %v", err)
	}
	t.Logf("mcp store loaded, config path: %s", configPath)

	// === 安装 MCP server ===
	server := Server{
		Name:        "test-filesystem",
		Description: "Real integration test MCP server",
		Command:     "npx",
		Args:        []string{"-y", "@modelcontextprotocol/server-filesystem", "/tmp"},
		Transport:   TransportStdio,
		Env:         map[string]string{"TEST_VAR": "value"},
	}

	created, err := store.Add(server, []string{"claude-code"}, reg)
	if err != nil {
		t.Fatalf("store.Add failed: %v", err)
	}
	t.Logf("installed MCP server: id=%s name=%s", created.ID, created.Name)

	// 验证配置文件已写入
	data, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatalf("read config file %s: %v", configPath, err)
	}
	configContent := string(data)
	t.Logf("config file size: %d bytes", len(configContent))

	// 验证配置文件包含 server 名称（claude-code JSON 格式为 mcpServers.{name}）
	if !strings.Contains(configContent, "test-filesystem") {
		t.Errorf("config file should contain server name 'test-filesystem', got:\n%s", configContent)
	} else {
		t.Logf("config write verified: 'test-filesystem' found in config")
	}

	// 验证 store.List() 包含已安装的 server
	listed := store.List()
	foundInList := false
	for _, s := range listed {
		if s.Name == "test-filesystem" {
			foundInList = true
			if len(s.BoundAgents) == 0 || s.BoundAgents[0] != "claude-code" {
				t.Errorf("server bound agents mismatch: %v", s.BoundAgents)
			}
			break
		}
	}
	if !foundInList {
		t.Errorf("test-filesystem not found in store.List() after install")
	}

	// 验证 Get 返回正确的 server
	got, ok := store.Get(created.ID)
	if !ok {
		t.Errorf("store.Get(%s) returned false after install", created.ID)
	} else if got.Name != "test-filesystem" {
		t.Errorf("Get returned wrong name: %s", got.Name)
	}

	// === 卸载 MCP server ===
	if err := store.Remove(created.ID, reg); err != nil {
		t.Fatalf("store.Remove failed: %v", err)
	}
	t.Logf("uninstalled MCP server: id=%s", created.ID)

	// 验证配置文件不再包含 server 名称
	dataAfter, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatalf("read config file after remove: %v", err)
	}
	configAfter := string(dataAfter)
	if strings.Contains(configAfter, "test-filesystem") {
		t.Errorf("config file should NOT contain 'test-filesystem' after remove, got:\n%s", configAfter)
	} else {
		t.Logf("config removal verified: 'test-filesystem' removed from config")
	}

	// 验证 store.List() 不再包含该 server
	for _, s := range store.List() {
		if s.Name == "test-filesystem" {
			t.Errorf("test-filesystem still present in store.List() after remove")
			break
		}
	}

	// 验证 Get 返回 false
	if _, ok := store.Get(created.ID); ok {
		t.Errorf("store.Get(%s) should return false after remove", created.ID)
	}
}
