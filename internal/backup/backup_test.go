package backup

import (
	"agentpack/internal/agents"
	"agentpack/internal/database"
	"agentpack/internal/mcp"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestMain(m *testing.M) {
	os.Setenv("AGENTPACK_ALLOW_TEMP_DIR", "1")
	agents.SetSkipRegistryLookupForTesting(true)
	os.Exit(m.Run())
}

func newTestManager(t *testing.T) *Manager {
	t.Helper()
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "test.db")
	if err := database.Init(dbPath); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = database.Close()
	})
	reg := agents.NewRegistry()
	reg.Register(agents.Agent{
		ID:   "claude-code",
		Name: "Claude Code",
		Type: agents.TypeClaudeCode,
	})
	return NewManager(dir, 50, reg)
}

func TestCreateAndListBackup(t *testing.T) {
	m := newTestManager(t)
	_, err := m.Create("test backup", "manual", "agent-1", "/tmp/foo.json", `{"hello":"world"}`)
	if err != nil {
		t.Fatal(err)
	}
	list, err := m.List(10)
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 1 {
		t.Errorf("expected 1 backup, got %d", len(list))
	}
	if list[0].Size != len(`{"hello":"world"}`) {
		t.Errorf("unexpected size: %d", list[0].Size)
	}
}

func TestCreateRejectsEmptyData(t *testing.T) {
	m := newTestManager(t)
	_, err := m.Create("", "", "", "", "")
	if err == nil {
		t.Fatal("expected error for empty data")
	}
}

func TestGetBackup(t *testing.T) {
	m := newTestManager(t)
	b, err := m.Create("desc", "action", "ag", "/path", "content")
	if err != nil {
		t.Fatal(err)
	}
	got, err := m.Get(b.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Data != "content" {
		t.Errorf("unexpected data: %q", got.Data)
	}
}

func TestGetBackupNotFound(t *testing.T) {
	m := newTestManager(t)
	_, err := m.Get("nonexistent")
	if err == nil {
		t.Fatal("expected error for missing")
	}
}

func TestDeleteBackup(t *testing.T) {
	m := newTestManager(t)
	b, _ := m.Create("d", "a", "ag", "/p", "data")
	if err := m.DeleteBackup(b.ID); err != nil {
		t.Fatal(err)
	}
	list, _ := m.List(10)
	for _, x := range list {
		if x.ID == b.ID {
			t.Errorf("backup not deleted")
		}
	}
}

func TestCount(t *testing.T) {
	m := newTestManager(t)
	for i := 0; i < 5; i++ {
		_, _ = m.Create("d", "a", "ag", "/p", "x")
	}
	n, err := m.Count()
	if err != nil {
		t.Fatal(err)
	}
	if n != 5 {
		t.Errorf("expected 5, got %d", n)
	}
}

func TestTruncateKeepsRecent(t *testing.T) {
	m := newTestManager(t)
	for i := 0; i < 10; i++ {
		_, _ = m.Create("d", "a", "ag", "/p", "x")
	}
	if err := m.Truncate(3); err != nil {
		t.Fatal(err)
	}
	n, _ := m.Count()
	if n != 3 {
		t.Errorf("expected 3 after truncate, got %d", n)
	}
}

func TestSnapshotRoundTrip(t *testing.T) {
	dir := t.TempDir()
	if err := database.Init(filepath.Join(dir, "test.db")); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.Close() })
	snap := Snapshot{
		Version:       CurrentVersion,
		SchemaVersion: CurrentSchemaVersion,
		MCPServers: []SnapshotMCP{
			{Name: "test-server", Command: "echo", Args: []string{"hi"}, Transport: "stdio"},
		},
	}
	data, err := json.Marshal(snap)
	if err != nil {
		t.Fatal(err)
	}
	var got Snapshot
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatal(err)
	}
	if len(got.MCPServers) != 1 {
		t.Errorf("expected 1 server, got %d", len(got.MCPServers))
	}
	if got.MCPServers[0].Name != "test-server" {
		t.Errorf("unexpected name: %s", got.MCPServers[0].Name)
	}
	if got.MCPServers[0].Command != "echo" {
		t.Errorf("unexpected command: %s", got.MCPServers[0].Command)
	}
}

