// Package diagnostics 生成排障所需的"环境快照"和"诊断包"。
//
// 产出物：
//   - env.json：启动自检快照（版本 / 系统 / WebView2 / 关键目录可写性 / 代理环境）；
//   - 诊断包 zip：env.json + 最近日志 + 崩溃日志 + 脱敏后的配置，由用户手动
//     导出并附到 Issue（本地优先、默认不上传任何数据）。
package diagnostics

import (
	"archive/zip"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strings"
	"time"
)

const (
	envFileName      = "env.json"
	defaultMaxLog    = 5 << 20  // 单个日志文件上限 5MB
	defaultMaxTotal  = 8 << 20  // 日志总量上限 8MB
	keepExportFiles  = 5        // exports 目录保留的最新诊断包数量
	exportFilePrefix = "AgentPack-diag-"
)

// LogChannel 描述一个日志通道（分类）及其当前级别，随 env.json 导出。
type LogChannel struct {
	Name  string `json:"name"`
	File  string `json:"file"`
	Level string `json:"level"`
}

// Env 是启动自检快照，随诊断包导出，用于跨机器排障。
type Env struct {
	GeneratedAt    string          `json:"generatedAt"`
	AppVersion     string          `json:"appVersion"`
	GoVersion      string          `json:"goVersion"`
	OS             string          `json:"os"`
	Arch           string          `json:"arch"`
	OSVersion      string          `json:"osVersion,omitempty"`
	WebView2       string          `json:"webView2Version,omitempty"`
	Locale         string          `json:"locale,omitempty"`
	ExecutablePath string          `json:"executablePath,omitempty"`
	DataDir        string          `json:"dataDir,omitempty"`
	LogDir         string          `json:"logDir,omitempty"`
	LogLevel       string          `json:"logLevel"`
	LogChannels    []LogChannel    `json:"logChannels,omitempty"`
	DevMode        bool            `json:"devMode"`
	Writable       map[string]bool `json:"writable"`
	ProxyEnv       map[string]string `json:"proxyEnv,omitempty"`
}

// Options 是 Collect 的输入（由 main/app 提供，避免 diagnostics 反向依赖）。
type Options struct {
	AppVersion  string
	Locale      string
	DataDir     string
	LogDir      string
	LogLevel    string
	LogChannels []LogChannel
	DevMode     bool
}

// Collect 采样当前运行环境。包含少量文件系统写入探测（创建后立即删除），
// 用来发现"数据目录不可写 / 被沙箱降权"这类只在用户机器上出现的问题。
func Collect(opts Options) Env {
	exe, _ := os.Executable()
	env := Env{
		GeneratedAt:    time.Now().Format(time.RFC3339),
		AppVersion:     opts.AppVersion,
		GoVersion:      runtime.Version(),
		OS:             runtime.GOOS,
		Arch:           runtime.GOARCH,
		OSVersion:      osVersion(),
		WebView2:       webView2Version(),
		Locale:         opts.Locale,
		ExecutablePath: exe,
		DataDir:        opts.DataDir,
		LogDir:         opts.LogDir,
		LogLevel:       opts.LogLevel,
		LogChannels:    opts.LogChannels,
		DevMode:        opts.DevMode,
		Writable: map[string]bool{
			"data": probeWritable(opts.DataDir),
			"logs": probeWritable(opts.LogDir),
		},
		ProxyEnv: proxyEnv(),
	}
	if dir := webViewProfileDir(); dir != "" {
		env.Writable["webviewProfile"] = probeWritable(dir)
	}
	return env
}

// WriteEnv 把快照写入 dir/env.json（每次启动覆盖），返回文件路径。
func WriteEnv(dir string, env Env) (string, error) {
	if dir == "" {
		return "", fmt.Errorf("empty dir")
	}
	if err := os.MkdirAll(dir, 0700); err != nil {
		return "", err
	}
	raw, err := json.MarshalIndent(env, "", "  ")
	if err != nil {
		return "", err
	}
	path := filepath.Join(dir, envFileName)
	if err := os.WriteFile(path, raw, 0600); err != nil {
		return "", err
	}
	return path, nil
}

// ZipOptions 是 BuildZip 的输入。
type ZipOptions struct {
	ExportsDir string // 诊断包输出目录（不存在会自动创建）
	LogDir     string // 日志目录（可为空）
	DataDir    string // 数据目录（用于取 config.json）
	Env        Env
	MaxLogBytes      int64 // 单日志文件上限；<=0 用默认 5MB
	MaxTotalLogBytes int64 // 日志总量上限；<=0 用默认 8MB
}

