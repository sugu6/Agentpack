package skills

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"agentpack/internal/config"
)

func contentSHAHex(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])
}

func contentSHAB64(s string) string {
	sum := sha256.Sum256([]byte(s))
	return base64.StdEncoding.EncodeToString(sum[:])
}

// testTreeJSON 构造 jsDelivr data API 风格的嵌套文件树
// （仓库根 → skills/ → demo/ 下两个文件）。
func testTreeJSON(skillHash, noteHash string) string {
	return fmt.Sprintf(`{"name":"repo","type":"directory","files":[
	  {"name":"skills","type":"directory","files":[
	    {"name":"demo","type":"directory","files":[
	      {"name":"SKILL.md","type":"file","hash":%q},
	      {"name":"note.txt","type":"file","hash":%q}
	    ]}
	  ]}
	]}`, skillHash, noteHash)
}

// mockJsDelivr 用 httptest 服务器模拟 jsDelivr data API + 内容 CDN。
func mockJsDelivr(t *testing.T, treeJSON string, files map[string]string) *httptest.Server {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/v1/packages/") {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(treeJSON))
			return
		}
		if strings.HasPrefix(r.URL.Path, "/gh/") {
			content, ok := files[r.URL.Path]
			if !ok {
				w.WriteHeader(http.StatusNotFound)
				return
			}
			_, _ = w.Write([]byte(content))
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	t.Cleanup(server.Close)
	origBase := jsDelivrDataBase
	origDataFallback := jsDelivrDataFallbackBases
	origHosts := jsDelivrFileHosts
	origGH := gitHubAPIBases
	origRawProxies := gitHubRawProxies
	origRawMirrors := gitHubRawMirrors
	origRawDirect := gitHubRawDirect
	jsDelivrDataBase = server.URL
	jsDelivrDataFallbackBases = nil
	jsDelivrFileHosts = []string{server.URL}
	// GitHub 树/raw 链路也指向 mock server（路径不匹配 → 404 快速失败），
	// 避免测试意外打到真实 GitHub。
	gitHubAPIBases = []string{server.URL}
	gitHubRawProxies = []string{server.URL}
	gitHubRawMirrors = nil
	gitHubRawDirect = server.URL
	t.Cleanup(func() {
		jsDelivrDataBase = origBase
		jsDelivrDataFallbackBases = origDataFallback
		jsDelivrFileHosts = origHosts
		gitHubAPIBases = origGH
		gitHubRawProxies = origRawProxies
		gitHubRawMirrors = origRawMirrors
		gitHubRawDirect = origRawDirect
	})
	return server
}

// mockGitSHA 只替换 git fallback 的 SHA 获取函数，不影响 jsDelivr 主链路。
func mockGitSHA(t *testing.T, sha string) {
	t.Helper()
	orig := fetchSkillCommitSHAFunc
	fetchSkillCommitSHAFunc = func(ctx context.Context, owner, repo, branch string) (string, error) {
		return sha, nil
	}
	t.Cleanup(func() { fetchSkillCommitSHAFunc = orig })
}

func TestFetchRemoteFileTree_FlattensNestedTree(t *testing.T) {
	mockJsDelivr(t, testTreeJSON(contentSHAB64("skill"), contentSHAB64("note")), nil)

	files, err := fetchRemoteFileTree(context.Background(), "owner", "repo", "main")
	if err != nil {
		t.Fatalf("fetchRemoteFileTree: %v", err)
	}
	if len(files) != 2 {
		t.Fatalf("expected 2 files, got %d: %v", len(files), files)
	}
	if got := files["skills/demo/SKILL.md"]; got != contentSHAHex("skill") {
		t.Errorf("expected skills/demo/SKILL.md hash %s, got %s", contentSHAHex("skill"), got)
	}
	if got := files["skills/demo/note.txt"]; got != contentSHAHex("note") {
		t.Errorf("expected skills/demo/note.txt hash %s, got %s", contentSHAHex("note"), got)
	}
}

