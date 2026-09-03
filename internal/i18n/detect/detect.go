// Package detect 提供系统语言检测，独立成包以与翻译函数解耦。
package detect

import "strings"

// DetectSystemLanguage 检测系统语言,返回 "zh-CN" 或 "en"
// Windows: 调 GetUserDefaultLocaleName
// Unix/macOS: 读 LANG 环境变量
// 检测失败或不支持的语言统一回退到 "en"
func DetectSystemLanguage() string {
	lang := detectSystemLanguageOS()
	if strings.HasPrefix(strings.ToLower(lang), "zh") {
		return "zh-CN"
	}
	return "en"
}
