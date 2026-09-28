package logging

import (
	"log"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func readLog(t *testing.T, dir, file string) string {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(dir, file))
	if err != nil {
		t.Fatalf("log file %s not written: %v", file, err)
	}
	return string(raw)
}

func TestChannelsWriteSeparateFiles(t *testing.T) {
	dir := t.TempDir()
	stop := Init(Options{Dir: dir, Level: "debug"})

	L().Info("app-line")
	Cat("webview").Info("webview-line")
	Cat("mcp").Info("mcp-line")
	Cat("skills").Warn("skills-line")
	Cat("update").Error("update-line")

	stop()
	if got := readLog(t, dir, "app.log"); !strings.Contains(got, "app-line") || !strings.Contains(got, "cat=app") {
		t.Errorf("app.log unexpected: %q", got)
	}
	if got := readLog(t, dir, "webview.log"); !strings.Contains(got, "webview-line") || !strings.Contains(got, "cat=webview") {
		t.Errorf("webview.log unexpected: %q", got)
	}
	if got := readLog(t, dir, "mcp.log"); !strings.Contains(got, "mcp-line") {
		t.Errorf("mcp.log unexpected: %q", got)
	}
	if got := readLog(t, dir, "skills.log"); !strings.Contains(got, "level=WARN") || !strings.Contains(got, "skills-line") {
		t.Errorf("skills.log unexpected: %q", got)
	}
	if got := readLog(t, dir, "update.log"); !strings.Contains(got, "level=ERROR") {
		t.Errorf("update.log unexpected: %q", got)
	}
}

func TestUnknownCategoryFallsBackToAppFile(t *testing.T) {
	dir := t.TempDir()
	stop := Init(Options{Dir: dir})
	defer stop()

	Cat("custom-area").Info("custom-line")
	stop()
	got := readLog(t, dir, "app.log")
	if !strings.Contains(got, "custom-line") || !strings.Contains(got, "cat=custom-area") {
		t.Errorf("unknown category should land in app.log with its own cat tag: %q", got)
	}
	if strings.Contains(got, "cat=app cat=") {
		t.Errorf("cat attribute should not be duplicated: %q", got)
	}
}

func TestLevelThresholds(t *testing.T) {
	dir := t.TempDir()
	t.Setenv(LevelEnv, "")
	stop := Init(Options{Dir: dir, Level: "warn"})
	defer stop()

	L().Info("info-hidden")
	L().Warn("app-warn-visible")
	Cat("mcp").Warn("warn-visible")
	Cat("mcp").Error("error-visible")
	stop()
	got := readLog(t, dir, "app.log")
	if strings.Contains(got, "info-hidden") {
		t.Errorf("info should be filtered at warn level: %q", got)
	}
	if !strings.Contains(got, "app-warn-visible") {
		t.Errorf("warn should pass at warn level: %q", got)
	}
	mcp := readLog(t, dir, "mcp.log")
	if !strings.Contains(mcp, "warn-visible") || !strings.Contains(mcp, "error-visible") {
		t.Errorf("warn/error should pass at warn level: %q", mcp)
	}
}

func TestTraceLevelIsMostVerbose(t *testing.T) {
	dir := t.TempDir()
	t.Setenv(LevelEnv, "")
	stop := Init(Options{Dir: dir, Level: "trace"})
	defer stop()

	if LevelName() != "trace" {
		t.Fatalf("LevelName() = %q, want trace", LevelName())
	}
	Cat("app").Log(nil, LevelTrace, "trace-line") //nolint:staticcheck // 测试自定义级别
	stop()
	if got := readLog(t, dir, "app.log"); !strings.Contains(got, "trace-line") {
		t.Errorf("trace level should pass the most verbose lines: %q", got)
	}
}

func TestParseLevel(t *testing.T) {
	cases := map[string]slog.Level{
		"error": slog.LevelError, "warn": slog.LevelWarn, "warning": slog.LevelWarn,
		"info": slog.LevelInfo, "debug": slog.LevelDebug, "trace": LevelTrace,
		" DEBUG ": slog.LevelDebug,
	}
	for in, want := range cases {
		got, ok := ParseLevel(in)
		if !ok || got != want {
			t.Errorf("ParseLevel(%q) = %v,%v want %v", in, got, ok, want)
		}
	}
	if _, ok := ParseLevel("bogus"); ok {
		t.Error("ParseLevel(bogus) should fail")
	}
}

