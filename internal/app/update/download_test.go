package update

import (
	"bytes"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"agentpack/internal/config"
)

// ---------- 测试基础设施 ----------

// evt 记录一次前端事件（name + 载荷）。
type evt struct {
	name string
	data interface{}
}

// eventLog 线程安全地收集 Service.emit 的事件，供断言与等待。
type eventLog struct {
	mu   sync.Mutex
	evts []evt
}

func (l *eventLog) emit(name string, data ...interface{}) {
	l.mu.Lock()
	defer l.mu.Unlock()
	var d interface{}
	if len(data) > 0 {
		d = data[0]
	}
	l.evts = append(l.evts, evt{name: name, data: d})
}

// find 返回首个匹配事件的载荷（不阻塞）。
func (l *eventLog) find(name string) (interface{}, bool) {
	l.mu.Lock()
	defer l.mu.Unlock()
	for _, e := range l.evts {
		if e.name == name {
			return e.data, true
		}
	}
	return nil, false
}

// wait 轮询等待事件到达，超时则 Failf（附已收到事件清单便于诊断）。
func (l *eventLog) wait(t *testing.T, name string, timeout time.Duration) interface{} {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if d, ok := l.find(name); ok {
			return d
		}
		time.Sleep(10 * time.Millisecond)
	}
	l.mu.Lock()
	names := make([]string, 0, len(l.evts))
	for _, e := range l.evts {
		names = append(names, e.name)
	}
	l.mu.Unlock()
	t.Fatalf("timed out waiting for event %q after %v; received: %v", name, timeout, names)
	return nil
}

// mustStr 从事件载荷取字符串字段（兼容 map[string]interface{} / map[string]string）。
func mustStr(t *testing.T, data interface{}, key string) string {
	t.Helper()
	switch m := data.(type) {
	case map[string]interface{}:
		v, ok := m[key]
		if !ok {
			t.Fatalf("event payload missing key %q: %#v", key, data)
		}
		s, ok := v.(string)
		if !ok {
			t.Fatalf("event payload key %q is not string: %#v", key, v)
		}
		return s
	case map[string]string:
		v, ok := m[key]
		if !ok {
			t.Fatalf("event payload missing key %q: %#v", key, data)
		}
		return v
	}
	t.Fatalf("event payload is not a map: %#v", data)
	return ""
}

// mustNum 从事件载荷取数值字段（int/int64/float64 归一为 int64）。
func mustNum(t *testing.T, data interface{}, key string) int64 {
	t.Helper()
	m, ok := data.(map[string]interface{})
	if !ok {
		t.Fatalf("event payload is not map[string]interface{}: %#v", data)
	}
	v, ok := m[key]
	if !ok {
		t.Fatalf("event payload missing key %q: %#v", key, data)
	}
	switch n := v.(type) {
	case int64:
		return n
	case int:
		return int64(n)
	case float64:
		return int64(n)
	}
	t.Fatalf("event payload key %q is not numeric: %#v", key, v)
	return 0
}

// newTestService 构造一个事件被捕获的 Service（其余回调为无操作桩）。
func newTestService(t *testing.T, log *eventLog) *Service {
	t.Helper()
	return NewService(
		log.emit,
		func() string { return "en" },
		func() {},
		func() error { return nil },
		func() {},
	)
}

// setupDownloadDir 把 downloadDir() 隔离进 t.TempDir（按平台设置对应环境变量），
// 并返回被测代码同源计算出的下载目录，避免测试写入真实 Downloads。
func setupDownloadDir(t *testing.T) string {
	t.Helper()
	base := t.TempDir()
	home := filepath.Join(base, "home")
	dl := filepath.Join(home, "Downloads")
	if err := os.MkdirAll(dl, 0o755); err != nil {
		t.Fatalf("mkdir test download dir: %v", err)
	}
	t.Setenv("USERPROFILE", home) // windows: USERPROFILE\Downloads
	t.Setenv("HOME", home)        // darwin/linux 回退
	t.Setenv("XDG_DOWNLOAD_DIR", dl)
	dir := downloadDir()
	if dir != dl {
		t.Fatalf("downloadDir() = %q, want isolated dir %q", dir, dl)
	}
	return dir
}

