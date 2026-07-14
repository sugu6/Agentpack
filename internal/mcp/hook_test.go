package mcp

import (
	"agentpack/internal/agents"
	"path/filepath"
	"sync"
	"testing"
)

type captureHook struct {
	mu        sync.Mutex
	events    []string
	oldConfig map[string]string
}

func (h *captureHook) OnMutation(action string, detail MutationDetail) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.events = append(h.events, action)
	h.oldConfig = detail.OldConfigs
}

func newHookedStore(t *testing.T) (*Store, *agents.Registry, *captureHook, string) {
	t.Helper()
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "settings.json")
	reg := agents.NewRegistry()
	reg.Register(agents.Agent{
		ID:         "claude-code",
		Name:       "Claude Code",
		Type:       agents.AgentType("claude-code"),
		ConfigPath: cfgPath,
		Status:     agents.StatusDetected,
	})
	store := NewStore()
	hook := &captureHook{}
	store.SetMutationHandler(hook)
	return store, reg, hook, cfgPath
}

func TestHookFiresOnAdd(t *testing.T) {
	store, reg, hook, _ := newHookedStore(t)
	_, err := store.Add(Server{Name: "test", Command: "echo", Transport: TransportStdio}, []string{"claude-code"}, reg)
	if err != nil {
		t.Fatal(err)
	}
	if len(hook.events) != 1 || hook.events[0] != "mcp.add" {
		t.Errorf("expected one mcp.add event, got %v", hook.events)
	}
}

func TestHookFiresOnRemove(t *testing.T) {
	store, reg, hook, _ := newHookedStore(t)
	created, _ := store.Add(Server{Name: "test", Command: "echo"}, []string{"claude-code"}, reg)
	hook.events = nil
	if err := store.Remove(created.ID, reg); err != nil {
		t.Fatal(err)
	}
	if len(hook.events) != 1 || hook.events[0] != "mcp.remove" {
		t.Errorf("expected mcp.remove event, got %v", hook.events)
	}
}

func TestHookFiresOnToggle(t *testing.T) {
	store, reg, hook, _ := newHookedStore(t)
	created, _ := store.Add(Server{Name: "test", Command: "echo"}, []string{"claude-code"}, reg)
	hook.events = nil
	if err := store.ToggleAgent(created.ID, "claude-code", false, reg); err != nil {
		t.Fatal(err)
	}
	if len(hook.events) != 1 || hook.events[0] != "mcp.unbind" {
		t.Errorf("expected mcp.unbind, got %v", hook.events)
	}
	hook.events = nil
	if err := store.ToggleAgent(created.ID, "claude-code", true, reg); err != nil {
		t.Fatal(err)
	}
	if len(hook.events) != 1 || hook.events[0] != "mcp.bind" {
		t.Errorf("expected mcp.bind, got %v", hook.events)
	}
}

func TestHookCarriesOldConfig(t *testing.T) {
	store, reg, hook, cfgPath := newHookedStore(t)
	created, err := store.Add(Server{Name: "test", Command: "echo"}, []string{"claude-code"}, reg)
	if err != nil {
		t.Fatal(err)
	}
	hook.oldConfig = nil
	err = store.Update(created.ID, Server{Name: "test", Command: "echo2"}, []string{"claude-code"}, reg)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := hook.oldConfig[cfgPath]; !ok {
		t.Errorf("expected old config captured for %s, got keys %v", cfgPath, keys(hook.oldConfig))
	}
}

func keys(m map[string]string) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}
