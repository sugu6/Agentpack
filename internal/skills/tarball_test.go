package skills

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

// createTestTarball 创建一个测试用 tar.gz 文件（不含恶意条目）
// repoName: 顶层目录名（模拟 GitHub tarball 的 {repo}-{hash}/ 结构）
// skills: map[directory]SKILL.md content
func createTestTarball(t *testing.T, repoName string, skills map[string]string) []byte {
	var buf bytes.Buffer
	gw := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gw)

	// 顶层目录
	topDir := repoName + "-abc123"
	_ = tw.WriteHeader(&tar.Header{
		Name:     topDir + "/",
		Typeflag: tar.TypeDir,
		Mode:     0755,
	})

	// 写入 README.md（非 skill 文件）
	readmeContent := "# Test Repo"
	_ = tw.WriteHeader(&tar.Header{
		Name:     topDir + "/README.md",
		Typeflag: tar.TypeReg,
		Mode:     0644,
		Size:     int64(len(readmeContent)),
	})
	_, _ = tw.Write([]byte(readmeContent))

	// 写入每个 skill 的 SKILL.md
	for dir, content := range skills {
		skillDir := topDir + "/" + dir + "/"
		_ = tw.WriteHeader(&tar.Header{
			Name:     skillDir,
			Typeflag: tar.TypeDir,
			Mode:     0755,
		})
		_ = tw.WriteHeader(&tar.Header{
			Name:     skillDir + "SKILL.md",
			Typeflag: tar.TypeReg,
			Mode:     0644,
			Size:     int64(len(content)),
		})
		_, _ = tw.Write([]byte(content))
	}

	_ = tw.Close()
	_ = gw.Close()
	return buf.Bytes()
}

// createTestTarballWithTraversal 创建含路径穿越条目的 tarball
func createTestTarballWithTraversal(t *testing.T, repoName string, skills map[string]string) []byte {
	var buf bytes.Buffer
	gw := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gw)

	topDir := repoName + "-abc123"
	_ = tw.WriteHeader(&tar.Header{
		Name:     topDir + "/",
		Typeflag: tar.TypeDir,
		Mode:     0755,
	})

	for dir, content := range skills {
		skillDir := topDir + "/" + dir + "/"
		_ = tw.WriteHeader(&tar.Header{
			Name:     skillDir,
			Typeflag: tar.TypeDir,
			Mode:     0755,
		})
		_ = tw.WriteHeader(&tar.Header{
			Name:     skillDir + "SKILL.md",
			Typeflag: tar.TypeReg,
			Mode:     0644,
			Size:     int64(len(content)),
		})
		_, _ = tw.Write([]byte(content))
	}

	// 路径穿越条目
	_ = tw.WriteHeader(&tar.Header{
		Name:     topDir + "/../escaped.txt",
		Typeflag: tar.TypeReg,
		Mode:     0644,
		Size:     5,
	})
	_, _ = tw.Write([]byte("evil"))

	_ = tw.Close()
	_ = gw.Close()
	return buf.Bytes()
}

func TestDownloadAndExtractTarball_Success(t *testing.T) {
	tarball := createTestTarball(t, "testrepo", map[string]string{
		"my-skill": "---\nname: My Skill\ndescription: Test\n---\nbody",
	})

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/gzip")
		_, _ = w.Write(tarball)
	}))
	defer server.Close()

	tmpDir := t.TempDir()
	if err := downloadAndExtractTarball(context.Background(), server.URL, tmpDir); err != nil {
		t.Fatal(err)
	}

	// 验证文件被解压
	entries, err := os.ReadDir(tmpDir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) == 0 {
		t.Fatal("expected extracted files, got empty dir")
	}
}

func TestDownloadAndExtractTarball_TarSlipRejected(t *testing.T) {
	tarball := createTestTarballWithTraversal(t, "testrepo", map[string]string{
		"my-skill": "---\nname: My Skill\n---\nbody",
	})

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write(tarball)
	}))
	defer server.Close()

	tmpDir := t.TempDir()
	err := downloadAndExtractTarball(context.Background(), server.URL, tmpDir)
	if err == nil {
		t.Fatal("expected error for tarball with path traversal, got nil")
	}
	// 验证错误信息包含 path traversal
	if !containsStr(err.Error(), "path traversal") {
		t.Errorf("expected 'path traversal' in error, got %q", err.Error())
	}
}

func TestDownloadAndExtractTarball_SymlinkSkipped(t *testing.T) {
	// 符号链接条目应被跳过（不报错），正常文件仍正常解压
	var buf bytes.Buffer
	gw := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gw)

	_ = tw.WriteHeader(&tar.Header{
		Name:     "topdir/",
		Typeflag: tar.TypeDir,
		Mode:     0755,
	})
	skillContent := "---\nname: Test\n---\nbody"
	_ = tw.WriteHeader(&tar.Header{
		Name:     "topdir/SKILL.md",
		Typeflag: tar.TypeReg,
		Mode:     0644,
		Size:     int64(len(skillContent)),
	})
	_, _ = tw.Write([]byte(skillContent))
	_ = tw.WriteHeader(&tar.Header{
		Name:     "topdir/evil-link",
		Typeflag: tar.TypeSymlink,
		Linkname: "/etc/passwd",
		Mode:     0644,
	})
	_ = tw.Close()
	_ = gw.Close()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write(buf.Bytes())
	}))
	defer server.Close()

	tmpDir := t.TempDir()
	// 应该成功解压，符号链接被跳过
	err := downloadAndExtractTarball(context.Background(), server.URL, tmpDir)
	if err != nil {
		t.Fatalf("expected success (symlink skipped), got error: %v", err)
	}
	// 验证 SKILL.md 被解压
	skillPath := filepath.Join(tmpDir, "topdir", "SKILL.md")
	if _, err := os.Stat(skillPath); err != nil {
		t.Errorf("expected SKILL.md to be extracted: %v", err)
	}
	// 验证 evil-link 未被创建
	evilPath := filepath.Join(tmpDir, "topdir", "evil-link")
	if _, err := os.Stat(evilPath); err == nil {
		t.Error("expected evil-link to NOT be created (symlink skipped)")
	}
}

