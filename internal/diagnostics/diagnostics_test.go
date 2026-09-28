package diagnostics

import (
	"archive/zip"
	"bytes"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRedactJSONMasksSensitiveKeys(t *testing.T) {
	in := `{
		"theme": "system",
		"apiKey": "sk-123",
		"authMode": "basic",
		"authorization": "Bearer x",
		"authToken": "tok",
		"nested": {"marketToken": "t", "name": "ok"},
		"list": [{"password": "p"}, {"value": 1}]
	}`
	out := string(RedactJSON([]byte(in)))
	// 宽匹配（宁多脱勿漏脱）：apiKey / authMode / authorization / authToken /
	// marketToken / password 六个含敏感子串的键值全部打码。
	for _, secret := range []string{"sk-123", `"basic"`, `"Bearer x"`, `"tok"`, `"t"`, `"p"`} {
		if strings.Contains(out, secret) {
			t.Errorf("secret %s leaked in redacted output: %s", secret, out)
		}
	}
	if strings.Count(out, `"***"`) != 6 {
		t.Errorf("expected 6 masked values, got: %s", out)
	}
	for _, keep := range []string{`"system"`, `"ok"`, `"value": 1`} {
		if !strings.Contains(out, keep) {
			t.Errorf("non-sensitive value %s should be kept: %s", keep, out)
		}
	}
}

func TestRedactJSONInvalidInput(t *testing.T) {
	out := string(RedactJSON([]byte("not json")))
	if !strings.Contains(out, `"redacted"`) {
		t.Errorf("invalid json should produce redacted marker, got: %s", out)
	}
}

func TestBuildZipContainsExpectedEntries(t *testing.T) {
	root := t.TempDir()
	dataDir := filepath.Join(root, "data")
	logDir := filepath.Join(root, "logs")
	if err := os.MkdirAll(dataDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(logDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dataDir, "config.json"),
		[]byte(`{"theme":"dark","apiKey":"secret-1"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(logDir, "agentpack.log"),
		[]byte("time=now level=INFO msg=hello\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(logDir, "crash-20260101-000000.log"),
		[]byte("panic: boom\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	zipPath, err := BuildZip(ZipOptions{
		ExportsDir: filepath.Join(root, "exports"),
		LogDir:     logDir,
		DataDir:    dataDir,
		Env:        Env{AppVersion: "0.4.0", OS: "windows"},
	})
	if err != nil {
		t.Fatalf("BuildZip: %v", err)
	}

	r, err := zip.OpenReader(zipPath)
	if err != nil {
		t.Fatalf("open zip: %v", err)
	}
	defer r.Close()

	got := map[string]string{}
	for _, f := range r.File {
		rc, err := f.Open()
		if err != nil {
			t.Fatal(err)
		}
		buf, err := io.ReadAll(rc)
		rc.Close()
		if err != nil {
			t.Fatalf("read %s: %v", f.Name, err)
		}
		got[f.Name] = string(buf)
	}

	for _, name := range []string{"README.txt", "env.json", "config.redacted.json",
		"logs/agentpack.log", "logs/crash-20260101-000000.log"} {
		if _, ok := got[name]; !ok {
			t.Errorf("zip missing entry %q (have %v)", name, keys(got))
		}
	}
	if strings.Contains(got["config.redacted.json"], "secret-1") {
		t.Error("config secret leaked into zip")
	}
	var env Env
	if err := json.Unmarshal([]byte(got["env.json"]), &env); err != nil {
		t.Fatalf("env.json not parseable: %v", err)
	}
	if env.AppVersion != "0.4.0" {
		t.Errorf("env version = %q", env.AppVersion)
	}
}

func TestBuildZipTruncatesLargeLogs(t *testing.T) {
	root := t.TempDir()
	logDir := filepath.Join(root, "logs")
	if err := os.MkdirAll(logDir, 0o700); err != nil {
		t.Fatal(err)
	}
	big := strings.Repeat("x", 4096) + "TAIL-MARKER"
	if err := os.WriteFile(filepath.Join(logDir, "agentpack.log"), []byte(big), 0o600); err != nil {
		t.Fatal(err)
	}

	zipPath, err := BuildZip(ZipOptions{
		ExportsDir:  filepath.Join(root, "exports"),
		LogDir:      logDir,
		DataDir:     filepath.Join(root, "data"),
		Env:         Env{},
		MaxLogBytes: 64,
	})
	if err != nil {
		t.Fatalf("BuildZip: %v", err)
	}
	r, err := zip.OpenReader(zipPath)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	for _, f := range r.File {
		if f.Name != "logs/agentpack.log" {
			continue
		}
		rc, err := f.Open()
		if err != nil {
			t.Fatal(err)
		}
		buf, err := io.ReadAll(rc)
		rc.Close()
		if err != nil {
			t.Fatalf("read: %v", err)
		}
		content := string(buf)
		if !strings.Contains(content, "truncated") {
			t.Errorf("large log should carry truncation marker: %q", content)
		}
		if !strings.Contains(content, "TAIL-MARKER") {
			t.Errorf("tail content missing: %q", content)
		}
		return
	}
	t.Fatal("logs/agentpack.log missing")
}

func TestBuildZipGzHandling(t *testing.T) {
	root := t.TempDir()

	// 小 gz（<maxLog）应全量包含且字节完整——绝不按明文日志那样截断（会损坏 gzip 流）。
	smallDir := filepath.Join(root, "logdir1")
	if err := os.MkdirAll(smallDir, 0o700); err != nil {
		t.Fatal(err)
	}
	small := bytes.Repeat([]byte("s"), 100)
	if err := os.WriteFile(filepath.Join(smallDir, "small.log.gz"), small, 0o600); err != nil {
		t.Fatal(err)
	}
	zipPath, err := BuildZip(ZipOptions{
		ExportsDir:  filepath.Join(root, "exports1"),
		LogDir:      smallDir,
		DataDir:     filepath.Join(root, "data"),
		Env:         Env{},
		MaxLogBytes: 1024,
	})
	if err != nil {
		t.Fatalf("BuildZip: %v", err)
	}
	r, err := zip.OpenReader(zipPath)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, f := range r.File {
		if f.Name != "logs/small.log.gz" {
			continue
		}
		rc, err := f.Open()
		if err != nil {
			t.Fatal(err)
		}
		buf, err := io.ReadAll(rc)
		rc.Close()
		if err != nil {
			t.Fatalf("read: %v", err)
		}
		found = true
		if len(buf) != len(small) || !bytes.Equal(buf, small) {
			t.Errorf("gz archive not intact (want %d bytes, got %d)", len(small), len(buf))
		}
	}
	r.Close()
	if !found {
		t.Fatal("small gz missing from zip")
	}

	// 剩余空间装不下完整归档时，大 gz 应整体跳过，绝不写入半截损坏流。
	bigDir := filepath.Join(root, "logdir2")
	if err := os.MkdirAll(bigDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(bigDir, "big.log.gz"), bytes.Repeat([]byte("b"), 400), 0o600); err != nil {
		t.Fatal(err)
	}
	zipPath2, err := BuildZip(ZipOptions{
		ExportsDir:  filepath.Join(root, "exports2"),
		LogDir:      bigDir,
		DataDir:     filepath.Join(root, "data"),
		Env:         Env{},
		MaxLogBytes: 100,
	})
	if err != nil {
		t.Fatalf("BuildZip2: %v", err)
	}
	r2, err := zip.OpenReader(zipPath2)
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range r2.File {
		if f.Name == "logs/big.log.gz" {
			t.Fatal("oversized gz should be skipped whole, not included truncated")
		}
	}
	r2.Close()
}

func TestProbeWritable(t *testing.T) {
	if !probeWritable(t.TempDir()) {
		t.Error("temp dir should be writable")
	}
	if probeWritable("") {
		t.Error("empty dir should not be writable")
	}
	// 探测后不留任何临时文件
	dir := t.TempDir()
	probeWritable(dir)
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Errorf("probe left files behind: %v", entries)
	}
}

func keys(m map[string]string) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}