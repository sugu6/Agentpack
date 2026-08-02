package skills

import (
	"bytes"
	"context"
	"crypto/sha1"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

// jsDelivrDataBase 是 jsDelivr 数据 API 的 base URL（可覆盖，便于测试）。
// data API 与内容 CDN 同族，国内可达性好于 GitHub 直连。
var jsDelivrDataBase = "https://data.jsdelivr.com"

// jsDelivrFileHosts 是内容 CDN 主机列表，按顺序 fallback。
// 实测 cdn / fastly / testingcf / gcore 四个域名当前均可达。
var jsDelivrFileHosts = []string{
	"https://cdn.jsdelivr.net",
	"https://fastly.jsdelivr.net",
	"https://testingcf.jsdelivr.net",
	"https://gcore.jsdelivr.net",
}

// gitHubAPIBases 是 GitHub Trees API 的候选 base URL。
// 直连优先（当前环境可达），gh-proxy 代理作为回退。
var gitHubAPIBases = []string{
	"https://api.github.com",
	"https://gh-proxy.com/https://api.github.com",
}

// gitHubRawProxies 是 raw.githubusercontent.com 的代理前缀（不含直连，
// 直连追加在列表最后）。按顺序尝试，网络错误才切换，4xx 立即失败。
var gitHubRawProxies = []string{
	"https://gh-proxy.com",
	"https://ghfast.top",
}

// gitHubRawDirect 是 raw.githubusercontent.com 直连 URL（可覆盖，便于测试）。
var gitHubRawDirect = "https://raw.githubusercontent.com"

// jsDelivrTimeout 是单个 HTTP 请求的超时（多域名顺序尝试，每个都短超时）。
const jsDelivrTimeout = 8 * time.Second

// httpStatusError 标识 HTTP 状态码错误，用于多源 fallback 时区分
// "资源确定不存在"（换域名无用）与"网络/服务端临时故障"（应尝试下一个源）。
type httpStatusError struct {
	status int
	url    string
}

func (e *httpStatusError) Error() string {
	return fmt.Sprintf("status %d (%s)", e.status, e.url)
}

// jsDelivrFileTree 对应 data API v1 的嵌套文件树节点。
type jsDelivrFileTree struct {
	Name  string             `json:"name"`
	Type  string             `json:"type"` // "file" | "directory"
	Hash  string             `json:"hash"` // 文件：base64 编码的 SHA-256；目录：空
	Files []jsDelivrFileTree `json:"files"`
}

// fetchRemoteFileTree 从 jsDelivr data API 获取仓库文件树（扁平化为 path → hex SHA-256）。
// 目录节点被展开，仅记录文件。
func fetchRemoteFileTree(ctx context.Context, owner, repo, branch string) (map[string]string, error) {
	if owner == "" || repo == "" {
		return nil, fmt.Errorf("owner/repo required")
	}
	if branch == "" {
		branch = "main"
	}
	u := fmt.Sprintf("%s/v1/packages/gh/%s/%s@%s",
		strings.TrimSuffix(jsDelivrDataBase, "/"),
		url.PathEscape(owner), url.PathEscape(repo), url.PathEscape(branch))
	data, err := httpGetBody(ctx, []string{u})
	if err != nil {
		return nil, fmt.Errorf("fetch jsdelivr file tree: %w", err)
	}
	var root jsDelivrFileTree
	if err := json.Unmarshal(data, &root); err != nil {
		return nil, fmt.Errorf("decode jsdelivr file tree: %w", err)
	}
	files := make(map[string]string)
	flattenTree(root.Files, "", files)
	if len(files) == 0 {
		return nil, fmt.Errorf("jsdelivr file tree is empty for %s/%s@%s", owner, repo, branch)
	}
	return files, nil
}

func flattenTree(nodes []jsDelivrFileTree, prefix string, out map[string]string) {
	for _, n := range nodes {
		path := n.Name
		if prefix != "" {
			path = prefix + "/" + n.Name
		}
		isDir := n.Type == "directory" || (n.Type == "" && len(n.Files) > 0)
		if isDir {
			flattenTree(n.Files, path, out)
			continue
		}
		if n.Type == "file" || n.Hash != "" {
			out[path] = decodeSHA256Hex(n.Hash)
		}
	}
}

// decodeSHA256Hex 将 jsDelivr 返回的 base64 SHA-256 转成 hex；
// 无法解码时原样返回（对比不一致会触发下载，由下载结果兜底）。
func decodeSHA256Hex(b64 string) string {
	if b64 == "" {
		return ""
	}
	raw, err := base64.StdEncoding.DecodeString(b64)
	if err != nil {
		if raw2, err2 := base64.RawStdEncoding.DecodeString(b64); err2 == nil {
			return hex.EncodeToString(raw2)
		}
		return b64
	}
	return hex.EncodeToString(raw)
}

// contentSHA256Hex 返回文件内容的 SHA-256（hex），与 jsDelivr 树一致。
func contentSHA256Hex(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

// gitBlobSHA1Hex 计算 Git blob 对象的 SHA-1（"blob <len>\0" + content），
// 与 GitHub Trees API 返回的 blob sha 一致，用于 GitHub 树源的内容对比。
func gitBlobSHA1Hex(data []byte) string {
	h := sha1.New()
	_, _ = fmt.Fprintf(h, "blob %d\x00", len(data))
	_, _ = h.Write(data)
	return hex.EncodeToString(h.Sum(nil))
}

// treeSource 标识远程文件树的来源。
type treeSource int

const (
	treeSourceJsDelivr treeSource = iota
	treeSourceGitHub
)

// remoteTree 是远程文件树（path → 文件内容 hash）及其来源。
// hashFn 按来源计算本地文件的同算法 hash，用于内容对比。
type remoteTree struct {
	files  map[string]string
	source treeSource
	hashFn func([]byte) string
}

// fetchRemoteTree 获取仓库文件树：优先 jsDelivr（国内可达、内容 SHA-256），
// 失败时回退 GitHub Trees API（实时、git blob SHA-1）。
func fetchRemoteTree(ctx context.Context, owner, repo, branch string) (remoteTree, error) {
	files, err := fetchRemoteFileTree(ctx, owner, repo, branch)
	if err == nil {
		return remoteTree{files: files, source: treeSourceJsDelivr, hashFn: contentSHA256Hex}, nil
	}
	ghFiles, gerr := fetchGitHubFileTree(ctx, owner, repo, branch)
	if gerr == nil {
		return remoteTree{files: ghFiles, source: treeSourceGitHub, hashFn: gitBlobSHA1Hex}, nil
	}
	return remoteTree{}, fmt.Errorf("jsdelivr: %v; github: %w", err, gerr)
}

// fetchGitHubFileTree 使用 GitHub Trees API（recursive）获取仓库文件树：
// path → git blob SHA-1。直连失败时尝试代理。
func fetchGitHubFileTree(ctx context.Context, owner, repo, branch string) (map[string]string, error) {
	if owner == "" || repo == "" {
		return nil, fmt.Errorf("owner/repo required")
	}
	if branch == "" {
		branch = "main"
	}
	rel := fmt.Sprintf("/repos/%s/%s/git/trees/%s?recursive=1",
		url.PathEscape(owner), url.PathEscape(repo), url.PathEscape(branch))
	urls := make([]string, 0, len(gitHubAPIBases))
	for _, base := range gitHubAPIBases {
		urls = append(urls, strings.TrimSuffix(base, "/")+rel)
	}
	data, err := httpGetBody(ctx, urls)
	if err != nil {
		return nil, fmt.Errorf("fetch github file tree: %w", err)
	}
	var resp struct {
		Truncated bool `json:"truncated"`
		Tree      []struct {
			Path string `json:"path"`
			Type string `json:"type"`
			SHA  string `json:"sha"`
		} `json:"tree"`
	}
	if err := json.Unmarshal(data, &resp); err != nil {
		return nil, fmt.Errorf("decode github file tree: %w", err)
	}
	if resp.Truncated {
		return nil, fmt.Errorf("github file tree truncated (repo too large)")
	}
	files := make(map[string]string)
	for _, item := range resp.Tree {
		if item.Type == "blob" {
			files[item.Path] = item.SHA
		}
	}
	if len(files) == 0 {
		return nil, fmt.Errorf("github file tree is empty for %s/%s@%s", owner, repo, branch)
	}
	return files, nil
}

// filterTreeByPrefix 返回技能目录（fullPath，如 "skills/pdf"；空=仓库根）下的文件，
// key 为相对该目录的路径。
func filterTreeByPrefix(tree map[string]string, fullPath string) map[string]string {
	prefix := strings.Trim(fullPath, "/")
	out := make(map[string]string)
	for p, h := range tree {
		if prefix == "" {
			out[p] = h
			continue
		}
		if p == prefix || !strings.HasPrefix(p, prefix+"/") {
			continue
		}
		out[strings.TrimPrefix(p, prefix+"/")] = h
	}
	return out
}

// resolveSkillDirInTree 在远程文件树中定位技能目录的完整路径（如 "skills/pdf"）。
// 优先约定路径 skills/{directory}，其次仓库根 {directory}，
// 最后兜底任意以 /{directory}/SKILL.md 结尾的路径。
// 返回空表示树中找不到该技能的 SKILL.md。
// 用于修复历史数据中 fullPath 缺失（空 fullPath 不能当作"整个仓库"）。
func resolveSkillDirInTree(tree map[string]string, directory string) string {
	if directory == "" {
		return ""
	}
	candidates := []string{
		"skills/" + directory,
		directory,
	}
	for _, c := range candidates {
		if _, ok := tree[c+"/SKILL.md"]; ok {
			return c
		}
	}
	// 兜底：任意子目录结构（如 docs/{directory}/SKILL.md）
	for p := range tree {
		if strings.HasSuffix(p, "/"+directory+"/SKILL.md") {
			return strings.TrimSuffix(p, "/SKILL.md")
		}
	}
	return ""
}

// skillRemoteDiffWith 对比远程文件树（过滤出 fullPath 目录）与本地技能目录。
// 返回相对技能目录的变化文件列表与是否有差异。本地多出的文件（用户自定义）
// 不视为差异，更新时也会保留。
func skillRemoteDiffWith(tree remoteTree, fullPath, localDir string) (changed []string, hasDiff bool) {
	remote := filterTreeByPrefix(tree.files, fullPath)
	if len(remote) == 0 {
		return nil, false
	}
	local := localDirFileHashes(localDir, tree.hashFn)
	for rel, rh := range remote {
		if lh, ok := local[rel]; !ok || lh != rh {
			changed = append(changed, rel)
			hasDiff = true
		}
	}
	sort.Strings(changed)
	return changed, hasDiff
}

// remoteTreeHash 计算技能目录远程文件的聚合 SHA-256（用于展示，路径+hash 有序拼接）。
func remoteTreeHash(tree remoteTree, fullPath string) string {
	remote := filterTreeByPrefix(tree.files, fullPath)
	paths := make([]string, 0, len(remote))
	for p := range remote {
		paths = append(paths, p)
	}
	sort.Strings(paths)
	h := sha256.New()
	for _, p := range paths {
		h.Write([]byte(p))
		h.Write([]byte{0})
		h.Write([]byte(remote[p]))
		h.Write([]byte{0})
	}
	return hex.EncodeToString(h.Sum(nil))
}

// localTreeHash 计算技能目录本地文件的聚合 SHA-256（与 remoteTreeHash 对称）。
func localTreeHash(hashFn func([]byte) string, localDir string) string {
	local := localDirFileHashes(localDir, hashFn)
	paths := make([]string, 0, len(local))
	for p := range local {
		paths = append(paths, p)
	}
	sort.Strings(paths)
	h := sha256.New()
	for _, p := range paths {
		h.Write([]byte(p))
		h.Write([]byte{0})
		h.Write([]byte(local[p]))
		h.Write([]byte{0})
	}
	return hex.EncodeToString(h.Sum(nil))
}

// localDirFileHashes 返回目录下所有文件的相对路径 → 内容 hash（按 hashFn 计算）。
func localDirFileHashes(dir string, hashFn func([]byte) string) map[string]string {
	out := make(map[string]string)
	_ = filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil
		}
		rel, rerr := filepath.Rel(dir, path)
		if rerr != nil {
			return nil
		}
		data, rerr := os.ReadFile(path)
		if rerr != nil {
			return nil
		}
		out[filepath.ToSlash(rel)] = hashFn(data)
		return nil
	})
	return out
}

