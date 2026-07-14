// Package i18n 提供后端用户可见字符串的国际化支持
// 仅支持 "zh-CN" 和 "en" 两种语言,其他语言回退到英文
package i18n

import (
	"fmt"
	"strings"
)

// T 翻译指定 key 到 lang 语言,使用 args 进行命名插值
// lang 为 "" 或不支持的语言时,回退到英文
// 缺失键最终回退到 key 本身
func T(lang, key string, args ...map[string]interface{}) string {
	msg, ok := lookup(lang, key)
	if !ok {
		msg, ok = lookup("en", key) // 回退到英文
		if !ok {
			return key // 最终回退:返回 key 本身
		}
	}
	if len(args) > 0 {
		for k, v := range args[0] {
			msg = strings.ReplaceAll(msg, "{"+k+"}", fmt.Sprintf("%v", v))
		}
	}
	return msg
}

// lookup 根据 lang 选择消息 map
// 仅 "zh-CN" 使用中文 map,其他(含 "" 和不支持语言)均使用英文 map
func lookup(lang, key string) (string, bool) {
	if lang == "zh-CN" {
		v, ok := MessagesZh[key]
		return v, ok
	}
	v, ok := MessagesEn[key]
	return v, ok
}

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

// ResolveLanguage 将 Settings.Language 解析为最终语言
// "" (跟随系统) → DetectSystemLanguage()
// "zh-CN" / "en" → 原值
// 其他 → "en"
func ResolveLanguage(setting string) string {
	switch setting {
	case "zh-CN":
		return "zh-CN"
	case "en":
		return "en"
	case "":
		return DetectSystemLanguage()
	default:
		return "en"
	}
}