// overrideProxy 把下载代理指向本地 httptest 服务（skills 测试同款手法）。
func overrideProxy(t *testing.T, srv *httptest.Server) {
	t.Helper()
	orig := config.DefaultGitHubProxy
	config.DefaultGitHubProxy = srv.URL + "/"
	t.Cleanup(func() { config.DefaultGitHubProxy = orig })
}

// assetURL 构造形如 CI 命名规则的 github.com 资产 URL（v9.9.9 仅测试用）。
func assetURL(name string) string {
	return fmt.Sprintf("https://github.com/%s/releases/download/v9.9.9/%s", Repo, name)
}

// patternBytes 生成确定性测试负载。
func patternBytes(n int) []byte {
	b := make([]byte, n)
	for i := range b {
		b[i] = byte(i*31 + i/7%251)
	}
	return b
}

// dlTestHandler 模拟 GitHub/代理的资产响应，支持慢速滴灌、Range/206、
// 忽略 Range、错误码、超大 Content-Length、截断等行为组合。
type dlTestHandler struct {
	payload       []byte
	drip          bool  // 无 Range 请求按 32KB/20ms 慢速发送（留出暂停窗口）
	ignoreRange   bool  // 忽略 Range，始终 200 全量
	status        int   // 非 0：始终返回该状态码
	statusFirst   int   // 非 0：仅首个请求返回该状态码
	oversizeCL    int64 // >0：只声明该 Content-Length、不发响应体（超大预检）
	truncateAfter int   // >0：声明完整 CL 但只发前 N 字节（截断）
	hits          int32 // 原子请求计数
	sawRange      int32 // 原子：是否收到并以 206 服务过 Range
}

func (h *dlTestHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	hit := atomic.AddInt32(&h.hits, 1)
	st := h.status
	if h.statusFirst != 0 && hit == 1 {
		st = h.statusFirst
	}
	if st != 0 {
		w.WriteHeader(st)
		fmt.Fprint(w, "server error")
		return
	}
	if h.oversizeCL > 0 {
		w.Header().Set("Content-Length", strconv.FormatInt(h.oversizeCL, 10))
		w.WriteHeader(http.StatusOK)
		return
	}
	if h.truncateAfter > 0 {
		w.Header().Set("Content-Length", strconv.Itoa(len(h.payload)))
		w.WriteHeader(http.StatusOK)
		w.Write(h.payload[:h.truncateAfter])
		return
	}

	if !h.ignoreRange && r.Header.Get("Range") != "" {
		var off int64
		fmt.Sscanf(r.Header.Get("Range"), "bytes=%d-", &off)
		if off > 0 && int(off) < len(h.payload) {
			atomic.AddInt32(&h.sawRange, 1)
			w.Header().Set("Content-Length", strconv.Itoa(len(h.payload)-int(off)))
			w.Header().Set("Content-Range", fmt.Sprintf("bytes %d-%d/%d", off, len(h.payload)-1, len(h.payload)))
			w.WriteHeader(http.StatusPartialContent)
			w.Write(h.payload[off:])
			return
		}
	}

	w.Header().Set("Content-Length", strconv.Itoa(len(h.payload)))
	w.WriteHeader(http.StatusOK)
	if len(h.payload) == 0 {
		return
	}
	// 滴灌只用于首个全量请求（带 Range 的续传尾巴全速发完，缩短测试时长）
	if h.drip && r.Header.Get("Range") == "" {
		flusher, _ := w.(http.Flusher)
		const chunk = 32 * 1024
		for i := 0; i < len(h.payload); i += chunk {
			end := i + chunk
			if end > len(h.payload) {
				end = len(h.payload)
			}
			if _, err := w.Write(h.payload[i:end]); err != nil {
				return
			}
			if flusher != nil {
				flusher.Flush()
			}
			time.Sleep(20 * time.Millisecond)
		}
		return
	}
	w.Write(h.payload)
}

// ---------- 特征测试基线（现有行为的护栏） ----------