// localHashEqual 判断本地文件内容是否与远程 hash 一致（按 hashFn 计算本地 hash）。
func localHashEqual(path, remoteHex string, hashFn func([]byte) string) bool {
	if remoteHex == "" {
		return false
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return false
	}
	return hashFn(data) == remoteHex
}

// verifyChangedFiles 下载候选差异文件并与本地字节逐一对比，返回真实差异文件。
// 实测 jsDelivr data API 的树 hash 与文件实际内容可能不一致（缓存/归一化差异），
// 因此检测阶段对候选差异做字节级验证，避免把"已是最新"误报为"有更新"。
func verifyChangedFiles(ctx context.Context, sk Skill, fullPath, branch, ssotPath string, tree remoteTree, changed []string) ([]string, error) {
	var realChanged []string
	for _, rel := range changed {
		remotePath := rel
		if prefix := strings.Trim(fullPath, "/"); prefix != "" {
			remotePath = prefix + "/" + rel
		}
		data, err := downloadRemoteFile(ctx, sk.RepoOwner, sk.RepoName, branch, remotePath,
			tree.source == treeSourceJsDelivr)
		if err != nil {
			return nil, fmt.Errorf("verify %s: %w", rel, err)
		}
		local, lerr := os.ReadFile(filepath.Join(ssotPath, filepath.FromSlash(rel)))
		if lerr != nil || !bytes.Equal(local, data) {
			realChanged = append(realChanged, rel)
		}
	}
	return realChanged, nil
}

