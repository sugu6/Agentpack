package update

import (
	"context"
	"crypto/sha256"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// ---------- 方案E：expanded_assets digest 校验 ----------

// overrideDigestBase 把 expanded_assets 抓取基址指向本地服务（同 DefaultGitHubProxy 手法）。
func overrideDigestBase(t *testing.T, handler http.HandlerFunc) {
	t.Helper()
	srv := httptest.NewServer(handler)
	t.Cleanup(func() { srv.Close() })
	orig := expandedAssetsBase
	expandedAssetsBase = srv.URL
	t.Cleanup(func() { expandedAssetsBase = orig })
}

// synthDigestHTML 构造与真实页面同形的最小 digest 按钮片段。
func synthDigestHTML(name string, payload []byte) string {
	sum := sha256.Sum256(payload)
	return fmt.Sprintf(`<button aria-label="Copy to clipboard digest for %s" type="button" value="sha256:%x" data-view-component="true"></button>`, name, sum)
}

// overrideDigestFor 为资产提供"正确摘要"（方案E 成功路径的摘要来源）。
func overrideDigestFor(t *testing.T, name string, payload []byte) {
	t.Helper()
	body := synthDigestHTML(name, payload)
	overrideDigestBase(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		fmt.Fprint(w, body)
	})
}

// TestFetchExpectedDigest_RealSnapshot 真实 v0.3.0 页面快照：按资产名解析 sha256
// （pin GitHub 前端结构；GitHub 改版导致解析失败时此测先红）。
func TestFetchExpectedDigest_RealSnapshot(t *testing.T) {
	fixture, err := os.ReadFile(filepath.Join("testdata", "expanded_assets_v0.3.0.html"))
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}
	overrideDigestBase(t, func(w http.ResponseWriter, r *http.Request) {
		w.Write(fixture)
	})

	// 代理形 URL 也要能解析（host 无关，只认 /releases/download/{tag}/{asset}）
	got, algo, err := fetchExpectedDigest(context.Background(), "https://gh-proxy.com/github.com/sugu6/AgentPack/releases/download/v0.3.0/AgentPack-0.3.0-windows-amd64-installer.exe")
	if err != nil {
		t.Fatalf("fetchExpectedDigest: %v", err)
	}
	if algo != "" {
		t.Errorf("algo = %q, want sha256 (empty algo)", algo)
	}
	want := "3629e54c50d756a391bb6b00def9b8a9190486ce1036590760fd8ede769fbba0"
	if got != want {
		t.Errorf("digest = %q, want %q", got, want)
	}

	// 快照中不存在的资产 → fail-closed 错误
	if _, _, err := fetchExpectedDigest(context.Background(), assetURL("AgentPack-9.9.9-doesnotexist.exe")); err == nil {
		t.Errorf("missing asset must fail closed")
	}
}

// TestDownloadVerifyTamperRejected 摘要与代理投毒字节不符：fail-closed 拒收，
// 删除临时文件、不产出安装包。
func TestDownloadVerifyTamperRejected(t *testing.T) {
	dir := setupDownloadDir(t)
	clean := patternBytes(64 * 1024)
	tampered := append([]byte(nil), clean...)
	tampered[10] ^= 0xFF

	name := "update-test-tamper.exe"
	overrideDigestFor(t, name, clean)
	srv := httptest.NewServer(&dlTestHandler{payload: tampered})
	defer srv.Close()
	overrideProxy(t, srv)

	log := &eventLog{}
	svc := newTestService(t, log)
	if err := svc.StartDownload(assetURL(name)); err != nil {
		t.Fatalf("StartDownload: %v", err)
	}
	data := log.wait(t, "update:download:error", 15*time.Second)
	if msg := mustStr(t, data, "message"); !strings.Contains(msg, "integrity") {
		t.Errorf("error message should mention integrity check, got %q", msg)
	}
	final := filepath.Join(dir, name)
	if _, err := os.Stat(final); !os.IsNotExist(err) {
		t.Errorf("tampered download must not produce final file")
	}
	if _, err := os.Stat(final + ".downloading"); !os.IsNotExist(err) {
		t.Errorf("tampered temp file must be removed")
	}
}