// BuildZip 生成诊断包并返回 zip 完整路径。
// 内容：README.txt / env.json / config.redacted.json（脱敏）/ logs/（最近日志与崩溃日志）。
func BuildZip(opts ZipOptions) (string, error) {
	if opts.ExportsDir == "" {
		return "", fmt.Errorf("empty exports dir")
	}
	maxLog := opts.MaxLogBytes
	if maxLog <= 0 {
		maxLog = defaultMaxLog
	}
	maxTotal := opts.MaxTotalLogBytes
	if maxTotal <= 0 {
		maxTotal = defaultMaxTotal
	}
	if err := os.MkdirAll(opts.ExportsDir, 0700); err != nil {
		return "", err
	}
	name := fmt.Sprintf("%sv%s-%s.zip", exportFilePrefix, sanitizeVersion(opts.Env.AppVersion), time.Now().Format("20060102-150405.000"))
	dest := filepath.Join(opts.ExportsDir, name)

	f, err := os.Create(dest)
	if err != nil {
		return "", err
	}
	zw := zip.NewWriter(f)
	err = func() error {
		if err := addBytes(zw, "README.txt", []byte(readmeText)); err != nil {
			return err
		}
		envRaw, err := json.MarshalIndent(opts.Env, "", "  ")
		if err != nil {
			return err
		}
		if err := addBytes(zw, envFileName, envRaw); err != nil {
			return err
		}
		if cfgPath := filepath.Join(opts.DataDir, "config.json"); opts.DataDir != "" {
			if raw, err := os.ReadFile(cfgPath); err == nil {
				if err := addBytes(zw, "config.redacted.json", RedactJSON(raw)); err != nil {
					return err
				}
			}
		}
		return addLogs(zw, opts.LogDir, maxLog, maxTotal)
	}()
	if closeErr := zw.Close(); err == nil {
		err = closeErr
	}
	if closeErr := f.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		_ = os.Remove(dest)
		return "", err
	}
	pruneExports(opts.ExportsDir)
	return dest, nil
}

// addLogs 按修改时间从新到旧选择日志文件加入 zip，总量不超过 maxTotal。
// 收集全部通道文件（*.log / 轮转归档 *.log.gz）与崩溃记录（crash-*.log）。
func addLogs(zw *zip.Writer, logDir string, maxLog, maxTotal int64) error {
	if logDir == "" {
		return nil
	}
	entries, err := os.ReadDir(logDir)
	if err != nil {
		return nil // 日志目录不可读时导出其余内容即可
	}
	type logFile struct {
		path string
		mod  time.Time
	}
	var files []logFile
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		n := e.Name()
		if !strings.HasSuffix(n, ".log") && !strings.HasSuffix(n, ".log.gz") {
			continue
		}
		info, err := e.Info()
		if err != nil {
			continue
		}
		files = append(files, logFile{path: filepath.Join(logDir, n), mod: info.ModTime()})
	}
	sort.Slice(files, func(i, j int) bool { return files[i].mod.After(files[j].mod) })

	var total int64
	for _, lf := range files {
		if total >= maxTotal {
			break
		}
		limit := maxLog
		if remain := maxTotal - total; remain < limit {
			limit = remain
		}
		// gzip 压缩归档不可截断（截断会破坏 CRC/EOF，导致无法解压）；
		// 剩余空间不足以容纳完整归档时整体跳过，保证包里每个 .gz 都可用。
		if strings.HasSuffix(lf.path, ".gz") {
			if si, err := os.Stat(lf.path); err == nil && int64(si.Size()) > limit {
				continue
			}
			limit = 0
		}
		w, err := zw.Create("logs/" + filepath.Base(lf.path))
		if err != nil {
			return err
		}
		n, err := copyTail(w, lf.path, limit)
		total += n
		if err != nil {
			return err
		}
	}
	return nil
}

// copyTail 把 src 写入 w：limit>0 时只保留末尾 limit 字节（大文件只留最近部分，
// 最可能包含现场）；limit==0 时全量拷贝。返回实际写入的字节数。
func copyTail(w io.Writer, src string, limit int64) (int64, error) {
	f, err := os.Open(src)
	if err != nil {
		return 0, nil // 单个日志不可读不应中断整个导出
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return 0, nil
	}
	if limit > 0 && info.Size() > limit {
		offset := info.Size() - limit
		if _, err := f.Seek(offset, io.SeekStart); err != nil {
			return 0, nil
		}
		marker := fmt.Sprintf("[truncated: showing last %d bytes]\n", limit)
		if _, err := io.WriteString(w, marker); err != nil {
			return 0, err
		}
	}
	return io.Copy(w, f)
}

func addBytes(zw *zip.Writer, name string, data []byte) error {
	w, err := zw.Create(name)
	if err != nil {
		return err
	}
	_, err = w.Write(data)
	return err
}

// pruneExports 只保留最新的 keepExportFiles 个诊断包，避免目录无限增长。
func pruneExports(dir string) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	type f struct {
		path string
		mod  time.Time
	}
	var files []f
	for _, e := range entries {
		if e.IsDir() || !strings.HasPrefix(e.Name(), exportFilePrefix) || !strings.HasSuffix(e.Name(), ".zip") {
			continue
		}
		info, err := e.Info()
		if err != nil {
			continue
		}
		files = append(files, f{path: filepath.Join(dir, e.Name()), mod: info.ModTime()})
	}
	if len(files) <= keepExportFiles {
		return
	}
	sort.Slice(files, func(i, j int) bool { return files[i].mod.After(files[j].mod) })
	for _, x := range files[keepExportFiles:] {
		_ = os.Remove(x.path)
	}
}