// VerifySkillSource 验证 skills.sh 匹配到的仓库中是否存在与本地内容一致的技能。
// 先按目录名（skills/{dir}、{dir}）定位验证；名字不符时按内容扫描仓库中
// 所有含 SKILL.md 的目录——内容一致即可匹配（名字不同也能写入）。
// 返回定位到的 fullPath（可写入 lock）；仓库中无内容一致的技能时 ok=false。
func VerifySkillSource(ctx context.Context, dir, owner, repo, branch, localDir string) (fullPath string, ok bool, err error) {
	if dir == "" || owner == "" || repo == "" {
		return "", false, fmt.Errorf("dir/owner/repo required")
	}
	if branch == "" {
		branch = "main"
	}
	tree, err := fetchRemoteTree(ctx, owner, repo, branch)
	if err != nil {
		return "", false, fmt.Errorf("fetch remote tree: %w", err)
	}
	localSKILL, lerr := os.ReadFile(filepath.Join(localDir, "SKILL.md"))
	if lerr != nil {
		return "", false, fmt.Errorf("read local SKILL.md: %w", lerr)
	}

	// 1) 名字优先：skills/{dir} 或 {dir} 目录
	if fp := resolveSkillDirInTree(tree.files, dir); fp != "" {
		match, verr := remoteSkillMatches(ctx, tree, owner, repo, branch, fp, localSKILL)
		if verr != nil {
			return fp, false, verr
		}
		if match {
			return fp, true, nil
		}
	}

	// 2) 内容优先：扫描仓库中所有含 SKILL.md 的目录（名字不同但内容一致也匹配）
	scanCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	sem := make(chan struct{}, 3)
	var mu sync.Mutex
	var found string
	var firstErr error
	var wg sync.WaitGroup
	for _, fp := range skillDirsInTree(tree.files, 30) {
		fp := fp
		wg.Add(1)
		go func() {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			select {
			case <-scanCtx.Done():
				return
			default:
			}
			match, verr := remoteSkillMatches(scanCtx, tree, owner, repo, branch, fp, localSKILL)
			if verr != nil {
				mu.Lock()
				if firstErr == nil {
					firstErr = verr
				}
				mu.Unlock()
				return
			}
			if match {
				mu.Lock()
				if found == "" {
					found = fp
				}
				mu.Unlock()
				cancel()
			}
		}()
	}
	wg.Wait()
	if found != "" {
		return found, true, nil
	}
	if firstErr != nil {
		return "", false, firstErr
	}
	return "", false, nil
}