// TestDownloadFreshCompletes 全新下载：完成事件、最终字节、临时文件收尾。
func TestDownloadFreshCompletes(t *testing.T) {
	dir := setupDownloadDir(t)
	payload := patternBytes(64 * 1024)
	srv := httptest.NewServer(&dlTestHandler{payload: payload})
	defer srv.Close()
	overrideProxy(t, srv)

	log := &eventLog{}
	svc := newTestService(t, log)
	name := "update-test-fresh.exe"
	overrideDigestFor(t, name, payload)
	if err := svc.StartDownload(assetURL(name)); err != nil {
		t.Fatalf("StartDownload: %v", err)
	}
	data := log.wait(t, "update:download:complete", 15*time.Second)
	final := filepath.Join(dir, name)
	if got := mustStr(t, data, "filePath"); got != final {
		t.Errorf("filePath = %q, want %q", got, final)
	}
	if got := mustStr(t, data, "fileName"); got != name {
		t.Errorf("fileName = %q, want %q", got, name)
	}
	got, err := os.ReadFile(final)
	if err != nil {
		t.Fatalf("read downloaded file: %v", err)
	}
	if !bytes.Equal(got, payload) {
		t.Errorf("downloaded bytes mismatch: got %d bytes, want %d", len(got), len(payload))
	}
	if _, err := os.Stat(final + ".downloading"); !os.IsNotExist(err) {
		t.Errorf("temp file %s.downloading should be removed after complete", name)
	}
}

// TestDownloadServerError 非 2xx：错误事件、无最终文件、无临时残留。
func TestDownloadServerError(t *testing.T) {
	dir := setupDownloadDir(t)
	srv := httptest.NewServer(&dlTestHandler{payload: nil, status: http.StatusInternalServerError})
	defer srv.Close()
	overrideProxy(t, srv)

	log := &eventLog{}
	svc := newTestService(t, log)
	name := "update-test-500.exe"
	overrideDigestFor(t, name, nil)
	if err := svc.StartDownload(assetURL(name)); err != nil {
		t.Fatalf("StartDownload: %v", err)
	}
	data := log.wait(t, "update:download:error", 15*time.Second)
	if msg := mustStr(t, data, "message"); msg == "" {
		t.Error("error message should not be empty")
	}
	final := filepath.Join(dir, name)
	if _, err := os.Stat(final); !os.IsNotExist(err) {
		t.Errorf("final file should not exist after server error")
	}
	if _, err := os.Stat(final + ".downloading"); !os.IsNotExist(err) {
		t.Errorf("temp file should be removed after server error")
	}
}

// TestDownloadRestartAfterError 错误后再次 StartDownload 可复位并成功完成。
func TestDownloadRestartAfterError(t *testing.T) {
	dir := setupDownloadDir(t)
	payload := patternBytes(32 * 1024)
	srv := httptest.NewServer(&dlTestHandler{payload: payload, statusFirst: http.StatusInternalServerError})
	defer srv.Close()
	overrideProxy(t, srv)

	log := &eventLog{}
	svc := newTestService(t, log)
	name := "update-test-restart.exe"
	overrideDigestFor(t, name, payload)
	if err := svc.StartDownload(assetURL(name)); err != nil {
		t.Fatalf("first StartDownload: %v", err)
	}
	log.wait(t, "update:download:error", 15*time.Second)
	if _, ok := log.find("update:download:complete"); ok {
		t.Fatal("first attempt should not complete (500)")
	}
	if err := svc.StartDownload(assetURL(name)); err != nil {
		t.Fatalf("second StartDownload: %v", err)
	}
	log.wait(t, "update:download:complete", 15*time.Second)
	got, err := os.ReadFile(filepath.Join(dir, name))
	if err != nil {
		t.Fatalf("read final file: %v", err)
	}
	if !bytes.Equal(got, payload) {
		t.Errorf("final bytes mismatch after restart")
	}
}

// TestDownloadEmptyBody 空响应体：拒绝"空完成"。
func TestDownloadEmptyBody(t *testing.T) {
	dir := setupDownloadDir(t)
	srv := httptest.NewServer(&dlTestHandler{payload: nil})
	defer srv.Close()
	overrideProxy(t, srv)

	log := &eventLog{}
	svc := newTestService(t, log)
	name := "update-test-empty.exe"
	overrideDigestFor(t, name, nil)
	if err := svc.StartDownload(assetURL(name)); err != nil {
		t.Fatalf("StartDownload: %v", err)
	}
	log.wait(t, "update:download:error", 15*time.Second)
	final := filepath.Join(dir, name)
	if _, err := os.Stat(final); !os.IsNotExist(err) {
		t.Errorf("final file should not exist after empty download")
	}
	if _, err := os.Stat(final + ".downloading"); !os.IsNotExist(err) {
		t.Errorf("temp file should be removed after empty download")
	}
}