func TestSkillRemoteDiff_DetectsOnlyRemoteChanges(t *testing.T) {
	tmp := t.TempDir()
	localDir := filepath.Join(tmp, "demo")
	if err := os.MkdirAll(localDir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(localDir, "SKILL.md"), []byte("old"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(localDir, "note.txt"), []byte("note"), 0644); err != nil {
		t.Fatal(err)
	}
	// 本地额外文件：不应视为差异
	if err := os.WriteFile(filepath.Join(localDir, "local-extra.txt"), []byte("keep"), 0644); err != nil {
		t.Fatal(err)
	}

	tree := map[string]string{
		"skills/demo/SKILL.md":  gitBlobSHA1Hex([]byte("new")),
		"skills/demo/note.txt":  gitBlobSHA1Hex([]byte("note")),
		"skills/other/SKILL.md": gitBlobSHA1Hex([]byte("other")),
	}
	treeWithSource := remoteTree{files: tree, source: treeSourceGitHub, hashFn: gitBlobSHA1Hex}
	changed, hasDiff := skillRemoteDiffWith(treeWithSource, "skills/demo", localDir)
	if !hasDiff {
		t.Fatal("expected diff when SKILL.md content changed")
	}
	if len(changed) != 1 || changed[0] != "SKILL.md" {
		t.Fatalf("expected changed=[SKILL.md], got %v", changed)
	}
}

func TestCheckUpdates_ContentBasedDetectsUpdate(t *testing.T) {
	setupTestHome(t)
	tmp := t.TempDir()
	ssotDir := filepath.Join(tmp, "ssot")
	makeSkillDir(t, ssotDir, "demo", "old")
	if err := os.WriteFile(filepath.Join(ssotDir, "demo", "note.txt"), []byte("note"), 0644); err != nil {
		t.Fatal(err)
	}
	files := map[string]string{
		"/gh/owner/repo@main/skills/demo/SKILL.md": "new",
		"/gh/owner/repo@main/skills/demo/note.txt": "note",
	}
	mockJsDelivr(t, testTreeJSON(contentSHAB64("new"), contentSHAB64("note")), files)

	store := NewStore(ssotDir, SyncMethodSymlink)
	store.skills["skill:demo"] = Skill{
		ID: "skill:demo", Directory: "demo",
		RepoOwner: "owner", RepoName: "repo", RepoBranch: "main",
		FullPath: "skills/demo",
	}
	results := store.CheckUpdates(nil)
	if len(results) != 1 {
		t.Fatalf("expected 1 result, got %d", len(results))
	}
	if !results[0].HasUpdate {
		t.Fatal("expected HasUpdate when remote SKILL.md differs")
	}
	if len(results[0].ChangedFiles) != 1 || results[0].ChangedFiles[0] != "SKILL.md" {
		t.Fatalf("expected changedFiles=[SKILL.md], got %v", results[0].ChangedFiles)
	}
	if results[0].RemoteHash == "" {
		t.Error("expected non-empty remote hash")
	}
}

// TestCheckUpdates_TreeHashMismatchVerifiedByDownload 验证 jsDelivr 树 hash
// 与实际文件内容不一致（实测 bug）时，检测会下载文件做字节级对比，
// 本地已是最新则不再误报"有更新"。
func TestCheckUpdates_TreeHashMismatchVerifiedByDownload(t *testing.T) {
	setupTestHome(t)
	tmp := t.TempDir()
	ssotDir := filepath.Join(tmp, "ssot")
	makeSkillDir(t, ssotDir, "demo", "current")
	if err := os.WriteFile(filepath.Join(ssotDir, "demo", "note.txt"), []byte("note"), 0644); err != nil {
		t.Fatal(err)
	}
	// 树 hash 故意与文件内容不符（模拟 jsDelivr 树 hash 漂移）
	files := map[string]string{
		"/gh/owner/repo@main/skills/demo/SKILL.md": "current",
		"/gh/owner/repo@main/skills/demo/note.txt": "note",
	}
	mockJsDelivr(t, testTreeJSON(contentSHAB64("WRONG-HASH"), contentSHAB64("note")), files)

	store := NewStore(ssotDir, SyncMethodSymlink)
	store.skills["skill:demo"] = Skill{
		ID: "skill:demo", Directory: "demo",
		RepoOwner: "owner", RepoName: "repo", RepoBranch: "main",
		FullPath: "skills/demo",
	}
	results := store.CheckUpdates(nil)
	if len(results) != 1 {
		t.Fatalf("expected 1 result, got %d", len(results))
	}
	if results[0].HasUpdate {
		t.Fatalf("expected no update when download matches local, got %+v", results[0])
	}
	if results[0].Error != "" {
		t.Fatalf("expected no error, got %s", results[0].Error)
	}
}

func TestCheckUpdates_ContentBasedNoUpdate(t *testing.T) {
	setupTestHome(t)
	tmp := t.TempDir()
	ssotDir := filepath.Join(tmp, "ssot")
	makeSkillDir(t, ssotDir, "demo", "current")
	if err := os.WriteFile(filepath.Join(ssotDir, "demo", "note.txt"), []byte("note"), 0644); err != nil {
		t.Fatal(err)
	}
	mockJsDelivr(t, testTreeJSON(contentSHAB64("current"), contentSHAB64("note")), nil)

	store := NewStore(ssotDir, SyncMethodSymlink)
	store.skills["skill:demo"] = Skill{
		ID: "skill:demo", Directory: "demo",
		RepoOwner: "owner", RepoName: "repo", RepoBranch: "main",
		FullPath: "skills/demo",
	}
	results := store.CheckUpdates(nil)
	if len(results) != 1 {
		t.Fatalf("expected 1 result, got %d", len(results))
	}
	if results[0].HasUpdate {
		t.Fatalf("expected no update when content matches, got %+v", results[0])
	}
	if len(results[0].ChangedFiles) != 0 {
		t.Fatalf("expected empty changedFiles, got %v", results[0].ChangedFiles)
	}
}

func TestUpdateSkillViaJsDelivr_IncrementalUpdate(t *testing.T) {
	setupSkillCapableAgentHome(t)
	tmp := t.TempDir()
	ssotDir := filepath.Join(tmp, "ssot")
	makeSkillDir(t, ssotDir, "demo", "old")
	extraPath := filepath.Join(ssotDir, "demo", "local-extra.txt")
	if err := os.WriteFile(extraPath, []byte("keep"), 0644); err != nil {
		t.Fatal(err)
	}

	files := map[string]string{
		"/gh/owner/repo@main/skills/demo/SKILL.md": "new",
		"/gh/owner/repo@main/skills/demo/note.txt": "note",
	}
	mockJsDelivr(t, testTreeJSON(contentSHAB64("new"), contentSHAB64("note")), files)
	mockGitSHA(t, "cached-sha")

	store := NewStore(ssotDir, SyncMethodCopy)
	store.skills["skill:demo"] = Skill{
		ID: "skill:demo", Directory: "demo",
		RepoOwner: "owner", RepoName: "repo", RepoBranch: "main",
		FullPath: "skills/demo",
	}
	store.bindings["skill:demo"] = map[string]bool{"claude-code": true}
	reg := newSkillTestRegistry()

	updated, err := store.UpdateSkill("skill:demo", reg)
	if err != nil {
		t.Fatalf("UpdateSkill: %v", err)
	}

	content, err := os.ReadFile(filepath.Join(ssotDir, "demo", "SKILL.md"))
	if err != nil {
		t.Fatalf("read updated SKILL.md: %v", err)
	}
	if string(content) != "new" {
		t.Fatalf("expected updated SKILL.md content %q, got %q", "new", string(content))
	}
	note, err := os.ReadFile(filepath.Join(ssotDir, "demo", "note.txt"))
	if err != nil {
		t.Fatalf("read added note.txt: %v", err)
	}
	if string(note) != "note" {
		t.Fatalf("expected note.txt content %q, got %q", "note", string(note))
	}
	extra, err := os.ReadFile(extraPath)
	if err != nil {
		t.Fatalf("local extra file was removed: %v", err)
	}
	if string(extra) != "keep" {
		t.Fatalf("local extra file changed: %q", string(extra))
	}
	agentTarget := filepath.Join(reg.AgentSkillsDir("claude-code"), "demo", "SKILL.md")
	agentContent, err := os.ReadFile(agentTarget)
	if err != nil {
		t.Fatalf("agent copy not synced: %v", err)
	}
	if string(agentContent) != "new" {
		t.Fatalf("agent content mismatch: %q", string(agentContent))
	}
	if updated.ContentHash == "" || updated.UpdatedAt == "" {
		t.Errorf("expected refreshed ContentHash and UpdatedAt, got %+v", updated)
	}
}

func TestUpdateSkillViaJsDelivr_AlreadyLatest(t *testing.T) {
	setupSkillCapableAgentHome(t)
	tmp := t.TempDir()
	ssotDir := filepath.Join(tmp, "ssot")
	makeSkillDir(t, ssotDir, "demo", "current")
	if err := os.WriteFile(filepath.Join(ssotDir, "demo", "note.txt"), []byte("note"), 0644); err != nil {
		t.Fatal(err)
	}
	// 内容 CDN 故意 404：已是最新时不应发起文件下载
	mockJsDelivr(t, testTreeJSON(contentSHAB64("current"), contentSHAB64("note")), nil)
	mockGitSHA(t, "cached-sha")

	store := NewStore(ssotDir, SyncMethodCopy)
	store.skills["skill:demo"] = Skill{
		ID: "skill:demo", Directory: "demo",
		RepoOwner: "owner", RepoName: "repo", RepoBranch: "main",
		FullPath: "skills/demo",
	}
	if _, err := store.UpdateSkill("skill:demo", newSkillTestRegistry()); err != nil {
		t.Fatalf("UpdateSkill on latest version should succeed, got: %v", err)
	}
}

func TestUpdateSkillViaJsDelivr_DownloadFailureLeavesSSOTUntouched(t *testing.T) {
	setupSkillCapableAgentHome(t)
	tmp := t.TempDir()
	ssotDir := filepath.Join(tmp, "ssot")
	makeSkillDir(t, ssotDir, "demo", "old")

	// data API 返回树，但文件下载 404 → 文件级更新失败且不改动 SSOT
	mockJsDelivr(t, testTreeJSON(contentSHAB64("new"), contentSHAB64("note")), nil)
	mockGitSHA(t, "cached-sha")

	store := NewStore(ssotDir, SyncMethodCopy)
	sk := Skill{
		ID: "skill:demo", Directory: "demo",
		RepoOwner: "owner", RepoName: "repo", RepoBranch: "main",
		FullPath: "skills/demo",
	}
	_, err := store.updateSkillViaJsDelivr(context.Background(), sk, "skills/demo", "main",
		ssotDir, nil, newSkillTestRegistry(), SyncMethodCopy)
	if err == nil {
		t.Fatal("expected download failure")
	}
	content, rerr := os.ReadFile(filepath.Join(ssotDir, "demo", "SKILL.md"))
	if rerr != nil {
		t.Fatalf("SSOT skill missing after failed download: %v", rerr)
	}
	if string(content) != "old" {
		t.Fatalf("SSOT content changed after failed download: %q", string(content))
	}
}

func TestGitBlobSHA1Hex_KnownValue(t *testing.T) {
	// git hash-object 空文件的标准 blob SHA-1
	if got := gitBlobSHA1Hex(nil); got != "e69de29bb2d1d6434b8b29ae775ad8c2e48c5391" {
		t.Fatalf("expected empty blob sha e69de29..., got %s", got)
	}
}

func TestFetchGitHubFileTree_ParsesBlobs(t *testing.T) {
	sha := gitBlobSHA1Hex([]byte("x"))
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/repos/") {
			_, _ = w.Write([]byte(fmt.Sprintf(`{
			  "truncated": false,
			  "tree": [
			    {"path":"skills/demo/SKILL.md","type":"blob","sha":%q},
			    {"path":"skills/demo/scripts/run.sh","type":"blob","sha":%q},
			    {"path":"skills/demo","type":"tree","sha":"abc"}
			  ]
			}`, sha, sha)))
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	defer server.Close()

	orig := gitHubAPIBases
	gitHubAPIBases = []string{server.URL}
	defer func() { gitHubAPIBases = orig }()

	files, _, err := fetchGitHubFileTree(context.Background(), "owner", "repo", "main")
	if err != nil {
		t.Fatalf("fetchGitHubFileTree: %v", err)
	}
	if len(files) != 2 {
		t.Fatalf("expected 2 blob entries (directories excluded), got %d: %v", len(files), files)
	}
	if files["skills/demo/scripts/run.sh"] != sha {
		t.Errorf("expected blob sha for run.sh, got %s", files["skills/demo/scripts/run.sh"])
	}
}

func TestResolveSkillDirInTree(t *testing.T) {
	tree := map[string]string{
		"skills/demo/SKILL.md":  "a",
		"skills/demo/note.txt":  "b",
		"docs/other/SKILL.md":   "c",
		"skills/other/SKILL.md": "d",
	}
	if got := resolveSkillDirInTree(tree, "demo"); got != "skills/demo" {
		t.Errorf("expected skills/demo, got %q", got)
	}
	if got := resolveSkillDirInTree(tree, "other"); got != "skills/other" {
		t.Errorf("expected skills/other, got %q", got)
	}
	if got := resolveSkillDirInTree(tree, "missing"); got != "" {
		t.Errorf("expected empty for missing skill, got %q", got)
	}

	// 仓库根目录技能
	rootTree := map[string]string{"SKILL.md": "a", "helper.txt": "b"}
	if got := resolveSkillDirInTree(rootTree, "root-skill"); got != "" {
		t.Errorf("expected empty for root skill without matching dir, got %q", got)
	}
}

// TestCheckUpdates_MissingFullPathResolvesFromTree 验证存量技能 fullPath 缺失时，
// 检测会从远程树定位真实路径（skills/{dir}），而不是把整个仓库当技能导致永久误报。
func TestCheckUpdates_MissingFullPathResolvesFromTree(t *testing.T) {
	setupTestHome(t)
	tmp := t.TempDir()
	ssotDir := filepath.Join(tmp, "ssot")
	makeSkillDir(t, ssotDir, "demo", "current")

	treeJSON := fmt.Sprintf(`{"name":"repo","type":"directory","files":[
	  {"name":"skills","type":"directory","files":[
	    {"name":"demo","type":"directory","files":[
	      {"name":"SKILL.md","type":"file","hash":%q}
	    ]},
	    {"name":"other","type":"directory","files":[
	      {"name":"SKILL.md","type":"file","hash":%q}
	    ]}
	  ]}
	]}`, contentSHAB64("current"), contentSHAB64("other"))
	mockJsDelivr(t, treeJSON, nil)

	store := NewStore(ssotDir, SyncMethodSymlink)
	store.skills["skill:demo"] = Skill{
		ID: "skill:demo", Directory: "demo",
		RepoOwner: "owner", RepoName: "repo", RepoBranch: "main",
		// FullPath 故意缺失（历史数据）
	}
	results := store.CheckUpdates(nil)
	if len(results) != 1 {
		t.Fatalf("expected 1 result, got %d", len(results))
	}
	if results[0].HasUpdate {
		t.Fatalf("expected no update when skill content matches, got %+v", results[0])
	}
	if results[0].Error != "" {
		t.Fatalf("expected no error when path resolved from tree, got %s", results[0].Error)
	}
	// 存量数据应被修复
	lockInfo, ok := ParseAgentsLock()["demo"]
	if !ok || lockInfo.FullPath != "skills/demo" {
		t.Fatalf("expected lock fullPath fixed to skills/demo, got %+v", lockInfo)
	}
}

// TestUpdateSkill_MissingFullPathResolvesAndStaysFresh 验证：fullPath 缺失的存量技能
// 更新成功（只更新自己的目录）后，再次检测不再报"有更新"（修复误报闭环）。
func TestUpdateSkill_MissingFullPathResolvesAndStaysFresh(t *testing.T) {
	setupSkillCapableAgentHome(t)
	tmp := t.TempDir()
	ssotDir := filepath.Join(tmp, "ssot")
	makeSkillDir(t, ssotDir, "demo", "old")

	files := map[string]string{
		"/gh/owner/repo@main/skills/demo/SKILL.md": "new",
		"/gh/owner/repo@main/skills/demo/note.txt": "note",
	}
	treeJSON := fmt.Sprintf(`{"name":"repo","type":"directory","files":[
	  {"name":"skills","type":"directory","files":[
	    {"name":"demo","type":"directory","files":[
	      {"name":"SKILL.md","type":"file","hash":%q},
	      {"name":"note.txt","type":"file","hash":%q}
	    ]},
	    {"name":"other","type":"directory","files":[
	      {"name":"SKILL.md","type":"file","hash":%q}
	    ]}
	  ]}
	]}`, contentSHAB64("new"), contentSHAB64("note"), contentSHAB64("other"))
	mockJsDelivr(t, treeJSON, files)
	mockGitSHA(t, "cached-sha")

	store := NewStore(ssotDir, SyncMethodCopy)
	store.skills["skill:demo"] = Skill{
		ID: "skill:demo", Directory: "demo",
		RepoOwner: "owner", RepoName: "repo", RepoBranch: "main",
	}
	store.bindings["skill:demo"] = map[string]bool{"claude-code": true}
	reg := newSkillTestRegistry()

	if _, err := store.UpdateSkill("skill:demo", reg); err != nil {
		t.Fatalf("UpdateSkill: %v", err)
	}
	content, err := os.ReadFile(filepath.Join(ssotDir, "demo", "SKILL.md"))
	if err != nil {
		t.Fatalf("read updated SKILL.md: %v", err)
	}
	if string(content) != "new" {
		t.Fatalf("expected updated content, got %q", string(content))
	}
	if _, err := os.Stat(filepath.Join(ssotDir, "demo", "note.txt")); err != nil {
		t.Fatalf("note.txt should be downloaded: %v", err)
	}
	// 不应把 other 目录下载进 demo
	if _, err := os.Stat(filepath.Join(ssotDir, "demo", "other")); !os.IsNotExist(err) {
		t.Fatalf("other skill should not be downloaded into demo: %v", err)
	}
	// 存量 lock 修复
	lockInfo, ok := ParseAgentsLock()["demo"]
	if !ok || lockInfo.FullPath != "skills/demo" {
		t.Fatalf("expected lock fullPath fixed to skills/demo, got %+v", lockInfo)
	}

	// 更新成功后再检测：不应再报"有更新"
	results := store.CheckUpdates(nil)
	if len(results) != 1 {
		t.Fatalf("expected 1 result, got %d", len(results))
	}
	if results[0].HasUpdate {
		t.Fatalf("expected no update right after successful update, got %+v", results[0])
	}
}

// TestUpdateSkill_FallsBackToGitHubTreeWhenJsDelivrStale 验证核心场景：
// jsDelivr 文件树过时（旧路径下载 404）时，自动切换到 GitHub 实时树重试，
// 用 raw 源下载新路径文件并完成更新，而不是整体失败。
func TestUpdateSkill_FallsBackToGitHubTreeWhenJsDelivrStale(t *testing.T) {
	setupSkillCapableAgentHome(t)
	tmp := t.TempDir()
	ssotDir := filepath.Join(tmp, "ssot")
	makeSkillDir(t, ssotDir, "demo", "current")

	// jsDelivr：文件树含过时路径 ooxml/old.txt，内容 CDN 全部 404
	jsSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/v1/packages/") {
			_, _ = w.Write([]byte(`{"name":"repo","type":"directory","files":[
			  {"name":"skills","type":"directory","files":[
			    {"name":"demo","type":"directory","files":[
			      {"name":"ooxml","type":"directory","files":[
			        {"name":"old.txt","type":"file","hash":"AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA="}
			      ]}
			    ]}
			  ]}
			]}`))
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	defer jsSrv.Close()

	// GitHub：实时树含 skills/demo/SKILL.md + skills/demo/scripts/new.txt，
	// raw 源提供新文件内容
	skillSHA := gitBlobSHA1Hex([]byte("current"))
	newSHA := gitBlobSHA1Hex([]byte("new content"))
	ghSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/repos/") {
			_, _ = w.Write([]byte(fmt.Sprintf(`{
			  "truncated": false,
			  "tree": [
			    {"path":"skills/demo/SKILL.md","type":"blob","sha":%q},
			    {"path":"skills/demo/scripts/new.txt","type":"blob","sha":%q}
			  ]
			}`, skillSHA, newSHA)))
			return
		}
		if strings.Contains(r.URL.Path, "/skills/demo/scripts/new.txt") {
			_, _ = w.Write([]byte("new content"))
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	defer ghSrv.Close()

	origBase, origHosts := jsDelivrDataBase, jsDelivrFileHosts
	origGH, origRawP, origRawD := gitHubAPIBases, gitHubRawProxies, gitHubRawDirect
	jsDelivrDataBase, jsDelivrFileHosts = jsSrv.URL, []string{jsSrv.URL}
	gitHubAPIBases, gitHubRawProxies, gitHubRawDirect = []string{ghSrv.URL}, []string{ghSrv.URL}, ghSrv.URL
	defer func() {
		jsDelivrDataBase, jsDelivrFileHosts = origBase, origHosts
		gitHubAPIBases, gitHubRawProxies, gitHubRawDirect = origGH, origRawP, origRawD
	}()
	mockGitSHA(t, "cached-sha")

	store := NewStore(ssotDir, SyncMethodCopy)
	store.skills["skill:demo"] = Skill{
		ID: "skill:demo", Directory: "demo",
		RepoOwner: "owner", RepoName: "repo", RepoBranch: "main",
		FullPath: "skills/demo",
	}
	store.bindings["skill:demo"] = map[string]bool{"claude-code": true}
	reg := newSkillTestRegistry()

	if _, err := store.UpdateSkill("skill:demo", reg); err != nil {
		t.Fatalf("UpdateSkill should succeed via github tree fallback, got: %v", err)
	}
	skillContent, err := os.ReadFile(filepath.Join(ssotDir, "demo", "SKILL.md"))
	if err != nil {
		t.Fatalf("read SKILL.md: %v", err)
	}
	if string(skillContent) != "current" {
		t.Fatalf("SKILL.md should be unchanged, got %q", string(skillContent))
	}
	newPath := filepath.Join(ssotDir, "demo", "scripts", "new.txt")
	newContent, err := os.ReadFile(newPath)
	if err != nil {
		t.Fatalf("new file should be downloaded from github tree: %v", err)
	}
	if string(newContent) != "new content" {
		t.Fatalf("new file content mismatch: %q", string(newContent))
	}
	agentNew := filepath.Join(reg.AgentSkillsDir("claude-code"), "demo", "scripts", "new.txt")
	if _, err := os.Stat(agentNew); err != nil {
		t.Fatalf("agent copy of new file missing: %v", err)
	}
}

// TestUpdateSkillViaJsDelivr_404FallsToRaw 验证 jsDelivr 单文件 404（大文件限制
// /缓存差异）但 raw 源可用时，直接走 raw 完成更新，无需切换 GitHub 树。
func TestUpdateSkillViaJsDelivr_404FallsToRaw(t *testing.T) {
	setupSkillCapableAgentHome(t)
	tmp := t.TempDir()
	ssotDir := filepath.Join(tmp, "ssot")
	makeSkillDir(t, ssotDir, "demo", "demo")

	// jsDelivr：树里有 big.bin，但内容 CDN 全部 404
	jsSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/v1/packages/") {
			_, _ = w.Write([]byte(fmt.Sprintf(`{"name":"repo","type":"directory","files":[
			  {"name":"skills","type":"directory","files":[
			    {"name":"demo","type":"directory","files":[
			      {"name":"SKILL.md","type":"file","hash":%q},
			      {"name":"big.bin","type":"file","hash":%q}
			    ]}
			  ]}
			]}`, contentSHAB64("demo"), contentSHAB64("big content"))))
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	defer jsSrv.Close()

	// raw 源：提供 big.bin 内容
	rawSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "/skills/demo/big.bin") {
			_, _ = w.Write([]byte("big content"))
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	defer rawSrv.Close()

	// GitHub Trees API：不应被调用（raw 已成功）
	var ghCalls atomic.Int32
	ghSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ghCalls.Add(1)
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer ghSrv.Close()

	origBase, origHosts := jsDelivrDataBase, jsDelivrFileHosts
	origGH, origRawP, origRawD := gitHubAPIBases, gitHubRawProxies, gitHubRawDirect
	jsDelivrDataBase, jsDelivrFileHosts = jsSrv.URL, []string{jsSrv.URL}
	gitHubAPIBases, gitHubRawProxies, gitHubRawDirect = []string{ghSrv.URL}, []string{rawSrv.URL}, rawSrv.URL
	defer func() {
		jsDelivrDataBase, jsDelivrFileHosts = origBase, origHosts
		gitHubAPIBases, gitHubRawProxies, gitHubRawDirect = origGH, origRawP, origRawD
	}()
	mockGitSHA(t, "cached-sha")

	store := NewStore(ssotDir, SyncMethodCopy)
	store.skills["skill:demo"] = Skill{
		ID: "skill:demo", Directory: "demo",
		RepoOwner: "owner", RepoName: "repo", RepoBranch: "main",
		FullPath: "skills/demo",
	}
	store.bindings["skill:demo"] = map[string]bool{"claude-code": true}
	reg := newSkillTestRegistry()

	if _, err := store.UpdateSkill("skill:demo", reg); err != nil {
		t.Fatalf("UpdateSkill should succeed via raw fallback, got: %v", err)
	}
	content, err := os.ReadFile(filepath.Join(ssotDir, "demo", "big.bin"))
	if err != nil {
		t.Fatalf("big.bin should be downloaded from raw: %v", err)
	}
	if string(content) != "big content" {
		t.Fatalf("big.bin content mismatch: %q", string(content))
	}
	if ghCalls.Load() != 0 {
		t.Fatalf("github tree should not be consulted when raw succeeds, got %d calls", ghCalls.Load())
	}
}

func TestVerifySkillSource_MatchesLocalContent(t *testing.T) {
	tmp := t.TempDir()
	localDir := filepath.Join(tmp, "demo")
	if err := os.MkdirAll(localDir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(localDir, "SKILL.md"), []byte("current"), 0644); err != nil {
		t.Fatal(err)
	}
	files := map[string]string{
		"/gh/owner/repo@main/skills/demo/SKILL.md": "current",
		"/gh/owner/repo@main/skills/demo/note.txt": "note",
	}
	mockJsDelivr(t, testTreeJSON(contentSHAB64("current"), contentSHAB64("note")), files)

	fullPath, ok, err := VerifySkillSource(context.Background(), "demo", "owner", "repo", "main", localDir)
	if err != nil {
		t.Fatalf("VerifySkillSource: %v", err)
	}
	if !ok {
		t.Fatal("expected ok=true when remote SKILL.md matches local")
	}
	if fullPath != "skills/demo" {
		t.Fatalf("expected fullPath skills/demo, got %q", fullPath)
	}
}

func TestVerifySkillSource_ContentMismatchRejected(t *testing.T) {
	tmp := t.TempDir()
	localDir := filepath.Join(tmp, "demo")
	if err := os.MkdirAll(localDir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(localDir, "SKILL.md"), []byte("local-version"), 0644); err != nil {
		t.Fatal(err)
	}
	files := map[string]string{
		"/gh/owner/repo@main/skills/demo/SKILL.md": "remote-version",
		"/gh/owner/repo@main/skills/demo/note.txt": "note",
	}
	mockJsDelivr(t, testTreeJSON(contentSHAB64("remote-version"), contentSHAB64("note")), files)

	fullPath, ok, err := VerifySkillSource(context.Background(), "demo", "owner", "repo", "main", localDir)
	if err != nil {
		t.Fatalf("VerifySkillSource: %v", err)
	}
	if ok {
		t.Fatalf("expected ok=false when content differs (fullPath=%q)", fullPath)
	}
	if fullPath != "" {
		t.Fatalf("expected empty fullPath when content differs, got %q", fullPath)
	}
}

func TestVerifySkillSource_MissingInTree(t *testing.T) {
	tmp := t.TempDir()
	localDir := filepath.Join(tmp, "demo")
	if err := os.MkdirAll(localDir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(localDir, "SKILL.md"), []byte("x"), 0644); err != nil {
		t.Fatal(err)
	}
	// 树里没有 demo 技能，且其他技能内容也与本地不同 → 不应匹配
	treeJSON := fmt.Sprintf(`{"name":"repo","type":"directory","files":[
	  {"name":"skills","type":"directory","files":[
	    {"name":"other","type":"directory","files":[
	      {"name":"SKILL.md","type":"file","hash":%q}
	    ]}
	  ]}
	]}`, contentSHAB64("other-content"))
	mockJsDelivr(t, treeJSON, map[string]string{
		"/gh/owner/repo@main/skills/other/SKILL.md": "other-content",
	})

	_, ok, err := VerifySkillSource(context.Background(), "demo", "owner", "repo", "main", localDir)
	if err != nil {
		t.Fatalf("VerifySkillSource: %v", err)
	}
	if ok {
		t.Fatal("expected ok=false when skill directory missing in tree")
	}
}

// TestVerifySkillSource_NameMismatchContentMatch 验证"内容优先"：
// 仓库中目录名与本地不同但 SKILL.md 内容一致时，也可匹配并返回真实 fullPath。
func TestVerifySkillSource_NameMismatchContentMatch(t *testing.T) {
	tmp := t.TempDir()
	localDir := filepath.Join(tmp, "demo")
	if err := os.MkdirAll(localDir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(localDir, "SKILL.md"), []byte("same-content"), 0644); err != nil {
		t.Fatal(err)
	}
	// 树里只有 skills/taste/SKILL.md（名字不同），内容与本地一致
	treeJSON := fmt.Sprintf(`{"name":"repo","type":"directory","files":[
	  {"name":"skills","type":"directory","files":[
	    {"name":"taste","type":"directory","files":[
	      {"name":"SKILL.md","type":"file","hash":%q}
	    ]}
	  ]}
	]}`, contentSHAB64("same-content"))
	files := map[string]string{
		"/gh/owner/repo@main/skills/taste/SKILL.md": "same-content",
	}
	mockJsDelivr(t, treeJSON, files)

	fullPath, ok, err := VerifySkillSource(context.Background(), "demo", "owner", "repo", "main", localDir)
	if err != nil {
		t.Fatalf("VerifySkillSource: %v", err)
	}
	if !ok {
		t.Fatalf("expected ok=true when content matches despite name mismatch")
	}
	if fullPath != "skills/taste" {
		t.Fatalf("expected fullPath skills/taste, got %q", fullPath)
	}
}

// TestCheckUpdates_LocatesByContentWhenNameMismatch 验证：lock fullPath 缺失且
// 仓库目录名与本地不同（内容优先回填场景）时，检查更新能按内容定位技能目录，
// 不报"cannot locate"错误、不误报，并把 fullPath 回写 lock。
func TestCheckUpdates_LocatesByContentWhenNameMismatch(t *testing.T) {
	setupTestHome(t)
	tmp := t.TempDir()
	ssotDir := filepath.Join(tmp, "ssot")
	makeSkillDir(t, ssotDir, "demo", "same-content")
	// 树里只有 skills/taste/SKILL.md（目录名不同，内容与本地一致）
	treeJSON := fmt.Sprintf(`{"name":"repo","type":"directory","files":[
	  {"name":"skills","type":"directory","files":[
	    {"name":"taste","type":"directory","files":[
	      {"name":"SKILL.md","type":"file","hash":%q}
	    ]}
	  ]}
	]}`, contentSHAB64("same-content"))
	files := map[string]string{
		"/gh/owner/repo@main/skills/taste/SKILL.md": "same-content",
	}
	mockJsDelivr(t, treeJSON, files)

	store := NewStore(ssotDir, SyncMethodSymlink)
	store.skills["skill:demo"] = Skill{
		ID: "skill:demo", Directory: "demo",
		RepoOwner: "owner", RepoName: "repo", RepoBranch: "main",
		// FullPath 故意缺失（内容优先回填的存量数据）
	}
	results := store.CheckUpdates(nil)
	if len(results) != 1 {
		t.Fatalf("expected 1 result, got %d", len(results))
	}
	if results[0].Error != "" {
		t.Fatalf("expected no error when located by content, got %s", results[0].Error)
	}
	if results[0].HasUpdate {
		t.Fatalf("expected no update when content matches, got %+v", results[0])
	}
	lockInfo, ok := ParseAgentsLock()["demo"]
	if !ok || lockInfo.FullPath != "skills/taste" {
		t.Fatalf("expected lock fullPath fixed to skills/taste, got %+v", lockInfo)
	}
}

// TestCheckUpdates_KeepsSourceWhenNotLocatable 验证：仓库中名字与内容都定位不到
// 时不再删除来源关联——内容比对基于本地 SKILL.md 字节，用户本地编辑即会失配，
// 删除来源会让合法关联被永久丢弃。因此保守保留 lock 与内存来源并静默跳过
//（不显示失败）。
func TestCheckUpdates_KeepsSourceWhenNotLocatable(t *testing.T) {
	setupTestHome(t)
	tmp := t.TempDir()
	ssotDir := filepath.Join(tmp, "ssot")
	makeSkillDir(t, ssotDir, "demo", "local-content")
	treeJSON := fmt.Sprintf(`{"name":"repo","type":"directory","files":[
	  {"name":"skills","type":"directory","files":[
	    {"name":"other","type":"directory","files":[
	      {"name":"SKILL.md","type":"file","hash":%q}
	    ]}
	  ]}
	]}`, contentSHAB64("other-content"))
	files := map[string]string{
		"/gh/owner/repo@main/skills/other/SKILL.md": "other-content",
	}
	mockJsDelivr(t, treeJSON, files)

	// 预置早期回填的错误关联（仓库中不存在 demo 技能）
	if err := WriteAgentsLock(AgentsLockEntry{
		Directory:  "demo",
		Source:     "owner/repo",
		SourceType: "github",
		SourceURL:  "https://github.com/owner/repo",
		Branch:     "main",
	}); err != nil {
		t.Fatal(err)
	}

	store := NewStore(ssotDir, SyncMethodSymlink)
	store.skills["skill:demo"] = Skill{
		ID: "skill:demo", Directory: "demo",
		RepoOwner: "owner", RepoName: "repo", RepoBranch: "main",
	}
	results := store.CheckUpdates(nil)
	if len(results) != 1 {
		t.Fatalf("expected 1 result, got %d", len(results))
	}
	if results[0].Error != "" {
		t.Fatalf("expected no error when source not locatable, got %s", results[0].Error)
	}
	if results[0].HasUpdate {
		t.Fatalf("expected no update when source not locatable, got %+v", results[0])
	}
	if _, ok := ParseAgentsLock()["demo"]; !ok {
		t.Fatal("expected lock source association to be preserved, not removed")
	}
	if sk, ok := store.Get("skill:demo"); !ok || sk.RepoOwner != "owner" || sk.RepoName != "repo" {
		t.Fatalf("expected in-memory source preserved, got %+v", sk)
	}
}

// TestCheckUpdates_CacheSkipsDownloadVerification 验证结果缓存：
// 远程树与本地内容未变化时，第二次检查直接复用上次结果（不再下载差异文件）；
// 本地内容变化后缓存失效并重查。
func TestCheckUpdates_CacheSkipsDownloadVerification(t *testing.T) {
	setupTestHome(t)
	tmp := t.TempDir()
	ssotDir := filepath.Join(tmp, "ssot")
	makeSkillDir(t, ssotDir, "demo", "old")

	var downloads atomic.Int32
	treeJSON := fmt.Sprintf(`{"name":"repo","type":"directory","files":[
	  {"name":"skills","type":"directory","files":[
	    {"name":"demo","type":"directory","files":[
	      {"name":"SKILL.md","type":"file","hash":%q}
	    ]}
	  ]}
	]}`, contentSHAB64("new"))
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/v1/packages/") {
			_, _ = w.Write([]byte(treeJSON))
			return
		}
		if strings.HasPrefix(r.URL.Path, "/gh/") {
			downloads.Add(1)
			_, _ = w.Write([]byte("new"))
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	defer server.Close()

	origBase, origHosts := jsDelivrDataBase, jsDelivrFileHosts
	origGH, origRawP, origRawD := gitHubAPIBases, gitHubRawProxies, gitHubRawDirect
	jsDelivrDataBase, jsDelivrFileHosts = server.URL, []string{server.URL}
	gitHubAPIBases, gitHubRawProxies, gitHubRawDirect = []string{server.URL}, []string{server.URL}, server.URL
	defer func() {
		jsDelivrDataBase, jsDelivrFileHosts = origBase, origHosts
		gitHubAPIBases, gitHubRawProxies, gitHubRawDirect = origGH, origRawP, origRawD
	}()

	store := NewStore(ssotDir, SyncMethodSymlink)
	store.skills["skill:demo"] = Skill{
		ID: "skill:demo", Directory: "demo",
		RepoOwner: "owner", RepoName: "repo", RepoBranch: "main",
		FullPath: "skills/demo",
	}

	first := store.CheckUpdates(nil)
	if len(first) != 1 || !first[0].HasUpdate {
		t.Fatalf("expected update on first check, got %+v", first)
	}
	d1 := downloads.Load()
	if d1 == 0 {
		t.Fatal("expected download verification on first check")
	}

	second := store.CheckUpdates(nil)
	if len(second) != 1 || !second[0].HasUpdate {
		t.Fatalf("expected cached update on second check, got %+v", second)
	}
	if got := downloads.Load(); got != d1 {
		t.Fatalf("expected no download on cached check, got %d -> %d", d1, got)
	}

	// 本地内容变化 → 缓存失效 → 重查
	if err := os.WriteFile(filepath.Join(ssotDir, "demo", "SKILL.md"), []byte("new"), 0644); err != nil {
		t.Fatal(err)
	}
	third := store.CheckUpdates(nil)
	if len(third) != 1 || third[0].HasUpdate {
		t.Fatalf("expected no update after local sync, got %+v", third)
	}
}

// TestDownloadRemoteFile_404SkipSemantics 验证 4xx 的候选跳过语义：
// jsDelivr CDN 主机与代理（非权威域）的 4xx 只说明该候选无法提供资源，
// 继续尝试后续候选；权威域（raw 直连）的 4xx 才是确定性"资源不存在"，
// 立即终止（update.go 的 is404 信号依赖它）。候选链：
// [js404(CDN 1), js404(CDN 2), raw404(权威直连), proxy200(代理候选)]，
// 期望：jsDelivr 404 继续下一 CDN → raw 直连 404 早退 → 代理不被尝试。
func TestDownloadRemoteFile_404SkipSemantics(t *testing.T) {
	s404 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	defer s404.Close()

	var secondHostHits atomic.Int32
	s404b := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		secondHostHits.Add(1)
		w.WriteHeader(http.StatusNotFound)
	}))
	defer s404b.Close()

	// raw 直连（权威域）：记录被请求次数，返回 404
	var rawHits atomic.Int32
	rawSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rawHits.Add(1)
		w.WriteHeader(http.StatusNotFound)
	}))
	defer rawSrv.Close()

	// 代理候选（raw 直连 404 早退后不应到达）
	var proxyHits atomic.Int32
	proxySrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		proxyHits.Add(1)
		_, _ = w.Write([]byte("ok"))
	}))
	defer proxySrv.Close()

	origHosts := jsDelivrFileHosts
	origRaw := gitHubRawDirect
	origProxies := gitHubRawProxies
	origMirrors := gitHubRawMirrors
	jsDelivrFileHosts = []string{s404.URL, s404b.URL}
	gitHubRawDirect = rawSrv.URL
	gitHubRawProxies = []string{proxySrv.URL}
	gitHubRawMirrors = nil
	defer func() {
		jsDelivrFileHosts = origHosts
		gitHubRawDirect = origRaw
		gitHubRawProxies = origProxies
		gitHubRawMirrors = origMirrors
	}()

	_, err := downloadRemoteFile(context.Background(), "owner", "repo", "main", "missing.txt", true)
	if err == nil {
		t.Fatal("expected download failure for 404")
	}
	var se *httpStatusError
	if !errors.As(err, &se) || se.status != http.StatusNotFound {
		t.Fatalf("expected 404 error, got %v", err)
	}
	// jsDelivr 404 非权威域：继续到下一个 jsDelivr CDN 主机
	if secondHostHits.Load() != 1 {
		t.Fatalf("jsDelivr 404（非权威域）应继续尝试下一 CDN 主机，got %d hits", secondHostHits.Load())
	}
	// 权威域（raw 直连）404 早退，代理候选不被尝试
	if proxyHits.Load() != 0 {
		t.Fatalf("authoritative 404 must abort before proxy candidates, got %d proxy hits", proxyHits.Load())
	}
	if rawHits.Load() < 1 {
		t.Fatal("raw direct candidate was not reached")
	}
}

