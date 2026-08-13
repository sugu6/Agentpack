package skills

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestHashSkillMarkdown 锁定与市场侧一致的算法契约：
// 读取 SKILL.md，CRLF 归一化为 LF 后计算 SHA256
// （与 internal/market 的 populateContentHashes / fetchSkillMeta 完全一致）。
// 两侧算法不一致是已安装检测中内容指纹规则失效的根本原因，必须保持同步。
func TestHashSkillMarkdown(t *testing.T) {
	lf := "# Test Skill\n\nThis is a skill with\nmultiple lines.\n"

	t.Run("CRLF normalized equal to LF", func(t *testing.T) {
		dir := t.TempDir()
		if err := os.WriteFile(filepath.Join(dir, "SKILL.md"), []byte(strings.ReplaceAll(lf, "\n", "\r\n")), 0600); err != nil {
			t.Fatal(err)
		}
		got, ok := HashSkillMarkdown(dir)
		if !ok {
			t.Fatal("HashSkillMarkdown returned ok=false for existing SKILL.md")
		}
		sum := sha256.Sum256([]byte(lf))
		if want := hex.EncodeToString(sum[:]); got != want {
			t.Errorf("hash mismatch: got %s, want %s", got, want)
		}
	})

	t.Run("content change changes hash", func(t *testing.T) {
		dir := t.TempDir()
		if err := os.WriteFile(filepath.Join(dir, "SKILL.md"), []byte(lf), 0600); err != nil {
			t.Fatal(err)
		}
		h1, ok1 := HashSkillMarkdown(dir)
		if !ok1 {
			t.Fatal("HashSkillMarkdown returned ok=false for existing SKILL.md")
		}
		if err := os.WriteFile(filepath.Join(dir, "SKILL.md"), []byte(lf+"extra line\n"), 0600); err != nil {
			t.Fatal(err)
		}
		h2, ok2 := HashSkillMarkdown(dir)
		if !ok2 {
			t.Fatal("HashSkillMarkdown returned ok=false for existing SKILL.md")
		}
		if h1 == h2 {
			t.Error("expected different hashes after content change")
		}
	})

	t.Run("missing SKILL.md returns ok=false", func(t *testing.T) {
		if _, ok := HashSkillMarkdown(t.TempDir()); ok {
			t.Error("expected ok=false for dir without SKILL.md")
		}
	})
}