func TestSnapshotFromStore_NilSafe(t *testing.T) {
	got := SnapshotFromStore(nil)
	if got != nil {
		t.Errorf("expected nil for nil store")
	}
}

func TestExporterExportToFile(t *testing.T) {
	dir := t.TempDir()
	reg := agents.NewRegistry()
	reg.Scan()
	store := mcp.NewStore()
	_ = store.Load(reg)

	ex := NewExporter(store, reg)
	path := filepath.Join(dir, "export.json")
	if err := ex.ExportToFile(path); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var snap Snapshot
	if err := json.Unmarshal(data, &snap); err != nil {
		t.Fatal(err)
	}
	if snap.Version != CurrentVersion {
		t.Errorf("unexpected version: %d", snap.Version)
	}
}

func TestManagerExportToFileAllowsChosenAbsolutePath(t *testing.T) {
	baseDir := t.TempDir()
	dbPath := filepath.Join(baseDir, "test.db")
	if err := database.Init(dbPath); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.Close() })
	reg := agents.NewRegistry()
	reg.Register(agents.Agent{
		ID:   "claude-code",
		Name: "Claude Code",
		Type: agents.TypeClaudeCode,
	})
	mcpStore := mcp.NewStore()
	_ = mcpStore.Load(reg)
	m := NewManager(baseDir, 50, reg)
	m.Bind(reg, mcpStore)

	id, err := m.CreateSnapshot(Snapshot{
		Description: "manual export",
		MCPServers: []SnapshotMCP{
			{Name: "test-server", Command: "echo", Transport: "stdio"},
		},
	})
	if err != nil {
		t.Fatal(err)
	}

	// Positive: a path inside baseDir is accepted.
	dest := filepath.Join(baseDir, "chosen", "agentpack-export.json")
	got, err := m.ExportToFile(id, dest)
	if err != nil {
		t.Fatalf("expected export within baseDir to succeed: %v", err)
	}
	if got != filepath.Clean(dest) {
		t.Fatalf("expected export path %s, got %s", filepath.Clean(dest), got)
	}
	if _, err := os.Stat(dest); err != nil {
		t.Fatalf("expected export file: %v", err)
	}

	// Positive: a path outside baseDir is also accepted (user explicitly chose the directory).
	outsideDir, err := os.MkdirTemp("", "agentpack-outside-*")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(outsideDir) })
	outside := filepath.Join(outsideDir, "outside", "agentpack-export.json")
	got2, err := m.ExportToFile(id, outside)
	if err != nil {
		t.Fatalf("expected export outside baseDir to succeed: %v", err)
	}
	if got2 != filepath.Clean(outside) {
		t.Fatalf("expected export path %s, got %s", filepath.Clean(outside), got2)
	}
	if _, err := os.Stat(outside); err != nil {
		t.Fatalf("expected export file: %v", err)
	}
}

func TestExporterImportFromReader(t *testing.T) {
	_ = newTestManager(t)
	dir := t.TempDir()
	reg := agents.NewRegistry()
	reg.Register(agents.Agent{
		ID:         "claude-code",
		Name:       "Claude Code",
		Type:       agents.AgentType("claude-code"),
		ConfigPath: filepath.Join(dir, "settings.json"),
		Status:     agents.StatusDetected,
	})
	store := mcp.NewStore()
	_ = store.Load(reg)
	ex := NewExporter(store, reg)

	snap := Snapshot{
		MCPServers: []SnapshotMCP{
			{Name: "github", Command: "npx", Args: []string{"-y", "test"}, Transport: "stdio"},
		},
	}
	data, _ := json.Marshal(snap)
	res, err := ex.ImportFromReader(stringReader(data), ImportOptions{ApplyMCP: true})
	if err != nil {
		t.Fatal(err)
	}
	if res.MCPApplied != 1 {
		t.Errorf("expected 1 applied, got %d", res.MCPApplied)
	}
}