// remoteSkillMatches 下载远程技能目录的 SKILL.md 并与本地内容字节对比。
func remoteSkillMatches(ctx context.Context, tree remoteTree, owner, repo, branch, fullPath string, local []byte) (bool, error) {
	data, err := downloadRemoteFile(ctx, owner, repo, branch, fullPath+"/SKILL.md",
		tree.source == treeSourceJsDelivr)
	if err != nil {
		return false, err
	}
	return bytes.Equal(local, data), nil
}

// skillDirsInTree 返回树中所有含 SKILL.md 的目录路径（排序、去重、限量）。
func skillDirsInTree(tree map[string]string, max int) []string {
	seen := make(map[string]bool)
	var out []string
	for p := range tree {
		if strings.HasSuffix(p, "/SKILL.md") {
			dir := strings.TrimSuffix(p, "/SKILL.md")
			if !seen[dir] {
				seen[dir] = true
				out = append(out, dir)
			}
		}
	}
	sort.Strings(out)
	if len(out) > max {
		out = out[:max]
	}
	return out
}

// remoteFileURLs 生成同一文件在多个 jsDelivr CDN 主机上的 URL。
func remoteFileURLs(owner, repo, branch, relPath string) []string {
	suffix := fmt.Sprintf("/gh/%s/%s@%s/%s",
		url.PathEscape(owner), url.PathEscape(repo), url.PathEscape(branch),
		escapePathSegments(relPath))
	out := make([]string, 0, len(jsDelivrFileHosts))
	for _, host := range jsDelivrFileHosts {
		out = append(out, strings.TrimSuffix(host, "/")+suffix)
	}
	return out
}

