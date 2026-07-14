package i18n

// MessagesEn English message bundle
// Note: new keys must also be added to messages_zh.go; TestMessagesKeyConsistency validates key set parity
var MessagesEn = map[string]string{
	// tray - system tray menu
	"tray.show":      "Show main window",
	"tray.settings":  "Settings",
	"tray.quit":      "Quit",
	"tray.tooltip":   "AgentPack - Agent management tool",

	// update - check for updates
	"update.message.hasUpdate":      "Found new version v{version}",
	"update.message.latest":         "You're on the latest version v{version}",
	"update.message.noRelease":      "No releases published yet",
	"update.message.rateLimited":    "GitHub API rate limited, please try again later",
	"update.message.networkFailed":  "Network request failed: {error}",
	"update.download.serverError":   "Server returned {code}",
	"update.download.failed":        "Download failed: {error}",
	"update.download.canceled":      "Download canceled",

	// error - generic errors
	"error.network":          "Network error: {error}",
	"error.fileNotFound":     "File not found: {path}",
	"error.permissionDenied": "Permission denied",
	"error.invalidInput":     "Invalid input: {detail}",
	"error.unknown":          "Unknown error: {error}",

	// startup - startup errors
	"startup.errors": "The following issues occurred during startup:",
}