// TestDownloadRemoteFile_NetworkErrorFallsThrough 验证网络错误（非 4xx）时
// 会继续尝试下一个源，fallback 仍然有效。
func TestDownloadRemoteFile_NetworkErrorFallsThrough(t *testing.T) {
	dead := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	deadURL := dead.URL
	dead.Close() // 连接将立即失败（网络错误）

	okServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("ok"))
	}))
	defer okServer.Close()

	origHosts := jsDelivrFileHosts
	jsDelivrFileHosts = []string{deadURL, okServer.URL}
	defer func() { jsDelivrFileHosts = origHosts }()

	data, err := downloadRemoteFile(context.Background(), "owner", "repo", "main", "file.txt", true)
	if err != nil {
		t.Fatalf("expected fallback to succeed after network error, got: %v", err)
	}
	if string(data) != "ok" {
		t.Fatalf("expected fallback content, got %q", string(data))
	}
}

// TestReorderPreferred 验证"上次成功优先"排序：获胜 URL 提到最前、其余
// 保持原顺序；pref 为空/不在列表中时原样返回。
func TestReorderPreferred(t *testing.T) {
	urls := []string{"https://a", "https://b", "https://c"}

	pref := ""
	if got := reorderPreferred(urls, &pref); !equalStrings(got, urls) {
		t.Fatalf("empty pref must keep order, got %v", got)
	}

	pref = "https://b"
	got := reorderPreferred(urls, &pref)
	want := []string{"https://b", "https://a", "https://c"}
	if !equalStrings(got, want) {
		t.Fatalf("reorder = %v, want %v", got, want)
	}

	pref = "https://gone"
	if got := reorderPreferred(urls, &pref); !equalStrings(got, urls) {
		t.Fatalf("stale pref must keep order, got %v", got)
	}

	// 原切片不得被修改
	if !equalStrings(urls, []string{"https://a", "https://b", "https://c"}) {
		t.Fatalf("input slice mutated: %v", urls)
	}
}