func TestExporterImportRejectsMissing(t *testing.T) {
	ex := NewExporter(nil, nil)
	_, err := ex.ImportFromReader(stringReader([]byte("not json")), ImportOptions{})
	if err == nil {
		t.Fatal("expected error")
	}
}

func TestExporterImportNoAgents(t *testing.T) {
	reg := agents.NewRegistry()
	store := mcp.NewStore()
	_ = store.Load(reg)
	ex := NewExporter(store, reg)
	snap := Snapshot{MCPServers: []SnapshotMCP{{Name: "x", Command: "y"}}}
	res, err := ex.Import(snap, ImportOptions{ApplyMCP: true})
	if err != nil {
		t.Fatal(err)
	}
	if res.MCPApplied != 0 {
		t.Errorf("expected 0 applied without agents, got %d", res.MCPApplied)
	}
}

func TestManifestFromSnapshot(t *testing.T) {
	snap := Snapshot{
		Version:       1,
		SchemaVersion: 1,
		MCPServers:    []SnapshotMCP{{Name: "a"}, {Name: "b"}},
	}
	mf := ManifestFromSnapshot(snap)
	if mf.MCPCount != 2 {
		t.Errorf("expected 2 mcp, got %d", mf.MCPCount)
	}
	if mf.AppName != AppName {
		t.Errorf("expected app name %s, got %s", AppName, mf.AppName)
	}
}

func TestCaptureEncryptsSensitiveEnv(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "test.db")
	if err := database.Init(dbPath); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.Close() })

	claudePath := filepath.Join(dir, ".claude.json")
	data := `{}`
	if err := os.WriteFile(claudePath, []byte(data), 0644); err != nil {
		t.Fatal(err)
	}

	reg := agents.NewRegistry()
	reg.Register(agents.Agent{
		ID: "claude-code", Name: "Claude Code", Type: agents.AgentType("claude-code"),
		ConfigPath: claudePath, Status: agents.StatusDetected,
	})

	mcpStore := mcp.NewStore()
	if err := mcpStore.Load(reg); err != nil {
		t.Fatal(err)
	}
	if _, err := mcpStore.Add(mcp.Server{
		Name:      "github",
		Command:   "npx",
		Env:       map[string]string{"GITHUB_TOKEN": "secret-token-123", "DEBUG": "1"},
		Transport: mcp.TransportStdio,
	}, []string{"claude-code"}, reg); err != nil {
		t.Fatal(err)
	}

	mgr := NewManager(dir, 50, reg)
	mgr.Bind(reg, mcpStore)

	sum, err := mgr.Capture("manual", "claude-code", claudePath, "test capture")
	if err != nil {
		t.Fatal(err)
	}

	snap, err := mgr.GetSnapshot(sum.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(snap.MCPServers) != 1 {
		t.Fatalf("expected 1 mcp server in snapshot, got %d", len(snap.MCPServers))
	}
	srv := snap.MCPServers[0]
	if strings.Contains(srv.Env["GITHUB_TOKEN"], "secret-token-123") {
		t.Fatalf("expected sensitive env to be encrypted in snapshot, got %q", srv.Env["GITHUB_TOKEN"])
	}
	if !strings.Contains(srv.Env["GITHUB_TOKEN"], "enc:") {
		t.Fatalf("expected encrypted env marker, got %q", srv.Env["GITHUB_TOKEN"])
	}
	if srv.Env["DEBUG"] != "1" {
		t.Fatalf("expected non-sensitive env to remain readable, got %q", srv.Env["DEBUG"])
	}

	var rawData string
	if err := database.GetDB().QueryRow(`SELECT data FROM export_snapshots WHERE id = ?`, sum.ID).Scan(&rawData); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(rawData, "secret-token-123") {
		t.Fatalf("expected sensitive env to be encrypted in raw db data, got %s", rawData)
	}
}

type stringReaderImpl struct {
	data []byte
	pos  int
}

func stringReader(data []byte) *stringReaderImpl {
	return &stringReaderImpl{data: data}
}

func (r *stringReaderImpl) Read(p []byte) (int, error) {
	if r.pos >= len(r.data) {
		return 0, io.EOF
	}
	n := copy(p, r.data[r.pos:])
	r.pos += n
	return n, nil
}
