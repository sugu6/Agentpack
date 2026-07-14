package skills

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// withTempHome 在临时 HOME 目录下执行 fn
func withTempHome(t *testing.T, fn func(homeDir string)) {
	t.Helper()
	tmpHome := t.TempDir()

	// 保存并覆盖 HOME
	origHome := os.Getenv("HOME")
	origUserProfile := os.Getenv("USERPROFILE")
	origHomeDrive := os.Getenv("HOMEDRIVE")
	origHomePath := os.Getenv("HOMEPATH")

	// Windows 下 os.UserHomeDir() 优先使用 USERPROFILE
	os.Setenv("HOME", tmpHome)
	os.Setenv("USERPROFILE", tmpHome)
	os.Setenv("HOMEDRIVE", "")
	os.Setenv("HOMEPATH", "")

	defer func() {
		os.Setenv("HOME", origHome)
		os.Setenv("USERPROFILE", origUserProfile)
		os.Setenv("HOMEDRIVE", origHomeDrive)
		os.Setenv("HOMEPATH", origHomePath)
	}()

	fn(tmpHome)
}

func readLockFile(t *testing.T, homeDir string) AgentsLockFile {
	t.Helper()
	path := filepath.Join(homeDir, ".agents", ".skill-lock.json")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read lock file: %v", err)
	}
	var lock AgentsLockFile
	if err := json.Unmarshal(data, &lock); err != nil {
		t.Fatalf("unmarshal lock file: %v", err)
	}
	return lock
}

func TestWriteAgentsLock_NewFile(t *testing.T) {
	withTempHome(t, func(homeDir string) {
		err := WriteAgentsLock(AgentsLockEntry{
			Directory:  "my-skill",
			Source:     "anthropics/skills",
			SourceType: "github",
			SourceURL:  "https://github.com/anthropics/skills",
			SkillPath:  "/home/user/.agentpack/skills/my-skill",
			Branch:     "main",
		})
		if err != nil {
			t.Fatal(err)
		}

		lock := readLockFile(t, homeDir)
		if len(lock.Skills) != 1 {
			t.Fatalf("expected 1 skill, got %d", len(lock.Skills))
		}
		skill, ok := lock.Skills["my-skill"]
		if !ok {
			t.Fatal("expected 'my-skill' key in lock file")
		}
		if skill.Source != "anthropics/skills" {
			t.Errorf("expected source 'anthropics/skills', got %q", skill.Source)
		}
		if skill.SourceType != "github" {
			t.Errorf("expected sourceType 'github', got %q", skill.SourceType)
		}
		if skill.Branch != "main" {
			t.Errorf("expected branch 'main', got %q", skill.Branch)
		}
		if skill.SourceBranch != "main" {
			t.Errorf("expected sourceBranch 'main', got %q", skill.SourceBranch)
		}
	})
}

func TestWriteAgentsLock_UpdateExisting(t *testing.T) {
	withTempHome(t, func(homeDir string) {
		// 先写入一个条目
		err := WriteAgentsLock(AgentsLockEntry{
			Directory:  "skill-a",
			Source:     "owner-a/repo",
			SourceType: "github",
			SourceURL:  "https://github.com/owner-a/repo",
			SkillPath:  "/path/a",
			Branch:     "main",
		})
		if err != nil {
			t.Fatal(err)
		}

		// 再写入另一个条目
		err = WriteAgentsLock(AgentsLockEntry{
			Directory:  "skill-b",
			Source:     "owner-b/repo",
			SourceType: "github",
			SourceURL:  "https://github.com/owner-b/repo",
			SkillPath:  "/path/b",
			Branch:     "develop",
		})
		if err != nil {
			t.Fatal(err)
		}

		lock := readLockFile(t, homeDir)
		if len(lock.Skills) != 2 {
			t.Fatalf("expected 2 skills, got %d", len(lock.Skills))
		}

		// 验证两个条目都在
		if _, ok := lock.Skills["skill-a"]; !ok {
			t.Error("expected 'skill-a' to be preserved")
		}
		if _, ok := lock.Skills["skill-b"]; !ok {
			t.Error("expected 'skill-b' to be present")
		}

		// 验证第二个条目的分支
		if lock.Skills["skill-b"].Branch != "develop" {
			t.Errorf("expected branch 'develop', got %q", lock.Skills["skill-b"].Branch)
		}
	})
}

