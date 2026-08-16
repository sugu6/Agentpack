package skills

import (
	"agentpack/internal/agents"
	"agentpack/internal/config"
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/tls"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// TarballInstallInput 是 tarball 安装的输入参数
// 从 MarketSkill 转换而来，避免 skills 包依赖 market 包
type TarballInstallInput struct {
	TarballURL string // GitHub codeload tar.gz URL
	Directory  string // skill 目录名（= slug / 仓�库名 / SKILL.md 父目录的最后一段）
	FullPath   string // SKILL.md 所在目录的完整相对路径（如 "skills/pdf"，根目录为空）— 优先用于精准定位
	RepoOwner  string
	RepoName   string
	RepoBranch string
}

// maxTarballSize 限制 tarball 大小为 50MB，防止恶意大文件
const maxTarballSize = 50 * 1024 * 1024

// maxTarEntrySize 限制单个解压文件大小为 10MB，防止 zip bomb
const maxTarEntrySize = 10 * 1024 * 1024

// maxTarFileCount 限制 tarball 条目数，防止海量小文件耗尽磁盘/内存。
const maxTarFileCount = 5000

// maxTarTotalSize 限制解压后总大小（与下载大小上限一致），防 zip bomb。
const maxTarTotalSize = 50 * 1024 * 1024

// tarballHTTPClient 是 tarball 下载用的 HTTP client（独立于 market.HTTPClient，避免循环依赖）
var tarballHTTPClient = &http.Client{
	Timeout: 5 * time.Minute, // tarball 下载可能较大
	Transport: &http.Transport{
		MaxIdleConns:        8,
		MaxIdleConnsPerHost: 2,
		IdleConnTimeout:     30 * time.Second,
		// 禁用 HTTP/2，避免代理环境下空闲连接收到 400
		TLSNextProto: make(map[string]func(authority string, c *tls.Conn) http.RoundTripper),
	},
}

// InstallFromTarball 从 GitHub tarball URL 安装 skill 到 SSOT 并同步到 agents
// 流程：下载 → 解压到临时目录 → 识别 skill 根目录 → 调用 Import 纳管
func (s *Store) InstallFromTarball(ctx context.Context, input TarballInstallInput, agentIDs []string, reg *agents.Registry) (Skill, error) {
	if err := validateTarballInput(input); err != nil {
		return Skill{}, err
	}

	// 1. 创建临时目录
	tmpDir, err := os.MkdirTemp("", "skill-tarball-")
	if err != nil {
		return Skill{}, fmt.Errorf("create temp dir: %w", err)
	}
	defer os.RemoveAll(tmpDir)

	// 2. 下载并解压 tarball
	if err := downloadAndExtractTarball(ctx, input.TarballURL, tmpDir); err != nil {
		return Skill{}, fmt.Errorf("download/extract tarball: %w", err)
	}

	// 3. 识别 skill 根目录
	//    tarball 解压后通常有一个顶层目录 {repo}-{hash}/
	//    skill 可能在这个顶层目录的子目录中（input.FullPath 或 input.Directory 指定）
	skillRoot, err := findSkillRootInTarball(tmpDir, input.Directory, input.FullPath)
	if err != nil {
		return Skill{}, err
	}

	// 4. 调用现有 Import 纳管到 SSOT。
	// 显式传入 input.Directory：当仓库根目录本身就是 skill（SKILL.md 直接位于
	// {repo}-{hash}/ 下）时，skillRoot 的目录名是随机 hash，不能作为 SSOT 目录名。
	skill, err := s.ImportWithDirName(skillRoot, input.Directory, agentIDs, reg, input.RepoOwner, input.RepoName)
	if err != nil {
		return Skill{}, fmt.Errorf("import skill from tarball: %w", err)
	}

	return skill, nil
}

// validateTarballInput 校验 tarball 安装输入
func validateTarballInput(input TarballInstallInput) error {
	if input.TarballURL == "" {
		return fmt.Errorf("tarball URL is required")
	}
	if input.Directory == "" {
		return fmt.Errorf("directory is required")
	}
	// Directory 会被直接拼入 findSkillRootInTarball 的定位路径，必须通过
	// 目录名校验（与 zip/ImportWithDirName 一致）：含 /、\、.. 的恶意或
	// 异常目录名会让 HasSkillManifest 命中解压目录外的任意目录。
	if err := ValidateDirectoryName(input.Directory); err != nil {
		return fmt.Errorf("invalid directory %q: %w", input.Directory, err)
	}
	// 校验 URL 是 codeload.github.com
	if !strings.HasPrefix(input.TarballURL, "https://codeload.github.com/") {
		return fmt.Errorf("tarball URL must be from codeload.github.com")
	}
	if !isSafeGitHubIdent(input.RepoOwner) || !isSafeGitHubIdent(input.RepoName) {
		return fmt.Errorf("invalid repo owner/name")
	}
	// FullPath 由前端传入（安装时用于在 tarball 内精准定位 SKILL.md 目录），
	// 必须通过相对路径校验，防止 ".." 逃出解压临时目录后纳管磁盘上任意目录。
	if input.FullPath != "" {
		if _, err := safeRelPath(input.FullPath); err != nil {
			return fmt.Errorf("invalid skill full path %q: %w", input.FullPath, err)
		}
	}
	return nil
}

// isSafeGitHubIdent 复用 market 包的校验逻辑（这里独立定义避免循环依赖）
func isSafeGitHubIdent(s string) bool {
	if s == "" || len(s) > 100 {
		return false
	}
	for _, r := range s {
		if !((r >= 'a' && r <= 'z') ||
			(r >= 'A' && r <= 'Z') ||
			(r >= '0' && r <= '9') ||
			r == '.' || r == '_' || r == '-') {
			return false
		}
	}
	return true
}

// tarballFallbackURLs 是配置代理之外的备用代理前缀（可覆盖，便于测试隔离）。
// 条目为空字符串表示直连 codeload。
var tarballFallbackURLs = []string{"https://ghfast.top/", ""}

// tarballCandidateURLs 生成 tarball 下载候选 URL：
// 配置的代理优先，随后按 tarballFallbackURLs 补充备用代理，最后直连 codeload。
func tarballCandidateURLs(tarballURL string) []string {
	if !strings.HasPrefix(tarballURL, "https://codeload.github.com/") {
		return []string{tarballURL}
	}
	rest := strings.TrimPrefix(tarballURL, "https://")
	var urls []string
	if p := strings.TrimSpace(config.DefaultGitHubProxy); p != "" {
		urls = append(urls, strings.TrimSuffix(p, "/")+"/"+rest)
	}
	for _, p := range tarballFallbackURLs {
		if strings.TrimSpace(p) == "" {
			urls = append(urls, "https://"+rest) // 直连兜底
			continue
		}
		urls = append(urls, strings.TrimSuffix(p, "/")+"/"+rest)
	}
	return urls
}

// downloadAndExtractTarball 下载 tar.gz（多代理候选）并安全解压到目标目录
func downloadAndExtractTarball(ctx context.Context, tarballURL, dest string) error {
	data, err := httpGetBody(ctx, tarballCandidateURLs(tarballURL))
	if err != nil {
		return fmt.Errorf("download tarball: %w", err)
	}

	// gzip 解压
	gzReader, err := gzip.NewReader(bytes.NewReader(data))
	if err != nil {
		return fmt.Errorf("gzip reader: %w", err)
	}
	defer gzReader.Close()

	// tar 解压
	tarReader := tar.NewReader(gzReader)

	destAbs, err := filepath.Abs(dest)
	if err != nil {
		return fmt.Errorf("resolve dest abs: %w", err)
	}

	var fileCount int
	var totalSize int64
	for {
		header, err := tarReader.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return fmt.Errorf("tar read: %w", err)
		}
		// 所有条目都计入 fileCount 上限：仅对普通文件计数时，恶意 tar 可注入
		// 海量目录条目（每个都触发 MkdirAll 磁盘操作）绕过限制造成资源耗尽。
		fileCount++
		// 用实际写入的字节数累计总量：header.Size 是声明值，恶意 tar 可虚报
		// 绕过 maxTarTotalSize（zip bomb 防护）。
		written, err := extractTarEntry(tarReader, header, dest, destAbs)
		if err != nil {
			return err
		}
		totalSize += written
		if totalSize > maxTarTotalSize {
			return fmt.Errorf("tar total uncompressed size exceeds limit: %d bytes (max %d)", totalSize, maxTarTotalSize)
		}
		if fileCount > maxTarFileCount {
			return fmt.Errorf("tar contains too many entries: %d (max %d)", fileCount, maxTarFileCount)
		}
	}

	return nil
}

