package update

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

// execInstaller 创建并启动安装器进程（按平台选择打开方式）。
// 提取为包级变量作为测试注入点：测试替换它以断言 Install 的启动行为而不真执行。
// 注意：Windows 下直接使用 CreateProcess（exec.Command(path)）启动。
// 安装器为 machine 级，launch 时会触发提权、以全新 STARTUPINFO 重新拉起，
// 故不压制安装器窗口。不经 cmd.exe，避免文件名中的 & 等元字符导致命令注入。
var execInstaller = func(path string) error {
	switch runtime.GOOS {
	case "windows":
		return exec.Command(path).Start()
	case "darwin":
		return exec.Command("open", path).Start()
	default:
		return exec.Command("xdg-open", path).Start()
	}
}

// Install 启动已下载的安装程序并通知宿主退出应用。
// 必须退出主程序，否则 Windows 上安装器无法覆盖正在运行的 exe。
func (s *Service) Install() error {
	s.mu.Lock()
	dlPath := s.file
	expected := s.expectedDigest
	s.mu.Unlock()
	if dlPath == "" {
		return fmt.Errorf("no downloaded installer")
	}
	if _, err := os.Stat(dlPath); err != nil {
		return fmt.Errorf("installer not found: %w", err)
	}

	// 完整性校验：确保下载的是非空可执行的安装程序，避免用户下载到 zip/HTML 错误页面后
	// exec 启动失败，用户只看到笼统的“启动安装程序失败”。
	if err := validateUpdateInstaller(dlPath); err != nil {
		return err
	}

	// 方案E复核之二：exec 前重新读盘哈希，与下载时记录的发布页摘要比对，
	// 拦截下载完成到安装启动之间的本地替换（篡改时间窗）。无条件执行：
	// expectedDigest 为空即视为不符（fail-closed），绝不放行未校验的安装器。
	sum, err := fileSHA256(dlPath)
	if err != nil {
		return fmt.Errorf("verify installer integrity: %w", err)
	}
	if sum != expected {
		return fmt.Errorf("installer sha256 mismatch: expected %s, got %s", expected, sum)
	}

	// 经注入点启动安装器（测试替换 execInstaller 断言行为）。
	if err := execInstaller(dlPath); err != nil {
		return err
	}

	go func() {
		time.Sleep(1 * time.Second)
		if s.installExit != nil {
			s.installExit()
		}
	}()
	return nil
}

// validateUpdateInstaller 校验安装文件的基本有效性（非空、扩展名）。
func validateUpdateInstaller(path string) error {
	info, err := os.Stat(path)
	if err != nil {
		return fmt.Errorf("stat installer: %w", err)
	}
	if info.Size() == 0 {
		return fmt.Errorf("installer file is empty")
	}
	switch runtime.GOOS {
	case "windows":
		ext := strings.ToLower(filepath.Ext(path))
		if ext != ".exe" && ext != ".msi" {
			return fmt.Errorf("invalid installer type for windows: %s (expected .exe or .msi)", ext)
		}
	case "darwin":
		if ext := strings.ToLower(filepath.Ext(path)); ext != ".dmg" {
			return fmt.Errorf("invalid installer type for macos: %s (expected .dmg)", ext)
		}
	case "linux":
		if ext := strings.ToLower(filepath.Ext(path)); ext != ".tar.gz" && ext != ".tgz" {
			return fmt.Errorf("invalid installer type for linux: %s (expected .tar.gz or .tgz)", ext)
		}
	}
	return nil
}

// fileSHA256 计算文件 sha256（hex 小写）。
func fileSHA256(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}
