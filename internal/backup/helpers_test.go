package backup

import (
	"agentpack/internal/agents"
	"agentpack/internal/database"
	"io"
	"os"
	"path/filepath"
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