// extractTarEntry 安全解压单个 tar 条目，防止 Tar Slip（路径穿越）
// extractTarEntry 安全解压单个 tar 条目，防止 Tar Slip（路径穿越）。
// 返回实际写入的字节数（非普通文件条目返回 0），供调用方累计真实解压总量。
func extractTarEntry(reader *tar.Reader, header *tar.Header, dest, destAbs string) (int64, error) {
	// 清理路径，逐段拒绝 ".." 段（防 Tar Slip）。
	// 不用 Contains 子串判断，避免误拒 "foo..bar" 这类合法文件名。
	name := filepath.FromSlash(header.Name)
	for _, seg := range strings.Split(name, string(filepath.Separator)) {
		if seg == ".." {
			return 0, fmt.Errorf("tar entry contains path traversal: %s", header.Name)
		}
	}

	target := filepath.Join(dest, name)
	targetAbs, err := filepath.Abs(target)
	if err != nil {
		return 0, fmt.Errorf("resolve target abs: %w", err)
	}

	// 验证 target 在 dest 内（Tar Slip 防护）
	if !isWithinDir(targetAbs, destAbs) {
		return 0, fmt.Errorf("tar entry escapes dest dir: %s", header.Name)
	}

	switch header.Typeflag {
	case tar.TypeDir:
		return 0, os.MkdirAll(target, 0755)
	case tar.TypeReg:
		if err := os.MkdirAll(filepath.Dir(target), 0755); err != nil {
			return 0, fmt.Errorf("create parent dir: %w", err)
		}
		// 权限收敛：归档声明的权限可能带 0777（恶意/异常归档），若原样保留会
		// 传播到 SSOT 与各 agent 技能目录，本机其他用户可读写。去除 group/other
		// 写位（&^ 0022），保留 owner 完整权限与可执行位（技能脚本需要）。
		perm := header.FileInfo().Mode().Perm() &^ 0022
		w, err := os.OpenFile(target, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, perm)
		if err != nil {
			return 0, fmt.Errorf("create file %s: %w", target, err)
		}
		defer w.Close()
		// 限制单个文件大小，防止 zip bomb。
		// 先按 tar 头声明的 size 预检，再用 LimitReader+1 校验实际流，
		// 超过上限时报错而不是静默截断后安装损坏文件。
		if header.Size > maxTarEntrySize {
			return 0, fmt.Errorf("tar entry %s too large: %d bytes (max %d)", header.Name, header.Size, maxTarEntrySize)
		}
		written, err := io.Copy(w, io.LimitReader(reader, maxTarEntrySize+1))
		if err != nil {
			return 0, fmt.Errorf("write file %s: %w", target, err)
		}
		if written > maxTarEntrySize {
			return 0, fmt.Errorf("tar entry %s exceeds size limit: more than %d bytes", header.Name, maxTarEntrySize)
		}
		return written, nil
	case tar.TypeSymlink, tar.TypeLink:
		// 跳过符号链接和硬链接（防止指向 SSOT 外的敏感路径），不报错
		return 0, nil
	default:
		// 忽略其他类型（char device, block device 等）
		return 0, nil
	}
}

