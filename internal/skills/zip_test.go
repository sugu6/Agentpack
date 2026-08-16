package skills

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestInstallFromZip_RootSkillUsesZipName 验证 zip 根目录直接含 SKILL.md 时，
// 技能目录名取自 zip 文件名，而不是解压临时目录的随机名（skill-zip-123456）。
func TestInstallFromZip_RootSkillUsesZipName(t *testing.T) {
	setupSkillCapableAgentHome(t)
	tmp := t.TempDir()
	ssotDir := filepath.Join(tmp, "ssot")
	zipPath := filepath.Join(tmp, "my-skill.zip")

	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	f, err := zw.Create("SKILL.md")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.Write([]byte("---\nname: my-skill\n---\n# body")); err != nil {
		t.Fatal(err)
	}
	if _, err := zw.Create("helper.txt"); err != nil {
		t.Fatal(err)
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(zipPath, buf.Bytes(), 0644); err != nil {
		t.Fatal(err)
	}

	s := NewStore(ssotDir, SyncMethodCopy)
	sk, err := s.InstallFromZip(zipPath, []string{"claude-code"}, newSkillTestRegistry())
	if err != nil {
		t.Fatalf("InstallFromZip: %v", err)
	}
	if sk.Directory != "my-skill" {
		t.Fatalf("expected skill directory %q derived from zip name, got %q", "my-skill", sk.Directory)
	}
	if !HasSkillManifest(filepath.Join(ssotDir, "my-skill")) {
		t.Fatalf("expected skill installed under SSOT/%s", "my-skill")
	}
	if _, ok := s.Get("skill:my-skill"); !ok {
		t.Fatal("expected registered skill with id skill:my-skill")
	}
}

// TestFindSkillRootInTarball_RepoRootIsSkill 验证仓库根目录本身就是 skill 时
// （{repo}-{hash}/SKILL.md）能正确定位，而不是报"未找到 SKILL.md"。
func TestFindSkillRootInTarball_RepoRootIsSkill(t *testing.T) {
	tmpDir := t.TempDir()
	repoDir := filepath.Join(tmpDir, "my-skill-abc123")
	if err := os.MkdirAll(repoDir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repoDir, "SKILL.md"), []byte("---\nname: My Skill\n---\n"), 0644); err != nil {
		t.Fatal(err)
	}

	root, err := findSkillRootInTarball(tmpDir, "my-skill", "")
	if err != nil {
		t.Fatalf("expected repo-root skill to be found, got error: %v", err)
	}
	if filepath.Base(root) != "my-skill-abc123" {
		t.Errorf("expected root base 'my-skill-abc123', got %q", filepath.Base(root))
	}
}

// TestImportWithDirName_UsesExplicitName 验证 ImportWithDirName 使用显式目录名，
// 而不是源路径的随机临时目录名（tarball 顶层目录 {repo}-{hash} 场景）。
func TestImportWithDirName_UsesExplicitName(t *testing.T) {
	setupSkillCapableAgentHome(t)
	tmp := t.TempDir()
	ssotDir := filepath.Join(tmp, "ssot")
	src := filepath.Join(tmp, "my-skill-abc123")
	makeSkillDir(t, tmp, "my-skill-abc123", "---\nname: repo-skill\n---\n# body")

	s := NewStore(ssotDir, SyncMethodCopy)
	sk, err := s.ImportWithDirName(src, "my-skill", []string{"claude-code"}, newSkillTestRegistry(), "owner", "repo")
	if err != nil {
		t.Fatalf("ImportWithDirName: %v", err)
	}
	if sk.Directory != "my-skill" {
		t.Fatalf("expected directory 'my-skill', got %q", sk.Directory)
	}
	if !HasSkillManifest(filepath.Join(ssotDir, "my-skill")) {
		t.Fatal("expected skill installed under SSOT/my-skill")
	}
}

// buildTarWithHugeDeclaredSize 手工构造一个 size 字段声明超限、
// 实际内容很少的 tar 流，用于验证解压前的大小预检。
func buildTarWithHugeDeclaredSize(declared int64, content []byte) []byte {
	var buf bytes.Buffer
	var hdr [512]byte
	name := "topdir/big.bin"
	copy(hdr[:], name)
	copy(hdr[124:], []byte(fmt.Sprintf("%011o", declared)))
	hdr[156] = '0' // TypeReg
	for i := 148; i < 156; i++ {
		hdr[i] = ' '
	}
	var sum int64
	for _, b := range hdr {
		sum += int64(b)
	}
	copy(hdr[148:], []byte(fmt.Sprintf("%06o", sum)))
	hdr[154] = 0
	hdr[155] = ' '
	buf.Write(hdr[:])
	buf.Write(content)
	if rem := len(content) % 512; rem != 0 {
		buf.Write(make([]byte, 512-rem))
	}
	// end-of-archive: two zero blocks
	buf.Write(make([]byte, 1024))
	return buf.Bytes()
}

// TestExtractTar_DeclaredSizeOverLimitRejected 验证 tar 条目声明大小超过
// maxTarEntrySize 时直接报错，而不是静默截断后安装损坏文件。
func TestExtractTar_DeclaredSizeOverLimitRejected(t *testing.T) {
	tarData := buildTarWithHugeDeclaredSize(maxTarEntrySize+1, []byte("small content"))
	var buf bytes.Buffer
	gw := gzip.NewWriter(&buf)
	if _, err := gw.Write(tarData); err != nil {
		t.Fatal(err)
	}
	if err := gw.Close(); err != nil {
		t.Fatal(err)
	}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write(buf.Bytes())
	}))
	defer server.Close()

	tmpDir := t.TempDir()
	err := downloadAndExtractTarball(context.Background(), server.URL, tmpDir)
	if err == nil {
		t.Fatal("expected error for tar entry with declared size over limit")
	}
	if !strings.Contains(err.Error(), "too large") {
		t.Errorf("expected 'too large' error, got: %v", err)
	}
}

// TestExtractTar_TooManyEntriesRejected 验证海量小文件 tar 被条目数上限拦截，
// 防止撑爆磁盘/内存。
func TestExtractTar_TooManyEntriesRejected(t *testing.T) {
	var buf bytes.Buffer
	gw := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gw)
	for i := 0; i < maxTarFileCount+1; i++ {
		if err := tw.WriteHeader(&tar.Header{
			Name:     fmt.Sprintf("top/d%d", i),
			Typeflag: tar.TypeReg,
			Mode:     0644,
		}); err != nil {
			t.Fatal(err)
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gw.Close(); err != nil {
		t.Fatal(err)
	}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write(buf.Bytes())
	}))
	defer server.Close()

	err := downloadAndExtractTarball(context.Background(), server.URL, t.TempDir())
	if err == nil {
		t.Fatal("expected error for tar with too many entries")
	}
	if !strings.Contains(err.Error(), "too many entries") {
		t.Errorf("expected 'too many entries' error, got: %v", err)
	}
}
