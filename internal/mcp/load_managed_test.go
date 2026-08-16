package mcp

import (
	"path/filepath"
	"testing"

	"agentpack/internal/agents"
	"agentpack/internal/dbutil"
)

// managedRegistry 构造一个"已检测、已启用"的 Cursor agent，配置指向临时 mcp.json。
// 模拟其他 agent 的 MCP 配置存在但尚未被 AgentPack 纳入管理的场景。
func managedRegistry(t *testing.T, configBody string) (*agents.Registry, string) {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "mcp.json")
	writeFile(t, path, configBody)
	reg := agents.NewRegistry()
	reg.Register(agents.Agent{
		ID:           "cursor",
		Name:         "Cursor",
		Type:         agents.TypeCursor,
		Status:       agents.StatusEnabled,
		ConfigPath:   path,
		ConfigFormat: agents.FormatJSON,
	})
	return reg, path
}

const twoServersConfig = `{"mcpServers": {
  "alpha": {"command": "npx", "args": ["-y", "alpha-pkg"]},
  "beta":  {"command": "npx", "args": ["-y", "beta-pkg"]}
}}`

// 场景：其他 agent 的 mcp 从未通过 AgentPack 纳入管理（"加入管理"按钮从未被点击）。
// 重启后 Load 不应把配置文件里的服务器静默纳入列表。
func TestLoad_KeepsUnmanagedServersUnmanaged(t *testing.T) {
	resetTestDB(t)
	reg, _ := managedRegistry(t, twoServersConfig)

	store := NewStore()
	if err := store.Load(reg); err != nil {
		t.Fatal(err)
	}
	if got := store.List(); len(got) != 0 {
		t.Fatalf("Load 把未管理的配置服务器纳入了列表: %d 个", len(got))
	}

	// 扫描仍能发现它们（标记为"新发现"），但未纳入管理
	res := store.Scan(reg)
	if res.Total != 2 || res.Managed != 0 || res.NewFound != 2 {
		t.Fatalf("scan: total=%d managed=%d new=%d, want 2/0/2", res.Total, res.Managed, res.NewFound)
	}
}

// 用户通过扫描对话框显式"加入管理"后，重启应保留该服务器（含 ID 与安装时间），
// 而未经确认的其他服务器仍保持未管理。
func TestLoad_KeepsExplicitlyAddedServerAcrossRestart(t *testing.T) {
	resetTestDB(t)
	reg, _ := managedRegistry(t, twoServersConfig)

	store := NewStore()
	if err := store.Load(reg); err != nil {
		t.Fatal(err)
	}
	added, err := store.Add(Server{
		Name:      "alpha",
		Command:   "npx",
		Args:      []string{"-y", "alpha-pkg"},
		Transport: TransportStdio,
	}, []string{"cursor"}, reg)
	if err != nil {
		t.Fatal(err)
	}

	// 模拟重启：全新 Store 重新 Load（同一数据库）
	store2 := NewStore()
	if err := store2.Load(reg); err != nil {
		t.Fatal(err)
	}
	list := store2.List()
	if len(list) != 1 {
		t.Fatalf("explicitly added server should persist after reload, got %d servers", len(list))
	}
	if list[0].ID != added.ID {
		t.Errorf("server id changed across restart: got %s want %s", list[0].ID, added.ID)
	}
	// 数据库存秒级 unix 时间，跨重启比较按秒级精度
	if got, want := dbutil.ParseTimeToInt64(list[0].InstalledAt), dbutil.ParseTimeToInt64(added.InstalledAt); got != want {
		t.Errorf("installedAt changed across restart: got %d want %d", got, want)
	}

	// beta 依然未被纳入，扫描中标记为新发现
	res := store2.Scan(reg)
	if res.Total != 2 || res.Managed != 1 || res.NewFound != 1 {
		t.Fatalf("scan: total=%d managed=%d new=%d, want 2/1/1", res.Total, res.Managed, res.NewFound)
	}
}

// 已管理的服务器被外部从配置中移除后，重启应从列表消失（数据库项同步清理）。
func TestLoad_DropsServerRemovedFromConfig(t *testing.T) {
	resetTestDB(t)
	reg, path := managedRegistry(t, twoServersConfig)

	store := NewStore()
	if err := store.Load(reg); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Add(Server{
		Name:      "alpha",
		Command:   "npx",
		Args:      []string{"-y", "alpha-pkg"},
		Transport: TransportStdio,
	}, []string{"cursor"}, reg); err != nil {
		t.Fatal(err)
	}

	// 外部（如其他工具/手改）把 alpha 从配置中删除
	writeFile(t, path, `{"mcpServers": {
  "beta": {"command": "npx", "args": ["-y", "beta-pkg"]}
}}`)

	store2 := NewStore()
	if err := store2.Load(reg); err != nil {
		t.Fatal(err)
	}
	if got := store2.List(); len(got) != 0 {
		t.Fatalf("server removed from config should not remain managed, got %+v", got)
	}
}

// 已管理服务器经由 AgentPack UI 修改命令（key 变化）后重启：按同名唯一兜底保留。
func TestLoad_KeepsServerWhenCommandChanged(t *testing.T) {
	resetTestDB(t)
	reg, path := managedRegistry(t, twoServersConfig)

	store := NewStore()
	if err := store.Load(reg); err != nil {
		t.Fatal(err)
	}
	added, err := store.Add(Server{
		Name:      "alpha",
		Command:   "npx",
		Args:      []string{"-y", "alpha-pkg"},
		Transport: TransportStdio,
	}, []string{"cursor"}, reg)
	if err != nil {
		t.Fatal(err)
	}

	// 模拟 UI Update 改写命令：key 变化但名字不变
	writeFile(t, path, `{"mcpServers": {
  "alpha": {"command": "npx", "args": ["-y", "alpha-pkg-v2"]},
  "beta":  {"command": "npx", "args": ["-y", "beta-pkg"]}
}}`)

	store2 := NewStore()
	if err := store2.Load(reg); err != nil {
		t.Fatal(err)
	}
	list := store2.List()
	if len(list) != 1 {
		t.Fatalf("expected the renamed server to persist, got %d servers: %+v", len(list), list)
	}
	if list[0].ID != added.ID {
		t.Errorf("server id changed across restart: got %s want %s", list[0].ID, added.ID)
	}
	if list[0].Args[0] != "-y" || len(list[0].Args) < 2 || list[0].Args[1] != "alpha-pkg-v2" {
		t.Errorf("server content should reflect new config, got %+v", list[0].Args)
	}
}