// TestDownloadTruncated 提前断流（声明完整 CL 只发一段）：报错且不产出安装包。
func TestDownloadTruncated(t *testing.T) {
	dir := setupDownloadDir(t)
	payload := patternBytes(100 * 1024)
	srv := httptest.NewServer(&dlTestHandler{payload: payload, truncateAfter: 16 * 1024})
	defer srv.Close()
	overrideProxy(t, srv)

	log := &eventLog{}
	svc := newTestService(t, log)
	name := "update-test-truncated.exe"
	overrideDigestFor(t, name, payload)
	if err := svc.StartDownload(assetURL(name)); err != nil {
		t.Fatalf("StartDownload: %v", err)
	}
	log.wait(t, "update:download:error", 15*time.Second)
	final := filepath.Join(dir, name)
	if _, err := os.Stat(final); !os.IsNotExist(err) {
		t.Errorf("truncated download must not produce a final file")
	}
	if _, err := os.Stat(final + ".downloading"); !os.IsNotExist(err) {
		t.Errorf("temp file should be removed after truncation")
	}
}

// TestDownloadOversizeRejected Content-Length 超过 1GB 上限：预检拒绝。
func TestDownloadOversizeRejected(t *testing.T) {
	dir := setupDownloadDir(t)
	srv := httptest.NewServer(&dlTestHandler{oversizeCL: 1<<30 + 1})
	defer srv.Close()
	overrideProxy(t, srv)

	log := &eventLog{}
	svc := newTestService(t, log)
	name := "update-test-oversize.exe"
	overrideDigestFor(t, name, nil)
	if err := svc.StartDownload(assetURL(name)); err != nil {
		t.Fatalf("StartDownload: %v", err)
	}
	log.wait(t, "update:download:error", 15*time.Second)
	final := filepath.Join(dir, name)
	if _, err := os.Stat(final); !os.IsNotExist(err) {
		t.Errorf("oversized download must not produce a final file")
	}
	if _, err := os.Stat(final + ".downloading"); !os.IsNotExist(err) {
		t.Errorf("temp file should be removed after oversize rejection")
	}
}

// TestDownloadCancelSilentCleanup 主动取消：静默（无错误/完成事件）、清理临时文件、可重启。
func TestDownloadCancelSilentCleanup(t *testing.T) {
	dir := setupDownloadDir(t)
	payload := patternBytes(1024 * 1024) // 1MB，滴灌 ~640ms，留出取消窗口
	srv := httptest.NewServer(&dlTestHandler{payload: payload, drip: true})
	defer srv.Close()
	overrideProxy(t, srv)

	log := &eventLog{}
	svc := newTestService(t, log)
	name := "update-test-cancel.exe"
	overrideDigestFor(t, name, payload)
	if err := svc.StartDownload(assetURL(name)); err != nil {
		t.Fatalf("StartDownload: %v", err)
	}
	prog := log.wait(t, "update:download:progress", 15*time.Second)
	if got := mustNum(t, prog, "downloaded"); got <= 0 {
		t.Fatalf("progress downloaded = %d, want > 0", got)
	}
	if err := svc.Cancel(); err != nil {
		t.Fatalf("Cancel: %v", err)
	}
	// Cancel 返回时下载 goroutine 已退出，此后不可能再有错误/完成事件
	if _, ok := log.find("update:download:error"); ok {
		t.Error("cancel must not emit download:error")
	}
	if _, ok := log.find("update:download:complete"); ok {
		t.Error("cancel must not emit download:complete")
	}
	final := filepath.Join(dir, name)
	if _, err := os.Stat(final + ".downloading"); !os.IsNotExist(err) {
		t.Errorf("temp file should be removed after cancel")
	}
	// 取消后可重新开始并成功完成
	if err := svc.StartDownload(assetURL(name)); err != nil {
		t.Fatalf("StartDownload after cancel: %v", err)
	}
	log.wait(t, "update:download:complete", 15*time.Second)
	got, err := os.ReadFile(final)
	if err != nil {
		t.Fatalf("read final file: %v", err)
	}
	if !bytes.Equal(got, payload) {
		t.Errorf("bytes mismatch after cancel+restart")
	}
}

