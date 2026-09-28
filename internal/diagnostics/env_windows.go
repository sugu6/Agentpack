//go:build windows

package diagnostics

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"golang.org/x/sys/windows/registry"
)

// osVersion 返回 Windows 版本描述（如 "Windows 11 专业版 23H2 (build 22631)"）。
// 读取注册表 CurrentVersion；ProductName 在 Win11 上仍显示 Windows 10，
// 用 build 号（>=22000 为 Win11）修正，避免排障时误判系统。
func osVersion() string {
	k, err := registry.OpenKey(registry.LOCAL_MACHINE,
		`SOFTWARE\Microsoft\Windows NT\CurrentVersion`, registry.QUERY_VALUE)
	if err != nil {
		return ""
	}
	defer k.Close()

	name, _, _ := k.GetStringValue("ProductName")
	display, _, _ := k.GetStringValue("DisplayVersion")
	build, _, _ := k.GetStringValue("CurrentBuildNumber")

	var buildNum int
	if _, err := fmt.Sscanf(build, "%d", &buildNum); err == nil && buildNum >= 22000 {
		name = strings.Replace(name, "Windows 10", "Windows 11", 1)
	}
	parts := []string{strings.TrimSpace(name)}
	if display != "" {
		parts = append(parts, display)
	}
	if build != "" {
		parts = append(parts, "(build "+build+")")
	}
	return strings.TrimSpace(strings.Join(parts, " "))
}

// webView2Version 返回 WebView2 运行时版本（EdgeUpdate Clients 注册表）。
// 任一读取失败返回空串——属于尽力而为的辅助信息。
func webView2Version() string {
	const guid = `{F3017226-FE2A-4295-8BDF-00C3A9A7E4C5}`
	paths := []struct {
		root registry.Key
		path string
	}{
		{registry.LOCAL_MACHINE, `SOFTWARE\WOW6432Node\Microsoft\EdgeUpdate\Clients\` + guid},
		{registry.LOCAL_MACHINE, `SOFTWARE\Microsoft\EdgeUpdate\Clients\` + guid},
		{registry.CURRENT_USER, `SOFTWARE\Microsoft\EdgeUpdate\Clients\` + guid},
	}
	for _, p := range paths {
		k, err := registry.OpenKey(p.root, p.path, registry.QUERY_VALUE)
		if err != nil {
			continue
		}
		pv, _, err := k.GetStringValue("pv")
		k.Close()
		if err == nil && pv != "" {
			return pv
		}
	}
	return ""
}

// webViewProfileDir 返回 Wails 默认 WebView2 用户数据目录（%APPDATA%\<exe 名>）。
// 与 Wails 内部默认值保持一致（见 wails internal/webview2/pkg/edge/chromium.go）。
func webViewProfileDir() string {
	appData := os.Getenv("APPDATA")
	if appData == "" {
		return ""
	}
	exe, err := os.Executable()
	if err != nil {
		return ""
	}
	return filepath.Join(appData, filepath.Base(exe))
}