func escapePathSegments(p string) string {
	segs := strings.Split(p, "/")
	for i := range segs {
		segs[i] = url.PathEscape(segs[i])
	}
	return strings.Join(segs, "/")
}

// downloadRemoteFile 按来源选择下载链路：
//   - jsDelivr 树：jsDelivr CDN 多域名 → raw/代理；
//   - GitHub 树：直接 raw/代理（jsDelivr 不可信，避免旧缓存 404 阻断 raw）。
func downloadRemoteFile(ctx context.Context, owner, repo, branch, relPath string, useJsDelivr bool) ([]byte, error) {
	var urls []string
	if useJsDelivr {
		urls = append(urls, remoteFileURLs(owner, repo, branch, relPath)...)
	}
	urls = append(urls, remoteRawURLs(owner, repo, branch, relPath)...)
	return httpGetBody(ctx, urls)
}

// remoteRawURLs 生成 raw.githubusercontent.com 的候选 URL：
// 直连优先（实测当前网络可用，且是 GitHub 权威源），代理作为回退。
func remoteRawURLs(owner, repo, branch, relPath string) []string {
	suffix := fmt.Sprintf("/%s/%s/%s/%s",
		url.PathEscape(owner), url.PathEscape(repo), url.PathEscape(branch),
		escapePathSegments(relPath))
	direct := strings.TrimSuffix(gitHubRawDirect, "/") + suffix
	out := make([]string, 0, 1+len(gitHubRawProxies))
	out = append(out, direct)
	for _, p := range gitHubRawProxies {
		out = append(out, strings.TrimSuffix(p, "/")+"/"+direct)
	}
	return out
}

// httpGetBody 顺序尝试多个 URL，首个成功返回响应体。
func httpGetBody(ctx context.Context, urls []string) ([]byte, error) {
	var lastErr error
	for _, u := range urls {
		body, err := httpGetBodyOne(ctx, u)
		if err == nil {
			return body, nil
		}
		lastErr = err
		// 4xx（除 429 限流、403 权限外）是确定性问题：资源不存在/已删除/大小受限，
		// 换域名结果相同，立即失败避免逐个域名空等。
		var se *httpStatusError
		if errors.As(err, &se) && se.status >= 400 && se.status < 500 &&
			se.status != http.StatusTooManyRequests && se.status != http.StatusForbidden {
			return nil, err
		}
		log.Printf("http get failed (%s): %v", u, err)
	}
	if lastErr == nil {
		lastErr = fmt.Errorf("no candidate URLs")
	}
	return nil, lastErr
}

func httpGetBodyOne(ctx context.Context, u string) ([]byte, error) {
	reqCtx, cancel := context.WithTimeout(ctx, jsDelivrTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(reqCtx, http.MethodGet, u, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "AgentPack/0.1 (+https://github.com/anomalyco/agentpack)")
	resp, err := tarballHTTPClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))
		return nil, &httpStatusError{status: resp.StatusCode, url: u}
	}
	// 限制响应大小（与 tarball 解压限制一致，防止异常响应撑爆内存）
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxTarballSize+1))
	if err != nil {
		return nil, err
	}
	if len(data) > maxTarballSize {
		return nil, fmt.Errorf("response exceeds %d bytes", maxTarballSize)
	}
	return data, nil
}

// mergeDirOverwrite 将 src 目录下所有文件复制到 dst（覆盖同名文件，保留 dst 中额外文件）。
func mergeDirOverwrite(src, dst string) error {
	return filepath.WalkDir(src, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, rerr := filepath.Rel(src, path)
		if rerr != nil {
			return rerr
		}
		target := filepath.Join(dst, rel)
		if d.IsDir() {
			return os.MkdirAll(target, 0755)
		}
		if err := os.MkdirAll(filepath.Dir(target), 0755); err != nil {
			return err
		}
		data, rerr := os.ReadFile(path)
		if rerr != nil {
			return rerr
		}
		return os.WriteFile(target, data, 0644)
	})
}
