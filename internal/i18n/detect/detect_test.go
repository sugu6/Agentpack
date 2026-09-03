package detect

import "testing"

// TestDetectSystemLanguage 验证系统语言检测返回受支持的语言（zh-CN 或 en）。
func TestDetectSystemLanguage(t *testing.T) {
	got := DetectSystemLanguage()
	if got != "zh-CN" && got != "en" {
		t.Errorf("DetectSystemLanguage = %q, want zh-CN or en", got)
	}
}
