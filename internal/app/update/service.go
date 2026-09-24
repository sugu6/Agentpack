// Package update 提供应用更新的完整服务：GitHub releases.atom 订阅源解析、
// 版本比较、按 CI 命名规则构造下载地址（见 update.go），以及下载/暂停/续传/
// 安装的状态机（本文件 Service）。Service 不持有 App 状态，通过回调与宿主
// 解耦（事件触发、语言、安装后退出），可独立测试。
package update

import (
	"context"
	"log"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// DownloadState 下载状态
type DownloadState int

const (
	StateIdle        DownloadState = iota // 空闲
	StateDownloading                      // 下载中
	StatePaused                           // 已暂停
	StateCompleted                        // 完成
	StateError                            // 错误
)

// Service 管理更新检查与安装包下载/暂停/续传/安装。所有下载状态受内部 mu 保护。
type Service struct {
	mu    sync.Mutex
	state DownloadState
	// paused 暂停标志（原子操作，0=否，1=是）
	paused         int32
	cancel         context.CancelFunc
	done           chan struct{} // 当前下载 goroutine 结束信号
	pausedFile     string        // 暂停时保存的临时文件路径
	offset         int64         // 暂停时的下载偏移量
	url            string        // 当前下载的 URL
	file           string        // 下载完成的安装包路径（供 Install 使用）
	dlTmpPath      string        // 当前下载的临时文件路径（.downloading），用于 Shutdown 兜底清理
	expectedDigest string        // 发布页 sha256 摘要（hex）；与 s.file 同周期设置，Install 前 exec 复核
	// tmpRemoved 记录 dlTmpPath 是否已被 Cleanup/Shutdown 移除。
	// 用 int32 而非 bool：Go 对 struct 中 bool 字段不能直接原子读写，
	// 用 int32 配合 atomic 操作保证 Shutdown 与下载 goroutine 的 os.Remove 互斥，
	// 避免并发两次 Remove 同一文件产生多余的 ErrNotExist 系统调用。
	tmpRemoved int32 // atomic: 0=未移除，1=已移除

	// CheckUpdate 的 singleflight + 结果缓存状态（checkMu 保护）。
	checkMu      sync.Mutex
	checkCh      chan checkRes
	checkAt      time.Time
	checkRateLtd bool
	checkResult  *UpdateCheckResult
	checkMsgKey  string
	checkMsgArgs map[string]interface{}
	checkErr     error

	// 与宿主的解耦回调
	emit          func(event string, data ...interface{})
	lang          func() string // 返回 "zh-CN"/"en"，用于 i18n 消息
	installExit   func()        // 安装器启动后退出应用（Windows 需覆盖 exe）
	beginInFlight func() error  // 下载开始时登记宿主 in-flight（关闭门控），失败表示应用关闭中
	endInFlight   func()        // 下载结束时释放宿主 in-flight
}

// NewService 创建更新服务。emit 用于触发前端事件；lang 返回当前语言；
// installExit 在安装器成功启动后回调（宿主应退出应用）；beginInFlight/endInFlight
// 由宿主接入下载的 in-flight 门控（关闭流程在下载进行中阻止退出）。
func NewService(emit func(string, ...interface{}), lang func() string, installExit func(), beginInFlight func() error, endInFlight func()) *Service {
	return &Service{
		state:         StateIdle,
		emit:          emit,
		lang:          lang,
		installExit:   installExit,
		beginInFlight: beginInFlight,
		endInFlight:   endInFlight,
	}
}

// Shutdown 取消活动下载并等待其清理（removeTmp）。供宿主关闭流程调用。
func (s *Service) Shutdown() {
	s.mu.Lock()
	var dlDone chan struct{}
	if s.cancel != nil {
		s.cancel()
		dlDone = s.done
	}
	// 兜底清理：进程退出前无法等待 goroutine 完成时，直接删除临时文件。
	// 用 atomic 标志与 goroutine 内的 removeTmp 互斥：已标记则跳过，
	// 避免 goroutine 后续也 Remove 已删除的文件产生无意义的 ErrNotExist。
	tmpPath := s.dlTmpPath
	if tmpPath != "" {
		s.mu.Unlock()
		if atomic.CompareAndSwapInt32(&s.tmpRemoved, 0, 1) {
			if err := os.Remove(tmpPath); err != nil && !os.IsNotExist(err) {
				log.Printf("update: remove stale tmp on shutdown %s: %v", tmpPath, err)
			}
		}
	} else {
		s.mu.Unlock()
	}
	if dlDone != nil {
		select {
		case <-dlDone:
		case <-time.After(2 * time.Second):
			log.Printf("update: download goroutine did not exit within 2s, tmp file may remain")
		}
	}
}

// downloadDir 返回默认下载目录（优先系统 Downloads，失败回退临时目录）。
func downloadDir() string {
	switch runtime.GOOS {
	case "windows":
		if home := os.Getenv("USERPROFILE"); home != "" {
			if fi, err := os.Stat(filepath.Join(home, "Downloads")); err == nil && fi.IsDir() {
				return filepath.Join(home, "Downloads")
			}
		}
	case "darwin":
		if xdg := os.Getenv("XDG_DOWNLOAD_DIR"); xdg != "" {
			return xdg
		}
		if home, err := os.UserHomeDir(); err == nil {
			if fi, err := os.Stat(filepath.Join(home, "Downloads")); err == nil && fi.IsDir() {
				return filepath.Join(home, "Downloads")
			}
		}
	default:
		if xdg := os.Getenv("XDG_DOWNLOAD_DIR"); xdg != "" {
			return xdg
		}
		if home, err := os.UserHomeDir(); err == nil {
			if fi, err := os.Stat(filepath.Join(home, "Downloads")); err == nil && fi.IsDir() {
				return filepath.Join(home, "Downloads")
			}
		}
	}
	return os.TempDir()
}

// CleanStaleDownloads 清理 Downloads 目录中残留的 .downloading 临时文件。
// 删除超过 24 小时未修改的临时文件，防止进程异常退出后永久残留。
// 在 ServiceStartup 时调用一次即可。
func CleanStaleDownloads() {
	cleanStaleDownloads(downloadDir())
}

func cleanStaleDownloads(dir string) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	cutoff := time.Now().Add(-24 * time.Hour)
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".downloading") {
			continue
		}
		info, err := entry.Info()
		if err != nil {
			continue
		}
		if info.ModTime().Before(cutoff) {
			_ = os.Remove(filepath.Join(dir, entry.Name()))
		}
	}
}