// 敏感键子串匹配（有意偏宽，宁多脱勿漏脱）：token/secret/password 等出现在
// key 名任意位置即脱敏。auth/cookie 等宽词可能连带脱敏 authMode、cookieConsent
// 等非敏感字段——因 config.json 实际不含此类字段，误伤几乎不触发，安全优先。
var sensitiveKeyRe = regexp.MustCompile(`(?i)(token|secret|password|passwd|credential|api[_-]?key|private[_-]?key|access[_-]?key|auth|cookie|session[_-]?id|machine[_-]?key)`)

// RedactJSON 解析 JSON 并把敏感键（token/密钥/口令等）的值替换为 ***。
// 解析失败时不冒险导出原文，返回提示对象。
func RedactJSON(raw []byte) []byte {
	var v any
	if err := json.Unmarshal(raw, &v); err != nil {
		return []byte(`{"redacted":true,"note":"config not parseable"}`)
	}
	out, err := json.MarshalIndent(redactValue("", v), "", "  ")
	if err != nil {
		return []byte(`{"redacted":true,"note":"config redaction failed"}`)
	}
	return out
}

func redactValue(key string, v any) any {
	switch t := v.(type) {
	case map[string]any:
		out := make(map[string]any, len(t))
		for k, val := range t {
			out[k] = redactValue(k, val)
		}
		return out
	case []any:
		out := make([]any, len(t))
		for i, val := range t {
			out[i] = redactValue(key, val)
		}
		return out
	default:
		if key != "" && sensitiveKeyRe.MatchString(key) {
			return "***"
		}
		return v
	}
}

// probeWritable 通过"创建临时文件后立即删除"探测目录可写性。
func probeWritable(dir string) bool {
	if dir == "" {
		return false
	}
	if err := os.MkdirAll(dir, 0700); err != nil {
		return false
	}
	f, err := os.CreateTemp(dir, ".ap-write-probe-*")
	if err != nil {
		return false
	}
	name := f.Name()
	_, werr := f.WriteString("probe")
	cerr := f.Close()
	_ = os.Remove(name)
	return werr == nil && cerr == nil
}

// proxyEnv 收集代理相关环境变量（凭据打码，超长截断）。
func proxyEnv() map[string]string {
	keys := []string{"HTTP_PROXY", "HTTPS_PROXY", "ALL_PROXY", "NO_PROXY",
		"http_proxy", "https_proxy", "all_proxy", "no_proxy"}
	out := map[string]string{}
	for _, k := range keys {
		v := strings.TrimSpace(os.Getenv(k))
		if v == "" {
			continue
		}
		if i := strings.Index(v, "@"); i >= 0 {
			v = "***@" + v[i+1:]
		}
		if len(v) > 120 {
			v = v[:120]
		}
		out[k] = v
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

func sanitizeVersion(v string) string {
	v = strings.TrimSpace(v)
	if v == "" {
		return "unknown"
	}
	return strings.Map(func(r rune) rune {
		if r == '/' || r == '\\' || r == ':' || r == '*' || r == '?' || r == '"' || r == '<' || r == '>' || r == '|' {
			return '-'
		}
		return r
	}, v)
}

const readmeText = `AgentPack 诊断包 / Diagnostics bundle
=====================================

内容说明：
- env.json              启动环境快照（版本 / 系统与构建号 / WebView2 版本 /
                        关键目录可写性自检 / 当前日志级别与通道 / 代理环境）
- logs/                 分类日志（默认收集 info 及以上；每个通道一个文件）：
    app.log        应用生命周期、配置、数据库与通用日志
    webview.log    Wails / WebView2 内部日志
    frontend.log   前端错误与告警（console.error/warn、未捕获异常）
    update.log     更新检查、下载与安装
    market.log     市场与 GitHub 请求（状态码/耗时，不含响应体）
    mcp.log        MCP 配置读写与多 Agent 同步
    skills.log     Skills 安装、同步与冲突处理
    crash-*.log    panic 崩溃记录（含调用栈）
- config.redacted.json  脱敏后的应用配置（token / 密钥 / 口令类字段已替换为 ***）

日志级别（设置 → 日志与诊断 → 日志级别；也可用环境变量 AGENTPACK_LOG_LEVEL）：
- error  仅严重错误
- warn   错误 + 警告
- info   一般操作信息（默认）
- debug  详细信息，包含请求/响应详情与 SSE 流
- trace  全部日志，最详细

隐私说明：
- 日志可能包含本机路径、网络请求错误等排障信息；
- 不包含 MCP / Skills 配置内容，不包含任何密钥或口令原文。

使用方式：可先自行打开检查，确认无误后附到 GitHub Issue 中。
`