// TestDownloadPauseResumeRoundtrip 暂停→续传往返：暂停事件/临时文件保留/
// Range 请求（206 追加）→ 完成字节与原负载一致。
func TestDownloadPauseResumeRoundtrip(t *testing.T) {
	dir := setupDownloadDir(t)
	payload := patternBytes(1024 * 1024)
	h := &dlTestHandler{payload: payload, drip: true}
	srv := httptest.NewServer(h)
	defer srv.Close()
	overrideProxy(t, srv)

	log := &eventLog{}
	svc := newTestService(t, log)
	name := "update-test-pause.exe"
	overrideDigestFor(t, name, payload)
	if err := svc.StartDownload(assetURL(name)); err != nil {
		t.Fatalf("StartDownload: %v", err)
	}
	// 等首个进度事件（≥200ms 已有字节落盘）再暂停，确保 offset > 0
	log.wait(t, "update:download:progress", 15*time.Second)
	if err := svc.Pause(); err != nil {
		t.Fatalf("Pause: %v", err)
	}
	paused := log.wait(t, "update:download:paused", 15*time.Second)
	downloaded := mustNum(t, paused, "downloaded")
	total := mustNum(t, paused, "total")
	if downloaded <= 0 || downloaded >= total {
		t.Fatalf("paused downloaded = %d, total = %d, want 0 < d < total", downloaded, total)
	}
	if got := mustStr(t, paused, "fileName"); got != name {
		t.Errorf("paused fileName = %q, want %q", got, name)
	}
	if total != int64(len(payload)) {
		t.Errorf("paused total = %d, want %d", total, len(payload))
	}
	final := filepath.Join(dir, name)
	fi, err := os.Stat(final + ".downloading")
	if err != nil {
		t.Fatalf("temp file should exist while paused: %v", err)
	}
	if fi.Size() != downloaded {
		t.Errorf("temp file size = %d, want downloaded = %d", fi.Size(), downloaded)
	}

	if err := svc.Resume(); err != nil {
		t.Fatalf("Resume: %v", err)
	}
	log.wait(t, "update:download:complete", 15*time.Second)
	if atomic.LoadInt32(&h.sawRange) == 0 {
		t.Error("resume should have issued a Range request (206 append path)")
	}
	got, err := os.ReadFile(final)
	if err != nil {
		t.Fatalf("read final file: %v", err)
	}
	if !bytes.Equal(got, payload) {
		t.Errorf("resumed bytes mismatch: got %d, want %d", len(got), len(payload))
	}
	if _, err := os.Stat(final + ".downloading"); !os.IsNotExist(err) {
		t.Errorf("temp file should be removed after resume completes")
	}
}

// TestDownloadResumeTempSizeMismatchFailsClosed 续传前临时文件尺寸被改动：
// 必须 fail-closed 报错、移除损坏临时文件、不产出安装包。
func TestDownloadResumeTempSizeMismatchFailsClosed(t *testing.T) {
	dir := setupDownloadDir(t)
	payload := patternBytes(1024 * 1024)
	srv := httptest.NewServer(&dlTestHandler{payload: payload, drip: true})
	defer srv.Close()
	overrideProxy(t, srv)

	log := &eventLog{}
	svc := newTestService(t, log)
	name := "update-test-mismatch.exe"
	overrideDigestFor(t, name, payload)
	if err := svc.StartDownload(assetURL(name)); err != nil {
		t.Fatalf("StartDownload: %v", err)
	}
	log.wait(t, "update:download:progress", 15*time.Second)
	if err := svc.Pause(); err != nil {
		t.Fatalf("Pause: %v", err)
	}
	paused := log.wait(t, "update:download:paused", 15*time.Second)
	downloaded := mustNum(t, paused, "downloaded")

	final := filepath.Join(dir, name)
	tmp := final + ".downloading"
	if err := os.Truncate(tmp, downloaded/2); err != nil {
		t.Fatalf("truncate temp file: %v", err)
	}
	if err := svc.Resume(); err != nil {
		t.Fatalf("Resume: %v", err)
	}
	data := log.wait(t, "update:download:error", 15*time.Second)
	if msg := mustStr(t, data, "message"); msg == "" {
		t.Error("error message should not be empty")
	}
	if _, err := os.Stat(tmp); !os.IsNotExist(err) {
		t.Errorf("mismatched temp file must be removed (fail-closed)")
	}
	if _, err := os.Stat(final); !os.IsNotExist(err) {
		t.Errorf("mismatched resume must not produce a final file")
	}
}