func TestDownloadAndExtractTarball_ServerError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(500)
	}))
	defer server.Close()

	tmpDir := t.TempDir()
	err := downloadAndExtractTarball(context.Background(), server.URL, tmpDir)
	if err == nil {
		t.Fatal("expected error for 500 response, got nil")
	}
}

func TestFindSkillRootInTarball_MatchDirectory(t *testing.T) {
	tmpDir := t.TempDir()
	// 创建 {repo}-hash/{directory}/SKILL.md 结构
	repoDir := filepath.Join(tmpDir, "testrepo-abc123")
	skillDir := filepath.Join(repoDir, "my-skill")
	_ = os.MkdirAll(skillDir, 0755)
	_ = os.WriteFile(filepath.Join(skillDir, "SKILL.md"), []byte("---\nname: My Skill\n---\n"), 0644)

	root, err := findSkillRootInTarball(tmpDir, "my-skill", "")
	if err != nil {
		t.Fatal(err)
	}
	if filepath.Base(root) != "my-skill" {
		t.Errorf("expected root base 'my-skill', got %q", filepath.Base(root))
	}
}

func TestFindSkillRootInTarball_NoSkillMD(t *testing.T) {
	tmpDir := t.TempDir()
	repoDir := filepath.Join(tmpDir, "testrepo-abc123")
	_ = os.MkdirAll(repoDir, 0755)
	_ = os.WriteFile(filepath.Join(repoDir, "README.md"), []byte("readme"), 0644)

	_, err := findSkillRootInTarball(tmpDir, "my-skill", "")
	if err == nil {
		t.Fatal("expected error for no SKILL.md, got nil")
	}
}

// TestFindSkillRootInTarball_FullPath 验证 fullPath 精准定位
// 如 anthropics/skills 仓库的 skills/pdf/SKILL.md
// tarball 解压后: {repo}-hash/skills/pdf/SKILL.md
func TestFindSkillRootInTarball_FullPath(t *testing.T) {
	tmpDir := t.TempDir()
	// 创建 {repo}-hash/skills/pdf/SKILL.md 结构（模拟 anthropics/skills tarball）
	repoDir := filepath.Join(tmpDir, "skills-abc123")
	skillDir := filepath.Join(repoDir, "skills", "pdf")
	_ = os.MkdirAll(skillDir, 0755)
	_ = os.WriteFile(filepath.Join(skillDir, "SKILL.md"), []byte("---\nname: PDF\n---\n"), 0644)
	// 同时创建 skills/xlsx/SKILL.md
	xlsxDir := filepath.Join(repoDir, "skills", "xlsx")
	_ = os.MkdirAll(xlsxDir, 0755)
	_ = os.WriteFile(filepath.Join(xlsxDir, "SKILL.md"), []byte("---\nname: XLSX\n---\n"), 0644)

	// 用 fullPath="skills/pdf" 应精准定位到 pdf 目录
	root, err := findSkillRootInTarball(tmpDir, "pdf", "skills/pdf")
	if err != nil {
		t.Fatal(err)
	}
	if filepath.Base(root) != "pdf" {
		t.Errorf("expected root base 'pdf', got %q", filepath.Base(root))
	}
}

func TestValidateTarballInput(t *testing.T) {
	tests := []struct {
		name    string
		input   TarballInstallInput
		wantErr bool
	}{
		{
			name:    "valid",
			input:   TarballInstallInput{TarballURL: "https://codeload.github.com/a/b/tar.gz/refs/heads/main", Directory: "skill", RepoOwner: "a", RepoName: "b"},
			wantErr: false,
		},
		{
			name:    "empty URL",
			input:   TarballInstallInput{Directory: "skill", RepoOwner: "a", RepoName: "b"},
			wantErr: true,
		},
		{
			name:    "non-codeload URL",
			input:   TarballInstallInput{TarballURL: "https://evil.com/tar.gz", Directory: "skill", RepoOwner: "a", RepoName: "b"},
			wantErr: true,
		},
		{
			name:    "empty directory",
			input:   TarballInstallInput{TarballURL: "https://codeload.github.com/a/b/tar.gz/refs/heads/main", RepoOwner: "a", RepoName: "b"},
			wantErr: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := validateTarballInput(tt.input)
			if (err != nil) != tt.wantErr {
				t.Errorf("validateTarballInput() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

// containsStr 简单的字符串包含检查
func containsStr(s, substr string) bool {
	return len(s) >= len(substr) && (s == substr || len(substr) == 0 ||
		(len(s) > 0 && len(substr) > 0 && stringContains(s, substr)))
}

func stringContains(s, substr string) bool {
	for i := 0; i+len(substr) <= len(s); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}