// TestDownloadRemoteFile_PrefersLastSuccessfulSource 验证成功下载后记录
// 获胜候选，下次下载该链路把获胜候选排在最前（先请求者先命中）。
func TestDownloadRemoteFile_PrefersLastSuccessfulSource(t *testing.T) {
	first := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("first"))
	}))
	defer first.Close()
	second := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("second"))
	}))
	defer second.Close()

	origHosts := jsDelivrFileHosts
	jsDelivrFileHosts = []string{first.URL, second.URL}
	origPref := prefJsDelivr
	prefJsDelivr = ""
	defer func() {
		jsDelivrFileHosts = origHosts
		prefJsDelivr = origPref
	}()

	// 第一次下载：first 可达，记录为偏好
	if data, err := downloadRemoteFile(context.Background(), "owner", "repo", "main", "f.txt", true); err != nil || string(data) != "first" {
		t.Fatalf("first download: %v (%s)", err, data)
	}
	wantURL := first.URL + "/gh/owner/repo@main/f.txt"
	if prefJsDelivr != wantURL {
		t.Fatalf("expected pref = %s, got %q", wantURL, prefJsDelivr)
	}

	// 第二次下载：first 保持最前（先于列表次序的 second 被请求）。
	// 用请求顺序计数验证：如果 first 先被请求，order 计数器为 1。
	var order int32
	second.Config.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&order, 1)
		_, _ = w.Write([]byte("second"))
	})
	// first handler 记录顺序
	first.Config.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !atomic.CompareAndSwapInt32(&order, 0, 10) {
			atomic.AddInt32(&order, 10)
		}
		_, _ = w.Write([]byte("first"))
	})
	if data, err := downloadRemoteFile(context.Background(), "owner", "repo", "main", "f.txt", true); err != nil || string(data) != "first" {
		t.Fatalf("second download: %v (%s)", err, data)
	}
	if order != 10 {
		t.Fatalf("expected preferred host to be requested first (order=10), got %d", order)
	}
}

