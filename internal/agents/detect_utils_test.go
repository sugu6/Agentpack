package agents

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestIsRegistryEntryLive(t *testing.T) {
	dir := t.TempDir()
	appDir := filepath.Join(dir, "app")
	if err := os.MkdirAll(appDir, 0755); err != nil {
		t.Fatal(err)
	}
	exe := filepath.Join(dir, "uninstaller.exe")
	if err := os.WriteFile(exe, []byte("x"), 0644); err != nil {
		t.Fatal(err)
	}
	spaceDir := filepath.Join(dir, "My App")
	if err := os.MkdirAll(spaceDir, 0755); err != nil {
		t.Fatal(err)
	}
	spaceExe := filepath.Join(spaceDir, "unins.exe")
	if err := os.WriteFile(spaceExe, []byte("x"), 0644); err != nil {
		t.Fatal(err)
	}
	msiExe := filepath.Join(dir, "MsiExec.exe")
	if err := os.WriteFile(msiExe, []byte("x"), 0644); err != nil {
		t.Fatal(err)
	}
	missing := filepath.Join(dir, "missing")

	tests := []struct {
		name   string
		loc    string
		uninst string
		want   bool
	}{
		{"安装目录存在", appDir, "", true},
		{"安装目录缺失但卸载程序存在（引号路径）", missing, `"` + exe + `" /S`, true},
		{"无安装目录，卸载程序存在（裸路径）", "", exe + " /quiet", true},
		{"无引号的含空格路径截取到 .exe", "", spaceExe + " /S", true},
		{"安装目录与卸载程序均缺失", missing, `"` + missing + `"`, false},
		{"两项均为空", "", "", false},
		{"MSI 条目（绝对路径 msiexec）按已安装处理", missing, `"` + msiExe + `" /X{1234}`, true},
		{"MSI 条目（相对 MsiExec.exe）按已安装处理", "", `MsiExec.exe /X{1234-5678}`, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := isRegistryEntryLive(tt.loc, tt.uninst); got != tt.want {
				t.Errorf("isRegistryEntryLive(%q, %q) = %v, want %v", tt.loc, tt.uninst, got, tt.want)
			}
		})
	}
}

func TestExtractUninstallPath(t *testing.T) {
	dir := t.TempDir()
	exe := filepath.Join(dir, "uninstall.exe")
	if err := os.WriteFile(exe, []byte("x"), 0644); err != nil {
		t.Fatal(err)
	}
	spaceExe := filepath.Join(dir, "My App", "unins.exe")

	tests := []struct {
		name string
		in   string
		want string
	}{
		{"引号包裹的完整路径", `"` + exe + `" /S`, exe},
		{"MsiExec GUID 返回空", `MsiExec.exe /X{1234-5678}`, ""},
		{"裸绝对路径", exe + " --silent", exe},
		{"含空格目录未加引号，截取到 .exe", spaceExe + " /S", spaceExe},
		{"空串", "", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := extractUninstallPath(tt.in); got != tt.want {
				t.Errorf("extractUninstallPath(%q) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}

func TestExpandEnvPath(t *testing.T) {
	t.Setenv("AGENTPACK_TEST_VAR", "envval")

	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)

	tests := []struct {
		name string
		in   string
		want string
	}{
		{"%VAR% 展开", "%AGENTPACK_TEST_VAR%" + string(os.PathSeparator) + "sub", filepath.Join("envval", "sub")},
		{"$VAR 展开", "$AGENTPACK_TEST_VAR" + string(os.PathSeparator) + "sub", filepath.Join("envval", "sub")},
		{"前导 ~ 展开", "~/app", filepath.Join(home, "app")},
		{"普通路径不变", filepath.Join("C", "x"), filepath.Join("C", "x")},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := expandEnvPath(tt.in); got != tt.want {
				t.Errorf("expandEnvPath(%q) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}

func TestDetectIDE_InstallPathFallback(t *testing.T) {
	// TestMain 已开启 skipRegistryLookup，注册表不会干扰本测试
	dir := t.TempDir()
	exe := filepath.Join(dir, "fake-ide.exe")
	if err := os.WriteFile(exe, []byte("x"), 0644); err != nil {
		t.Fatal(err)
	}

	// 候选路径命中 → 判定已安装
	paths := map[string][]string{runtime.GOOS: {exe}}
	if !DetectIDE([]string{"Fake IDE"}, paths) {
		t.Error("expected detected via existing install path candidate")
	}

	// 候选路径不存在 → 判定未安装
	if DetectIDE([]string{"Fake IDE"}, map[string][]string{runtime.GOOS: {filepath.Join(dir, "nope.exe")}}) {
		t.Error("expected not detected when install path candidate missing")
	}

	// 空候选列表 → 判定未安装（该平台仅依赖注册表）
	if DetectIDE([]string{"Fake IDE"}, map[string][]string{runtime.GOOS: {}}) {
		t.Error("expected not detected with empty install paths")
	}
}

func TestPercentToDollar(t *testing.T) {
	if got := percentToDollar(`%LOCALAPPDATA%\cursor\x%y%`); got != `$LOCALAPPDATA\cursor\x$y` {
		t.Errorf("percentToDollar unexpected: %q", got)
	}
	if got := percentToDollar(`plain-path`); got != `plain-path` {
		t.Errorf("percentToDollar(%q) = %q", `plain-path`, got)
	}
	if !strings.Contains(percentToDollar(`%A%%B%`), "$A$B") {
		t.Errorf("percentToDollar should expand consecutive vars, got %q", percentToDollar(`%A%%B%`))
	}
}