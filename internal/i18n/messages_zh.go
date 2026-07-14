package i18n

// MessagesZh 中文消息包
// 注意: 新增键必须同步更新 messages_en.go,TestMessagesKeyConsistency 会校验键集一致
var MessagesZh = map[string]string{
	// tray - 系统托盘菜单
	"tray.show":      "显示主窗口",
	"tray.settings":  "设置",
	"tray.quit":      "退出",
	"tray.tooltip":   "AgentPack - Agent 管理工具",

	// update - 检查更新
	"update.message.hasUpdate":      "发现新版本 v{version}",
	"update.message.latest":         "当前已是最新版本 v{version}",
	"update.message.noRelease":      "尚未发布任何版本",
	"update.message.rateLimited":    "GitHub API 请求过于频繁,请稍后再试",
	"update.message.networkFailed":  "网络请求失败: {error}",
	"update.download.serverError":   "服务器返回 {code}",
	"update.download.failed":        "下载失败: {error}",
	"update.download.canceled":      "下载已取消",

	// error - 通用错误
	"error.network":          "网络错误: {error}",
	"error.fileNotFound":     "文件未找到: {path}",
	"error.permissionDenied": "权限不足",
	"error.invalidInput":     "输入无效: {detail}",
	"error.unknown":          "未知错误: {error}",

	// startup - 启动错误
	"startup.errors": "启动时出现以下问题:",
}
