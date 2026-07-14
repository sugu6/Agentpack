package skills

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"agentpack/internal/agents"
)

// realTestRequired 判断是否运行真实网络测试。
// 设置环境变量 AGENTPACK_REALNET=1 启用，默认跳过。
func realTestRequired(t *testing.T) {
	t.Helper()
	if os.Getenv("AGENTPACK_REALNET") != "1" {
		t.Skip("skipping real network test; set AGENTPACK_REALNET=1 to run")
	}
}

// withTempHomeAndSkillCache 设置临时 HOME 并重置 agents 包的 skillDirCache，
// 使 agent skills 目录指向临时目录，实现测试隔离。
// 与 lockfile_test.go 中的 withTempHome 区别：本函数额外重置 skillDirCache。
func withTempHomeAndSkillCache(t *testing.T, fn func(homeDir string)) {
	t.Helper()
	tmpHome := t.TempDir()
	// Windows 用 USERPROFILE，Unix 用 HOME
	origHome := os.Getenv("HOME")
	origUserProfile := os.Getenv("USERPROFILE")
	origHomeDrive := os.Getenv("HOMEDRIVE")
	origHomePath := os.Getenv("HOMEPATH")
	os.Setenv("HOME", tmpHome)
	os.Setenv("USERPROFILE", tmpHome)
	os.Setenv("HOMEDRIVE", "")
	os.Setenv("HOMEPATH", "")
	// 重置 skillDirCache，使其基于新的临时 HOME 重新计算
	agents.ResetSkillDirCacheForTesting()
	defer func() {
		os.Setenv("HOME", origHome)
		os.Setenv("USERPROFILE", origUserProfile)
		os.Setenv("HOMEDRIVE", origHomeDrive)
		os.Setenv("HOMEPATH", origHomePath)
		agents.ResetSkillDirCacheForTesting()
	}()
	fn(tmpHome)
}

// TestReal_InstallAndUninstallTarball 真实从 GitHub codeload 下载
// anthropics/skills 仓库的 tarball，安装 pdf skill 到 SSOT 并同步到
// claude-code agent 目录，验证安装结果后卸载并验证清理。
func TestReal_InstallAndUninstallTarball(t *testing.T) {
	realTestRequired(t)

	withTempHomeAndSkillCache(t, func(homeDir string) {
		// 构造 registry，注册 claude-code agent
		reg := agents.NewRegistry()
		reg.Register(agents.Agent{
			ID:           "claude-code",
			Name:         "Claude Code",
			Type:         agents.TypeClaudeCode,
			Status:       agents.StatusEnabled,
			ConfigPath:   filepath.Join(homeDir, ".claude.json"),
			ConfigFormat: agents.FormatJSON,
		})

		// 用临时目录作为 SSOT
		ssotDir := filepath.Join(t.TempDir(), "ssot-skills")
		if err := os.MkdirAll(ssotDir, 0755); err != nil {
			t.Fatal(err)
		}
		ss := NewStore(ssotDir, SyncMethodCopy)
		if err := ss.Load(reg); err != nil {
			t.Fatalf("skills store load: %v", err)
		}

		// agent skills 目录（由 skillDirCache 基于 tempHome 计算）
		agentSkillsDir := reg.AgentSkillsDir("claude-code")
		t.Logf("claude-code skills dir: %s", agentSkillsDir)
		t.Logf("SSOT dir: %s", ssotDir)

		// 构造 tarball 安装输入：anthropics/skills 仓库的 pdf skill
		input := TarballInstallInput{
			TarballURL: "https://codeload.github.com/anthropics/skills/tar.gz/refs/heads/main",
			Directory:  "pdf",
			FullPath:   "skills/pdf",
			RepoOwner:  "anthropics",
			RepoName:   "skills",
			RepoBranch: "main",
		}

		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
		defer cancel()

		// === 安装 ===
		installed, err := ss.InstallFromTarball(ctx, input, []string{"claude-code"}, reg)
		if err != nil {
			t.Fatalf("InstallFromTarball failed: %v", err)
		}
		t.Logf("installed skill: id=%s dir=%s name=%s", installed.ID, installed.Directory, installed.Name)

		// 验证 SSOT 目录存在 pdf skill
		ssotSkillPath := filepath.Join(ssotDir, "pdf")
		if !HasSkillManifest(ssotSkillPath) {
			t.Errorf("SSOT path %s should contain SKILL.md after install", ssotSkillPath)
		} else {
			t.Logf("SSOT install verified: %s/SKILL.md exists", ssotSkillPath)
		}

		// 验证 agent skills 目录已同步
		agentSkillPath := filepath.Join(agentSkillsDir, "pdf")
		if !HasSkillManifest(agentSkillPath) {
			t.Errorf("agent skills path %s should contain SKILL.md after sync", agentSkillPath)
		} else {
			t.Logf("agent sync verified: %s/SKILL.md exists", agentSkillPath)
		}

		// 验证 store.List() 包含已安装的 skill
		listed := ss.List()
		foundInList := false
		for _, s := range listed {
			if s.Directory == "pdf" {
				foundInList = true
				if s.RepoOwner != "anthropics" || s.RepoName != "skills" {
					t.Errorf("repo info mismatch: owner=%s name=%s", s.RepoOwner, s.RepoName)
				}
				break
			}
		}
		if !foundInList {
			t.Errorf("pdf skill not found in store.List() after install")
		}

		// 获取 skill ID 用于卸载
		sk, ok := ss.Get(installed.ID)
		if !ok {
			t.Fatalf("installed skill %s not found in store", installed.ID)
		}
		t.Logf("skill before uninstall: id=%s boundAgents=%v", sk.ID, sk.BoundAgents)

		// === 卸载 ===
		result, err := ss.Uninstall(installed.ID, reg)
		if err != nil {
			t.Fatalf("Uninstall failed: %v", err)
		}
		t.Logf("uninstalled: id=%s backup=%s", result.ID, result.BackupPath)

		// 验证 SSOT 目录已删除
		if _, err := os.Stat(ssotSkillPath); !os.IsNotExist(err) {
			t.Errorf("SSOT path %s should be removed after uninstall", ssotSkillPath)
		} else {
			t.Logf("SSOT uninstall verified: %s removed", ssotSkillPath)
		}

		// 验证 agent skills 目录已删除
		if _, err := os.Stat(agentSkillPath); !os.IsNotExist(err) {
			t.Errorf("agent skills path %s should be removed after uninstall", agentSkillPath)
		} else {
			t.Logf("agent sync cleanup verified: %s removed", agentSkillPath)
		}

		// 验证 store.List() 不再包含该 skill
		for _, s := range ss.List() {
			if s.Directory == "pdf" {
				t.Errorf("pdf skill still present in store.List() after uninstall")
				break
			}
		}
	})
}