// TestDownloadVerifyFetchFailsClosed 摘要页取不到：下载请求根本不该发起（fail-closed）。
func TestDownloadVerifyFetchFailsClosed(t *testing.T) {
	dir := setupDownloadDir(t)
	overrideDigestBase(t, func(w http.ResponseWriter, r *http.Request) {
		http.NotFound(w, r)
	})
	h := &dlTestHandler{payload: patternBytes(1024)}
	srv := httptest.NewServer(h)
	defer srv.Close()
	overrideProxy(t, srv)

	log := &eventLog{}
	svc := newTestService(t, log)
	name := "update-test-nodigest.exe"
	if err := svc.StartDownload(assetURL(name)); err != nil {
		t.Fatalf("StartDownload: %v", err)
	}
	data := log.wait(t, "update:download:error", 15*time.Second)
	if msg := mustStr(t, data, "message"); !strings.Contains(msg, "integrity") {
		t.Errorf("error message should mention integrity, got %q", msg)
	}
	if h.hits != 0 {
		t.Errorf("download request must not be issued when digest unavailable, proxy hits=%d", h.hits)
	}
	final := filepath.Join(dir, name)
	if _, err := os.Stat(final); !os.IsNotExist(err) {
		t.Errorf("final file must not exist")
	}
	if _, err := os.Stat(final + ".downloading"); !os.IsNotExist(err) {
		t.Errorf("temp file must not exist")
	}
}

// TestDownloadVerifyUnsupportedAlgo 摘要算法非 sha256：fail-closed 拒绝执行链。
func TestDownloadVerifyUnsupportedAlgo(t *testing.T) {
	dir := setupDownloadDir(t)
	name := "update-test-algo.exe"
	sum := sha256.Sum256(nil)
	body := fmt.Sprintf(`<button aria-label="Copy to clipboard digest for %s" type="button" value="sha512:%x"></button>`, name, sum)
	overrideDigestBase(t, func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, body)
	})
	h := &dlTestHandler{payload: patternBytes(1024)}
	srv := httptest.NewServer(h)
	defer srv.Close()
	overrideProxy(t, srv)

	log := &eventLog{}
	svc := newTestService(t, log)
	if err := svc.StartDownload(assetURL(name)); err != nil {
		t.Fatalf("StartDownload: %v", err)
	}
	data := log.wait(t, "update:download:error", 15*time.Second)
	if msg := mustStr(t, data, "message"); !strings.Contains(msg, "unsupported algorithm") {
		t.Errorf("error message should mention unsupported algorithm, got %q", msg)
	}
	if h.hits != 0 {
		t.Errorf("download must not start with unsupported digest algorithm, hits=%d", h.hits)
	}
	if _, err := os.Stat(filepath.Join(dir, name)); !os.IsNotExist(err) {
		t.Errorf("final file must not exist")
	}
}

// TestDownloadVerifyResumeStillChecks 暂停→续传→完成也必须过校验：
// 若前 offset 字节未补喂哈希器，最终摘要会失配而报错 —— 本测即该逻辑的回归网。
func TestDownloadVerifyResumeStillChecks(t *testing.T) {
	dir := setupDownloadDir(t)
	payload := patternBytes(1024 * 1024)
	name := "update-test-verify-resume.exe"
	overrideDigestFor(t, name, payload)
	srv := httptest.NewServer(&dlTestHandler{payload: payload, drip: true})
	defer srv.Close()
	overrideProxy(t, srv)

	log := &eventLog{}
	svc := newTestService(t, log)
	if err := svc.StartDownload(assetURL(name)); err != nil {
		t.Fatalf("StartDownload: %v", err)
	}
	log.wait(t, "update:download:progress", 15*time.Second)
	if err := svc.Pause(); err != nil {
		t.Fatalf("Pause: %v", err)
	}
	log.wait(t, "update:download:paused", 15*time.Second)
	if err := svc.Resume(); err != nil {
		t.Fatalf("Resume: %v", err)
	}
	log.wait(t, "update:download:complete", 15*time.Second)
	got, err := os.ReadFile(filepath.Join(dir, name))
	if err != nil {
		t.Fatalf("read final file: %v", err)
	}
	if len(got) != len(payload) {
		t.Errorf("final bytes = %d, want %d", len(got), len(payload))
	}
	if _, err := os.Stat(filepath.Join(dir, name) + ".downloading"); !os.IsNotExist(err) {
		t.Errorf("temp file should be removed")
	}
}

