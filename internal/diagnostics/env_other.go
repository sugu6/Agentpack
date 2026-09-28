//go:build !windows

package diagnostics

// osVersion：非 Windows 平台暂不采集（runtime.GOOS/GOARCH 已提供基础信息）。
func osVersion() string { return "" }

// webView2Version：WebView2 仅存在于 Windows。
func webView2Version() string { return "" }

// webViewProfileDir：WebView2 用户数据目录仅存在于 Windows。
func webViewProfileDir() string { return "" }