// TestReal_InstallFromSkillsSh_Pptx 复现用户反馈的 bug：
// 从 skills.sh 安装 pptx skill 时，fullPath="pptx"（无 skills/ 前缀），
// 但 tarball 中 skill 位于 skills/pptx/，导致报错 "no SKILL.md found"。
// 验证 findSkillRootInTarball 的递归查找能按 directory 名正确定位。
func TestReal_InstallFromSkillsSh_Pptx(t *testing.T) {
	realTestRequired(t)

	withTempHomeAndSkillCache(t, func(homeDir string) {
		reg := agents.NewRegistry()
		reg.Register(agents.Agent{
			ID:           "claude-code",
			Name:         "Claude Code",
			Type:         agents.TypeClaudeCode,
			Status:       agents.StatusEnabled,
			ConfigPath:   filepath.Join(homeDir, ".claude.json"),
			ConfigFormat: agents.FormatJSON,
		})

		ssotDir := filepath.Join(t.TempDir(), "ssot-skills")
		if err := os.MkdirAll(ssotDir, 0755); err != nil {
			t.Fatal(err)
		}
		ss := NewStore(ssotDir, SyncMethodCopy)
		if err := ss.Load(reg); err != nil {
			t.Fatalf("skills store load: %v", err)
		}

		// 模拟 skills.sh 返回的数据：fullPath="pptx"（无 skills/ 前缀）
		input := TarballInstallInput{
			TarballURL: "https://codeload.github.com/anthropics/skills/tar.gz/refs/heads/main",
			Directory:  "pptx",
			FullPath:   "pptx", // skills.sh 不含 skills/ 前缀
			RepoOwner:  "anthropics",
			RepoName:   "skills",
			RepoBranch: "main",
		}

		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
		defer cancel()

		installed, err := ss.InstallFromTarball(ctx, input, []string{"claude-code"}, reg)
		if err != nil {
			t.Fatalf("InstallFromTarball failed: %v", err)
		}
		t.Logf("installed skill: id=%s dir=%s name=%s", installed.ID, installed.Directory, installed.Name)

		// 验证 SSOT 目录存在 pptx skill
		ssotSkillPath := filepath.Join(ssotDir, "pptx")
		if !HasSkillManifest(ssotSkillPath) {
			t.Fatalf("SSOT path %s should contain SKILL.md after install", ssotSkillPath)
		}
		t.Logf("SSOT install verified: %s/SKILL.md exists", ssotSkillPath)

		// 清理
		if _, err := ss.Uninstall(installed.ID, reg); err != nil {
			t.Logf("cleanup uninstall: %v", err)
		}
	})
}