// findSkillRootInTarball 在 tarball 解压目录中查找含 SKILL.md 的 skill 根目录
// 优先用 fullPath 精准定位（如 "skills/pdf" → topDir/skills/pdf/）
// fullPath 为空时用 directory 名查找，最后回退到递归查找
func findSkillRootInTarball(dir, directory, fullPath string) (string, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return "", fmt.Errorf("read tarball root: %w", err)
	}

	// tarball 通常有一个顶层目录（如 {repo}-{hash}/）
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		topDir := filepath.Join(dir, entry.Name())

		// 情况 0（优先）：fullPath 非空时直接拼接完整路径
		// 如 fullPath="skills/pdf" → topDir/skills/pdf/SKILL.md
		if fullPath != "" {
			// 纵深防御：即使调用方未走 validateTarballInput，也拒绝穿越路径
			if _, err := safeRelPath(fullPath); err != nil {
				return "", fmt.Errorf("invalid full path %q: %w", fullPath, err)
			}
			targetSub := filepath.Join(topDir, filepath.FromSlash(fullPath))
			if HasSkillManifest(targetSub) {
				return targetSub, nil
			}
		}

		// 情况 1：顶层目录直接含 SKILL.md（repo 本身就是一个 skill）
		// GitHub codeload 顶层目录为 {repo}-{hash}，目录名需要兼容该后缀。
		if directory != "" && HasSkillManifest(topDir) &&
			(entry.Name() == directory || strings.HasPrefix(entry.Name(), directory+"-")) {
			return topDir, nil
		}

		// 情况 2：在顶层目录下查找匹配 directory 的子目录
		if directory != "" {
			targetSub := filepath.Join(topDir, directory)
			if HasSkillManifest(targetSub) {
				return targetSub, nil
			}
		}

		// 情况 3：递归查找含 SKILL.md 的子目录（最多 3 层）
		// 仅在没有 fullPath 或 fullPath 定位失败时使用。
		// 当 directory 非空时，按目录名过滤，避免多 skill 仓库中误匹配到其他 skill。
		if found, err := findSkillManifestRecursive(topDir, 3, directory); err == nil && found != "" {
			return found, nil
		}
	}

	// 情况 4：根目录直接含 SKILL.md
	if HasSkillManifest(dir) {
		return dir, nil
	}

	return "", fmt.Errorf("no SKILL.md found in tarball for directory %q (fullPath %q)", directory, fullPath)
}

// findSkillManifestRecursive 递归查找含 SKILL.md 的子目录（限制深度）。
// targetName 非空时，仅返回目录名匹配的 skill（用于多 skill 仓库中定位特定 skill）。
func findSkillManifestRecursive(dir string, maxDepth int, targetName string) (string, error) {
	if maxDepth <= 0 {
		return "", fmt.Errorf("max depth reached")
	}

	entries, err := os.ReadDir(dir)
	if err != nil {
		return "", err
	}

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
			// targetName 为空时接受任意含 SKILL.md 的目录；
			// 非空时仅接受目录名匹配的（避免多 skill 仓库误匹配）
			if targetName == "" || name == targetName {
				return sub, nil
			}
			// 当前目录名不匹配，不再深入（skill 目录下不会再嵌套同名 skill）
			continue
		}
		// 递归
		if found, err := findSkillManifestRecursive(sub, maxDepth-1, targetName); err == nil && found != "" {
			return found, nil
		}
	}

	return "", fmt.Errorf("no SKILL.md found")
}
