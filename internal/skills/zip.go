package skills

import (
	"archive/zip"
	"agentpack/internal/agents"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

// maxZipEntrySize 限制单个 zip 条目解压大小为 10MB，防止 zip bomb
const maxZipEntrySize = 10 * 1024 * 1024

// maxZipTotalSize 限制 zip 总解压大小为 50MB，与 tarball 限制一致
const maxZipTotalSize = 50 * 1024 * 1024

// maxZipFileCount 限制 zip 条目数量，防止通过海量小条目耗尽资源
const maxZipFileCount = 1000

// InstallFromZip 从 zip 文件安装 skill 到 SSOT 并同步到指定 agent 目录。
// zip 文件结构支持两种形式：
//   - 根目录直接包含 SKILL.md
//   - 包含一个子目录，子目录内有 SKILL.md
//
// 解压到临时目录后调用 Import 完成纳管，最后清理临时文件。
func (s *Store) InstallFromZip(zipPath string, agentIDs []string, reg *agents.Registry) (Skill, error) {
	// 1. 解压到临时目录
	tmpDir, err := os.MkdirTemp("", "skill-zip-")
	if err != nil {
		return Skill{}, fmt.Errorf("create temp dir: %w", err)
	}
	defer os.RemoveAll(tmpDir)

	if err := extractZip(zipPath, tmpDir); err != nil {
		return Skill{}, fmt.Errorf("extract zip: %w", err)
	}

	// 2. 识别 skill 根目录
	skillRoot, err := findSkillRoot(tmpDir)
	if err != nil {
		return Skill{}, err
	}

	// 3. 调用 Import 纳管（zip 来源无仓库信息）
	return s.Import(skillRoot, agentIDs, reg, "", "")
}

// extractZip 安全解压 zip 文件到目标目录，防止 Zip Slip（路径穿越）和 zip bomb。
func extractZip(zipPath, dest string) error {
	r, err := zip.OpenReader(zipPath)
	if err != nil {
		return fmt.Errorf("open zip: %w", err)
	}
	defer r.Close()

	destAbs, err := filepath.Abs(dest)
	if err != nil {
		return fmt.Errorf("resolve dest abs: %w", err)
	}

	// 防止 zip bomb：限制条目数量
	if len(r.File) > maxZipFileCount {
		return fmt.Errorf("zip contains too many entries: %d (max %d)", len(r.File), maxZipFileCount)
	}

	var totalSize uint64
	for _, f := range r.File {
		// 防止 zip bomb：拒绝解压前已知过大的单条目
		if f.UncompressedSize64 > maxZipEntrySize {
			return fmt.Errorf("zip entry %s too large: %d bytes (max %d)", f.Name, f.UncompressedSize64, maxZipEntrySize)
		}
		totalSize += f.UncompressedSize64
		if totalSize > maxZipTotalSize {
			return fmt.Errorf("zip total uncompressed size exceeds limit: %d bytes (max %d)", totalSize, maxZipTotalSize)
		}
		if err := extractZipEntry(f, dest, destAbs); err != nil {
			return err
		}
	}
	return nil
}

func extractZipEntry(f *zip.File, dest, destAbs string) error {
	// 清理路径并验证不包含 ..（防止 Zip Slip）
	name := filepath.FromSlash(f.Name)
	if strings.Contains(name, "..") {
		return fmt.Errorf("zip entry contains path traversal: %s", f.Name)
	}

	target := filepath.Join(dest, name)
	targetAbs, err := filepath.Abs(target)
	if err != nil {
		return fmt.Errorf("resolve target abs: %w", err)
	}
	if !isWithinDir(targetAbs, destAbs) {
		return fmt.Errorf("zip entry escapes dest dir: %s", f.Name)
	}

	if f.FileInfo().IsDir() {
		return os.MkdirAll(target, 0755)
	}

	if err := os.MkdirAll(filepath.Dir(target), 0755); err != nil {
		return fmt.Errorf("create parent dir: %w", err)
	}

	rc, err := f.Open()
	if err != nil {
		return fmt.Errorf("open zip entry %s: %w", f.Name, err)
	}
	defer rc.Close()

	w, err := os.OpenFile(target, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0644)
	if err != nil {
		return fmt.Errorf("create file %s: %w", target, err)
	}
	defer w.Close()

	// 纵深防御：即使 UncompressedSize64 被篡改，LimitReader 也会阻止超量写入
	if _, err := io.Copy(w, io.LimitReader(rc, maxZipEntrySize+1)); err != nil {
		return fmt.Errorf("write file %s: %w", target, err)
	}
	return nil
}

// isWithinDir 检查 target 是否在 base 目录内（含 base 本身）。
func isWithinDir(target, base string) bool {
	rel, err := filepath.Rel(base, target)
	if err != nil {
		return false
	}
	if rel == "." {
		return true
	}
	return !strings.HasPrefix(rel, "..") && !strings.Contains(rel, string(filepath.Separator)+"..")
}

// findSkillRoot 在解压目录中查找含 SKILL.md 的 skill 根目录。
// 支持：根目录直接含 SKILL.md，或只有一个子目录含 SKILL.md。
func findSkillRoot(dir string) (string, error) {
	// 情况 1：根目录直接含 SKILL.md
	if HasSkillManifest(dir) {
		return dir, nil
	}

	// 情况 2：查找含 SKILL.md 的子目录
	entries, err := os.ReadDir(dir)
	if err != nil {
		return "", fmt.Errorf("read extracted dir: %w", err)
	}

	var candidates []string
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		name := entry.Name()
		if strings.HasPrefix(name, ".") {
			continue
		}
		sub := filepath.Join(dir, name)
		if HasSkillManifest(sub) {
			candidates = append(candidates, sub)
		}
	}

	if len(candidates) == 0 {
		return "", fmt.Errorf("no SKILL.md found in zip")
	}
	if len(candidates) > 1 {
		return "", fmt.Errorf("multiple skills found in zip, expected exactly one")
	}
	return candidates[0], nil
}