// TestDownloadResumeServerIgnoresRange 服务器忽略 Range（回 200 全量）：
// 退化为从头重建，最终字节仍须与负载一致。
func TestDownloadResumeServerIgnoresRange(t *testing.T) {
	dir := setupDownloadDir(t)
	payload := patternBytes(512 * 1024)
	h := &dlTestHandler{payload: payload, drip: true, ignoreRange: true}
	srv := httptest.NewServer(h)
	defer srv.Close()
	overrideProxy(t, srv)

	log := &eventLog{}
	svc := newTestService(t, log)
	name := "update-test-norange.exe"
	overrideDigestFor(t, name, payload)
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
	if atomic.LoadInt32(&h.sawRange) != 0 {
		t.Error("handler with ignoreRange must never serve 206")
	}
	got, err := os.ReadFile(filepath.Join(dir, name))
	if err != nil {
		t.Fatalf("read final file: %v", err)
	}
	if !bytes.Equal(got, payload) {
		t.Errorf("rebuilt bytes mismatch: got %d, want %d", len(got), len(payload))
	}
}

// TestAllowedRedirectHost 更新下载重定向 host 封闭白名单矩阵（P2-3）。
// 白名单外（任意第三方域、IP 字面量含私网/环回/链路本地、后缀伪装域）一律拒绝。
func TestAllowedRedirectHost(t *testing.T) {
	cases := []struct {
		host string
		want bool
	}{
		{"github.com", true},
		{"gh-proxy.com", true},
		{"release-assets.githubusercontent.com", true},
		{"objects.githubusercontent.com", true},
		{"GitHub.COM", true}, // host 大小写不敏感
		{"evil.com", false},
		{"github.com.evil.com", false},
		{"gh-proxy.com.evil.com", false},
		{"notgithub.com", false},
		{"127.0.0.1", false},
		{"192.168.1.1", false},
		{"169.254.169.254", false}, // 云元数据地址
		{"::1", false},
		{"", false},
	}
	for _, c := range cases {
		if got := allowedRedirectHost(c.host); got != c.want {
			t.Errorf("allowedRedirectHost(%q) = %v, want %v", c.host, got, c.want)
		}
	}
}

