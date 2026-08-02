package skills

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
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
	origHosts := jsDelivrFileHosts
	origGH := gitHubAPIBases
	origRawProxies := gitHubRawProxies
	origRawDirect := gitHubRawDirect
	jsDelivrDataBase = server.URL
	jsDelivrFileHosts = []string{server.URL}
	// GitHub 树/raw 链路也指向 mock server（路径不匹配 → 404 快速失败），
	// 避免测试意外打到真实 GitHub。
	gitHubAPIBases = []string{server.URL}
	gitHubRawProxies = []string{server.URL}
	gitHubRawDirect = server.URL
	t.Cleanup(func() {
		jsDelivrDataBase = origBase
		jsDelivrFileHosts = origHosts
		gitHubAPIBases = origGH
		gitHubRawProxies = origRawProxies
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

	files, err := fetchGitHubFileTree(context.Background(), "owner", "repo", "main")
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
	if fullPath != "skills/demo" {
		t.Fatalf("expected located fullPath skills/demo, got %q", fullPath)
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
	// 树里没有 demo 技能（只有 other）
	treeJSON := fmt.Sprintf(`{"name":"repo","type":"directory","files":[
	  {"name":"skills","type":"directory","files":[
	    {"name":"other","type":"directory","files":[
	      {"name":"SKILL.md","type":"file","hash":%q}
	    ]}
	  ]}
	]}`, contentSHAB64("x"))
	mockJsDelivr(t, treeJSON, nil)

	_, ok, err := VerifySkillSource(context.Background(), "demo", "owner", "repo", "main", localDir)
	if err != nil {
		t.Fatalf("VerifySkillSource: %v", err)
	}
	if ok {
		t.Fatal("expected ok=false when skill directory missing in tree")
	}
}

// TestDownloadRemoteFile_404StopsImmediately 验证 4xx（资源不存在）时
// 不再轮询其余 CDN 域名，避免每个域名空等。
func TestDownloadRemoteFile_404StopsImmediately(t *testing.T) {
	s404 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	defer s404.Close()

	var hits atomic.Int32
	s200 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		_, _ = w.Write([]byte("ok"))
	}))
	defer s200.Close()

	origHosts := jsDelivrFileHosts
	jsDelivrFileHosts = []string{s404.URL, s200.URL}
	defer func() { jsDelivrFileHosts = origHosts }()

	_, err := downloadRemoteFile(context.Background(), "owner", "repo", "main", "missing.txt", true)
	if err == nil {
		t.Fatal("expected download failure for 404")
	}
	if hits.Load() != 0 {
		t.Fatalf("expected no fallback attempts after 404, got %d", hits.Load())
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
