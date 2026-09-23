package skills

import (
	"os"
	"path/filepath"
	"testing"
)

func TestConflictAck_RoundtripAndDrift(t *testing.T) {
	ssotDir := filepath.Join(t.TempDir(), "skills")
	if err := os.MkdirAll(ssotDir, 0755); err != nil {
		t.Fatal(err)
	}
	key := conflictKey("foo", filepath.Join(filepath.Dir(ssotDir), "claude", "foo"))
	if got := ReadConflictAcks(ssotDir); len(got) != 0 {
		t.Fatalf("expected empty acks for missing file, got %d", len(got))
	}
	ack := ConflictAck{SSOTHash: "s1", LocalHash: "l1", CheckedAt: "2026-09-24T00:00:00Z"}
	if err := WriteConflictAck(ssotDir, key, ack); err != nil {
		t.Fatal(err)
	}
	got := ReadConflictAcks(ssotDir)
	if !got[key].Matches("s1", "l1") {
		t.Error("expected roundtripped ack to match identical fingerprints")
	}
	if got[key].Matches("s2", "l1") || got[key].Matches("s1", "l2") {
		t.Error("ack must fail to match when either fingerprint drifts")
	}
	if err := DeleteConflictAck(ssotDir, key); err != nil {
		t.Fatal(err)
	}
	if len(ReadConflictAcks(ssotDir)) != 0 {
		t.Error("expected empty after delete")
	}
}

func TestConflictAck_CorruptFileYieldsEmpty(t *testing.T) {
	ssotDir := filepath.Join(t.TempDir(), "skills")
	if err := os.MkdirAll(ssotDir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(conflictAckPath(ssotDir), []byte("{not json"), 0644); err != nil {
		t.Fatal(err)
	}
	if got := ReadConflictAcks(ssotDir); len(got) != 0 {
		t.Fatalf("corrupt ack file must yield empty map, got %d", len(got))
	}
}