// TestRecordPreferred_IgnoresForeignURL 验证不属于候选列表的 winner 不写入 pref。
func TestRecordPreferred_IgnoresForeignURL(t *testing.T) {
	pref := ""
	recordPreferred([]string{"https://a"}, "https://other", &pref)
	if pref != "" {
		t.Fatalf("foreign winner must not be recorded, got %q", pref)
	}
	recordPreferred([]string{"https://a"}, "https://a", &pref)
	if pref != "https://a" {
		t.Fatalf("expected pref https://a, got %q", pref)
	}
	recordPreferred([]string{"https://a"}, "", &pref)
	if pref != "https://a" {
		t.Fatalf("empty winner must keep pref, got %q", pref)
	}
}

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// TestHttpGetBodyOne_CrossHostRedirectNotFollowed 验证跨主机 301 不被跟随：
// jsDelivr 对无法提供的文件 301 到被墙的 raw.githubusercontent.com，跟随会让
// 候选耗满超时；拒绝后应立即以 3xx 状态错误返回，让候选循环切换下一源。
func TestHttpGetBodyOne_CrossHostRedirectNotFollowed(t *testing.T) {
	// 重定向目标：接受连接但不响应（模拟被墙的 raw）
	hang := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-r.Context().Done():
		case <-make(chan struct{}):
		}
	}))
	defer hang.Close()

	redirector := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, hang.URL+"/raw", http.StatusMovedPermanently)
	}))
	defer redirector.Close()

	start := time.Now()
	_, err := httpGetBodyOne(context.Background(), redirector.URL+"/f.txt")
	elapsed := time.Since(start)
	if err == nil {
		t.Fatal("expected error for cross-host redirect")
	}
	var se *httpStatusError
	if !errors.As(err, &se) || se.status != http.StatusMovedPermanently {
		t.Fatalf("expected 301 status error, got: %v", err)
	}
	// 跟随重定向会挂到 8s 超时；拒绝应秒级返回
	if elapsed > 3*time.Second {
		t.Fatalf("expected fast failure, took %v", elapsed)
	}
}

