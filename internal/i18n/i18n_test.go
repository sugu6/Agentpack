package i18n

import (
	"sort"
	"testing"
)

func TestT_Basic(t *testing.T) {
	if got := T("zh-CN", "tray.show"); got != "显示主窗口" {
		t.Errorf("zh-CN tray.show = %q, want %q", got, "显示主窗口")
	}
	if got := T("en", "tray.show"); got != "Show main window" {
		t.Errorf("en tray.show = %q, want %q", got, "Show main window")
	}
}

func TestT_FallbackChain(t *testing.T) {
	// 不支持语言回退到英文(而非中文)
	if got := T("ja", "tray.show"); got != "Show main window" {
		t.Errorf("ja fallback = %q, want English", got)
	}
	if got := T("fr", "tray.show"); got != "Show main window" {
		t.Errorf("fr fallback = %q, want English", got)
	}
	// 空语言回退到英文
	if got := T("", "tray.show"); got != "Show main window" {
		t.Errorf("empty lang fallback = %q, want English", got)
	}
}

func TestT_MissingKey(t *testing.T) {
	if got := T("en", "nonexistent.key"); got != "nonexistent.key" {
		t.Errorf("missing key = %q, want key itself", got)
	}
	if got := T("zh-CN", "nonexistent.key"); got != "nonexistent.key" {
		t.Errorf("zh-CN missing key = %q, want key itself", got)
	}
}

func TestT_Interpolation(t *testing.T) {
	got := T("en", "update.message.hasUpdate", map[string]interface{}{"version": "0.2.0"})
	want := "Found new version v0.2.0"
	if got != want {
		t.Errorf("interpolation = %q, want %q", got, want)
	}
	got = T("zh-CN", "update.message.hasUpdate", map[string]interface{}{"version": "0.2.0"})
	want = "发现新版本 v0.2.0"
	if got != want {
		t.Errorf("zh interpolation = %q, want %q", got, want)
	}
}

func TestMessagesKeyConsistency(t *testing.T) {
	zhKeys := keySet(MessagesZh)
	enKeys := keySet(MessagesEn)
	var missingInEn, missingInZh []string
	for k := range zhKeys {
		if !enKeys[k] {
			missingInEn = append(missingInEn, k)
		}
	}
	for k := range enKeys {
		if !zhKeys[k] {
			missingInZh = append(missingInZh, k)
		}
	}
	if len(missingInEn) > 0 {
		sort.Strings(missingInEn)
		t.Errorf("keys missing in en.json: %v", missingInEn)
	}
	if len(missingInZh) > 0 {
		sort.Strings(missingInZh)
		t.Errorf("keys missing in zh-CN.json: %v", missingInZh)
	}
}

func TestResolveLanguage(t *testing.T) {
	if got := ResolveLanguage("zh-CN"); got != "zh-CN" {
		t.Errorf("ResolveLanguage(zh-CN) = %q", got)
	}
	if got := ResolveLanguage("en"); got != "en" {
		t.Errorf("ResolveLanguage(en) = %q", got)
	}
	if got := ResolveLanguage("ja"); got != "en" {
		t.Errorf("ResolveLanguage(ja) = %q, want en", got)
	}
	if got := ResolveLanguage("fr"); got != "en" {
		t.Errorf("ResolveLanguage(fr) = %q, want en", got)
	}
	got := ResolveLanguage("")
	if got != "zh-CN" && got != "en" {
		t.Errorf("ResolveLanguage(empty) = %q, want zh-CN or en", got)
	}
}

func TestDetectSystemLanguage(t *testing.T) {
	got := DetectSystemLanguage()
	if got != "zh-CN" && got != "en" {
		t.Errorf("DetectSystemLanguage = %q, want zh-CN or en", got)
	}
}

func keySet(m map[string]string) map[string]bool {
	s := make(map[string]bool, len(m))
	for k := range m {
		s[k] = true
	}
	return s
}