// TestFetchExpectedDigestUnsupportedLongAlgo sha512（128 hex）必须归类为
// "不支持的算法"而非"摘要缺失"：值正则不得钉死 64 hex——长摘要落进
// not-found 分支会把不可自愈的算法问题误报成可重试的抓取失败。
func TestFetchExpectedDigestUnsupportedLongAlgo(t *testing.T) {
	name := "update-test-sha512.exe"
	body := fmt.Sprintf(`<button aria-label="Copy to clipboard digest for %s" type="button" value="sha512:%s"></button>`, name, strings.Repeat("ab", 64))
	overrideDigestBase(t, func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, body)
	})
	_, algo, err := fetchExpectedDigest(context.Background(), assetURL(name))
	if err == nil {
		t.Fatal("want error for unsupported algorithm")
	}
	if algo != "sha512" {
		t.Errorf("algo = %q, want sha512 (long digest must classify by algorithm, not fall into not-found)", algo)
	}
}

// TestDownloadCancelDuringDigestFetchSilent 摘要抓取进行中取消：
// 抓取必须随下载 ctx 中断（Cancel 秒退 = goroutine 无孤儿），且全程静默——
// 取消后不得出现错误事件、不得把状态翻成 Error（否则击穿并发启动守卫
// 并污染随后开始的新下载）。
func TestDownloadCancelDuringDigestFetchSilent(t *testing.T) {
	setupDownloadDir(t)
	entered := make(chan struct{})
	release := make(chan struct{})
	overrideDigestBase(t, func(w http.ResponseWriter, r *http.Request) {
		close(entered) // fetch 无重试，仅会进入一次
		<-release      // 服务端挂起直到测试放行（不理会客户端断开）
		w.WriteHeader(http.StatusInternalServerError)
	})
	srv := httptest.NewServer(&dlTestHandler{payload: patternBytes(1024)})
	defer srv.Close()
	overrideProxy(t, srv)

	log := &eventLog{}
	svc := newTestService(t, log)
	name := "update-test-cancel-fetch.exe"
	if err := svc.StartDownload(assetURL(name)); err != nil {
		t.Fatalf("StartDownload: %v", err)
	}
	select {
	case <-entered:
	case <-time.After(15 * time.Second):
		t.Fatal("digest fetch never started")
	}

	start := time.Now()
	if err := svc.Cancel(); err != nil {
		t.Fatalf("Cancel: %v", err)
	}
	if elapsed := time.Since(start); elapsed > 2*time.Second {
		t.Errorf("Cancel blocked %v on digest fetch; fetch must follow download ctx and exit promptly", elapsed)
	}
	close(release)

	// 放行后给残存抓取一个完成窗口：出现任何错误事件都是取消语义违例
	deadline := time.Now().Add(1500 * time.Millisecond)
	for time.Now().Before(deadline) {
		if _, ok := log.find("update:download:error"); ok {
			t.Error("cancel during digest fetch must not emit download:error")
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	svc.mu.Lock()
	st := svc.state
	svc.mu.Unlock()
	if st != StateIdle {
		t.Errorf("state = %v, want Idle after cancel", st)
	}
}
