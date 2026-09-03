package skills

import (
	"agentpack/internal/appmeta"
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

// jsDelivrDataBase 是 jsDelivr 数据 API 的主 base URL（可覆盖，便于测试）。
// data API 与内容 CDN 同族，国内可达性好于 GitHub 直连。
var jsDelivrDataBase = "https://data.jsdelivr.com"

// jsDelivrDataFallbackBases 是数据 API 的镜像回退 base：主域名按仓库封锁
// （对部分仓库固定 403）或整域不可达时按序尝试（格式完全兼容）。
var jsDelivrDataFallbackBases = []string{"https://data.jsdmirror.com"}

// jsDelivrFileHosts 是内容 CDN 主机列表，按顺序 fallback。
// 实测 cdn / fastly / testingcf / gcore 四个官方域名当前均可达
// （cdn/fastly 返回 301 重写到带版本号 URL，Go http.Client 默认跟随）；
// jsdmirror、onmicrosoft 为社区镜像（URL 格式兼容），官方域名被
// 限流/阻断时回退。
var jsDelivrFileHosts = []string{
	"https://cdn.jsdelivr.net",
	"https://fastly.jsdelivr.net",
	"https://testingcf.jsdelivr.net",
	"https://gcore.jsdelivr.net",
	"https://cdn.jsdmirror.com",
	"https://jsd.onmicrosoft.cn",
}

// gitHubAPIBases 是 GitHub Trees API 的候选 base URL。
// 直连优先（当前环境可达），gh-proxy 代理作为回退；
// hub.gitmirror 是 GitHub API 代理镜像，DNS 污染环境下快速失败，
// 可达的网络中作为最后的 trees API 回退。
var gitHubAPIBases = []string{
	"https://api.github.com",
	"https://gh-proxy.com/https://api.github.com",
	"https://hub.gitmirror.com/https://api.github.com",
}

// gitHubRawProxies 是 raw.githubusercontent.com 的代理前缀（不含直连，
// 直连追加在列表最后）。按顺序尝试，网络错误才切换，4xx 立即失败。
// gh-proxy / ghfast / ghproxy.net 实测对 raw 可达（注意：这类代理对
// api.github.com 路径普遍返回 403，仅放行 raw/codeload）。
var gitHubRawProxies = []string{
	"https://gh-proxy.com",
	"https://ghfast.top",
	"https://ghproxy.net",
}

// gitHubRawMirrors 是 raw.githubusercontent.com 的同格式镜像
// （{base}/{owner}/{repo}/{branch}/{path}，直接替换 base 而非代理完整 URL）。
// 追加在代理之后作为最后回退：DNS 污染环境下秒级失败，几乎不增加延迟。
var gitHubRawMirrors = []string{"https://raw.gitmirror.com"}

// gitHubRawDirect 是 raw.githubusercontent.com 直连 URL（可覆盖，便于测试）。
var gitHubRawDirect = "https://raw.githubusercontent.com"

// jsDelivrTimeout 是单个 HTTP 请求的超时（多域名顺序尝试，每个都短超时）。
const jsDelivrTimeout = 8 * time.Second

// gitHubDirectTimeout 是 GitHub 直连（api.github.com / raw.githubusercontent.com）
// 请求的超时：部分网络对 GitHub 直连限速，实测 trees API 响应可达 8-10s，
// 与 CDN 相同的 8s 短超时会在边界上反复失败（表现为 context deadline
// exceeded）。代理与镜像仍用短超时，失败后快速切换下一个候选。
const gitHubDirectTimeout = 15 * time.Second

// requestTimeoutForURL 按目标域名选择单请求超时。
func requestTimeoutForURL(u string) time.Duration {
	if strings.HasPrefix(u, "https://api.github.com/") ||
		strings.HasPrefix(u, "https://raw.githubusercontent.com/") {
		return gitHubDirectTimeout
	}
	return jsDelivrTimeout
}

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
	path := fmt.Sprintf("/v1/packages/gh/%s/%s@%s",
		url.PathEscape(owner), url.PathEscape(repo), url.PathEscape(branch))
	// 主 data API 按仓库封锁（403）或整域不可达时回退镜像（格式兼容）
	urls := make([]string, 0, 1+len(jsDelivrDataFallbackBases))
	urls = append(urls, strings.TrimSuffix(jsDelivrDataBase, "/")+path)
	for _, b := range jsDelivrDataFallbackBases {
		urls = append(urls, strings.TrimSuffix(b, "/")+path)
	}
	data, _, err := httpGetBody(ctx, urls)
	if err != nil {
		return nil, fmt.Errorf("fetch jsdelivr file tree: %w", err)
	}
	var root struct {
		jsDelivrFileTree
		// Truncated 是 jsDelivr data API 的顶层响应字段（>3000 文件时置位）：
		// 截断树若被当作完整树，CheckUpdates 内容级检测会假阴性"无更新"，
		// 且空集 TreeHash 会被缓存固化，必须显式拒绝。
		Truncated bool `json:"truncated"`
	}
	if err := json.Unmarshal(data, &root); err != nil {
		return nil, fmt.Errorf("decode jsdelivr file tree: %w", err)
	}
	if root.Truncated {
		return nil, fmt.Errorf("jsdelivr file tree truncated for %s/%s@%s", owner, repo, branch)
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
	data, _, err := httpGetBody(ctx, urls)
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

// normalizeCRLF 将 CRLF 行尾归一化为 LF。git 存储为 LF，jsDelivr SHA-256
// 与 GitHub blob SHA-1 均基于 git 字节；Windows 上编辑器可能把文件转为
// CRLF，直接按原始字节比对会永久误报"有更新"。无 \r\n 时原样返回。
func normalizeCRLF(data []byte) []byte {
	if !bytes.Contains(data, []byte("\r\n")) {
		return data
	}
	return bytes.ReplaceAll(data, []byte("\r\n"), []byte("\n"))
}

// fileHashForTree 计算用于聚合树 hash 的文件 hash：含 CRLF 行尾时按 LF
// 归一化，与远程树 hash（基于 git 字节）对齐，避免本地 CRLF 文件被聚合
// hash 恒判为差异。二进制文件按原始字节计算（无 \r\n 不受影响）。
func fileHashForTree(data []byte, hashFn func([]byte) string) string {
	if bytes.Contains(data, []byte("\r\n")) {
		return hashFn(normalizeCRLF(data))
	}
	return hashFn(data)
}

// fileHashMatches 判断文件内容是否与远程 hash 一致：优先按原始字节比对，
// 仅当本地含 CRLF 行尾时再按 LF 归一化尝试（避免误伤含 \r\n 序列的二进制）。
func fileHashMatches(data []byte, remoteHex string, hashFn func([]byte) string) bool {
	if hashFn(data) == remoteHex {
		return true
	}
	if bytes.Contains(data, []byte("\r\n")) {
		return hashFn(normalizeCRLF(data)) == remoteHex
	}
	return false
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
		out[filepath.ToSlash(rel)] = fileHashForTree(data, hashFn)
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
	return fileHashMatches(data, remoteHex, hashFn)
}

// verifyChangedFiles 下载候选差异文件并与本地字节逐一对比，返回真实差异文件。
// 实测 jsDelivr data API 的树 hash 与文件实际内容可能不一致（缓存/归一化差异），
// 因此检测阶段对候选差异做字节级验证，避免把"已是最新"误报为"有更新"。
func verifyChangedFiles(ctx context.Context, sk Skill, fullPath, branch, ssotPath string, tree remoteTree, changed []string) ([]string, error) {
	var realChanged []string
	for _, rel := range changed {
		// 与 updateSkillViaTree 一致：远程路径必须通过校验才能拼入本地路径
		safeRel, err := safeRelPath(rel)
		if err != nil {
			return nil, fmt.Errorf("verify %s: %w", rel, err)
		}
		rel = safeRel
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
		if lerr != nil || !bytes.Equal(normalizeCRLF(local), data) {
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
		// 文件树接口（jsDelivr data API + GitHub Trees API）全线不可达/限流时，
		// 降级为直接用内容 CDN/raw 下载约定路径的 SKILL.md 字节对比。文件树
		// 接口是回填瓶颈：无第三方 CDN 提供文件树 JSON，data API 与 GitHub
		// API 双双 403/超时时 fetchRemoteTree 必失败；但单文件下载链路
		//（jsDelivr CDN 多主机 + raw 多代理/镜像）独立且更可达，名字优先路径
		// 只需下载 SKILL.md 即可判定。降级保证"全线故障"时名字优先的回填仍能
		// 成功，而非直接判定网络失败放弃关联。
		return verifySkillSourceByNameFallback(ctx, owner, repo, branch, dir, localDir, err)
	}
	// 1) 名字优先：skills/{dir} 或 {dir} 目录
	if fp := resolveSkillDirInTree(tree.files, dir); fp != "" {
		localSKILL, lerr := os.ReadFile(filepath.Join(localDir, "SKILL.md"))
		if lerr != nil {
			return "", false, fmt.Errorf("read local SKILL.md: %w", lerr)
		}
		match, verr := remoteSkillMatches(ctx, tree, owner, repo, branch, fp, localSKILL)
		if verr != nil {
			return fp, false, verr
		}
		if match {
			return fp, true, nil
		}
	}

	// 2) 内容优先：扫描仓库中所有含 SKILL.md 的目录（名字不同但内容一致也匹配）
	fp, truncated, serr := findSkillDirByContent(ctx, tree, owner, repo, branch, localDir)
	if serr != nil {
		return "", false, serr
	}
	if fp != "" {
		return fp, true, nil
	}
	if truncated {
		// 候选被截断：tarball 全量扫描无截断限制，可给出确定结论；tarball
		// 也不可用时保守报错（不能断言"无匹配"，避免来源关联被放弃）。
		if fp, ok, terr := verifySkillSourceByTarball(ctx, owner, repo, branch, dir, localDir); terr == nil {
			return fp, ok, nil
		}
		return "", false, fmt.Errorf("skill dir scan truncated (too many candidate directories)")
	}
	return "", false, nil
}

// verifySkillSourceByNameFallback 在文件树接口（jsDelivr data API + GitHub
// Trees API）全线不可达/限流时降级验证：直接用内容 CDN/raw 下载约定路径的
// SKILL.md 与本地字节对比，无需文件树。
//
// 文件树接口是回填瓶颈——无第三方 CDN 提供文件树 JSON，data API 与 GitHub
// API 双双 403/超时时 fetchRemoteTree 必失败，原实现直接报错会让名字优先的
// 回填也连带失败。但单文件下载链路（jsDelivr CDN 多主机 + raw 多代理/镜像）
// 独立于文件树接口且更可达（实测 raw.githubusercontent.com 直连最快），名字
// 优先路径只需下载 SKILL.md 即可判定。降级保证"全线故障"时仍能匹配。
//
// 返回语义与 VerifySkillSource 一致：匹配返回 (fullPath, true, nil)；下载到
// 约定路径 SKILL.md 但内容均不一致返回 ("", false, nil)（内容不符，调用方据
// 此统计 mismatched 并继续下一个候选仓库）；约定路径全部不可下载（404/网络
// 失败，无法判定仓库是否含此技能）返回 treeErr（调用方统计 failed，下次网络
// 恢复后用文件树重新完整验证）。
func verifySkillSourceByNameFallback(ctx context.Context, owner, repo, branch, dir, localDir string, treeErr error) (string, bool, error) {
	log.Printf("backfill: %s <- %s/%s@%s: tree fetch failed, falling back to raw name-based download: %v", dir, owner, repo, branch, treeErr)
	localSKILL, lerr := os.ReadFile(filepath.Join(localDir, "SKILL.md"))
	if lerr != nil {
		return "", false, fmt.Errorf("read local SKILL.md: %w", lerr)
	}
	// 约定路径候选，与 resolveSkillDirInTree 同序：skills/{dir}、{dir}、根级
	candidates := []string{"skills/" + dir, dir, ""}
	downloaded := false // 是否成功下载过 SKILL.md（说明网络可达）
	for _, fp := range candidates {
		rel := "SKILL.md"
		if fp != "" {
			rel = fp + "/SKILL.md"
		}
		data, derr := downloadRemoteFile(ctx, owner, repo, branch, rel, true)
		if derr != nil {
			// 该约定路径不存在（404）或不可达：试下一个约定路径
			continue
		}
		downloaded = true
		if bytes.Equal(normalizeCRLF(localSKILL), data) {
			return fp, true, nil
		}
	}
	if downloaded {
		// 网络可达且下载到了约定路径的 SKILL.md，但内容均不一致：对这些路径
		// 而言内容不符是确定的，返回无匹配（不算网络失败），调用方统计为
		// mismatched 并继续下一个候选仓库。
		return "", false, nil
	}
	// 终极回退：codeload tarball。实测 gh-proxy/ghfast 等代理仅放行
	// raw/codeload，树接口与单文件链路全部停滞的窗口内 tarball 是唯一
	// 可达的完整内容源（tarball 不可用时保留原 treeErr）。
	if fp, ok, terr := verifySkillSourceByTarball(ctx, owner, repo, branch, dir, localDir); terr == nil {
		if ok {
			log.Printf("backfill: %s <- %s/%s@%s: matched via tarball fallback at %s", dir, owner, repo, branch, fp)
		}
		return fp, ok, nil
	} else {
		log.Printf("backfill: %s <- %s/%s@%s: tarball fallback unavailable: %v", dir, owner, repo, branch, terr)
	}
	// 约定路径全部不可下载（404/网络失败）：无法判定仓库是否含此技能
	//（可能用了非约定路径，仅文件树内容扫描能匹配）。保守返回网络错误，
	// 调用方统计为 failed，下次网络恢复后用文件树重新完整验证。
	return "", false, treeErr
}

// verifySkillSourceByTarball 通过 codeload tarball 验证回填候选：
// 下载（代理链可达，见 tarballCandidateURLs）→ 安全解压到临时目录 →
// 在仓库相对路径下定位 SKILL.md 与本地字节对比。
// tarball 自带全部文件，本地扫描无文件树接口 30 目录截断的限制，
// 结论是确定的（匹配 / 确定无匹配）。
// 返回语义与 VerifySkillSource 一致：匹配返回 (fullPath, true, nil)；
// tarball 下载/解压成功但无内容一致的 SKILL.md 返回 ("", false, nil)；
// tarball 本身不可得返回错误（调用方决定降级路径）。
func verifySkillSourceByTarball(ctx context.Context, owner, repo, branch, dir, localDir string) (string, bool, error) {
	if !isSafeGitHubIdent(owner) || !isSafeGitHubIdent(repo) || branch == "" {
		return "", false, fmt.Errorf("invalid owner/repo/branch: %q", branch)
	}
	// Git 分支名允许含 ".." 子串（如 fix/..bar），url.PathEscape 已对 / 编码，
	// 此处仅防御 ".." 作为路径段穿透（如 "foo/../bar"）。
	for _, seg := range strings.Split(branch, "/") {
		if seg == ".." {
			return "", false, fmt.Errorf("invalid branch %q: path segment '..' not allowed", branch)
		}
	}
	localSKILL, err := os.ReadFile(filepath.Join(localDir, "SKILL.md"))
	if err != nil {
		return "", false, fmt.Errorf("read local SKILL.md: %w", err)
	}
	tarballURL := fmt.Sprintf("https://codeload.github.com/%s/%s/tar.gz/refs/heads/%s",
		owner, repo, url.PathEscape(branch))
	dest, err := os.MkdirTemp("", "skill-verify-*")
	if err != nil {
		return "", false, err
	}
	defer os.RemoveAll(dest)
	if err := downloadAndExtractTarball(ctx, tarballURL, dest); err != nil {
		return "", false, fmt.Errorf("download tarball: %w", err)
	}
	// 收集 tarball 内全部 SKILL.md；剥离顶层 {repo}-{sha} 目录得到仓库相对路径
	type cand struct{ repoRel, abs string }
	var cands []cand
	err = filepath.WalkDir(dest, func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || d.Name() != "SKILL.md" {
			return nil
		}
		rel, rerr := filepath.Rel(dest, path)
		if rerr != nil {
			return nil
		}
		rel = filepath.ToSlash(rel)
		if i := strings.Index(rel, "/"); i >= 0 {
			rel = rel[i+1:]
		} else {
			rel = ""
		}
		cands = append(cands, cand{repoRel: strings.TrimSuffix(rel, "/SKILL.md"), abs: path})
		return nil
	})
	if err != nil {
		return "", false, err
	}
	const maxTarballScan = 200 // 防御性上限，正常仓库远小于此
	if len(cands) > maxTarballScan {
		cands = cands[:maxTarballScan]
	}
	// 约定路径优先（与 resolveSkillDirInTree 的名字优先语义一致），其余按序
	prefer := func(repoRel string) int {
		switch repoRel {
		case "skills/" + dir:
			return 0
		case dir:
			return 1
		}
		return 2
	}
	sort.SliceStable(cands, func(i, j int) bool { return prefer(cands[i].repoRel) < prefer(cands[j].repoRel) })
	for _, c := range cands {
		data, rerr := os.ReadFile(c.abs)
		if rerr != nil {
			continue
		}
		if bytes.Equal(normalizeCRLF(localSKILL), data) {
			return c.repoRel, true, nil
		}
	}
	return "", false, nil
}

// findSkillDirByContent 在远程树中扫描含 SKILL.md 的目录，下载并与本地
// SKILL.md 字节对比，返回内容一致的首个目录路径（目录名可与本地不同）。
// 树中无内容一致的技能时返回空串。truncated 表示候选被限量截断，
// 空结果可能是截断所致而非真正无匹配，调用方应保守处理。
func findSkillDirByContent(ctx context.Context, tree remoteTree, owner, repo, branch, localDir string) (string, bool, error) {
	localSKILL, lerr := os.ReadFile(filepath.Join(localDir, "SKILL.md"))
	if lerr != nil {
		return "", false, lerr
	}
	scanCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	sem := make(chan struct{}, 3)
	var mu sync.Mutex
	var found string
	var firstErr error
	var wg sync.WaitGroup
	dirs, truncated := skillDirsInTree(tree.files, 30)
	for _, fp := range dirs {
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
		return found, truncated, nil
	}
	if firstErr != nil {
		return "", truncated, firstErr
	}
	return "", truncated, nil
}

// remoteSkillMatches 下载远程技能目录的 SKILL.md 并与本地内容字节对比。
// 本地文件可能被 Windows 编辑器转为 CRLF，git 存储为 LF，对比前归一化，
// 避免内容一致却判不匹配（导致回填/定位失败或误删来源关联）。
func remoteSkillMatches(ctx context.Context, tree remoteTree, owner, repo, branch, fullPath string, local []byte) (bool, error) {
	rel := fullPath
	if rel != "" {
		rel += "/"
	}
	rel += "SKILL.md"
	data, err := downloadRemoteFile(ctx, owner, repo, branch, rel,
		tree.source == treeSourceJsDelivr)
	if err != nil {
		return false, err
	}
	return bytes.Equal(normalizeCRLF(local), data), nil
}

// skillDirsInTree 返回树中所有含 SKILL.md 的目录路径（排序、去重、限量）。
// 仓库根级 SKILL.md（repo 本身就是技能）以空串表示，必须作为候选，
// 否则根级技能在内容扫描中永远匹配不到，被误判"来源无效"并删除关联。
// truncated 表示候选数超过 max 被截断：内容扫描可能漏检排序靠后的目录，
// 调用方应对"未找到"做保守处理（跳过而非破坏性删除）。
func skillDirsInTree(tree map[string]string, max int) ([]string, bool) {
	seen := make(map[string]bool)
	var out []string
	if _, ok := tree["SKILL.md"]; ok {
		out = append(out, "")
	}
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
		return out, true
	}
	return out, false
}

// 链路级"上次成功优先"状态。实测各 CDN/GitHub 源的可达性随时间波动
// （同一域名时而 2s 可达时而整域超时），且官方 jsDelivr 域名整体被阻断时
// 每域 8s 超时、raw 直连 15s 超时，串行等待使单文件下载耗时 30s+，
// 120s 回填预算内几乎无法完成任何内容验证。记住上次成功的候选并优先
// 尝试，让波动窗口内的后续请求直达当前可用源；无记录时保持原顺序，行为不变。
var (
	urlOrderMu   sync.Mutex
	prefJsDelivr string // 上次成功的内容 CDN 完整 URL
	prefRaw      string // 上次成功的 raw 链路完整 URL（直连/代理/镜像）
)

// reorderPreferred 把 pref 记录的获胜 URL 提到最前，其余保持原顺序；
// pref 为空或不在列表中（列表被测试覆盖/域名下线）时原样返回。
func reorderPreferred(urls []string, pref *string) []string {
	if len(urls) == 0 {
		return urls
	}
	urlOrderMu.Lock()
	p := *pref
	urlOrderMu.Unlock()
	if p == "" {
		return urls
	}
	out := make([]string, 0, len(urls))
	found := false
	for _, u := range urls {
		if u == p {
			out = append(out, u)
			found = true
		}
	}
	if !found {
		return urls
	}
	for _, u := range urls {
		if u != p {
			out = append(out, u)
		}
	}
	return out
}

// recordPreferred 在 winner 确实属于本次候选列表时更新 pref
// （winner 为空或来自其他链路时不记录）。
func recordPreferred(urls []string, winner string, pref *string) {
	if winner == "" {
		return
	}
	urlOrderMu.Lock()
	defer urlOrderMu.Unlock()
	for _, u := range urls {
		if u == winner {
			*pref = winner
			return
		}
	}
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
//
// 两条链路各自按"上次成功优先"排序（见 urlOrderMu），波动网络下避免
// 每个文件都重新付一遍全链路串行超时。
func downloadRemoteFile(ctx context.Context, owner, repo, branch, relPath string, useJsDelivr bool) ([]byte, error) {
	var jsURLs []string
	if useJsDelivr {
		jsURLs = remoteFileURLs(owner, repo, branch, relPath)
	}
	rawURLs := remoteRawURLs(owner, repo, branch, relPath)
	urls := append(reorderPreferred(jsURLs, &prefJsDelivr), reorderPreferred(rawURLs, &prefRaw)...)
	data, winner, err := httpGetBody(ctx, urls)
	if err == nil {
		// winner 只属于成功的那条链路；recordPreferred 在 winner 不在
		// 对应列表时直接返回，另一条链路的调用天然无副作用。
		recordPreferred(jsURLs, winner, &prefJsDelivr)
		recordPreferred(rawURLs, winner, &prefRaw)
	}
	return data, err
}

// remoteRawURLs 生成 raw.githubusercontent.com 的候选 URL：
// 直连优先（实测当前网络可用，且是 GitHub 权威源），代理作为回退，
// 同格式镜像（直接替换 base）追加在最后。
func remoteRawURLs(owner, repo, branch, relPath string) []string {
	suffix := fmt.Sprintf("/%s/%s/%s/%s",
		url.PathEscape(owner), url.PathEscape(repo), url.PathEscape(branch),
		escapePathSegments(relPath))
	direct := strings.TrimSuffix(gitHubRawDirect, "/") + suffix
	out := make([]string, 0, 1+len(gitHubRawProxies)+len(gitHubRawMirrors))
	out = append(out, direct)
	for _, p := range gitHubRawProxies {
		out = append(out, strings.TrimSuffix(p, "/")+"/"+direct)
	}
	for _, m := range gitHubRawMirrors {
		out = append(out, strings.TrimSuffix(m, "/")+suffix)
	}
	return out
}

// httpGetBody 顺序尝试多个 URL，首个成功返回响应体与获胜 URL
// （供"上次成功优先"排序记录，失败时获胜 URL 为空串）。
func httpGetBody(ctx context.Context, urls []string) ([]byte, string, error) {
	var lastErr error
	// 403/429/网络错误非确定性失败：逐个尝试。全部失败后汇总打印一次，
	// 避免 jsDelivr→GitHub 多候选 URL 各自打一条（如 jsDelivr/CDN/代理
	// 全被网络拦截时启动即产生几十行 403 噪音日志）。
	var failures []string
	for _, u := range urls {
		body, err := httpGetBodyOne(ctx, u)
		if err == nil {
			return body, u, nil
		}
		lastErr = err
		// 4xx（除 429 限流、403 权限外）是确定性问题：资源不存在/已删除/大小受限，
		// 换域名结果相同，立即失败避免逐个域名空等。
		var se *httpStatusError
		if errors.As(err, &se) && se.status >= 400 && se.status < 500 &&
			se.status != http.StatusTooManyRequests && se.status != http.StatusForbidden {
			return nil, "", err
		}
		failures = append(failures, fmt.Sprintf("%s: %v", u, err))
	}
	if len(failures) > 0 {
		log.Printf("http get failed for %d candidate URL(s): %v", len(failures), strings.Join(failures, " | "))
	}
	if lastErr == nil {
		lastErr = fmt.Errorf("no candidate URLs")
	}
	return nil, "", lastErr
}

func httpGetBodyOne(ctx context.Context, u string) ([]byte, error) {
	reqCtx, cancel := context.WithTimeout(ctx, requestTimeoutForURL(u))
	defer cancel()
	req, err := http.NewRequestWithContext(reqCtx, http.MethodGet, u, nil)
	if err != nil {
		return nil, err
	}
	// 版本号由 appmeta 在启动时注入，避免与发布版本脱钩
	req.Header.Set("User-Agent", appmeta.UserAgent("https://github.com/sugu6/AgentPack"))
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