// TestDownloadRejectsRedirectToUntrustedHost 代理回 302 跳向非白名单 host
// （本地受害者服务）：必须 fail-closed 报错、绝不拨号受害者、不产出安装包（P2-3）。
func TestDownloadRejectsRedirectToUntrustedHost(t *testing.T) {
	dir := setupDownloadDir(t)
	var victimHits int32
	victim := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&victimHits, 1)
		w.Write(patternBytes(1024))
	}))
	defer victim.Close()

	proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Location", victim.URL+"/stolen.exe")
		w.WriteHeader(http.StatusFound)
	}))
	defer proxy.Close()
	overrideProxy(t, proxy)

	log := &eventLog{}
	svc := newTestService(t, log)
	name := "update-test-redirect.exe"
	overrideDigestFor(t, name, nil)
	if err := svc.StartDownload(assetURL(name)); err != nil {
		t.Fatalf("StartDownload: %v", err)
	}
	deadline := time.Now().Add(10 * time.Second)
	sawComplete, sawError := false, false
	for time.Now().Before(deadline) {
		if _, ok := log.find("update:download:error"); ok {
			sawError = true
			break
		}
		if _, ok := log.find("update:download:complete"); ok {
			sawComplete = true
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if sawComplete {
		t.Fatal("download followed redirect to untrusted host — must fail closed")
	}
	if !sawError {
		t.Fatal("want update:download:error event")
	}
	if n := atomic.LoadInt32(&victimHits); n != 0 {
		t.Errorf("untrusted redirect target contacted %d times, want 0", n)
	}
	if _, err := os.Stat(filepath.Join(dir, name)); !os.IsNotExist(err) {
		t.Errorf("final file must not exist after rejected redirect")
	}
}

// TestValidateUpdateInstallerTable validateUpdateInstaller 表驱动（按当前 GOOS）。
func TestValidateUpdateInstallerTable(t *testing.T) {
	dir := t.TempDir()
	validExt := ".exe"
	invalidExt := ".txt"
	switch runtime.GOOS {
	case "windows":
		validExt, invalidExt = ".exe", ".txt"
	case "darwin":
		validExt, invalidExt = ".dmg", ".txt"
	case "linux":
		validExt, invalidExt = ".tar.gz", ".txt"
	default:
		validExt, invalidExt = "", ".exe" // 未覆盖的 GOOS：无扩展名校验
	}

	write := func(name string, content string) string {
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
			t.Fatalf("write fixture: %v", err)
		}
		return p
	}

	cases := []struct {
		name    string
		path    string
		wantErr bool
	}{
		{"valid installer", write("app"+validExt, "payload"), false},
		{"empty file", write("empty"+validExt, ""), true},
	}
	if validExt != "" {
		cases = append(cases, struct {
			name    string
			path    string
			wantErr bool
		}{"wrong extension", write("app"+invalidExt, "payload"), true})
	}
	cases = append(cases, struct {
		name    string
		path    string
		wantErr bool
	}{"missing file", filepath.Join(dir, "nope"+validExt), true})

	for _, c := range cases {
		err := validateUpdateInstaller(c.path)
		if c.wantErr && err == nil {
			t.Errorf("%s: want error, got nil", c.name)
		}
		if !c.wantErr && err != nil {
			t.Errorf("%s: want nil, got %v", c.name, err)
		}
	}
}

// TestDownloadSecondFailureLeavesNoResidue 同一 Service 会话内第二次失败的
// 下载也必须清理 .downloading：tmpRemoved 是 CAS 单向标志，新下载登记
// dlTmpPath 时必须复位，否则首次清理后所有后续 removeTmp 都空转、残留文件。
func TestDownloadSecondFailureLeavesNoResidue(t *testing.T) {
	dir := setupDownloadDir(t)
	// 第1次：500（未创建文件，但 removeTmp 已把 CAS 置1）；第2次：空响应体
	// （已创建文件后失败）——若 CAS 未复位，第二次清理被跳过 → 残留。
	srv := httptest.NewServer(&dlTestHandler{payload: nil, statusFirst: http.StatusInternalServerError})
	defer srv.Close()
	overrideProxy(t, srv)

	log := &eventLog{}
	svc := newTestService(t, log)
	name := "update-test-residue.exe"
	overrideDigestFor(t, name, nil)

	waitErrs := func(n int) {
		t.Helper()
		deadline := time.Now().Add(15 * time.Second)
		for time.Now().Before(deadline) {
			log.mu.Lock()
			c := 0
			for _, e := range log.evts {
				if e.name == "update:download:error" {
					c++
				}
			}
			log.mu.Unlock()
			if c >= n {
				return
			}
			time.Sleep(20 * time.Millisecond)
		}
		t.Fatalf("timed out waiting for %d error events", n)
	}

	if err := svc.StartDownload(assetURL(name)); err != nil {
		t.Fatalf("first StartDownload: %v", err)
	}
	waitErrs(1)
	if _, err := os.Stat(filepath.Join(dir, name) + ".downloading"); !os.IsNotExist(err) {
		t.Fatalf("first failure should leave no temp file")
	}

	if err := svc.StartDownload(assetURL(name)); err != nil {
		t.Fatalf("second StartDownload: %v", err)
	}
	waitErrs(2)
	if _, err := os.Stat(filepath.Join(dir, name) + ".downloading"); !os.IsNotExist(err) {
		t.Errorf("second failure must remove .downloading (tmpRemoved must reset when registering a new download)")
	}
	if _, err := os.Stat(filepath.Join(dir, name)); !os.IsNotExist(err) {
		t.Errorf("final file must not exist")
	}
}