// TestHttpGetBody_Proxy404DoesNotBlockLaterCandidates 回归测试：候选列表跨
// 多个域名（代理/镜像/直连），前置候选（通常是代理）返回 4xx（如代理侧 404）
// 只说明该代理无法提供资源，不代表资源不存在；不得因此终止整个候选循环，
// 否则末位直连兜底永远得不到尝试（tarball 安装从"可回退"退化为硬失败）。
func TestHttpGetBody_Proxy404DoesNotBlockLaterCandidates(t *testing.T) {
	proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "not found", http.StatusNotFound)
	}))
	defer proxy.Close()

	direct := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("ok"))
	}))
	defer direct.Close()

	body, winner, err := httpGetBody(context.Background(), []string{proxy.URL + "/x.tar.gz", direct.URL + "/x.tar.gz"})
	if err != nil {
		t.Fatalf("4xx from a preceding candidate must not block later candidates: %v", err)
	}
	want := direct.URL + "/x.tar.gz"
	if string(body) != "ok" || winner != want {
		t.Fatalf("expected winner %q with body ok, got %q (%q)", want, winner, body)
	}
}

// TestHttpGetBody_Authoritative404StillAbortsEarly 验证权威域（raw 直连）
// 的 4xx 仍是确定性"资源不存在"信号：立即终止候选循环，不浪费后续
// 代理/镜像请求（update.go 的 is404 判断依赖该 404 快速切换 GitHub 树）。
func TestHttpGetBody_Authoritative404StillAbortsEarly(t *testing.T) {
	orig := gitHubRawDirect
	proxyCalls := 0
	auth := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "not found", http.StatusNotFound)
	}))
	defer auth.Close()
	proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		proxyCalls++
		_, _ = w.Write([]byte("ok"))
	}))
	defer proxy.Close()
	gitHubRawDirect = auth.URL
	defer func() { gitHubRawDirect = orig }()

	_, _, err := httpGetBody(context.Background(), []string{auth.URL + "/x", proxy.URL + "/x"})
	if err == nil {
		t.Fatal("expected 404 error")
	}
	var se *httpStatusError
	if !errors.As(err, &se) || se.status != http.StatusNotFound {
		t.Fatalf("expected authoritative 404 error, got %v", err)
	}
	if proxyCalls != 0 {
		t.Fatalf("authoritative 404 must abort early, proxy was tried %d time(s)", proxyCalls)
	}
}

