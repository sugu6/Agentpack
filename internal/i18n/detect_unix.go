//go:build !windows

package i18n

import "os"

// detectSystemLanguageOS 读取 LANG 环境变量
// 返回值如 "zh_CN.UTF-8"、"en_US.UTF-8",失败返回 ""
func detectSystemLanguageOS() string {
	return os.Getenv("LANG")
}
