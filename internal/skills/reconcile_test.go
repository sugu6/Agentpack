package skills

import (
	"os"
	"path/filepath"
	"testing"
)

func mustSymlink(t *testing.T, oldname, newname string) {
	t.Helper()
	if err := os.Symlink(oldname, newname); err != nil {
		t.Skipf("symlink unavailable (need privilege/dev mode): %v", err)
	}
}

func TestClassifyCopyEntry(t *testing.T) {
	ssotPath := filepath.Join(t.TempDir(), "foo")
	if err := os.MkdirAll(ssotPath, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(ssotPath, "SKILL.md"), []byte("---\nname: foo\n---\n"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(ssotPath, "extra.sh"), []byte("echo hi\n"), 0644); err != nil {
		t.Fatal(err)
	}
	ssotHash, ssotOK := HashDir(ssotPath)
	if !ssotOK || ssotHash == "" {
		t.Fatalf("ssot hash setup failed: %q %v", ssotHash, ssotOK)
	}
	target := filepath.Join(t.TempDir(), "foo")

	t.Run("absent entry yields no conflict", func(t *testing.T) {
		if cl := classifyCopyEntry(SyncMethodSymlink, ssotPath, target, ssotHash, ssotOK); cl.kind != "" {
			t.Errorf("expected no conflict, got %q", cl.kind)
		}
	})

	t.Run("identical plain copy in symlink mode is plain_same", func(t *testing.T) {
		if err := copyDirRecursive(ssotPath, target); err != nil {
			t.Fatal(err)
		}
		defer os.RemoveAll(target)
		cl := classifyCopyEntry(SyncMethodSymlink, ssotPath, target, ssotHash, ssotOK)
		if cl.kind != ConflictPlainSame || cl.localHash != ssotHash {
			t.Errorf("got %+v, want plain_same with equal hash", cl)
		}
	})

	t.Run("same SKILL.md but different sidecar is plain_diff", func(t *testing.T) {
		if err := copyDirRecursive(ssotPath, target); err != nil {
			t.Fatal(err)
		}
		defer os.RemoveAll(target)
		if err := os.WriteFile(filepath.Join(target, "extra.sh"), []byte("echo BYE\n"), 0644); err != nil {
			t.Fatal(err)
		}
		cl := classifyCopyEntry(SyncMethodSymlink, ssotPath, target, ssotHash, ssotOK)
		if cl.kind != ConflictPlainDiff {
			t.Errorf("got %q, want plain_diff (D1: full-tree guard)", cl.kind)
		}
	})

	t.Run("identical plain copy in copy mode yields no conflict", func(t *testing.T) {
		if err := copyDirRecursive(ssotPath, target); err != nil {
			t.Fatal(err)
		}
		defer os.RemoveAll(target)
		cl := classifyCopyEntry(SyncMethodCopy, ssotPath, target, ssotHash, ssotOK)
		if cl.kind != "" {
			t.Errorf("copy mode projection must not be a conflict, got %q", cl.kind)
		}
	})

	t.Run("incomplete ssot hash forces plain_diff", func(t *testing.T) {
		if err := copyDirRecursive(ssotPath, target); err != nil {
			t.Fatal(err)
		}
		defer os.RemoveAll(target)
		cl := classifyCopyEntry(SyncMethodSymlink, ssotPath, target, ssotHash, false)
		if cl.kind != ConflictPlainDiff {
			t.Errorf("got %q, want plain_diff when ssot hash incomplete", cl.kind)
		}
	})

	t.Run("live correct link yields no conflict", func(t *testing.T) {
		mustSymlink(t, ssotPath, target)
		defer os.RemoveAll(target)
		if cl := classifyCopyEntry(SyncMethodSymlink, ssotPath, target, ssotHash, ssotOK); cl.kind != "" {
			t.Errorf("correct projection must not be a conflict, got %q", cl.kind)
		}
	})

	t.Run("dead link is broken_link", func(t *testing.T) {
		mustSymlink(t, filepath.Join(filepath.Dir(target), "no-such-target"), target)
		defer os.RemoveAll(target)
		cl := classifyCopyEntry(SyncMethodSymlink, ssotPath, target, ssotHash, ssotOK)
		if cl.kind != ConflictBrokenLink {
			t.Errorf("got %q, want broken_link", cl.kind)
		}
	})

	t.Run("live link to wrong target is wrong_target", func(t *testing.T) {
		other := filepath.Join(t.TempDir(), "other")
		if err := os.MkdirAll(other, 0755); err != nil {
			t.Fatal(err)
		}
		mustSymlink(t, other, target)
		defer os.RemoveAll(target)
		cl := classifyCopyEntry(SyncMethodSymlink, ssotPath, target, ssotHash, ssotOK)
		if cl.kind != ConflictWrongTarget {
			t.Errorf("got %q, want wrong_target", cl.kind)
		}
	})
}
