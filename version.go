package main

import (
	_ "embed"
	"strings"

	"agentpack/internal/appmeta"
)

//go:embed build/config.yml
var buildConfigYML []byte

// init 注入应用版本到 appmeta，供各网络层构造 User-Agent（避免硬编码版本号）。
// embed 留在 main 包：go:embed 路径相对源文件目录，internal/app 无法引用
// 仓库根的 build/ 目录（禁止 ".." 路径）。
func init() {
	appmeta.Version = parseAppVersion()
}

// parseAppVersion 从 build/config.yml 的 info.version 字段提取版本号。
// 使用缩进感知的简单解析器：找到 "info:" 顶层键后，在其缩进范围内查找 "version:"，
// 避免误匹配顶层 version: '3' 等其他同名字段，比按行顺序扫描更健壮。
func parseAppVersion() string {
	return parseVersionFromYAML(buildConfigYML)
}

// parseVersionFromYAML 是 parseAppVersion 的可测试版本，接受 YAML 内容作为参数。
func parseVersionFromYAML(data []byte) string {
	lines := strings.Split(string(data), "\n")
	infoIndent := -1 // 记录 info: 所在行的缩进值（顶层为 0）；-1 表示不在 info 块内
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
			continue
		}
		indent := len(line) - len(strings.TrimLeft(line, " "))
		// 检测顶层或当前块的 "info:" 键
		if infoIndent < 0 && indent == 0 && strings.HasPrefix(trimmed, "info:") {
			infoIndent = indent // 记住 info: 本身的缩进（通常为 0）
			continue
		}
		// 在 info 块内（缩进严格大于 info: 行）查找 version 字段
		if infoIndent >= 0 && indent > infoIndent && strings.HasPrefix(trimmed, "version:") {
			val := strings.TrimPrefix(trimmed, "version:")
			val = strings.TrimSpace(val)
			val = strings.Trim(val, "\"'")
			// 取第一个空白字符前的内容（处理注释等）
			if idx := strings.IndexAny(val, " \t"); idx > 0 {
				val = val[:idx]
			}
			if strings.Count(val, ".") >= 2 {
				return val
			}
		}
		// 遇到与 info: 同行或更浅的缩进，退出 info 块
		if infoIndent >= 0 && indent <= infoIndent && trimmed != "" {
			infoIndent = -1
		}
	}
	return "0.0.0"
}

