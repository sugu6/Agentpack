package backup

import (
	"testing"
)

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
