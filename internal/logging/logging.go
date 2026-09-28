// Package logging 提供应用统一的运行日志基础设施。
//
// 设计要点：
//   - 按"通道（分类）"落盘：每个通道一个独立轮转文件（logs/<cat>.log），
//     便于按需收集与排障（如只附 update.log）；详见 categoryDefs；
//   - 历史 log.Printf 调用点零改动自动归类：按调用方包路径映射到通道
//     （见 categoryForFunc），显式指定用 Cat()/Printf()；
//   - 结构化输出（log/slog）+ lumberjack 轮转，生产构建（windowsgui 无控制台）
//     也有日志可查；
//   - 五级日志：error < warn < info(默认) < debug < trace；级别是"最低输出阈值"，
//     设置页下拉或 AGENTPACK_LOG_LEVEL 环境变量可调；
//   - panic 由 WriteCrash 写 crash-*.log，诊断包始终收集。
package logging

import (
	"fmt"
	"io"
	"log"
	"log/slog"
	"os"
	"path/filepath"
	"runtime"
	"runtime/debug"
	"strings"
	"sync"
	"time"

	"gopkg.in/natefinch/lumberjack.v2"
)

const (
	// LevelEnv 允许用环境变量强制日志级别（error/warn/info/debug/trace），优先于设置项。
	LevelEnv = "AGENTPACK_LOG_LEVEL"

	// LevelTrace 是比 Debug 更详细的级别：全部日志，含 SSE 流与请求/响应明细。
	LevelTrace = slog.Level(-8)

	maxSizeMB  = 5  // 单个通道文件上限（MB）
	maxBackups = 3  // 每通道归档保留数量
	maxAgeDays = 14 // 归档最长保留天数
)

// CategoryDef 定义一个日志通道（分类）。
type CategoryDef struct {
	Name        string // 通道名，写入日志行 cat 字段与文件名
	File        string // 通道文件名（logs/<File>）
	Description string // 用途说明（env.json / 诊断包 README）
}

// categoryDefs 是全部通道：默认收集类型为全部通道的 info 及以上 +
// crash-*.log；debug/trace 仅在显式调高级别后写入。
var categoryDefs = []CategoryDef{
	{Name: "app", File: "app.log",
		Description: "应用生命周期、配置、数据库与通用日志"},
	{Name: "webview", File: "webview.log",
		Description: "Wails / WebView2 内部日志"},
	{Name: "frontend", File: "frontend.log",
		Description: "前端错误与告警（console.error/warn、未捕获异常）"},
	{Name: "update", File: "update.log",
		Description: "更新检查、下载与安装"},
	{Name: "market", File: "market.log",
		Description: "市场与 GitHub 请求（状态码/耗时，不含响应体）"},
	{Name: "mcp", File: "mcp.log",
		Description: "MCP 配置读写与多 Agent 同步"},
	{Name: "skills", File: "skills.log",
		Description: "Skills 安装、同步与冲突处理"},
}

// moduleCategoryPrefixes 把包路径前缀映射到通道，用于自动归类历史 log.Printf。
// 未命中的 agentpack 包统一进 app 通道。
var moduleCategoryPrefixes = []struct{ prefix, cat string }{
	{"agentpack/internal/skills", "skills"},
	{"agentpack/internal/app/skillbackfill", "skills"},
	{"agentpack/internal/mcp", "mcp"},
	{"agentpack/internal/market", "market"},
	{"agentpack/internal/app/market", "market"},
	{"agentpack/internal/app/update", "update"},
}

type channel struct {
	def     CategoryDef
	level   *slog.LevelVar
	base    *slog.Logger // 不含 cat 属性，供未注册分类复用
	logger  *slog.Logger // 含 cat=<name>
	rotator *lumberjack.Logger
}

var (
	mu       sync.Mutex
	dirPath  string
	channels map[string]*channel
)

// Options 控制日志初始化行为。
type Options struct {
	// Dir 是日志目录；为空时仅输出到 stderr（如 home 目录不可用的极端情况）。
	Dir string
	// Level 是日志级别名（error/warn/info/debug/trace）；空串按 info。
	Level string
	// AlsoStderr 同时写 stderr：dev 构建/有控制台时便于观察；windowsgui 下写失败不影响文件。
	AlsoStderr bool
}