// TestHttpGetBodyOne_SameHostRedirectFollowed 验证同主机重定向（jsDelivr
// @branch → @version 的合法跳转）仍被正常跟随。
func TestHttpGetBodyOne_SameHostRedirectFollowed(t *testing.T) {
	final := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("resolved"))
	}))
	defer final.Close()

	redirector := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// 同主机路径跳转（模拟 cdn.jsdelivr.net 内部 @branch → @version）
		if r.URL.Path == "/resolved/f.txt" {
			_, _ = w.Write([]byte("resolved"))
			return
		}
		http.Redirect(w, r, "/resolved/f.txt", http.StatusMovedPermanently)
	}))
	defer redirector.Close()

	data, err := httpGetBodyOne(context.Background(), redirector.URL+"/f.txt")
	if err != nil {
		t.Fatalf("same-host redirect should be followed: %v", err)
	}
	if string(data) != "resolved" {
		t.Fatalf("expected redirected content, got %q", string(data))
	}
}

// buildVerifyTarball 构造带顶层 {repo}-{sha} 目录的 tar.gz（模拟 codeload 产物）。
func buildVerifyTarball(t *testing.T, files map[string]string) []byte {
	t.Helper()
	var buf bytes.Buffer
	gw := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gw)
	names := make([]string, 0, len(files))
	for name := range files {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		if err := tw.WriteHeader(&tar.Header{Name: name, Typeflag: tar.TypeReg, Mode: 0644, Size: int64(len(files[name]))}); err != nil {
			t.Fatal(err)
		}
		if _, err := tw.Write([]byte(files[name])); err != nil {
			t.Fatal(err)
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gw.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

// TestVerifySkillSourceByTarball 验证终极回退：codeload tarball 下载解压后
// 按内容匹配 SKILL.md（约定路径优先），顶层 {repo}-{sha} 目录被正确剥离，
// 内容不符时给出确定的无匹配结论。
func TestVerifySkillSourceByTarball(t *testing.T) {
	tmp := t.TempDir()
	localDir := filepath.Join(tmp, "demo")
	if err := os.MkdirAll(localDir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(localDir, "SKILL.md"), []byte("current"), 0644); err != nil {
		t.Fatal(err)
	}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write(buildVerifyTarball(t, map[string]string{
			"repo-abc123/skills/demo/SKILL.md":  "current",
			"repo-abc123/skills/demo/helper.sh": "x",
			"repo-abc123/other/SKILL.md":        "different",
		}))
	}))
	defer server.Close()

	// 隔离：把配置代理指向本地服务器（tarballCandidateURLs 对 codeload URL
	// 会先套配置代理），并清空备用代理避免真实网络请求。
	origProxy := config.DefaultGitHubProxy
	config.DefaultGitHubProxy = server.URL + "/"
	origFallbacks := tarballFallbackURLs
	tarballFallbackURLs = nil
	t.Cleanup(func() {
		config.DefaultGitHubProxy = origProxy
		tarballFallbackURLs = origFallbacks
	})

	fp, ok, err := verifySkillSourceByTarball(context.Background(), "owner", "repo", "main", "demo", localDir)
	if err != nil || !ok {
		t.Fatalf("expected match via tarball, got ok=%v err=%v", ok, err)
	}
	if fp != "skills/demo" {
		t.Fatalf("expected fullPath skills/demo, got %q", fp)
	}

	// 内容不匹配 → 确定无匹配（不算网络失败）
	if err := os.WriteFile(filepath.Join(localDir, "SKILL.md"), []byte("changed"), 0644); err != nil {
		t.Fatal(err)
	}
	fp, ok, err = verifySkillSourceByTarball(context.Background(), "owner", "repo", "main", "demo", localDir)
	if err != nil || ok || fp != "" {
		t.Fatalf("expected definitive no-match, got fp=%q ok=%v err=%v", fp, ok, err)
	}
}