func TestLevelEnvOverridesSetting(t *testing.T) {
	dir := t.TempDir()
	t.Setenv(LevelEnv, "error")
	stop := Init(Options{Dir: dir, Level: "debug"})
	defer stop()

	if got := Level(); got != slog.LevelError {
		t.Errorf("Level() = %v, want %v (env should win)", got, slog.LevelError)
	}
	L().Info("hidden")
	L().Error("visible")
	stop()
	got := readLog(t, dir, "app.log")
	if strings.Contains(got, "hidden") || !strings.Contains(got, "visible") {
		t.Errorf("env level not applied: %q", got)
	}
}

func TestSetLevelAtRuntime(t *testing.T) {
	dir := t.TempDir()
	t.Setenv(LevelEnv, "")
	stop := Init(Options{Dir: dir})
	defer stop()

	if LevelName() != "info" {
		t.Fatalf("default level = %q, want info", LevelName())
	}
	SetLevel("debug")
	if LevelName() != "debug" {
		t.Fatalf("SetLevel(debug) not applied: %q", LevelName())
	}
	SetLevel("error")
	if LevelName() != "error" {
		t.Fatalf("SetLevel(error) not applied: %q", LevelName())
	}
}

func TestLegacyPrintfGoesToAppChannel(t *testing.T) {
	dir := t.TempDir()
	stop := Init(Options{Dir: dir})
	defer stop()

	log.Printf("legacy line")
	stop()
	if got := readLog(t, dir, "app.log"); !strings.Contains(got, "legacy line") {
		t.Errorf("legacy log.Printf output not captured: %q", got)
	}
}

func TestCategoryForFunc(t *testing.T) {
	cases := map[string]string{
		"agentpack/internal/skills.(*Store).Save":      "skills",
		"agentpack/internal/app/skillbackfill.Run":     "skills",
		"agentpack/internal/mcp/store.(*Store).Load":   "mcp",
		"agentpack/internal/market/github.Client.Get":  "market",
		"agentpack/internal/app/market.Service.Export": "market",
		"agentpack/internal/app/update.Service.Check":  "update",
		"agentpack/internal/backup.(*Manager).Create":  "app",
		"agentpack/internal/config.Load":              "app",
		"main.main":                                   "app",
		"github.com/wailsapp/wails/v3/pkg/application.(*App).Run":            "webview",
		"github.com/wailsapp/wails/v3/internal/webview2/pkg/edge.(*Chromium).Embed": "webview",
		"runtime.Callers": "",
		"log.Printf":      "",
	}
	for fn, want := range cases {
		if got := categoryForFunc(fn); got != want {
			t.Errorf("categoryForFunc(%q) = %q, want %q", fn, got, want)
		}
	}
}

func TestWriteCrashCreatesFile(t *testing.T) {
	dir := t.TempDir()
	stop := Init(Options{Dir: dir})
	defer stop()

	path := WriteCrash("boom", []byte("goroutine 1 [running]:\nmain.main()"))
	if path == "" || !strings.HasPrefix(filepath.Base(path), "crash-") {
		t.Fatalf("WriteCrash path = %q", path)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("crash file not written: %v", err)
	}
	if !strings.Contains(string(raw), "boom") || !strings.Contains(string(raw), "goroutine 1") {
		t.Errorf("crash content incomplete: %q", string(raw))
	}
}

func TestCategoriesReportPolicy(t *testing.T) {
	stop := Init(Options{Dir: t.TempDir()})
	defer stop()
	cats := Categories()
	if len(cats) < 7 {
		t.Fatalf("expected at least 7 channels, got %d", len(cats))
	}
	seen := map[string]bool{}
	for _, c := range cats {
		if c.Name == "" || c.File == "" || c.Level == "" || c.Description == "" {
			t.Errorf("category incomplete: %+v", c)
		}
		seen[c.Name] = true
	}
	for _, want := range []string{"app", "webview", "frontend", "update", "market", "mcp", "skills"} {
		if !seen[want] {
			t.Errorf("missing channel %q", want)
		}
	}
}

func TestNoDirOnlyStderr(t *testing.T) {
	stop := Init(Options{Dir: ""})
	defer stop()
	if got := Dir(); got != "" {
		t.Errorf("Dir() = %q, want empty", got)
	}
	if path := WriteCrash("x", nil); path != "" {
		t.Errorf("WriteCrash without dir should return empty path, got %q", path)
	}
}