// Init 初始化全部日志通道，返回关闭函数（进程退出前调用以 flush 并关闭文件）。
func Init(opts Options) func() {
	mu.Lock()
	dirPath = opts.Dir
	channels = make(map[string]*channel, len(categoryDefs))

	for _, def := range categoryDefs {
		var writers []io.Writer
		ch := &channel{def: def, level: new(slog.LevelVar)}
		if opts.Dir != "" {
			if err := os.MkdirAll(opts.Dir, 0700); err == nil {
				// lumberjack 首次写入时才创建文件：未产生日志的通道不占磁盘。
				ch.rotator = &lumberjack.Logger{
					Filename:   filepath.Join(opts.Dir, def.File),
					MaxSize:    maxSizeMB,
					MaxBackups: maxBackups,
					MaxAge:     maxAgeDays,
					Compress:   true,
				}
				writers = append(writers, ch.rotator)
			}
		}
		if opts.AlsoStderr {
			writers = append(writers, os.Stderr)
		}
		var w io.Writer = io.Discard
		if len(writers) > 0 {
			w = io.MultiWriter(writers...)
		}
		ch.base = slog.New(slog.NewTextHandler(w, &slog.HandlerOptions{Level: ch.level}))
		ch.logger = ch.base.With("cat", def.Name)
		channels[def.Name] = ch
	}
	applyLevelsLocked(opts.Level)
	mu.Unlock()

	// 接管标准库 log：历史 log.Printf 调用点零改动进入分类日志流。
	log.SetFlags(0)
	log.SetOutput(legacyWriter{})

	var once sync.Once
	return func() {
		once.Do(func() {
			mu.Lock()
			defer mu.Unlock()
			for _, ch := range channels {
				if ch.rotator != nil {
					_ = ch.rotator.Close()
					ch.rotator = nil
				}
			}
		})
	}
}

// L 返回 app 通道 logger（历史调用点与未分类场景的默认入口）。
func L() *slog.Logger {
	return Cat("app")
}

// Cat 返回指定通道的 logger。未注册的分类会落到 app 通道的文件，
// 但保留 cat=<name> 标签，便于后续提升为独立通道而不丢历史检索能力。
func Cat(name string) *slog.Logger {
	mu.Lock()
	defer mu.Unlock()
	if ch, ok := channels[name]; ok {
		return ch.logger
	}
	if ch, ok := channels["app"]; ok {
		return ch.base.With("cat", name)
	}
	return slog.Default()
}

// Printf 以指定分类输出一行文本日志（历史 log.Printf 的迁移入口）：
//
//	logging.Printf("update", "check failed: %v", err)
func Printf(cat, format string, args ...any) {
	Cat(cat).Info(fmt.Sprintf(format, args...))
}

// CategoryInfo 描述一个通道的当前状态（供 env.json 与诊断包 README 使用）。
type CategoryInfo struct {
	Name        string `json:"name"`
	File        string `json:"file"`
	Level       string `json:"level"`
	Description string `json:"description"`
}

// Categories 返回全部通道及当前级别。
func Categories() []CategoryInfo {
	mu.Lock()
	defer mu.Unlock()
	out := make([]CategoryInfo, 0, len(categoryDefs))
	for _, def := range categoryDefs {
		lv := "info" // Init 前的默认级别；Init 后 channels 已填充，下方 if 必命中
		if ch, ok := channels[def.Name]; ok {
			lv = levelName(ch.level.Level())
		}
		out = append(out, CategoryInfo{Name: def.Name, File: def.File, Level: lv, Description: def.Description})
	}
	return out
}

// Dir 返回当前日志目录（空表示仅 stderr）。
func Dir() string {
	mu.Lock()
	defer mu.Unlock()
	return dirPath
}

// SetLevel 在运行时切换日志级别（LevelEnv 仍优先生效，作用于全部通道）。
func SetLevel(level string) {
	mu.Lock()
	defer mu.Unlock()
	applyLevelsLocked(level)
}

// Level 返回当前生效级别（app 通道），供 Wails Options.LogLevel 使用。
func Level() slog.Level {
	mu.Lock()
	defer mu.Unlock()
	if ch, ok := channels["app"]; ok {
		return ch.level.Level()
	}
	return slog.LevelInfo
}

// LevelName 返回当前生效级别名（trace/debug/info/warn/error），供 env.json 记录。
func LevelName() string {
	return levelName(Level())
}

