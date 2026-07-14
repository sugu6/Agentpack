package win32

import (
	"golang.org/x/sys/windows/registry"
)

// IsSystemLightTheme reads the Windows registry to detect if the OS is in light mode.
func IsSystemLightTheme() bool {
	k, err := registry.OpenKey(registry.CURRENT_USER,
		`Software\Microsoft\Windows\CurrentVersion\Themes\Personalize`,
		registry.READ|registry.WOW64_64KEY)
	if err != nil {
		return false
	}
	defer k.Close()
	val, _, err := k.GetIntegerValue("AppsUseLightTheme")
	if err != nil {
		return false
	}
	return val == 1
}