func TestWriteAgentsLock_OverwriteExistingEntry(t *testing.T) {
	withTempHome(t, func(homeDir string) {
		// 先写入
		err := WriteAgentsLock(AgentsLockEntry{
			Directory:  "my-skill",
			Source:     "old/repo",
			SourceType: "github",
			SourceURL:  "https://github.com/old/repo",
			SkillPath:  "/old/path",
			Branch:     "main",
		})
		if err != nil {
			t.Fatal(err)
		}

		// 用相同的 directory 覆盖
		err = WriteAgentsLock(AgentsLockEntry{
			Directory:  "my-skill",
			Source:     "new/repo",
			SourceType: "github",
			SourceURL:  "https://github.com/new/repo",
			SkillPath:  "/new/path",
			Branch:     "v2",
		})
		if err != nil {
			t.Fatal(err)
		}

		lock := readLockFile(t, homeDir)
		if len(lock.Skills) != 1 {
			t.Fatalf("expected 1 skill (overwritten), got %d", len(lock.Skills))
		}
		skill := lock.Skills["my-skill"]
		if skill.Source != "new/repo" {
			t.Errorf("expected source 'new/repo', got %q", skill.Source)
		}
		if skill.Branch != "v2" {
			t.Errorf("expected branch 'v2', got %q", skill.Branch)
		}
	})
}

func TestWriteAgentsLock_DefaultBranch(t *testing.T) {
	withTempHome(t, func(homeDir string) {
		err := WriteAgentsLock(AgentsLockEntry{
			Directory:  "my-skill",
			Source:     "owner/repo",
			SourceType: "github",
			SourceURL:  "https://github.com/owner/repo",
			SkillPath:  "/path",
			Branch:     "", // 空分支，应默认为 "main"
		})
		if err != nil {
			t.Fatal(err)
		}

		lock := readLockFile(t, homeDir)
		skill := lock.Skills["my-skill"]
		if skill.Branch != "main" {
			t.Errorf("expected default branch 'main', got %q", skill.Branch)
		}
		if skill.SourceBranch != "main" {
			t.Errorf("expected default sourceBranch 'main', got %q", skill.SourceBranch)
		}
	})
}

func TestWriteAgentsLock_EmptyDirectory(t *testing.T) {
	withTempHome(t, func(homeDir string) {
		err := WriteAgentsLock(AgentsLockEntry{
			Directory: "",
			Source:    "owner/repo",
		})
		if err == nil {
			t.Fatal("expected error for empty directory, got nil")
		}
	})
}

func TestWriteAgentsLock_PreservesOtherEntries(t *testing.T) {
	withTempHome(t, func(homeDir string) {
		// 模拟其他工具写入的 lock 文件
		lockDir := filepath.Join(homeDir, ".agents")
		if err := os.MkdirAll(lockDir, 0700); err != nil {
			t.Fatal(err)
		}
		existingLock := AgentsLockFile{
			Skills: map[string]AgentsLockSkill{
				"other-tool-skill": {
					Source:      "other/repo",
					SourceType:  "github",
					SourceURL:   "https://github.com/other/repo",
					SkillPath:   "/other/path",
					Branch:      "main",
					SourceBranch: "main",
				},
			},
		}
		data, _ := json.MarshalIndent(existingLock, "", "  ")
		if err := os.WriteFile(filepath.Join(lockDir, ".skill-lock.json"), data, 0600); err != nil {
			t.Fatal(err)
		}

		// 写入我们的条目
		err := WriteAgentsLock(AgentsLockEntry{
			Directory:  "my-skill",
			Source:     "my/repo",
			SourceType: "github",
			SourceURL:  "https://github.com/my/repo",
			SkillPath:  "/my/path",
			Branch:     "main",
		})
		if err != nil {
			t.Fatal(err)
		}

		lock := readLockFile(t, homeDir)
		if len(lock.Skills) != 2 {
			t.Fatalf("expected 2 skills (preserved + new), got %d", len(lock.Skills))
		}
		// 验证其他工具的条目被保留
		other, ok := lock.Skills["other-tool-skill"]
		if !ok {
			t.Fatal("expected 'other-tool-skill' to be preserved")
		}
		if other.Source != "other/repo" {
			t.Errorf("expected preserved source 'other/repo', got %q", other.Source)
		}
	})
}

func TestWriteAgentsLock_CorruptedFileNotOverwritten(t *testing.T) {
	withTempHome(t, func(homeDir string) {
		// 写入损坏的 JSON
		lockDir := filepath.Join(homeDir, ".agents")
		if err := os.MkdirAll(lockDir, 0700); err != nil {
			t.Fatal(err)
		}
		corrupted := []byte("{invalid json}")
		if err := os.WriteFile(filepath.Join(lockDir, ".skill-lock.json"), corrupted, 0600); err != nil {
			t.Fatal(err)
		}

		// 尝试写入应失败（不覆盖损坏文件）
		err := WriteAgentsLock(AgentsLockEntry{
			Directory:  "my-skill",
			Source:     "my/repo",
			SourceType: "github",
			SourceURL:  "https://github.com/my/repo",
			SkillPath:  "/my/path",
			Branch:     "main",
		})
		if err == nil {
			t.Fatal("expected error for corrupted file, got nil")
		}

		// 验证文件未被覆盖
		data, err := os.ReadFile(filepath.Join(lockDir, ".skill-lock.json"))
		if err != nil {
			t.Fatal(err)
		}
		if string(data) != "{invalid json}" {
			t.Errorf("expected corrupted file to be preserved, got %q", string(data))
		}
	})
}