// ParseLevel 解析级别名（error/warn/info/debug/trace，大小写不敏感，忽略空白）。
func ParseLevel(s string) (slog.Level, bool) {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "error":
		return slog.LevelError, true
	case "warn", "warning":
		return slog.LevelWarn, true
	case "info":
		return slog.LevelInfo, true
	case "debug":
		return slog.LevelDebug, true
	case "trace":
		return LevelTrace, true
	default:
		return 0, false
	}
}

// WriteCrash 把 panic 的值与调用栈写入 crash-<ts>.log，并返回文件路径（写入失败返回空串）。
// 崩溃信息同时以 Error 级别记入 app 通道，保证即使文件写入失败也可从 stderr 观察。
func WriteCrash(v any, stack []byte) string {
	d := Dir()
	path := ""
	if d != "" {
		path = filepath.Join(d, "crash-"+time.Now().Format("20060102-150405")+".log")
		content := fmt.Sprintf("time: %s\npanic: %v\n\n%s\n",
			time.Now().Format(time.RFC3339), v, stack)
		if err := os.WriteFile(path, []byte(content), 0600); err != nil {
			path = ""
		}
	}
	Cat("app").Error("panic captured", "value", fmt.Sprint(v), "file", path)
	return path
}

// GoGuarded 在独立 goroutine 中运行 fn，并 recover 其 panic 写入 crash-*.log。
// 用于常驻后台 goroutine（定时器、自动回填等）：它们的 panic 不会传播到
// main goroutine 的 recover，若不保护会直接终止整个进程且不留下崩溃记录，
// 使诊断包失去崩溃现场。name 用于在崩溃记录中标注来源 goroutine。
func GoGuarded(name string, fn func()) {
	go func() {
		defer func() {
			if r := recover(); r != nil {
				WriteCrash(fmt.Sprintf("goroutine %q panicked: %v", name, r), debug.Stack())
			}
		}()
		fn()
	}()
}

// applyLevelsLocked 计算并应用级别：LevelEnv > 设置项 > 默认 info，作用于全部通道。
func applyLevelsLocked(configured string) {
	l := slog.LevelInfo
	if v, ok := ParseLevel(configured); ok {
		l = v
	}
	// LevelEnv 优先级最高，覆盖配置项。
	if v, ok := ParseLevel(os.Getenv(LevelEnv)); ok {
		l = v
	}
	for _, ch := range channels {
		ch.level.Set(l)
	}
}

func levelName(l slog.Level) string {
	switch {
	case l <= LevelTrace:
		return "trace"
	case l <= slog.LevelDebug:
		return "debug"
	case l <= slog.LevelInfo:
		return "info"
	case l <= slog.LevelWarn:
		return "warn"
	default:
		return "error"
	}
}

// legacyWriter 把标准库 log 的输出写入"按调用方包路径自动归类"的通道。
type legacyWriter struct{}

func (legacyWriter) Write(p []byte) (int, error) {
	if msg := strings.TrimRight(string(p), "\r\n"); msg != "" {
		Cat(categoryForCaller()).Info(msg)
	}
	return len(p), nil
}

// categoryForCaller 沿调用栈找到第一个 agentpack/main 帧并归类；
// 全部为库帧（正常不会发生）时回退 app。
func categoryForCaller() string {
	pcs := make([]uintptr, 16)
	n := runtime.Callers(2, pcs)
	frames := runtime.CallersFrames(pcs[:n])
	for {
		frame, more := frames.Next()
		if cat := categoryForFunc(frame.Function); cat != "" {
			return cat
		}
		if !more {
			return "app"
		}
	}
}

// categoryForFunc 把函数全名映射到通道：
//   - Wails 内部帧（github.com/wailsapp/wails/...）→ webview 通道；
//   - agentpack/main 帧按前缀归类，未命中前缀的 agentpack 包 → app 通道；
//   - 其他库帧（标准库、logging 自身）返回空串表示"继续向上找"。
//
// Wails beta.26 的内部日志（Build Info / Platform Info / WebView2 报错）实际
// 走标准库 log.Printf 而非注入的 Options.Logger，因此必须在此识别其包路径，
// 否则会被回退到 app 通道。
func categoryForFunc(fn string) string {
	if strings.HasPrefix(fn, "github.com/wailsapp/wails/") {
		return "webview"
	}
	isProject := strings.HasPrefix(fn, "agentpack/") || strings.HasPrefix(fn, "main.")
	if !isProject {
		return ""
	}
	for _, m := range moduleCategoryPrefixes {
		if strings.HasPrefix(fn, m.prefix) {
			return m.cat
		}
	}
	return "app"
}