// Package appmeta 维护跨包共享的应用元数据（版本号等）。
// Version 由 main 包在 init 时从 build/config.yml 注入，避免各网络层
// 硬编码版本号导致发布后 User-Agent 失真。
package appmeta

// Version 当前应用版本号（X.Y.Z）。默认占位，main 包启动时更新。
var Version = "0.0.0"

// UserAgent 构造统一的 User-Agent 头。repoURL 为项目仓库主页，可为空。
func UserAgent(repoURL string) string {
	if repoURL == "" {
		return "AgentPack/" + Version
	}
	return "AgentPack/" + Version + " (+" + repoURL + ")"
}