// TestVerifySkillSource_FallsBackToRawWhenTreeFails 验证文件树接口（jsDelivr
// data API + GitHub Trees API）全线不可达时，VerifySkillSource 降级为用内容
// CDN/raw 直接下载约定路径的 SKILL.md 字节对比，名字优先回填仍能成功，
// 而不是因文件树接口 403/超时直接判定网络失败放弃来源关联。
func TestVerifySkillSource_FallsBackToRawWhenTreeFails(t *testing.T) {
	tmp := t.TempDir()
	localDir := filepath.Join(tmp, "demo")
	if err := os.MkdirAll(localDir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(localDir, "SKILL.md"), []byte("current"), 0644); err != nil {
		t.Fatal(err)
	}

	// 文件树接口（data API /v1/packages + GitHub Trees API /repos）全线 500，
	// 但单文件内容 CDN（/gh/.../skills/demo/SKILL.md）可达且内容匹配本地。
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/v1/packages/") ||
			strings.HasPrefix(r.URL.Path, "/repos/") {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		if strings.Contains(r.URL.Path, "/skills/demo/SKILL.md") {
			_, _ = w.Write([]byte("current"))
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	defer server.Close()

	origBase, origFallback, origHosts := jsDelivrDataBase, jsDelivrDataFallbackBases, jsDelivrFileHosts
	origGH, origRawP, origRawD := gitHubAPIBases, gitHubRawProxies, gitHubRawDirect
	jsDelivrDataBase = server.URL
	jsDelivrDataFallbackBases = nil
	jsDelivrFileHosts = []string{server.URL}
	gitHubAPIBases = []string{server.URL}
	gitHubRawProxies = []string{server.URL}
	gitHubRawDirect = server.URL
	defer func() {
		jsDelivrDataBase, jsDelivrDataFallbackBases, jsDelivrFileHosts = origBase, origFallback, origHosts
		gitHubAPIBases, gitHubRawProxies, gitHubRawDirect = origGH, origRawP, origRawD
	}()

	fullPath, ok, err := VerifySkillSource(context.Background(), "demo", "owner", "repo", "main", localDir)
	if err != nil {
		t.Fatalf("VerifySkillSource should succeed via raw fallback when tree fails, got: %v", err)
	}
	if !ok {
		t.Fatal("expected ok=true when remote SKILL.md matches local via fallback")
	}
	if fullPath != "skills/demo" {
		t.Fatalf("expected fullPath skills/demo, got %q", fullPath)
	}
}

// TestVerifySkillSource_NameFallbackMismatchRejected 验证降级路径下载到约定
// 路径 SKILL.md 但内容不一致时返回无匹配（nil err，算 mismatched 而非 failed），
// 让调用方继续下一个候选仓库而非误判网络失败反复重试。
func TestVerifySkillSource_NameFallbackMismatchRejected(t *testing.T) {
	tmp := t.TempDir()
	localDir := filepath.Join(tmp, "demo")
	if err := os.MkdirAll(localDir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(localDir, "SKILL.md"), []byte("local"), 0644); err != nil {
		t.Fatal(err)
	}

	// 文件树全线 500；约定路径 SKILL.md 可达但内容与本地不一致
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/v1/packages/") ||
			strings.HasPrefix(r.URL.Path, "/repos/") {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		if strings.Contains(r.URL.Path, "/skills/demo/SKILL.md") {
			_, _ = w.Write([]byte("remote")) // 内容不符
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	defer server.Close()

	origBase, origFallback, origHosts := jsDelivrDataBase, jsDelivrDataFallbackBases, jsDelivrFileHosts
	origGH, origRawP, origRawD := gitHubAPIBases, gitHubRawProxies, gitHubRawDirect
	jsDelivrDataBase = server.URL
	jsDelivrDataFallbackBases = nil
	jsDelivrFileHosts = []string{server.URL}
	gitHubAPIBases = []string{server.URL}
	gitHubRawProxies = []string{server.URL}
	gitHubRawDirect = server.URL
	defer func() {
		jsDelivrDataBase, jsDelivrDataFallbackBases, jsDelivrFileHosts = origBase, origFallback, origHosts
		gitHubAPIBases, gitHubRawProxies, gitHubRawDirect = origGH, origRawP, origRawD
	}()

	_, ok, err := VerifySkillSource(context.Background(), "demo", "owner", "repo", "main", localDir)
	if err != nil {
		t.Fatalf("expected nil err when SKILL.md downloaded but mismatched, got: %v", err)
	}
	if ok {
		t.Fatal("expected ok=false when content differs")
	}
}
