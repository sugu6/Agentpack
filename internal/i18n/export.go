// Package i18n 提供后端用户可见字符串的国际化支持。
// 实现按功能拆分：detect/ 子包负责系统语言检测，本包提供翻译函数与语言解析。
package i18n

import "agentpack/internal/i18n/detect"

// DetectSystemLanguage 检测系统语言,返回 "zh-CN" 或 "en"（re-export detect 子包）。
func DetectSystemLanguage() string {
	return detect.DetectSystemLanguage()
}
