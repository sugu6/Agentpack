# Skills Reconciliation Engine (对账引擎) Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 检测并处置技能副本的三态并存问题（SSOT 实体 / 映射投影 / 散装残留 / 断链），同内容自动转映射、分叉强制显式三选、孤儿死链可清理。

**Architecture:** 新增只读 `Store.ScanConflicts`，用全目录树哈希把 (SSOT 技能 × agent 目录) 每个已存在条目分类成五态状态机，外加一轮原始目录扫描抓孤儿死链；五个窄职责变更方法（convert/overwrite/adopt/keep/clean）各自校验路径并持 `importMu`（与 Import/Uninstall 同锁序）。保留分叉的 ack 持久化在 SSOT 同级目录、以双指纹为键，任一侧再漂移自动重新上屏。经 6 个新 App 服务方法（bindings 重新生成、已 gitignore）暴露到前端：徽标 + 对账面板 + 扫描第 5 步 + 导入后转换询问。

**Tech Stack:** Go（Wails v3 service）、复用 skills 包既有助手（`HashDir`/`copyDirRecursive`/`BackupSkillDir`/`SyncToAgentDir`/`RemovePath`/`scanAgentSkillDirs`/`refreshSkillAfterFileUpdate`）、Vue 3 + Pinia + vue-i18n、`wails3 generate bindings`。

**Spec:** 本文件「Design Decisions」章节即规格（2026-09-24 会话内获批的设计 D1-D5 + 状态机；无外部 spec 文档）。

**Out of scope:** P0 本机存量手工清理（交互式，另行执行）；B2（unified 模式对原生读者停止投影）；openskills 式 AGENTS.md 桥。

## Global Constraints

- **TDD 铁律**：每个生产代码改动前必须先有失败测试；任务顺序不可调换。
- 零改动：`build/**`、`Taskfile*.yml`、`wails.json`、CI 配置、`internal/app/**`。
- 根 `app.go` **仅允许**在 skills 方法块（1281-1475 行区间）末尾追加新方法，其余不动。
- `frontend/bindings/**` 已 gitignore：用 `wails3 generate bindings` 重新生成，永不 `git add`（用 `git check-ignore frontend/bindings` 自证）。
- gofmt 基线：`gofmt -l .` 输出只允许 `version.go` 与 `cleanup_test.go` 两个既有文件，不得新增未格式化文件。
- i18n：每个新键必须同时写入 `frontend/src/locales/zh-CN.json` 与 `en.json`（嵌套结构、两文件行号对齐；门禁 `pnpm check:i18n`）。
- 前端命令一律在 `frontend/` 目录执行；`pnpm build` = `vue-tsc --noEmit && vite build`。
- 本工作区带约 33 个既有未提交文件与未跟踪 `.review/`：**每任务只 stage 该任务 Files 列出的文件**，其余保持未提交。
- 并行工具批的第一个调用可能被丢弃：每批以占位 shell（`Write-Output placeholder`）打头。
- 测试建 symlink 需要权限：助手 `mustSymlink` 在 `os.Symlink` 失败时 `t.Skipf` 而非 fail。

## Review Focus

1. **同内容守卫**：`ConvertSkillCopyToLink` 对任何字节差异（含哈希不完整）必须拒绝且文件原样 → Task 4 的 "diverged plain copy refused and untouched" + Task 1 的 "same SKILL.md but different sidecar" 两测。
2. **清理边界**：`CleanOrphanSkillLinks` 绝不能删活链、普通目录、SSOT 同名条目 → Task 6 的 "removes dead links only" 测试（含 SSOT 同名死链存活、活外链存活、散装目录存活）。
3. **ack 指纹键控**：保留的分叉在任一侧漂移后必须重新上屏 → Task 2 `Matches` 单测 + Task 3 的 "local drift un-acknowledges" / "ssot drift un-acknowledges"。
4. **共享目录去重**（claude-code 与 claude-code-desktop 同目录）：只产一条 item、AgentIDs 聚合、清理只删一次 → Task 3 的 AgentIDs 断言 + Task 6 的 removed 长度断言。
5. **收编前备份失败必须中止且 SSOT 无恙** → Task 5 的 "backup failure aborts without touching SSOT"（用文件占位 `skill-backups` 路径强制造错）。

---

## Design Decisions (规格)

状态机（`ReconcileItem.Kind`，每条含 SkillID/Directory/Path/AgentIDs/SSOTHash/LocalHash/Acknowledged）：

| Kind | 判定 | 处置 |
|---|---|---|
| `plain_same` | symlink 模式下普通目录且全树哈希与 SSOT 全同（两侧完整） | 自动 `ConvertSkillCopyToLink`（字节未动，仅换形态） |
| `plain_diff` | 普通目录且哈希不同或任一侧不完整 | 显式三选：`AdoptSkillCopy`（本地收编，先备份 SSOT）/ `OverwriteSkillCopyFromSSOT`（SSOT 覆盖）/ `KeepSkillFork`（指纹 ack） |
| `broken_link` | 条目是死链 | `ConvertSkillCopyToLink` 重建 |
| `wrong_target` | 活链但目标≠SSOT 路径 | `ConvertSkillCopyToLink` 重链 |
| `orphan_link` | 死链且该名字不在 SSOT 技能集 | `CleanOrphanSkillLinks` 删除 |
| （不出产） | 不存在的条目、正确活链、无 SSOT 同名的散装目录（= 未管理视图） | 不动 |

- **D1 字节守卫**：仅全树哈希全同且两侧完整才允许自动转换；否则一律 `plain_diff`。
- **D2 分叉显式选源**：`plain_diff` 永不自动碰；三方并列由 UI 呈现，收编方向（本地→SSOT）与覆盖方向（SSOT→本地）互斥且都需确认。
- **D3 断链分级**：SSOT 同名的死链/错链=可重建（convert）；SSOT 无此名的死链=孤儿（clean，链接本体无内容，删除零数据损失）。`CleanOrphanSkillLinks` 只删「死链 ∧ 名字∉SSOT」。
- **D4 导入即询问**：从 agent 目录收编导入成功后，弹一次确认把来源目录转为映射（来源必然在 agent 目录内、导入刚保证字节全同 → 守卫必过）；拒绝则留 `plain_same` 待对账面板批量处理，不静默。
- **D5 模式感知**：copy 模式下与 SSOT 全同的普通目录是**正常投影**，不产冲突（否则 copy 模式全量误报）；分叉仍产 `plain_diff`。
- **锁序**：变更方法持 `importMu` 再取 `mu` 快照、I/O 在 `mu` 外（与 Import/Uninstall/ToggleAgent 相同）；`ScanConflicts` 只读持 `mu.RLock` 快照后释放。
- **不改既有语义**：`BoundAgents`/`scanFilesystem` 的「存在即绑定」推断保持原样（update/toggle 流程依赖），对账层是纯叠加视图。
- **ToggleAgent 守卫**：disable 时若目标是普通目录且与 SSOT 分叉 → 返回错误拒绝删除（防丢本地改动）；同内容普通目录与活链照旧可删。
- 关键契约：`ScanConflicts` 结果按 (kind, directory, path) 排序；ack key = `directory + "|" + filepath.Clean(path)`，值含双指纹，`Matches` 双指纹全等才成立。

### File Structure

- Create `internal/skills/reconcile.go` — 分类器 + `ScanConflicts` + 5 个变更方法 + 路径校验助手。
- Create `internal/skills/reconcile_test.go` — 上述全部测试与共享夹具。
- Create `internal/skills/conflictack.go` / `conflictack_test.go` — ack 持久化。
- Modify `internal/skills/types.go` — `ConflictKind`、`ReconcileItem`、`ConflictAck`。
- Modify `internal/skills/store.go` — 仅 `ToggleAgent` disable 分支加守卫。
- Modify `app.go` — 6 个服务方法（追加在 `ScanUnmanagedSkills` 之后）。
- Modify `frontend/src/lib/api.ts`、`frontend/src/stores/skills.ts`、`frontend/src/views/SkillsView.vue`、两个 locale 文件。

### Task 1: 冲突类型与纯分类器

**Files:**
- Modify: `internal/skills/types.go`（末尾追加）
- Create: `internal/skills/reconcile.go`
- Test: `internal/skills/reconcile_test.go`
- Commit 含本计划文档：`docs/superpowers/plans/2026-09-24-skills-reconciliation-engine.md`

**Interfaces:**
- Consumes: `HashDir(dir) (string, bool)`（sync.go:275）、`SyncMethod`/`SyncMethodSymlink`/`SyncMethodCopy`（types.go）。
- Produces: `type ConflictKind string`（常量 `ConflictPlainSame/ConflictPlainDiff/ConflictOrphanLink/ConflictBrokenLink/ConflictWrongTarget`，值 `plain_same|plain_diff|orphan_link|broken_link|wrong_target`）；`type ReconcileItem struct { Kind ConflictKind; SkillID, Directory, Path string; AgentIDs []string; SSOTHash, LocalHash string; Acknowledged bool }`（json tag 依次 `kind/skillId/directory/path/agentIds/ssotHash/localHash/acknowledged`，SkillID 与哈希字段 `omitempty`）；`func classifyCopyEntry(method SyncMethod, ssotPath, target, ssotHash string, ssotComplete bool) copyClass`，`copyClass{kind ConflictKind, localHash string}`（`kind==""` 表示无冲突）；测试助手 `mustSymlink(t, old, new string)`（symlink 不可用时 `t.Skipf`）。

- [ ] **Step 1: 写失败测试**（`internal/skills/reconcile_test.go`）

```go
package skills

import (
	"os"
	"path/filepath"
	"testing"
)

func mustSymlink(t *testing.T, oldname, newname string) {
	t.Helper()
	if err := os.Symlink(oldname, newname); err != nil {
		t.Skipf("symlink unavailable (need privilege/dev mode): %v", err)
	}
}

func TestClassifyCopyEntry(t *testing.T) {
	ssotPath := filepath.Join(t.TempDir(), "foo")
	if err := os.MkdirAll(ssotPath, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(ssotPath, "SKILL.md"), []byte("---\nname: foo\n---\n"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(ssotPath, "extra.sh"), []byte("echo hi\n"), 0644); err != nil {
		t.Fatal(err)
	}
	ssotHash, ssotOK := HashDir(ssotPath)
	if !ssotOK || ssotHash == "" {
		t.Fatalf("ssot hash setup failed: %q %v", ssotHash, ssotOK)
	}
	target := filepath.Join(t.TempDir(), "foo")

	t.Run("absent entry yields no conflict", func(t *testing.T) {
		if cl := classifyCopyEntry(SyncMethodSymlink, ssotPath, target, ssotHash, ssotOK); cl.kind != "" {
			t.Errorf("expected no conflict, got %q", cl.kind)
		}
	})

	t.Run("identical plain copy in symlink mode is plain_same", func(t *testing.T) {
		if err := copyDirRecursive(ssotPath, target); err != nil {
			t.Fatal(err)
		}
		defer os.RemoveAll(target)
		cl := classifyCopyEntry(SyncMethodSymlink, ssotPath, target, ssotHash, ssotOK)
		if cl.kind != ConflictPlainSame || cl.localHash != ssotHash {
			t.Errorf("got %+v, want plain_same with equal hash", cl)
		}
	})

	t.Run("same SKILL.md but different sidecar is plain_diff", func(t *testing.T) {
		if err := copyDirRecursive(ssotPath, target); err != nil {
			t.Fatal(err)
		}
		defer os.RemoveAll(target)
		if err := os.WriteFile(filepath.Join(target, "extra.sh"), []byte("echo BYE\n"), 0644); err != nil {
			t.Fatal(err)
		}
		cl := classifyCopyEntry(SyncMethodSymlink, ssotPath, target, ssotHash, ssotOK)
		if cl.kind != ConflictPlainDiff {
			t.Errorf("got %q, want plain_diff (D1: full-tree guard)", cl.kind)
		}
	})

	t.Run("identical plain copy in copy mode yields no conflict", func(t *testing.T) {
		if err := copyDirRecursive(ssotPath, target); err != nil {
			t.Fatal(err)
		}
		defer os.RemoveAll(target)
		if cl := classifyCopyEntry(SyncMethodCopy, ssotPath, target, ssotHash, ssotOK); cl.kind != "" {
			t.Errorf("copy mode projection must not be a conflict, got %q", cl.kind)
		}
	})

	t.Run("incomplete ssot hash forces plain_diff", func(t *testing.T) {
		if err := copyDirRecursive(ssotPath, target); err != nil {
			t.Fatal(err)
		}
		defer os.RemoveAll(target)
		cl := classifyCopyEntry(SyncMethodSymlink, ssotPath, target, ssotHash, false)
		if cl.kind != ConflictPlainDiff {
			t.Errorf("got %q, want plain_diff when ssot hash incomplete", cl.kind)
		}
	})

	t.Run("live correct link yields no conflict", func(t *testing.T) {
		mustSymlink(t, ssotPath, target)
		defer os.RemoveAll(target)
		if cl := classifyCopyEntry(SyncMethodSymlink, ssotPath, target, ssotHash, ssotOK); cl.kind != "" {
			t.Errorf("correct projection must not be a conflict, got %q", cl.kind)
		}
	})

	t.Run("dead link is broken_link", func(t *testing.T) {
		mustSymlink(t, filepath.Join(filepath.Dir(target), "no-such-target"), target)
		defer os.RemoveAll(target)
		cl := classifyCopyEntry(SyncMethodSymlink, ssotPath, target, ssotHash, ssotOK)
		if cl.kind != ConflictBrokenLink {
			t.Errorf("got %q, want broken_link", cl.kind)
		}
	})

	t.Run("live link to wrong target is wrong_target", func(t *testing.T) {
		other := filepath.Join(t.TempDir(), "other")
		if err := os.MkdirAll(other, 0755); err != nil {
			t.Fatal(err)
		}
		mustSymlink(t, other, target)
		defer os.RemoveAll(target)
		cl := classifyCopyEntry(SyncMethodSymlink, ssotPath, target, ssotHash, ssotOK)
		if cl.kind != ConflictWrongTarget {
			t.Errorf("got %q, want wrong_target", cl.kind)
		}
	})
}
```

- [ ] **Step 2: 跑测试确认失败**

Run: `go test ./internal/skills/ -run TestClassifyCopyEntry -count=1`
Expected: FAIL（`undefined: classifyCopyEntry` / `ConflictPlainSame`）

- [ ] **Step 3: 实现类型与分类器**

`internal/skills/types.go` 末尾追加：

```go
// ConflictKind 是对账扫描产出的条目分类。
type ConflictKind string

const (
	ConflictPlainSame   ConflictKind = "plain_same"
	ConflictPlainDiff   ConflictKind = "plain_diff"
	ConflictOrphanLink  ConflictKind = "orphan_link"
	ConflictBrokenLink  ConflictKind = "broken_link"
	ConflictWrongTarget ConflictKind = "wrong_target"
)

// ReconcileItem 是对账扫描的单条 (技能, agent 目录) 冲突。
type ReconcileItem struct {
	Kind         ConflictKind `json:"kind"`
	SkillID      string       `json:"skillId,omitempty"` // orphan_link 无 SSOT 技能，为空
	Directory    string       `json:"directory"`
	Path         string       `json:"path"`
	AgentIDs     []string     `json:"agentIds"`
	SSOTHash     string       `json:"ssotHash,omitempty"`
	LocalHash    string       `json:"localHash,omitempty"`
	Acknowledged bool         `json:"acknowledged"`
}
```

`internal/skills/reconcile.go`：

```go
package skills

import "os"
import "path/filepath"

type copyClass struct {
	kind      ConflictKind // "" = 无冲突
	localHash string
}

// classifyCopyEntry 判定 agent 目录中一个已存在条目与 SSOT 的关系。
// copy 模式下与 SSOT 全同的普通目录是正常投影（D5），不产冲突；
// symlink 模式下同状态是散装残留（plain_same）。
func classifyCopyEntry(method SyncMethod, ssotPath, target, ssotHash string, ssotComplete bool) copyClass {
	info, err := os.Lstat(target)
	if err != nil {
		return copyClass{}
	}
	if info.Mode()&os.ModeSymlink != 0 {
		if _, err := os.Stat(target); err != nil {
			return copyClass{kind: ConflictBrokenLink}
		}
		link, err := os.Readlink(target)
		if err != nil {
			return copyClass{kind: ConflictWrongTarget}
		}
		if !filepath.IsAbs(link) {
			link = filepath.Join(filepath.Dir(target), link)
		}
		a, aerr := filepath.Abs(link)
		b, berr := filepath.Abs(ssotPath)
		if aerr != nil || berr != nil || filepath.Clean(a) != filepath.Clean(b) {
			return copyClass{kind: ConflictWrongTarget}
		}
		return copyClass{}
	}
	localHash, complete := HashDir(target)
	if ssotComplete && complete && ssotHash != "" && localHash == ssotHash {
		if method == SyncMethodCopy {
			return copyClass{}
		}
		return copyClass{kind: ConflictPlainSame, localHash: localHash}
	}
	return copyClass{kind: ConflictPlainDiff, localHash: localHash}
}
```

- [ ] **Step 4: 跑测试确认通过**

Run: `go test ./internal/skills/ -run TestClassifyCopyEntry -count=1 -v`
Expected: PASS（全部子测试；symlink 权限不足时对应子测试 SKIP）

- [ ] **Step 5: Commit**

```bash
git add internal/skills/types.go internal/skills/reconcile.go internal/skills/reconcile_test.go docs/superpowers/plans/2026-09-24-skills-reconciliation-engine.md
git commit -m "feat(skills): add conflict kinds and full-tree copy classifier"
```

### Task 2: 分叉保留 ack 持久化

**Files:**
- Modify: `internal/skills/types.go`（追加 `ConflictAck`）
- Create: `internal/skills/conflictack.go`
- Test: `internal/skills/conflictack_test.go`

**Interfaces:**
- Consumes: 无新外部依赖（`encoding/json`/`os`/`log`/`filepath`）。
- Produces: `type ConflictAck struct { SSOTHash, LocalHash, CheckedAt string }`（json `ssotHash/localHash/checkedAt`）；`(ConflictAck).Matches(ssotHash, localHash string) bool`（双指纹全等且 SSOTHash 非空）；`conflictAckPath(ssotDir string) string`（= `filepath.Join(filepath.Dir(ssotDir), ".skill-conflict-ack.json")`）；`conflictKey(directory, target string) string`（= `directory + "|" + filepath.Clean(target)`）；`ReadConflictAcks(ssotDir string) map[string]ConflictAck`（缺失/损坏→空表）；`WriteConflictAck(ssotDir, key string, ack ConflictAck) error`；`DeleteConflictAck(ssotDir, key string) error`。

- [ ] **Step 1: 写失败测试**（`internal/skills/conflictack_test.go`）

```go
package skills

import (
	"os"
	"path/filepath"
	"testing"
)

func TestConflictAck_RoundtripAndDrift(t *testing.T) {
	ssotDir := filepath.Join(t.TempDir(), "skills")
	if err := os.MkdirAll(ssotDir, 0755); err != nil {
		t.Fatal(err)
	}
	key := conflictKey("foo", filepath.Join(filepath.Dir(ssotDir), "claude", "foo"))
	if got := ReadConflictAcks(ssotDir); len(got) != 0 {
		t.Fatalf("expected empty acks for missing file, got %d", len(got))
	}
	ack := ConflictAck{SSOTHash: "s1", LocalHash: "l1", CheckedAt: "2026-09-24T00:00:00Z"}
	if err := WriteConflictAck(ssotDir, key, ack); err != nil {
		t.Fatal(err)
	}
	got := ReadConflictAcks(ssotDir)
	if !got[key].Matches("s1", "l1") {
		t.Error("expected roundtripped ack to match identical fingerprints")
	}
	if got[key].Matches("s2", "l1") || got[key].Matches("s1", "l2") {
		t.Error("ack must fail to match when either fingerprint drifts")
	}
	if err := DeleteConflictAck(ssotDir, key); err != nil {
		t.Fatal(err)
	}
	if len(ReadConflictAcks(ssotDir)) != 0 {
		t.Error("expected empty after delete")
	}
}

func TestConflictAck_CorruptFileYieldsEmpty(t *testing.T) {
	ssotDir := filepath.Join(t.TempDir(), "skills")
	if err := os.MkdirAll(ssotDir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(conflictAckPath(ssotDir), []byte("{not json"), 0644); err != nil {
		t.Fatal(err)
	}
	if got := ReadConflictAcks(ssotDir); len(got) != 0 {
		t.Fatalf("corrupt ack file must yield empty map, got %d", len(got))
	}
}
```

- [ ] **Step 2: 跑测试确认失败**

Run: `go test ./internal/skills/ -run TestConflictAck -count=1`
Expected: FAIL（`undefined: conflictAckPath` 等）

- [ ] **Step 3: 实现**

`types.go` 追加：

```go
// ConflictAck 记录用户对一次内容分叉的"保留"决定。
// 双指纹：任一侧后续再漂移即失效（重新上屏）。
type ConflictAck struct {
	SSOTHash  string `json:"ssotHash"`
	LocalHash string `json:"localHash"`
	CheckedAt string `json:"checkedAt"`
}
```

`internal/skills/conflictack.go`：

```go
package skills

import (
	"encoding/json"
	"log"
	"os"
	"path/filepath"
)

// Matches 仅当两侧指纹与当前完全一致时成立。
func (a ConflictAck) Matches(ssotHash, localHash string) bool {
	return a.SSOTHash != "" && a.SSOTHash == ssotHash && a.LocalHash == localHash
}

func conflictAckPath(ssotDir string) string {
	return filepath.Join(filepath.Dir(ssotDir), ".skill-conflict-ack.json")
}

func conflictKey(directory, target string) string {
	return directory + "|" + filepath.Clean(target)
}

type conflictAckFile struct {
	Version int                    `json:"version"`
	Acks    map[string]ConflictAck `json:"acks"`
}

// ReadConflictAcks 读取保留记录；文件缺失或损坏返回空表，扫描不因此失败。
func ReadConflictAcks(ssotDir string) map[string]ConflictAck {
	data, err := os.ReadFile(conflictAckPath(ssotDir))
	if err != nil {
		return map[string]ConflictAck{}
	}
	var f conflictAckFile
	if err := json.Unmarshal(data, &f); err != nil {
		log.Printf("warn: corrupt conflict ack file ignored: %v", err)
		return map[string]ConflictAck{}
	}
	if f.Acks == nil {
		return map[string]ConflictAck{}
	}
	return f.Acks
}

func writeConflictAcks(ssotDir string, acks map[string]ConflictAck) error {
	data, err := json.MarshalIndent(conflictAckFile{Version: 1, Acks: acks}, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(conflictAckPath(ssotDir), data, 0644)
}

// WriteConflictAck 记录（或覆盖）一条保留决定。
func WriteConflictAck(ssotDir, key string, ack ConflictAck) error {
	acks := ReadConflictAcks(ssotDir)
	acks[key] = ack
	return writeConflictAcks(ssotDir, acks)
}

// DeleteConflictAck 删除一条保留决定（收编/覆盖后调用）。
func DeleteConflictAck(ssotDir, key string) error {
	acks := ReadConflictAcks(ssotDir)
	if _, ok := acks[key]; !ok {
		return nil
	}
	delete(acks, key)
	return writeConflictAcks(ssotDir, acks)
}
```

- [ ] **Step 4: 跑测试确认通过**

Run: `go test ./internal/skills/ -run TestConflictAck -count=1 -v`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add internal/skills/types.go internal/skills/conflictack.go internal/skills/conflictack_test.go
git commit -m "feat(skills): fingerprint-keyed conflict fork acknowledgement persistence"
```

### Task 3: Store.ScanConflicts 对账扫描

**Files:**
- Modify: `internal/skills/reconcile.go`（追加）
- Test: `internal/skills/reconcile_test.go`（追加夹具与测试）

**Interfaces:**
- Consumes: Task 1 `classifyCopyEntry`、Task 2 `ReadConflictAcks/conflictKey`、既有 `scanAgentSkillDirs(capableIDs, dirResolver)`（store.go:890，按绝对路径去重共享目录）、`setupSkillCapableAgentHome`/`newSkillTestRegistry`（store_test.go:230/241，同包复用）、`copyDirRecursive`、`agents.Registry.SkillCapableAgentIDs/AgentSkillsDir`、`ResolveSSOTDir(StorageUnified)`。
- Produces: `func (s *Store) ScanConflicts(reg *agents.Registry) []ReconcileItem`（只读；结果按 kind,directory,path 排序）；夹具 `type reconcileEnv struct { store *Store; reg *agents.Registry; home, ssotDir, claudeDir, codexDir string }` 与 `newReconcileEnv(t *testing.T) *reconcileEnv`、`writeReconcileSkill(t, dir, sidecar string)`。

**能力 ID 前置验证**（Step 1 内完成）：`internal/agents` 的 `computeSkillDirCache`（registry.go:318）以适配器 ID 为键；测试注册 `claude-code`（store_test.go:244 既有先例）、`claude-code-desktop`（store.go:103 注释证实其与 claude-code 共享 `~/.claude/skills`）、`codex`（本机 AgentPack 已绑定 `~/.codex/skills` 为实证）。夹具内以 `t.Fatalf` 断言 `AgentSkillsDir` 非空，ID 不符时测试立即失败并指出原因。

- [ ] **Step 1: 写失败测试**（`reconcile_test.go` 追加）

```go
import (
	"agentpack/internal/agents"
	// 既有 import 保持
)

type reconcileEnv struct {
	store     *Store
	reg       *agents.Registry
	home      string
	ssotDir   string
	claudeDir string
	codexDir  string
}

func newReconcileEnv(t *testing.T) *reconcileEnv {
	t.Helper()
	setupSkillCapableAgentHome(t)
	home, err := os.UserHomeDir()
	if err != nil {
		t.Fatal(err)
	}
	reg := agents.NewRegistry()
	reg.Register(agents.Agent{ID: "claude-code", Name: "Claude Code", Status: agents.StatusEnabled})
	reg.Register(agents.Agent{ID: "claude-code-desktop", Name: "Claude Code Desktop", Status: agents.StatusEnabled})
	reg.Register(agents.Agent{ID: "codex", Name: "Codex", Status: agents.StatusEnabled})
	claudeDir := reg.AgentSkillsDir("claude-code")
	codexDir := reg.AgentSkillsDir("codex")
	if claudeDir == "" || codexDir == "" {
		t.Fatalf("adapter IDs not skill-capable (claudeDir=%q codexDir=%q) — verify computeSkillDirCache keys", claudeDir, codexDir)
	}
	if reg.AgentSkillsDir("claude-code-desktop") != claudeDir {
		t.Fatal("claude-code-desktop must share claude skills dir (dedup fixture broken)")
	}
	ssotDir := ResolveSSOTDir(StorageUnified)
	// 目标 agent 目录预先建出：os.Symlink 的父目录必须存在，
	// 否则 mustSymlink 会因 ENOENT 跳过整测（假绿）。
	if err := os.MkdirAll(claudeDir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(codexDir, 0755); err != nil {
		t.Fatal(err)
	}
	return &reconcileEnv{
		store:     NewStore(ssotDir, SyncMethodSymlink),
		reg:       reg,
		home:      home,
		ssotDir:   ssotDir,
		claudeDir: claudeDir,
		codexDir:  codexDir,
	}
}

func writeReconcileSkill(t *testing.T, dir, sidecar string) {
	t.Helper()
	if err := os.MkdirAll(dir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "SKILL.md"), []byte("---\nname: test\n---\nbody\n"), 0644); err != nil {
		t.Fatal(err)
	}
	if sidecar != "" {
		if err := os.WriteFile(filepath.Join(dir, "extra.sh"), []byte(sidecar), 0644); err != nil {
			t.Fatal(err)
		}
	}
}

func findReconcileItem(items []ReconcileItem, kind ConflictKind, path string) (ReconcileItem, bool) {
	for _, it := range items {
		if it.Kind == kind && it.Path == path {
			return it, true
		}
	}
	return ReconcileItem{}, false
}

func TestScanConflicts_ClassifiesAllKinds(t *testing.T) {
	// 拆为两个子测（Ruling 4）：本机常无 SeCreateSymbolicLinkPrivilege，
	// monolithic 版本会在首个 mustSymlink 处整测 Skip，连不需要 symlink 的
	// 散装分类/去重/排除断言也一并丢失（证据假跳）。链接用例整体 Skip 时，
	// 散装用例仍须真实执行。
	t.Run("plain entries dedup and stray exclusion", func(t *testing.T) {
		env := newReconcileEnv(t)

		// alpha: claude 目录同内容散装副本 → plain_same（AgentIDs 含共享目录的两个 ID）
		alphaSSOT := filepath.Join(env.ssotDir, "alpha")
		writeReconcileSkill(t, alphaSSOT, "sidecar\n")
		if err := copyDirRecursive(alphaSSOT, filepath.Join(env.claudeDir, "alpha")); err != nil {
			t.Fatal(err)
		}

		// beta: claude 分叉散装 → plain_diff
		betaSSOT := filepath.Join(env.ssotDir, "beta")
		writeReconcileSkill(t, betaSSOT, "ssot-sidecar\n")
		writeReconcileSkill(t, filepath.Join(env.claudeDir, "beta"), "local-sidecar\n")

		// stray: claude 散装、SSOT 无同名 → 属未管理视图，不得出现
		writeReconcileSkill(t, filepath.Join(env.claudeDir, "stray"), "")

		if err := env.store.Load(env.reg); err != nil {
			t.Fatal(err)
		}
		items := env.store.ScanConflicts(env.reg)
		if len(items) != 2 {
			t.Fatalf("expected 2 items, got %d: %+v", len(items), items)
		}
		if _, ok := findReconcileItem(items, ConflictPlainDiff, filepath.Join(env.claudeDir, "stray")); ok {
			t.Error("plain dir without SSOT twin must not appear (belongs to unmanaged view)")
		}
		alpha, ok := findReconcileItem(items, ConflictPlainSame, filepath.Join(env.claudeDir, "alpha"))
		if !ok {
			t.Fatal("plain_same for claude/alpha not found")
		}
		if len(alpha.AgentIDs) != 2 {
			t.Errorf("shared dir must aggregate both agent IDs, got %v", alpha.AgentIDs)
		}
		if alpha.SkillID != "skill:alpha" || !stringsHas(alpha.AgentIDs, "claude-code") || !stringsHas(alpha.AgentIDs, "claude-code-desktop") {
			t.Errorf("unexpected alpha item: %+v", alpha)
		}
	})

	t.Run("link entries classification", func(t *testing.T) {
		env := newReconcileEnv(t)

		// beta: SSOT 有、codex 错链 → wrong_target
		betaSSOT := filepath.Join(env.ssotDir, "beta")
		writeReconcileSkill(t, betaSSOT, "ssot-sidecar\n")
		other := t.TempDir()
		otherSkill := filepath.Join(other, "beta")
		writeReconcileSkill(t, otherSkill, "other\n")
		mustSymlink(t, otherSkill, filepath.Join(env.codexDir, "beta"))

		// gamma: SSOT 有、codex 死链 → broken_link
		writeReconcileSkill(t, filepath.Join(env.ssotDir, "gamma"), "")
		mustSymlink(t, filepath.Join(env.ssotDir, "no-such-gamma"), filepath.Join(env.codexDir, "gamma"))

		// ghost: codex 死链、SSOT 无同名 → orphan_link
		mustSymlink(t, filepath.Join(env.ssotDir, "no-such-ghost"), filepath.Join(env.codexDir, "ghost"))

		if err := env.store.Load(env.reg); err != nil {
			t.Fatal(err)
		}
		items := env.store.ScanConflicts(env.reg)
		if len(items) != 3 {
			t.Fatalf("expected 3 items, got %d: %+v", len(items), items)
		}
		if _, ok := findReconcileItem(items, ConflictBrokenLink, filepath.Join(env.codexDir, "gamma")); !ok {
			t.Error("broken_link for codex/gamma not found")
		}
		ghost, ok := findReconcileItem(items, ConflictOrphanLink, filepath.Join(env.codexDir, "ghost"))
		if !ok || ghost.SkillID != "" {
			t.Errorf("orphan_link must have empty SkillID, got %+v", ghost)
		}
		if _, ok := findReconcileItem(items, ConflictWrongTarget, filepath.Join(env.codexDir, "beta")); !ok {
			t.Error("wrong_target for codex/beta not found")
		}
		for i := 1; i < len(items); i++ {
			prev, cur := items[i-1], items[i]
			if prev.Kind > cur.Kind || (prev.Kind == cur.Kind && prev.Directory > cur.Directory) {
				t.Errorf("items not sorted at %d: %q/%q before %q/%q", i, prev.Kind, prev.Directory, cur.Kind, cur.Directory)
			}
		}
	})
}

func stringsHas(list []string, v string) bool {
	for _, s := range list {
		if s == v {
			return true
		}
	}
	return false
}

func TestScanConflicts_AcknowledgeAndDrift(t *testing.T) {
	env := newReconcileEnv(t)
	writeReconcileSkill(t, filepath.Join(env.ssotDir, "fork"), "ssot-sidecar\n")
	local := filepath.Join(env.claudeDir, "fork")
	writeReconcileSkill(t, local, "local-sidecar\n")
	if err := env.store.Load(env.reg); err != nil {
		t.Fatal(err)
	}
	ssotHash, _ := HashDir(filepath.Join(env.ssotDir, "fork"))
	localHash, _ := HashDir(local)
	if err := WriteConflictAck(env.ssotDir, conflictKey("fork", local), ConflictAck{
		SSOTHash: ssotHash, LocalHash: localHash, CheckedAt: "2026-09-24T00:00:00Z",
	}); err != nil {
		t.Fatal(err)
	}
	items := env.store.ScanConflicts(env.reg)
	it, ok := findReconcileItem(items, ConflictPlainDiff, local)
	if !ok {
		t.Fatal("plain_diff for claude/fork not found")
	}
	if !it.Acknowledged {
		t.Error("matching ack must mark item acknowledged")
	}

	// 本地漂移 → ack 失效重新上屏
	if err := os.WriteFile(filepath.Join(local, "drift.txt"), []byte("x\n"), 0644); err != nil {
		t.Fatal(err)
	}
	items = env.store.ScanConflicts(env.reg)
	it, _ = findReconcileItem(items, ConflictPlainDiff, local)
	if it.Acknowledged {
		t.Error("local drift must un-acknowledge the fork")
	}

	// 复位本地后 SSOT 漂移 → 同样失效
	if err := os.Remove(filepath.Join(local, "drift.txt")); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(env.ssotDir, "fork", "drift2.txt"), []byte("y\n"), 0644); err != nil {
		t.Fatal(err)
	}
	items = env.store.ScanConflicts(env.reg)
	it, _ = findReconcileItem(items, ConflictPlainDiff, local)
	if it.Acknowledged {
		t.Error("ssot drift must un-acknowledge the fork")
	}
}
```

- [ ] **Step 2: 跑测试确认失败**

Run: `go test ./internal/skills/ -run 'TestScanConflicts' -count=1`
Expected: FAIL（`undefined: env.store.ScanConflicts`）

- [ ] **Step 3: 实现 ScanConflicts**（`reconcile.go` 追加；需补 import `agentpack/internal/agents`、`fmt`、`sort`、`strings`）

```go
// ScanConflicts 对账：分类 (SSOT 技能 × agent 目录) 中已存在的条目，
// 外加一轮原始目录扫描找孤儿死链。只读，不修改任何文件。
// 结果按 (kind, directory, path) 排序，多次调用顺序稳定。
func (s *Store) ScanConflicts(reg *agents.Registry) []ReconcileItem {
	s.mu.RLock()
	skills := make([]Skill, 0, len(s.skills))
	for _, sk := range s.skills {
		skills = append(skills, sk)
	}
	ssotDir := s.ssotDir
	method := s.syncMethod
	s.mu.RUnlock()

	acks := ReadConflictAcks(ssotDir)
	ssotDirs := make(map[string]bool, len(skills))
	for _, sk := range skills {
		ssotDirs[sk.Directory] = true
	}
	ssotHash := make(map[string]string, len(skills))
	ssotOK := make(map[string]bool, len(skills))
	for _, sk := range skills {
		h, ok := HashDir(filepath.Join(ssotDir, sk.Directory))
		ssotHash[sk.ID], ssotOK[sk.ID] = h, ok
	}

	var items []ReconcileItem
	for _, sd := range scanAgentSkillDirs(reg.SkillCapableAgentIDs(), reg.AgentSkillsDir) {
		for _, sk := range skills {
			target := filepath.Join(sd.dir, sk.Directory)
			cl := classifyCopyEntry(method, filepath.Join(ssotDir, sk.Directory), target, ssotHash[sk.ID], ssotOK[sk.ID])
			if cl.kind == "" {
				continue
			}
			items = append(items, ReconcileItem{
				Kind:         cl.kind,
				SkillID:      sk.ID,
				Directory:    sk.Directory,
				Path:         target,
				AgentIDs:     append([]string(nil), sd.agentIDs...),
				SSOTHash:     ssotHash[sk.ID],
				LocalHash:    cl.localHash,
				Acknowledged: acks[conflictKey(sk.Directory, target)].Matches(ssotHash[sk.ID], cl.localHash),
			})
		}
		// 孤儿死链：scanSkillEntries 会跳过死链，必须原始 ReadDir
		entries, err := os.ReadDir(sd.dir)
		if err != nil {
			continue
		}
		for _, entry := range entries {
			name := entry.Name()
			if strings.HasPrefix(name, ".") || ssotDirs[name] {
				continue
			}
			if entry.Type()&os.ModeSymlink == 0 {
				continue
			}
			path := filepath.Join(sd.dir, name)
			if _, err := os.Stat(path); err == nil {
				continue
			}
			items = append(items, ReconcileItem{
				Kind:      ConflictOrphanLink,
				Directory: name,
				Path:      path,
				AgentIDs:  append([]string(nil), sd.agentIDs...),
			})
		}
	}

	sort.Slice(items, func(i, j int) bool {
		if items[i].Kind != items[j].Kind {
			return items[i].Kind < items[j].Kind
		}
		if items[i].Directory != items[j].Directory {
			return items[i].Directory < items[j].Directory
		}
		return items[i].Path < items[j].Path
	})
	return items
}
```

- [ ] **Step 4: 跑测试确认通过（含既有全量防回归）**

Run: `go test ./internal/skills/ -count=1`
Expected: PASS（新测 + 既有全部；若夹具能力 ID 校验 `t.Fatalf`，按消息核对 `computeSkillDirCache` 键名并仅改夹具 ID）

- [ ] **Step 5: Commit**

```bash
git add internal/skills/reconcile.go internal/skills/reconcile_test.go
git commit -m "feat(skills): read-only reconciliation scan with five-kind classification"
```

### Task 4: 安全变更动作 — ConvertSkillCopyToLink 与 KeepSkillFork

**Files:**
- Modify: `internal/skills/reconcile.go`（追加）
- Test: `internal/skills/reconcile_test.go`（追加）

**Interfaces:**
- Consumes: Task 1/3 的 `classifyCopyEntry`/夹具；既有 `RemovePath`（sync.go）、`SyncToAgentDir(ssotPath, target, method)`（sync.go）、`HashDir`、`Store.importMu`/`mu` 锁序（与 ToggleAgent 相同：`importMu` 先于 `mu`，I/O 在 `mu` 外）。
- Produces:
  - `func (s *Store) ConvertSkillCopyToLink(skillID, sourcePath string, reg *agents.Registry) error` — 普通目录须全树哈希全同（D1 字节守卫）否则拒绝；死链/错链直接重建；成功后条目变为指向 SSOT 的正确投影。内存状态无需变更（「存在即绑定」推断在 plain/link 两态下同为 bound）。
  - `func (s *Store) KeepSkillFork(skillID, sourcePath string, reg *agents.Registry) error` — 仅接受与 SSOT 分叉的普通目录，写指纹 ack。
  - 内部助手 `lookupSkillPaths(skillID) (Skill, ssotPath string, method SyncMethod, err error)`（`mu.RLock` 快照）、`resolveAgentSkillPath(sourcePath, dirName string, reg) (canonical string, err error)`（校验 `sourcePath` 必须等于某 skill-capable 目录下 `dirName` 条目，返回按 `scanAgentSkillDirs` 同法构造的规范路径 —— 保证与 `ScanConflicts` 的 ack key 逐字节一致）。

- [ ] **Step 1: 写失败测试**（`reconcile_test.go` 追加）

```go
func TestConvertSkillCopyToLink(t *testing.T) {
	t.Run("identical plain copy becomes symlink", func(t *testing.T) {
		env := newReconcileEnv(t)
		ssot := filepath.Join(env.ssotDir, "alpha")
		writeReconcileSkill(t, ssot, "x\n")
		target := filepath.Join(env.claudeDir, "alpha")
		if err := copyDirRecursive(ssot, target); err != nil {
			t.Fatal(err)
		}
		if err := env.store.Load(env.reg); err != nil {
			t.Fatal(err)
		}
		if err := env.store.ConvertSkillCopyToLink("skill:alpha", target, env.reg); err != nil {
			t.Fatal(err)
		}
		info, err := os.Lstat(target)
		if err != nil {
			t.Fatal(err)
		}
		// 形状断言按主机能力门控（Ruling 5）：无 symlink 权限的主机上
		// createSymlink 走既有 copy fallback；下方内容断言（无条件）
		// 仍然完整验证投影语义，可授权主机上形状仍严格断言。
		if canCreateSymlinks(t) && info.Mode()&os.ModeSymlink == 0 {
			t.Fatal("expected target to become a symlink")
		}
		before, _ := HashDir(ssot)
		after, _ := HashDir(target)
		if before == "" || before != after {
			t.Errorf("content must be preserved after conversion: %q vs %q", before, after)
		}
	})

	t.Run("diverged plain copy refused and untouched", func(t *testing.T) {
		env := newReconcileEnv(t)
		ssot := filepath.Join(env.ssotDir, "beta")
		writeReconcileSkill(t, ssot, "ssot-sidecar\n")
		target := filepath.Join(env.claudeDir, "beta")
		writeReconcileSkill(t, target, "local-sidecar\n")
		if err := env.store.Load(env.reg); err != nil {
			t.Fatal(err)
		}
		localBefore, _ := HashDir(target)
		err := env.store.ConvertSkillCopyToLink("skill:beta", target, env.reg)
		if err == nil {
			t.Fatal("expected refusal for diverged copy (D1 byte guard)")
		}
		localAfter, _ := HashDir(target)
		if localBefore != localAfter {
			t.Error("refused conversion must not modify local content")
		}
		if info, lerr := os.Lstat(target); lerr != nil || info.Mode()&os.ModeSymlink != 0 {
			t.Errorf("entry must remain a plain dir, info=%v err=%v", info, lerr)
		}
	})

	t.Run("dead link repaired", func(t *testing.T) {
		env := newReconcileEnv(t)
		writeReconcileSkill(t, filepath.Join(env.ssotDir, "gamma"), "")
		target := filepath.Join(env.codexDir, "gamma")
		mustSymlink(t, filepath.Join(env.ssotDir, "no-such"), target)
		if err := env.store.Load(env.reg); err != nil {
			t.Fatal(err)
		}
		if err := env.store.ConvertSkillCopyToLink("skill:gamma", target, env.reg); err != nil {
			t.Fatal(err)
		}
		if _, err := os.Stat(target); err != nil {
			t.Errorf("link must resolve after conversion: %v", err)
		}
	})

	t.Run("wrong-target link relinked", func(t *testing.T) {
		env := newReconcileEnv(t)
		writeReconcileSkill(t, filepath.Join(env.ssotDir, "delta"), "")
		other := filepath.Join(t.TempDir(), "delta")
		writeReconcileSkill(t, other, "")
		target := filepath.Join(env.codexDir, "delta")
		mustSymlink(t, other, target)
		if err := env.store.Load(env.reg); err != nil {
			t.Fatal(err)
		}
		if err := env.store.ConvertSkillCopyToLink("skill:delta", target, env.reg); err != nil {
			t.Fatal(err)
		}
		link, err := os.Readlink(target)
		if err != nil {
			t.Fatal(err)
		}
		wantAbs, _ := filepath.Abs(filepath.Join(env.ssotDir, "delta"))
		gotAbs, _ := filepath.Abs(link)
		if filepath.Clean(wantAbs) != filepath.Clean(gotAbs) {
			t.Errorf("link must point at SSOT, got %q want %q", link, wantAbs)
		}
	})

	t.Run("path outside agent dirs rejected", func(t *testing.T) {
		env := newReconcileEnv(t)
		writeReconcileSkill(t, filepath.Join(env.ssotDir, "eps"), "")
		outsider := filepath.Join(t.TempDir(), "eps")
		writeReconcileSkill(t, outsider, "")
		if err := env.store.Load(env.reg); err != nil {
			t.Fatal(err)
		}
		if err := env.store.ConvertSkillCopyToLink("skill:eps", outsider, env.reg); err == nil {
			t.Fatal("expected error for path not inside any agent skills dir")
		}
	})

	t.Run("unknown skill rejected", func(t *testing.T) {
		env := newReconcileEnv(t)
		if err := env.store.Load(env.reg); err != nil {
			t.Fatal(err)
		}
		if err := env.store.ConvertSkillCopyToLink("skill:nope", filepath.Join(env.claudeDir, "nope"), env.reg); err == nil {
			t.Fatal("expected error for unknown skill ID")
		}
	})
}

func TestKeepSkillFork(t *testing.T) {
	t.Run("diverged fork records ack matching ScanConflicts", func(t *testing.T) {
		env := newReconcileEnv(t)
		writeReconcileSkill(t, filepath.Join(env.ssotDir, "fork"), "ssot-sidecar\n")
		local := filepath.Join(env.claudeDir, "fork")
		writeReconcileSkill(t, local, "local-sidecar\n")
		if err := env.store.Load(env.reg); err != nil {
			t.Fatal(err)
		}
		if err := env.store.KeepSkillFork("skill:fork", local, env.reg); err != nil {
			t.Fatal(err)
		}
		items := env.store.ScanConflicts(env.reg)
		it, ok := findReconcileItem(items, ConflictPlainDiff, local)
		if !ok {
			t.Fatal("plain_diff item not found after keep")
		}
		if !it.Acknowledged {
			t.Error("kept fork must be acknowledged in subsequent scan")
		}
	})

	t.Run("identical copy rejected without ack", func(t *testing.T) {
		env := newReconcileEnv(t)
		ssot := filepath.Join(env.ssotDir, "same")
		writeReconcileSkill(t, ssot, "x\n")
		target := filepath.Join(env.claudeDir, "same")
		if err := copyDirRecursive(ssot, target); err != nil {
			t.Fatal(err)
		}
		if err := env.store.Load(env.reg); err != nil {
			t.Fatal(err)
		}
		if err := env.store.KeepSkillFork("skill:same", target, env.reg); err == nil {
			t.Fatal("expected error when nothing diverges")
		}
		if acks := ReadConflictAcks(env.ssotDir); len(acks) != 0 {
			t.Errorf("no ack must be written, got %d", len(acks))
		}
	})

	t.Run("path outside agent dirs rejected", func(t *testing.T) {
		env := newReconcileEnv(t)
		writeReconcileSkill(t, filepath.Join(env.ssotDir, "k"), "a\n")
		outsider := filepath.Join(t.TempDir(), "k")
		writeReconcileSkill(t, outsider, "b\n")
		if err := env.store.Load(env.reg); err != nil {
			t.Fatal(err)
		}
		if err := env.store.KeepSkillFork("skill:k", outsider, env.reg); err == nil {
			t.Fatal("expected error for foreign path")
		}
	})
}

// canCreateSymlinks probes whether this host may create symlinks
// (Windows: SeCreateSymbolicLinkPrivilege or Developer Mode).
// 形状断言按其结果门控；内容/行为断言始终无条件执行（Ruling 5）。
func canCreateSymlinks(t *testing.T) bool {
	t.Helper()
	probe := filepath.Join(t.TempDir(), "probe")
	if err := os.Symlink("x", probe); err != nil {
		t.Logf("symlink unavailable, shape assertion skipped: %v", err)
		return false
	}
	return true
}
```

- [ ] **Step 2: 跑测试确认失败**

Run: `go test ./internal/skills/ -run 'TestConvertSkillCopyToLink|TestKeepSkillFork' -count=1`
Expected: FAIL（`undefined: ConvertSkillCopyToLink` / `KeepSkillFork`）

- [ ] **Step 3: 实现**（`reconcile.go` 追加；import 需含 `agentpack/internal/shared`（`agents`/`fmt` 由 Task 3 已引入；`log` 由 Task 5 的回滚路径引入，本任务**不要**加——未使用的 import 编译不过））

```go
// lookupSkillPaths 取技能的 SSOT 路径与同步方式（读锁快照）。
func (s *Store) lookupSkillPaths(skillID string) (Skill, string, SyncMethod, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	sk, ok := s.skills[skillID]
	if !ok {
		return Skill{}, "", "", fmt.Errorf("skill %s not found", skillID)
	}
	return sk, filepath.Join(s.ssotDir, sk.Directory), s.syncMethod, nil
}

// resolveAgentSkillPath 校验 sourcePath 必须等于 dirName 在某个
// skill-capable agent 目录下的条目；返回与 scan 相同构造法的规范路径。
func resolveAgentSkillPath(sourcePath, dirName string, reg *agents.Registry) (string, error) {
	abs, err := filepath.Abs(sourcePath)
	if err != nil {
		return "", err
	}
	abs = filepath.Clean(abs)
	for _, id := range reg.SkillCapableAgentIDs() {
		d := reg.AgentSkillsDir(id)
		if d == "" {
			continue
		}
		candidate := filepath.Join(d, dirName)
		cAbs, err := filepath.Abs(candidate)
		if err != nil {
			continue
		}
		if abs == filepath.Clean(cAbs) {
			return candidate, nil
		}
	}
	return "", fmt.Errorf("path %q is not entry %q of any agent skills directory", sourcePath, dirName)
}

// ConvertSkillCopyToLink 将 agent 目录中的条目转换为指向 SSOT 的正确投影。
// 字节守卫：普通目录必须与 SSOT 全树哈希完全一致（两侧均完整），否则拒绝——
// 内容分叉必须先经 AdoptSkillCopy / OverwriteSkillCopyFromSSOT 显式处置。
func (s *Store) ConvertSkillCopyToLink(skillID, sourcePath string, reg *agents.Registry) error {
	s.importMu.Lock()
	defer s.importMu.Unlock()

	sk, ssotPath, method, err := s.lookupSkillPaths(skillID)
	if err != nil {
		return err
	}
	target, err := resolveAgentSkillPath(sourcePath, sk.Directory, reg)
	if err != nil {
		return err
	}
	info, lerr := os.Lstat(target)
	if lerr != nil {
		return fmt.Errorf("entry %q not found: %w", target, lerr)
	}
	if info.Mode()&os.ModeSymlink == 0 {
		ssotHash, ssotOK := HashDir(ssotPath)
		localHash, localOK := HashDir(target)
		if !ssotOK || !localOK || ssotHash == "" || localHash != ssotHash {
			return fmt.Errorf("local copy of %q differs from SSOT; choose adopt or overwrite first", sk.Directory)
		}
	}
	if err := RemovePath(target); err != nil {
		return fmt.Errorf("remove entry: %w", err)
	}
	if err := SyncToAgentDir(ssotPath, target, method); err != nil {
		return fmt.Errorf("create projection: %w", err)
	}
	return nil
}

// KeepSkillFork 记录一次内容分叉的"保留"决定（指纹级 ack，
// 任一侧后续再漂移会重新出现在对账面板）。
func (s *Store) KeepSkillFork(skillID, sourcePath string, reg *agents.Registry) error {
	s.importMu.Lock()
	defer s.importMu.Unlock()

	sk, ssotPath, _, err := s.lookupSkillPaths(skillID)
	if err != nil {
		return err
	}
	target, err := resolveAgentSkillPath(sourcePath, sk.Directory, reg)
	if err != nil {
		return err
	}
	info, lerr := os.Lstat(target)
	if lerr != nil {
		return fmt.Errorf("entry %q not found: %w", target, lerr)
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("entry %q is a link, nothing to keep", target)
	}
	ssotHash, ssotOK := HashDir(ssotPath)
	localHash, localOK := HashDir(target)
	if ssotOK && localOK && localHash == ssotHash {
		return fmt.Errorf("contents of %q are identical to SSOT; nothing to keep", sk.Directory)
	}
	ack := ConflictAck{
		SSOTHash:  ssotHash,
		LocalHash: localHash,
		CheckedAt: shared.NowRFC3339(),
	}
	return WriteConflictAck(s.ssotDir, conflictKey(sk.Directory, target), ack)
}
```

- [ ] **Step 4: 跑测试确认通过**

Run: `go test ./internal/skills/ -count=1`
Expected: PASS（新测 + Task 1–3 + 既有全部）

- [ ] **Step 5: Commit**

```bash
git add internal/skills/reconcile.go internal/skills/reconcile_test.go
git commit -m "feat(skills): byte-guarded convert and fingerprint ack for kept forks"
```

### Task 5: 破坏性变更动作 — OverwriteSkillCopyFromSSOT 与 AdoptSkillCopy

**Files:**
- Modify: `internal/skills/reconcile.go`（追加）
- Test: `internal/skills/reconcile_test.go`（追加）

**Interfaces:**
- Consumes: Task 4 助手；既有 `BackupSkillDir(ssotDir, backupDir, dirName) (string, error)`（sync.go:382）、`copyDirRecursive`（sync.go:132）、`HasSkillManifest`（manifest.go:109）、`refreshSkillAfterFileUpdate(skillID, ssotPath) (Skill, error)`（update.go:1113，内部自取 `s.mu`，调用时必须在 `mu` 外）、`DeleteConflictAck`。
- Produces:
  - `func (s *Store) OverwriteSkillCopyFromSSOT(skillID, sourcePath string, reg *agents.Registry) error` — 删除本地条目并以 SSOT 重建投影（无字节守卫，这是分叉三选的「SSOT 覆盖」显式授权路径）；顺带删掉该 key 的 stale ack。
  - `func (s *Store) AdoptSkillCopy(skillID, sourcePath string, reg *agents.Registry) (Skill, error)` — 本地收编：先 `BackupSkillDir` 备份原 SSOT（**备份失败即中止，SSOT 一字不动**）→ 删 SSOT → `copyDirRecursive(local→SSOT)`（失败则尽力从备份回滚）→ `refreshSkillAfterFileUpdate` 刷指纹 → 来源转投影 → 删 ack。
  - `backupDir` 约定 = `filepath.Join(filepath.Dir(s.ssotDir), "skill-backups")`（与 Uninstall 同址）。

- [ ] **Step 1: 写失败测试**（`reconcile_test.go` 追加）

```go
func TestOverwriteSkillCopyFromSSOT(t *testing.T) {
	t.Run("diverged plain replaced by SSOT projection", func(t *testing.T) {
		env := newReconcileEnv(t)
		ssot := filepath.Join(env.ssotDir, "beta")
		writeReconcileSkill(t, ssot, "ssot-sidecar\n")
		target := filepath.Join(env.claudeDir, "beta")
		writeReconcileSkill(t, target, "local-sidecar\n")
		if err := env.store.Load(env.reg); err != nil {
			t.Fatal(err)
		}
		ssotHash, _ := HashDir(ssot)
		if err := env.store.OverwriteSkillCopyFromSSOT("skill:beta", target, env.reg); err != nil {
			t.Fatal(err)
		}
		info, err := os.Lstat(target)
		if err != nil {
			t.Fatal(err)
		}
		if canCreateSymlinks(t) && info.Mode()&os.ModeSymlink == 0 {
			t.Fatal("expected projection after overwrite")
		}
		got, _ := HashDir(target)
		if got != ssotHash {
			t.Errorf("content must equal SSOT after overwrite: %q vs %q", got, ssotHash)
		}
	})

	t.Run("missing entry rejected", func(t *testing.T) {
		env := newReconcileEnv(t)
		writeReconcileSkill(t, filepath.Join(env.ssotDir, "gamma"), "")
		if err := env.store.Load(env.reg); err != nil {
			t.Fatal(err)
		}
		err := env.store.OverwriteSkillCopyFromSSOT("skill:gamma", filepath.Join(env.claudeDir, "gamma"), env.reg)
		if err == nil {
			t.Fatal("expected error when entry absent")
		}
	})

	t.Run("identical plain replaced without guard", func(t *testing.T) {
		env := newReconcileEnv(t)
		ssot := filepath.Join(env.ssotDir, "same")
		writeReconcileSkill(t, ssot, "x\n")
		target := filepath.Join(env.claudeDir, "same")
		if err := copyDirRecursive(ssot, target); err != nil {
			t.Fatal(err)
		}
		if err := env.store.Load(env.reg); err != nil {
			t.Fatal(err)
		}
		if err := env.store.OverwriteSkillCopyFromSSOT("skill:same", target, env.reg); err != nil {
			t.Fatal(err)
		}
		if info, _ := os.Lstat(target); canCreateSymlinks(t) && info.Mode()&os.ModeSymlink == 0 {
			t.Error("expected symlink projection")
		}
	})
}

func TestAdoptSkillCopy(t *testing.T) {
	t.Run("diverged local adopted, source linked, backup created", func(t *testing.T) {
		env := newReconcileEnv(t)
		ssot := filepath.Join(env.ssotDir, "beta")
		writeReconcileSkill(t, ssot, "ssot-sidecar\n")
		target := filepath.Join(env.claudeDir, "beta")
		writeReconcileSkill(t, target, "local-sidecar\n")
		if err := env.store.Load(env.reg); err != nil {
			t.Fatal(err)
		}
		sk, err := env.store.AdoptSkillCopy("skill:beta", target, env.reg)
		if err != nil {
			t.Fatal(err)
		}
		got, err := os.ReadFile(filepath.Join(ssot, "extra.sh"))
		if err != nil {
			t.Fatal(err)
		}
		if string(got) != "local-sidecar\n" {
			t.Errorf("SSOT must carry adopted local content, got %q", got)
		}
		if info, _ := os.Lstat(target); canCreateSymlinks(t) && info.Mode()&os.ModeSymlink == 0 {
			t.Error("source must become a projection after adopt")
		}
		fresh, _ := HashDir(ssot)
		if sk.ContentHash != fresh {
			t.Errorf("returned skill hash stale: %q vs %q", sk.ContentHash, fresh)
		}
		entries, err := os.ReadDir(filepath.Join(env.home, ".agents", "skill-backups"))
		if err != nil || len(entries) == 0 {
			t.Errorf("expected a backup of original SSOT, err=%v entries=%d", err, len(entries))
		}
	})

	t.Run("identical content rejected with guidance", func(t *testing.T) {
		env := newReconcileEnv(t)
		ssot := filepath.Join(env.ssotDir, "same")
		writeReconcileSkill(t, ssot, "x\n")
		target := filepath.Join(env.claudeDir, "same")
		if err := copyDirRecursive(ssot, target); err != nil {
			t.Fatal(err)
		}
		if err := env.store.Load(env.reg); err != nil {
			t.Fatal(err)
		}
		if _, err := env.store.AdoptSkillCopy("skill:same", target, env.reg); err == nil {
			t.Fatal("expected error: identical content should use convert instead")
		}
	})

	t.Run("backup failure aborts without touching SSOT", func(t *testing.T) {
		env := newReconcileEnv(t)
		ssot := filepath.Join(env.ssotDir, "beta")
		writeReconcileSkill(t, ssot, "ssot-sidecar\n")
		target := filepath.Join(env.claudeDir, "beta")
		writeReconcileSkill(t, target, "local-sidecar\n")
		// 用普通文件占住 skill-backups 路径，强制造错
		blocker := filepath.Join(env.home, ".agents", "skill-backups")
		if err := os.WriteFile(blocker, []byte("occupied"), 0644); err != nil {
			t.Fatal(err)
		}
		if err := env.store.Load(env.reg); err != nil {
			t.Fatal(err)
		}
		ssotBefore, _ := HashDir(ssot)
		localBefore, _ := HashDir(target)
		if _, err := env.store.AdoptSkillCopy("skill:beta", target, env.reg); err == nil {
			t.Fatal("expected adopt to abort on backup failure")
		}
		ssotAfter, _ := HashDir(ssot)
		if ssotBefore != ssotAfter {
			t.Error("SSOT must remain untouched when backup fails")
		}
		localAfter, _ := HashDir(target)
		if localAfter != localBefore {
			t.Error("local source must remain untouched on abort")
		}
	})
}
```

- [ ] **Step 2: 跑测试确认失败**

Run: `go test ./internal/skills/ -run 'TestOverwriteSkillCopyFromSSOT|TestAdoptSkillCopy' -count=1`
Expected: FAIL（`undefined: OverwriteSkillCopyFromSSOT` / `AdoptSkillCopy`）

- [ ] **Step 3: 实现**（`reconcile.go` 追加）

```go
// OverwriteSkillCopyFromSSOT 用 SSOT 内容覆盖 agent 目录中的条目并重建投影。
// 分叉三选中的「SSOT 覆盖」显式授权路径：本地分叉内容会被删除。
func (s *Store) OverwriteSkillCopyFromSSOT(skillID, sourcePath string, reg *agents.Registry) error {
	s.importMu.Lock()
	defer s.importMu.Unlock()

	sk, ssotPath, method, err := s.lookupSkillPaths(skillID)
	if err != nil {
		return err
	}
	target, err := resolveAgentSkillPath(sourcePath, sk.Directory, reg)
	if err != nil {
		return err
	}
	if _, err := os.Lstat(target); err != nil {
		return fmt.Errorf("entry %q not found: %w", target, err)
	}
	_ = DeleteConflictAck(s.ssotDir, conflictKey(sk.Directory, target))
	if err := RemovePath(target); err != nil {
		return fmt.Errorf("remove entry: %w", err)
	}
	if err := SyncToAgentDir(ssotPath, target, method); err != nil {
		return fmt.Errorf("create projection: %w", err)
	}
	return nil
}

// AdoptSkillCopy 用 agent 目录中的散装副本内容覆盖 SSOT（分叉三选之「本地收编」）。
// 覆盖前备份原 SSOT 到 skill-backups；备份失败即中止，绝不丢数据。
func (s *Store) AdoptSkillCopy(skillID, sourcePath string, reg *agents.Registry) (Skill, error) {
	s.importMu.Lock()
	defer s.importMu.Unlock()

	sk, ssotPath, method, err := s.lookupSkillPaths(skillID)
	if err != nil {
		return Skill{}, err
	}
	target, err := resolveAgentSkillPath(sourcePath, sk.Directory, reg)
	if err != nil {
		return Skill{}, err
	}
	info, lerr := os.Lstat(target)
	if lerr != nil {
		return Skill{}, fmt.Errorf("entry %q not found: %w", target, lerr)
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return Skill{}, fmt.Errorf("entry %q is a link, nothing to adopt", target)
	}
	if !HasSkillManifest(target) {
		return Skill{}, fmt.Errorf("entry %q has no SKILL.md", target)
	}
	ssotHash, ssotOK := HashDir(ssotPath)
	localHash, localOK := HashDir(target)
	if ssotOK && localOK && localHash == ssotHash {
		return Skill{}, fmt.Errorf("contents of %q are identical to SSOT; use convert instead", sk.Directory)
	}
	backupDir := filepath.Join(filepath.Dir(s.ssotDir), "skill-backups")
	backupPath, backupErr := BackupSkillDir(s.ssotDir, backupDir, sk.Directory)
	if backupErr != nil {
		return Skill{}, fmt.Errorf("backup SSOT before adopt (aborted): %w", backupErr)
	}
	if err := RemovePath(ssotPath); err != nil {
		return Skill{}, fmt.Errorf("remove SSOT (backup at %s): %w", backupPath, err)
	}
	if err := copyDirRecursive(target, ssotPath); err != nil {
		if backupPath != "" {
			if berr := copyDirRecursive(backupPath, ssotPath); berr != nil {
				log.Printf("adopt: restore from backup %s failed: %v", backupPath, berr)
			}
		}
		return Skill{}, fmt.Errorf("copy local into SSOT: %w", err)
	}
	_ = DeleteConflictAck(s.ssotDir, conflictKey(sk.Directory, target))
	refreshed, err := s.refreshSkillAfterFileUpdate(skillID, ssotPath)
	if err != nil {
		return Skill{}, err
	}
	if err := SyncToAgentDir(ssotPath, target, method); err != nil {
		return refreshed, fmt.Errorf("SSOT adopted, but projection refresh failed: %w", err)
	}
	return refreshed, nil
}
```

- [ ] **Step 4: 跑测试确认通过**

Run: `go test ./internal/skills/ -count=1`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add internal/skills/reconcile.go internal/skills/reconcile_test.go
git commit -m "feat(skills): guarded overwrite and backup-first local adoption"
```

### Task 6: 孤儿死链清理与 ToggleAgent 数据丢失守卫

**Files:**
- Modify: `internal/skills/reconcile.go`（追加 `CleanOrphanSkillLinks`）
- Modify: `internal/skills/store.go`（仅 `ToggleAgent` disable 分支，原 571-576 行区间）
- Test: `internal/skills/reconcile_test.go`（追加）

**Interfaces:**
- Consumes: Task 3 夹具；既有 `scanAgentSkillDirs`、`RemovePath`、`HashDir`。
- Produces:
  - `func (s *Store) CleanOrphanSkillLinks(reg *agents.Registry) ([]string, error)` — 只删「条目是死链 ∧ 名字不在 SSOT 技能集 ∧ 非隐藏」；活链、普通目录、SSOT 同名条目一律不动；返回已删路径（排序）；部分失败聚合成 error 但保留已删清单。
  - `ToggleAgent` 行为变更：disable 时若 `Lstat` 显示目标为**普通目录**且与 SSOT 全树哈希不一致（或任一侧不完整）→ 返回错误拒绝删除；活链与同内容普通目录行为不变。

- [ ] **Step 1: 写失败测试**（`reconcile_test.go` 追加）

```go
func TestCleanOrphanSkillLinks_RemovesDeadLinksOnly(t *testing.T) {
	env := newReconcileEnv(t)
	writeReconcileSkill(t, filepath.Join(env.ssotDir, "delta"), "")

	// 1) 孤儿死链 → 删除
	ghost := filepath.Join(env.claudeDir, "ghost")
	mustSymlink(t, filepath.Join(env.ssotDir, "no-such-ghost"), ghost)
	// 2) SSOT 同名死链 → 保留（归 resync/convert 管辖）
	broken := filepath.Join(env.claudeDir, "delta")
	mustSymlink(t, filepath.Join(env.ssotDir, "no-such-delta"), broken)
	// 3) 活的外链条目（不在 SSOT）→ 保留
	elsewhere := filepath.Join(t.TempDir(), "ext")
	writeReconcileSkill(t, elsewhere, "")
	live := filepath.Join(env.claudeDir, "ext")
	mustSymlink(t, elsewhere, live)
	// 4) 普通散装目录（不在 SSOT）→ 保留
	stray := filepath.Join(env.claudeDir, "stray")
	writeReconcileSkill(t, stray, "")

	if err := env.store.Load(env.reg); err != nil {
		t.Fatal(err)
	}
	removed, err := env.store.CleanOrphanSkillLinks(env.reg)
	if err != nil {
		t.Fatal(err)
	}
	// claude-code 与 claude-code-desktop 共享同一物理目录：
	// 只注册两个 ID 也必须只删一次（Review Focus 4）
	if len(removed) != 1 || removed[0] != ghost {
		t.Fatalf("expected exactly [%s], got %v", ghost, removed)
	}
	if _, err := os.Lstat(ghost); !os.IsNotExist(err) {
		t.Error("orphan dead link must be removed")
	}
	for _, keep := range []string{broken, live, stray} {
		if _, err := os.Lstat(keep); err != nil {
			t.Errorf("entry %s must survive cleanup: %v", keep, err)
		}
	}
}

func TestToggleAgent_GuardAgainstDeletingDivergedCopy(t *testing.T) {
	t.Run("diverged plain copy refuses disable", func(t *testing.T) {
		env := newReconcileEnv(t)
		writeReconcileSkill(t, filepath.Join(env.ssotDir, "gamma2"), "ssot\n")
		local := filepath.Join(env.claudeDir, "gamma2")
		writeReconcileSkill(t, local, "local\n")
		if err := env.store.Load(env.reg); err != nil {
			t.Fatal(err)
		}
		localBefore, _ := HashDir(local)
		err := env.store.ToggleAgent("skill:gamma2", "claude-code", false, env.reg)
		if err == nil {
			t.Fatal("expected refusal: disable must not delete diverged local edits")
		}
		localAfter, _ := HashDir(local)
		if localBefore != localAfter {
			t.Error("local content must be intact after refused disable")
		}
	})

	t.Run("identical plain copy disables normally", func(t *testing.T) {
		env := newReconcileEnv(t)
		ssot := filepath.Join(env.ssotDir, "gamma3")
		writeReconcileSkill(t, ssot, "x\n")
		local := filepath.Join(env.claudeDir, "gamma3")
		if err := copyDirRecursive(ssot, local); err != nil {
			t.Fatal(err)
		}
		if err := env.store.Load(env.reg); err != nil {
			t.Fatal(err)
		}
		if err := env.store.ToggleAgent("skill:gamma3", "claude-code", false, env.reg); err != nil {
			t.Fatal(err)
		}
		if _, err := os.Lstat(local); !os.IsNotExist(err) {
			t.Error("identical copy must be removable on disable")
		}
	})

	t.Run("bound symlink disables normally", func(t *testing.T) {
		env := newReconcileEnv(t)
		writeReconcileSkill(t, filepath.Join(env.ssotDir, "gamma4"), "")
		if err := env.store.Load(env.reg); err != nil {
			t.Fatal(err)
		}
		if err := env.store.ToggleAgent("skill:gamma4", "claude-code", true, env.reg); err != nil {
			t.Fatal(err)
		}
		target := filepath.Join(env.claudeDir, "gamma4")
		if err := env.store.ToggleAgent("skill:gamma4", "claude-code", false, env.reg); err != nil {
			t.Fatal(err)
		}
		if _, err := os.Lstat(target); !os.IsNotExist(err) {
			t.Error("symlink must be removed on disable")
		}
	})
}
```

- [ ] **Step 2: 跑测试确认失败**

Run: `go test ./internal/skills/ -run 'TestCleanOrphanSkillLinks|TestToggleAgent_Guard' -count=1`
Expected: FAIL（`undefined: CleanOrphanSkillLinks`；guard 用例因 `ToggleAgent` 无守卫而 `err == nil` 失败）

- [ ] **Step 3: 实现 A — `reconcile.go` 追加 `CleanOrphanSkillLinks`**

```go
// CleanOrphanSkillLinks 删除「目标已死且 SSOT 无同名技能」的孤儿死链。
// 只删死链：活链、普通目录、SSOT 同名条目一律不动。
// 返回实际删除的路径（按路径排序）；部分失败时已删清单照常返回。
func (s *Store) CleanOrphanSkillLinks(reg *agents.Registry) ([]string, error) {
	s.mu.RLock()
	ssotDirs := make(map[string]bool, len(s.skills))
	for _, sk := range s.skills {
		ssotDirs[sk.Directory] = true
	}
	s.mu.RUnlock()

	var removed []string
	var errs []string
	for _, sd := range scanAgentSkillDirs(reg.SkillCapableAgentIDs(), reg.AgentSkillsDir) {
		entries, err := os.ReadDir(sd.dir)
		if err != nil {
			continue
		}
		for _, entry := range entries {
			name := entry.Name()
			if strings.HasPrefix(name, ".") || ssotDirs[name] {
				continue
			}
			if entry.Type()&os.ModeSymlink == 0 {
				continue
			}
			path := filepath.Join(sd.dir, name)
			if _, err := os.Stat(path); err == nil {
				continue
			}
			if err := RemovePath(path); err != nil {
				errs = append(errs, fmt.Sprintf("%s: %v", path, err))
				continue
			}
			removed = append(removed, path)
		}
	}
	sort.Strings(removed)
	if len(errs) > 0 {
		return removed, fmt.Errorf("clean orphan links: %s", strings.Join(errs, "; "))
	}
	return removed, nil
}
```

- [ ] **Step 4: 实现 B — `store.go` `ToggleAgent` disable 分支加守卫**

定位 disable 分支（`store.go` 原 571-576 行，`} else {` 内注释 `// Disable: remove from agent dir` 之后紧跟 `if err := RemovePath(target); err != nil {`）。将该 `RemovePath` 调用替换为：

```go
		// Disable: remove from agent dir。
		// 字节守卫：与 SSOT 存在内容分叉的散装副本禁止直接删除——
		// 删除会永久丢失本地改动；先在对账面板选择处置方式。
		if info, lerr := os.Lstat(target); lerr == nil && info.Mode()&os.ModeSymlink == 0 {
			ssotHash, sOK := HashDir(ssotPath)
			localHash, lOK := HashDir(target)
			if !sOK || !lOK || localHash == "" || localHash != ssotHash {
				return fmt.Errorf("local copy of %q differs from SSOT; resolve the conflict before unbinding", sk.Directory)
			}
		}
		if err := RemovePath(target); err != nil {
			return fmt.Errorf("remove from agent dir: %w", err)
		}
```

（`sk`/`ssotPath` 在函数前半已取到，可直接引用；仅改 `else` 分支，enable 分支与锁序不动。）

- [ ] **Step 5: 跑测试确认通过（全包防回归）**

Run: `go test ./internal/skills/ -count=1`
Expected: PASS（新测 + `TestToggleAgent*` 既有用例全部通过）

- [ ] **Step 6: Commit**

```bash
git add internal/skills/reconcile.go internal/skills/store.go internal/skills/reconcile_test.go
git commit -m "feat(skills): orphan dead-link cleanup and diverged-copy delete guard"
```

### Task 7: App 服务方法与 bindings 重新生成

**Files:**
- Modify: `app.go`（仅在 `ScanUnmanagedSkills` 方法结束之后、`MigrateSkillStorage` 之前插入 6 个方法）
- Regenerate: `frontend/bindings/**`（gitignore，不入 git）

**Interfaces:**
- Consumes: Task 3-6 的 Store 方法；既有 `a.assertInit()`、`a.mu`/`a.closed`/`a.skillsStore`/`a.registry`、`a.withSkillsStore(fn func(*skills.Store) error) error`（app.go:512）、`a.emitLocked("skills:changed", ...)`。
- Produces（Wails v3 service 方法 → bindings 导出名）:
  - `func (a *App) ScanSkillConflicts() ([]skills.ReconcileItem, error)`
  - `func (a *App) ConvertSkillCopyToLink(skillID, sourcePath string) error`
  - `func (a *App) OverwriteSkillCopyFromSSOT(skillID, sourcePath string) error`
  - `func (a *App) AdoptSkillCopy(skillID, sourcePath string) (skills.Skill, error)` — 成功后 `emitLocked("skills:changed", ss.List())`（SSOT 内容变化）。
  - `func (a *App) KeepSkillFork(skillID, sourcePath string) error`
  - `func (a *App) CleanOrphanSkillLinks() ([]string, error)`
  - 事件约定：仅 Adopt 发 `skills:changed`（其余方法不改变 `List()` 所需状态）；前端每个动作后自行重扫 `ScanSkillConflicts`，不新增事件。

- [ ] **Step 1: 插入方法**（锚点为以下唯一文本，插在其后）

```go
	return a.skillsStore.ScanUnmanaged(a.registry), nil
}

func (a *App) MigrateSkillStorage(target string) (skills.MigrationResult, error) {
```

插入内容：

```go

// ScanSkillConflicts returns per-agent-directory skill copy conflicts
// (plain residual copies / diverged copies / dead or mis-pointed links).
// Read-only operation; results are sorted and deterministic.
func (a *App) ScanSkillConflicts() ([]skills.ReconcileItem, error) {
	if err := a.assertInit(); err != nil {
		return nil, err
	}
	a.mu.RLock()
	defer a.mu.RUnlock()
	if a.closed {
		return nil, fmt.Errorf("app is shutting down")
	}
	if a.skillsStore == nil {
		return nil, fmt.Errorf("skills store not initialized")
	}
	return a.skillsStore.ScanConflicts(a.registry), nil
}

// ConvertSkillCopyToLink converts an agent-dir entry into a correct SSOT
// projection. Refuses byte-diverged plain copies (D1 byte guard).
func (a *App) ConvertSkillCopyToLink(skillID, sourcePath string) error {
	return a.withSkillsStore(func(ss *skills.Store) error {
		return ss.ConvertSkillCopyToLink(skillID, sourcePath, a.registry)
	})
}

// OverwriteSkillCopyFromSSOT replaces a diverged local copy with SSOT content
// and rebuilds the projection (explicit fork-resolution choice).
func (a *App) OverwriteSkillCopyFromSSOT(skillID, sourcePath string) error {
	return a.withSkillsStore(func(ss *skills.Store) error {
		return ss.OverwriteSkillCopyFromSSOT(skillID, sourcePath, a.registry)
	})
}

// AdoptSkillCopy overwrites SSOT with the local copy (backing up the original
// first, aborting if the backup fails) and returns the refreshed skill.
func (a *App) AdoptSkillCopy(skillID, sourcePath string) (skills.Skill, error) {
	var sk skills.Skill
	err := a.withSkillsStore(func(ss *skills.Store) error {
		var e error
		sk, e = ss.AdoptSkillCopy(skillID, sourcePath, a.registry)
		if e != nil {
			return e
		}
		a.emitLocked("skills:changed", ss.List())
		return nil
	})
	return sk, err
}

// KeepSkillFork records an explicit decision to retain a diverged copy
// (fingerprint-keyed; auto re-surfaces when either side drifts).
func (a *App) KeepSkillFork(skillID, sourcePath string) error {
	return a.withSkillsStore(func(ss *skills.Store) error {
		return ss.KeepSkillFork(skillID, sourcePath, a.registry)
	})
}

// CleanOrphanSkillLinks removes dead symlinks whose names are absent from the
// SSOT. Returns the paths actually removed.
func (a *App) CleanOrphanSkillLinks() ([]string, error) {
	var removed []string
	err := a.withSkillsStore(func(ss *skills.Store) error {
		var e error
		removed, e = ss.CleanOrphanSkillLinks(a.registry)
		return e
	})
	return removed, err
}
```

- [ ] **Step 2: 编译 + vet**

Run: `go build ./... && go vet ./...`
Expected: exit 0（无新告警；方法为薄壳，逻辑已在 skills 包测试覆盖）

- [ ] **Step 3: 重新生成 bindings 并验证**

Run: `wails3 generate bindings`
Run: `Select-String -Path frontend\bindings\agentpack\app.ts -Pattern "ScanSkillConflicts|AdoptSkillCopy|CleanOrphanSkillLinks"`
Expected: 三个（以上）符号均出现在生成文件中；`git status --short frontend/bindings` 无输出（已 gitignore；用 `git check-ignore frontend/bindings` 自证）

- [ ] **Step 4: Commit（app.go 单文件；bindings 永不入 git）**

```bash
git add app.go
git commit -m "feat(app): expose reconciliation service methods for skills"
```

### Task 8: 前端 API 与状态层（api.ts + skills.ts）

**Files:**
- Modify: `frontend/src/lib/api.ts`
- Modify: `frontend/src/stores/skills.ts`

**Interfaces:**
- Consumes: Task 7 bindings 导出名（`ScanSkillConflicts`、`ConvertSkillCopyToLink`、`OverwriteSkillCopyFromSSOT`、`AdoptSkillCopy`、`KeepSkillFork`、`CleanOrphanSkillLinks`）；既有 `safeCall`、`optimizeToPlainObject`、`withApiError`、`rebuildList`、`clearCache`、`ApiError`。
- Produces:
  - `export type ConflictKind = 'plain_same' | 'plain_diff' | 'orphan_link' | 'broken_link' | 'wrong_target'`
  - `export interface ReconcileItem { kind: ConflictKind; skillId?: string; directory: string; path: string; agentIds: string[]; ssotHash?: string; localHash?: string; acknowledged: boolean }`（与 Go json tag 一一对应）
  - `api.skills` 新增：`scanConflicts`、`convertSkillCopy(skillId, sourcePath)`、`overwriteSkillCopy(skillId, sourcePath)`、`adoptSkillCopy(skillId, sourcePath)`、`keepSkillFork(skillId, sourcePath)`、`cleanOrphanLinks()`
  - store 新增：`conflicts: Ref<ReconcileItem[]>`、`conflictsLoading: Ref<boolean>`、`scanConflicts()`、`convertSkillCopy()`、`overwriteSkillCopy()`、`adoptSkillCopy()`、`keepSkillFork()`、`cleanOrphanLinks()`；每个变更动作完成后自动 `scanConflicts()`，adopt 另用 `rebuildList` 更新技能卡；`clearCache()` 同时清空 `conflicts`。

- [ ] **Step 1: api.ts — bindings import 增补**

锚点（唯一）：

```ts
  UninstallSkill,
} from '../../bindings/agentpack/app'
```

替换为：

```ts
  UninstallSkill,
  AdoptSkillCopy,
  CleanOrphanSkillLinks,
  ConvertSkillCopyToLink,
  KeepSkillFork,
  OverwriteSkillCopyFromSSOT,
  ScanSkillConflicts,
} from '../../bindings/agentpack/app'
```

- [ ] **Step 2: api.ts — 类型定义**

锚点（唯一）：`export interface SkillSourceBackfillResult {`（第 311 行），在其**之前**插入：

```ts
export type ConflictKind = 'plain_same' | 'plain_diff' | 'orphan_link' | 'broken_link' | 'wrong_target'

export interface ReconcileItem {
  kind: ConflictKind
  skillId?: string
  directory: string
  path: string
  agentIds: string[]
  ssotHash?: string
  localHash?: string
  acknowledged: boolean
}

```

- [ ] **Step 3: api.ts — skills 对象方法**

锚点（唯一）：

```ts
    scanUnmanaged: async () => optimizeToPlainObject(await ScanUnmanagedSkills()) as UnmanagedSkill[],
```

在其**之后**插入：

```ts
    scanConflicts: async () => optimizeToPlainObject(await ScanSkillConflicts()) as ReconcileItem[],
    convertSkillCopy: (skillId: string, sourcePath: string) => safeCall(() => ConvertSkillCopyToLink(skillId, sourcePath)),
    overwriteSkillCopy: (skillId: string, sourcePath: string) => safeCall(() => OverwriteSkillCopyFromSSOT(skillId, sourcePath)),
    adoptSkillCopy: async (skillId: string, sourcePath: string) => optimizeToPlainObject(await AdoptSkillCopy(skillId, sourcePath)) as Skill,
    keepSkillFork: (skillId: string, sourcePath: string) => safeCall(() => KeepSkillFork(skillId, sourcePath)),
    cleanOrphanLinks: async () => {
      const removed = await CleanOrphanSkillLinks()
      return Array.isArray(removed) ? removed : []
    },
```

- [ ] **Step 4: skills.ts — import 行增补**

锚点（唯一）：

```ts
import { api, type Skill, type Agent, type UnmanagedSkill, type UpdateStatus, ApiError } from '@/lib/api'
```

替换为：

```ts
import { api, type Skill, type Agent, type UnmanagedSkill, type ReconcileItem, type UpdateStatus, ApiError } from '@/lib/api'
```

- [ ] **Step 5: skills.ts — 状态字段**

锚点（唯一）：

```ts
  const unmanaged = ref<UnmanagedSkill[]>([])
```

在其后插入：

```ts
  const conflicts = ref<ReconcileItem[]>([])
  const conflictsLoading = ref(false)
```

- [ ] **Step 6: skills.ts — 动作函数**

锚点（唯一）：`function clearCache() {`（store 尾部），在其**之前**插入：

```ts
  async function scanConflicts() {
    if (conflictsLoading.value) return
    conflictsLoading.value = true
    try {
      conflicts.value = await api.skills.scanConflicts()
    } catch (e) {
      const apiError = ApiError.from(e)
      error.value = apiError.message
      // 扫描失败保留旧列表：让用户区分「扫描失败」与「结果为空」
    } finally {
      conflictsLoading.value = false
    }
  }

  async function convertSkillCopy(skillId: string, sourcePath: string) {
    await withApiError(() => api.skills.convertSkillCopy(skillId, sourcePath))
    await scanConflicts()
  }

  async function overwriteSkillCopy(skillId: string, sourcePath: string) {
    await withApiError(() => api.skills.overwriteSkillCopy(skillId, sourcePath))
    await scanConflicts()
  }

  async function adoptSkillCopy(skillId: string, sourcePath: string) {
    const skill = await withApiError(() => api.skills.adoptSkillCopy(skillId, sourcePath))
    rebuildList(list => list.map(s => (s.id === skill.id ? skill : s)))
    await scanConflicts()
    return skill
  }

  async function keepSkillFork(skillId: string, sourcePath: string) {
    await withApiError(() => api.skills.keepSkillFork(skillId, sourcePath))
    await scanConflicts()
  }

  async function cleanOrphanLinks() {
    const removed = await withApiError(() => api.skills.cleanOrphanLinks())
    await scanConflicts()
    return removed
  }

```

- [ ] **Step 7: skills.ts — clearCache 清空冲突、return 导出**

- 锚点（唯一）：`clearCache` 函数体内的 `unmanaged.value = []`，在其后插入 `conflicts.value = []`。
- 锚点（唯一）：return 块中的 `scanUnmanaged,` 行，在其后插入：

```ts
    conflicts,
    conflictsLoading,
    scanConflicts,
    convertSkillCopy,
    overwriteSkillCopy,
    adoptSkillCopy,
    keepSkillFork,
    cleanOrphanLinks,
```

- [ ] **Step 8: 类型检查**

Run: `cd frontend; pnpm build`
Expected: exit 0（`vue-tsc --noEmit` 通过；若报 `withApiError`/`rebuildList` 不存在于当前作用域，核对同文件既有用法并保持同风格引用）

- [ ] **Step 9: Commit**

```bash
git add frontend/src/lib/api.ts frontend/src/stores/skills.ts
git commit -m "feat(web): skills reconciliation api surface and store actions"
```

### Task 9: SkillsView 对账 UI、扫描第 5 步与 i18n

**Files:**
- Modify: `frontend/src/views/SkillsView.vue`
- Modify: `frontend/src/locales/zh-CN.json`、`frontend/src/locales/en.json`（两文件同位插入、行号对齐）

**Interfaces:**
- Consumes: Task 8 store 导出；既有 `confirm.confirm({title,message,confirmText})`、`toast.success/warning/error`、`toast.fromError(e, fallback)`、`Badge`/`Button`/`Card` 组件、`t()`。
- Produces（视图内部）: `conflictsBySkill` computed、`skillConflicts(skillId)`、`orphanCount`、`conflictsCleaning` ref、`kindLabel(kind)`、`onConvertConflict`/`onAdoptConflict`/`onOverwriteConflict`/`onKeepConflict`/`onCleanOrphans` 处理器；模板插入点两处：技能卡徽标（第 504 行后）与对账面板（第 553 行 `</Card>` 之后、第 555 行 `skills.error` 段之前）；`scanSkills()` 增加第 5 步。

- [ ] **Step 1: script — import 与类型**

锚点（唯一，第 8 行）：

```ts
import { api, events, ApiError } from '@/lib/api'
```

替换为：

```ts
import { api, events, ApiError, type ReconcileItem } from '@/lib/api'
```

- [ ] **Step 2: script — 计算属性与动作**

在 `scanSkills` 函数定义之前（锚点：`async function scanSkills() {` 所在行之前）插入：

```ts
// 对账：冲突按技能分组（卡片徽标）+ 平铺列表（对账面板）
const conflictsBySkill = computed(() => {
  const m = new Map<string, ReconcileItem[]>()
  for (const it of skills.conflicts) {
    if (!it.skillId) continue
    const arr = m.get(it.skillId) ?? []
    arr.push(it)
    m.set(it.skillId, arr)
  }
  return m
})
function skillConflicts(skillId: string): ReconcileItem[] {
  return conflictsBySkill.value.get(skillId) ?? []
}
const orphanCount = computed(() => skills.conflicts.filter(c => c.kind === 'orphan_link').length)
const conflictsCleaning = ref(false)

function kindLabel(kind: ReconcileItem['kind']): string {
  return t(`skills.conflicts.kind.${kind}`)
}

async function onConvertConflict(item: ReconcileItem) {
  if (!item.skillId) return
  try {
    await skills.convertSkillCopy(item.skillId, item.path)
    toast.success(t('skills.toast.conflictConverted'))
  } catch (e: unknown) {
    toast.error(toast.fromError(e, t('skills.toast.conflictActionFailed')))
  }
}

async function onAdoptConflict(item: ReconcileItem) {
  if (!item.skillId) return
  const ok = await confirm.confirm({
    title: t('skills.conflicts.adoptTitle'),
    message: t('skills.conflicts.adoptMessage', { path: item.path, directory: item.directory }),
    confirmText: t('skills.conflicts.adoptConfirm'),
  })
  if (!ok) return
  try {
    await skills.adoptSkillCopy(item.skillId, item.path)
    toast.success(t('skills.toast.adopted'))
  } catch (e: unknown) {
    toast.error(toast.fromError(e, t('skills.toast.conflictActionFailed')))
  }
}

async function onOverwriteConflict(item: ReconcileItem) {
  if (!item.skillId) return
  const ok = await confirm.confirm({
    title: t('skills.conflicts.overwriteTitle'),
    message: t('skills.conflicts.overwriteMessage', { path: item.path }),
    confirmText: t('skills.conflicts.overwriteConfirm'),
  })
  if (!ok) return
  try {
    await skills.overwriteSkillCopy(item.skillId, item.path)
    toast.success(t('skills.toast.overwritten'))
  } catch (e: unknown) {
    toast.error(toast.fromError(e, t('skills.toast.conflictActionFailed')))
  }
}

async function onKeepConflict(item: ReconcileItem) {
  if (!item.skillId) return
  try {
    await skills.keepSkillFork(item.skillId, item.path)
    toast.success(t('skills.toast.forkKept'))
  } catch (e: unknown) {
    toast.error(toast.fromError(e, t('skills.toast.conflictActionFailed')))
  }
}

async function onCleanOrphans() {
  const ok = await confirm.confirm({
    title: t('skills.conflicts.cleanAll'),
    message: t('skills.conflicts.cleanMessage', { count: orphanCount.value }),
    confirmText: t('skills.conflicts.cleanAll'),
  })
  if (!ok) return
  conflictsCleaning.value = true
  try {
    const removed = await skills.cleanOrphanLinks()
    toast.success(t('skills.toast.cleanedCount', { count: removed.length }))
  } catch (e: unknown) {
    toast.error(toast.fromError(e, t('skills.toast.conflictActionFailed')))
  } finally {
    conflictsCleaning.value = false
  }
}

```

- [ ] **Step 3: script — 扫描第 5 步**

锚点（唯一）：

```ts
    // 4. Scan unmanaged skills in global ~/.agents/skills (read-only)
    await skills.scanUnmanaged()
    toast.success(t('skills.toast.scanComplete'))
```

替换为：

```ts
    // 4. Scan unmanaged skills in global ~/.agents/skills (read-only)
    await skills.scanUnmanaged()
    // 5. 对账扫描：散装副本 / 分叉 / 死链分类（只读）
    await skills.scanConflicts()
    toast.success(t('skills.toast.scanComplete'))
```

- [ ] **Step 4: template — 技能卡冲突徽标**

锚点（唯一）：

```html
                  <Badge variant="outline">{{ skill.directory }}</Badge>
```

在其后插入一行：

```html
                  <Badge v-if="skillConflicts(skill.id).length > 0" variant="outline" class="border-destructive/40 text-destructive">{{ t('skills.conflicts.badge', { count: skillConflicts(skill.id).length }) }}</Badge>
```

（置于 505 行 `v-if` 更新徽标链之前是安全的：该 Badge 无 v-if，独立条件渲染。）

- [ ] **Step 5: template — 对账面板**

锚点（唯一，技能卡 `v-for` 结束与错误段之间）：

```html
        </Card>

        <p v-if="skills.error" class="mt-4 text-xs text-destructive">
```

在 `</Card>` 与 `<p v-if="skills.error">` 之间插入：

```html

        <!-- 对账面板：散装副本 / 分叉 / 死链 -->
        <Card v-if="skills.conflicts.length > 0">
          <CardContent class="p-4">
            <div class="flex items-center justify-between border-b border-border pb-2">
              <div class="flex items-center gap-2">
                <h3 class="text-sm font-semibold">{{ t('skills.conflicts.title') }}</h3>
                <Badge variant="outline" class="border-destructive/40 text-destructive">{{ skills.conflicts.length }}</Badge>
              </div>
              <Button v-if="orphanCount > 0" variant="outline" size="sm" :disabled="conflictsCleaning" @click="onCleanOrphans">
                {{ t('skills.conflicts.cleanAll') }}
              </Button>
            </div>
            <div class="mt-2 divide-y divide-border">
              <div v-for="item in skills.conflicts" :key="item.kind + item.path" class="flex items-start justify-between gap-3 py-2">
                <div class="min-w-0">
                  <span class="mr-2 inline-flex items-center rounded border border-border px-1.5 py-0.5 text-[10px] text-muted-foreground">{{ kindLabel(item.kind) }}</span>
                  <span class="text-xs font-medium">{{ item.directory }}</span>
                  <span v-if="item.acknowledged" class="ml-2 text-[10px] text-muted-foreground">{{ t('skills.conflicts.kept') }}</span>
                  <div class="mt-0.5 break-all text-[11px] text-muted-foreground">{{ item.path }} → {{ item.agentIds.join(', ') }}</div>
                </div>
                <div v-if="item.kind !== 'orphan_link'" class="flex shrink-0 gap-1">
                  <template v-if="item.kind === 'plain_diff'">
                    <Button variant="outline" size="sm" class="h-7 text-xs" @click="onAdoptConflict(item)">{{ t('skills.conflicts.adoptConfirm') }}</Button>
                    <Button variant="outline" size="sm" class="h-7 text-xs border-destructive/40 text-destructive" @click="onOverwriteConflict(item)">{{ t('skills.conflicts.overwriteConfirm') }}</Button>
                    <Button v-if="item.acknowledged === false" variant="ghost" size="sm" class="h-7 text-xs" @click="onKeepConflict(item)">{{ t('skills.conflicts.keepFork') }}</Button>
                  </template>
                  <Button v-else variant="outline" size="sm" class="h-7 text-xs" @click="onConvertConflict(item)">{{ t('skills.conflicts.convert') }}</Button>
                </div>
              </div>
            </div>
          </CardContent>
        </Card>
```

- [ ] **Step 6: i18n — zh-CN.json**

在 `zh-CN.json` 的 `skills` 对象内（与 `boundAgentCount` 同级，4 空格缩进）新增 `conflicts` 子对象；并在 `skills.toast` 对象内追加 6 个键。完整片段：

```json
    "conflicts": {
      "title": "对账",
      "badge": "{count} 处冲突",
      "kind": {
        "plain_same": "散装副本·内容相同",
        "plain_diff": "内容分叉",
        "orphan_link": "孤儿断链",
        "broken_link": "投影断链",
        "wrong_target": "错误指向"
      },
      "convert": "转为映射",
      "cleanAll": "清理全部断链",
      "cleanMessage": "将删除 {count} 条目标已失效的死链（链接本身无内容，删除无数据损失）。活链与散装技能不受影响。",
      "adoptTitle": "收编本地副本？",
      "adoptMessage": "将用 {path} 的内容覆盖 SSOT 中的 {directory}；覆盖前原版本会备份到 skill-backups。",
      "adoptConfirm": "收编",
      "overwriteTitle": "用 SSOT 覆盖本地副本？",
      "overwriteMessage": "将删除 {path} 的本地内容并重建为指向 SSOT 的投影，本地改动不可恢复。",
      "overwriteConfirm": "覆盖",
      "keepFork": "保留分叉",
      "kept": "已保留"
    },
```

`skills.toast` 内追加：

```json
      "conflictConverted": "已转为映射",
      "conflictActionFailed": "对账操作失败",
      "adopted": "已收编本地副本",
      "overwritten": "已用 SSOT 覆盖本地副本",
      "forkKept": "已保留该分叉",
      "cleanedCount": "已清理 {count} 条死链",
      "convertedCount": "已将 {count} 个来源目录转为映射"
```

- [ ] **Step 7: i18n — en.json（同位、同结构）**

`skills` 对象内：

```json
    "conflicts": {
      "title": "Reconciliation",
      "badge": "{count} conflicts",
      "kind": {
        "plain_same": "Identical plain copy",
        "plain_diff": "Diverged copy",
        "orphan_link": "Orphan dead link",
        "broken_link": "Broken projection",
        "wrong_target": "Wrong target"
      },
      "convert": "Convert to link",
      "cleanAll": "Clean dead links",
      "cleanMessage": "Removes {count} dead links whose targets are gone (a link holds no content, so nothing is lost). Live links and plain skills are untouched.",
      "adoptTitle": "Adopt local copy?",
      "adoptMessage": "SSOT entry {directory} will be replaced with the content of {path}; the original is backed up to skill-backups first.",
      "adoptConfirm": "Adopt",
      "overwriteTitle": "Overwrite local copy with SSOT?",
      "overwriteMessage": "Deletes the local content at {path} and rebuilds it as a projection of SSOT. Local edits cannot be recovered.",
      "overwriteConfirm": "Overwrite",
      "keepFork": "Keep fork",
      "kept": "Kept"
    },
```

`skills.toast` 内追加：

```json
      "conflictConverted": "Converted to link",
      "conflictActionFailed": "Reconciliation action failed",
      "adopted": "Local copy adopted",
      "overwritten": "Local copy overwritten from SSOT",
      "forkKept": "Fork kept",
      "cleanedCount": "Removed {count} dead link(s)",
      "convertedCount": "Converted {count} source director(y/ies) to links"
```

- [ ] **Step 8: 构建与 i18n 门禁**

Run: `cd frontend; pnpm build; pnpm check:i18n`
Expected: 两者 exit 0（`vue-tsc` 通过、`check-i18n` 报告 key 完全对齐）

- [ ] **Step 9: Commit**

```bash
git add frontend/src/views/SkillsView.vue frontend/src/locales/zh-CN.json frontend/src/locales/en.json
git commit -m "feat(web): reconciliation panel, conflict badges, scan step 5"
```

### Task 10: 导入后「来源转映射」询问（D4）

**Files:**
- Modify: `frontend/src/views/SkillsView.vue`（仅 `confirmImportExisting` 函数）
- Modify: `frontend/src/locales/zh-CN.json`、`frontend/src/locales/en.json`（各追加 3 键）

**Interfaces:**
- Consumes: Task 8/9 的 `skills.convertSkillCopy`、`confirm.confirm`、`toast`；`Promise.allSettled` 结果（fulfilled 值为 `Skill`，含 `id`）；`selectedPaths`。
- Produces: 导入成功后一次性询问「将 N 个来源目录转为映射？」；同意则逐个转换（单个失败不阻断——残留会显示在对账面板），完成后 `skills.load()` + `skills.scanConflicts()` 刷新。

- [ ] **Step 1: 修改 confirmImportExisting**

锚点（唯一）：

```ts
    if (failures.length > 0) {
      toast.warning(t('skills.toast.importFailedCount', { count: failures.length }))
    }
    await skills.load()
```

替换为：

```ts
    if (failures.length > 0) {
      toast.warning(t('skills.toast.importFailedCount', { count: failures.length }))
    }
    // D4：导入即询问——把来源目录转为指向 SSOT 的映射。
    // 来源必然在 agent 目录内（unmanaged 列表只扫 agent 目录），且导入刚保证字节全同，
    // 守卫必过；用户拒绝则留 plain_same 待对账面板处理，不静默。
    const converted: { skillId: string; path: string }[] = []
    results.forEach((r, i) => {
      if (r.status === 'fulfilled') converted.push({ skillId: r.value.id, path: selectedPaths.value[i] })
    })
    if (converted.length > 0) {
      const doConvert = await confirm.confirm({
        title: t('skills.importConvertTitle'),
        message: t('skills.importConvertMessage', { count: converted.length }),
        confirmText: t('skills.importConvertConfirm'),
      })
      if (doConvert) {
        let okCount = 0
        for (const c of converted) {
          try {
            await skills.convertSkillCopy(c.skillId, c.path)
            okCount++
          } catch {
            // 单个失败不阻断：残留会显示在对账面板
          }
        }
        if (okCount > 0) toast.success(t('skills.toast.convertedCount', { count: okCount }))
      }
    }
    await skills.load()
    await skills.scanConflicts()
```

（该锚点内原有的 `showImportExisting.value = false` 保持在 `await skills.load()` 之后不动。）

- [ ] **Step 2: i18n 追加**

`zh-CN.json` 的 `skills` 对象内（与 `conflicts` 同级）：

```json
    "importConvertTitle": "将来源转为映射？",
    "importConvertMessage": "导入完成。是否把 {count} 个来源目录转换为指向 SSOT 的映射？转换后由 AgentPack 统一管理（卸载技能时会一并移除）；不转换会保留在原目录并在对账面板留下标记。",
    "importConvertConfirm": "转为映射",
```

`en.json` 同位：

```json
    "importConvertTitle": "Convert source directories to links?",
    "importConvertMessage": "Import finished. Convert {count} source directory(ies) into links pointing at SSOT? Linked copies are managed by AgentPack (removed together on uninstall); declining keeps them as plain copies and flags them in the reconciliation panel.",
    "importConvertConfirm": "Convert to links",
```

- [ ] **Step 3: 构建与 i18n 门禁**

Run: `cd frontend; pnpm build; pnpm check:i18n`
Expected: 两者 exit 0

- [ ] **Step 4: Commit**

```bash
git add frontend/src/views/SkillsView.vue frontend/src/locales/zh-CN.json frontend/src/locales/en.json
git commit -m "feat(web): offer source-to-link conversion right after unmanaged import"
```

### Task 11: 全量门禁与收尾

**Files:** 无新增改动；仅验证与（如需的）格式化。

- [ ] **Step 1: Go 门禁**

Run: `go build ./... ; go vet ./... ; go test ./... -count=1`
Expected: build/vet exit 0；测试 24 个包全 PASS（含 internal/skills 既有全部用例）

- [ ] **Step 2: gofmt 基线**

Run: `gofmt -l .`
Expected: 输出**仅** `version.go` 与 `cleanup_test.go`（既有基线）；若新增文件未格式化，`gofmt -w` 后回到 Step 1

- [ ] **Step 3: 前端门禁**

Run: `cd frontend; pnpm build ; pnpm check:i18n ; pnpm test`
Expected: 三者 exit 0（vitest 为既有用例，无新增前端单测——与仓库先例一致，仓库仅有 `lib/__tests__` 两个纯函数测试）

- [ ] **Step 4: bindings 生成物不入 git**

Run: `git check-ignore frontend/bindings` ; `git status --short`
Expected: 前者输出 `frontend/bindings`；后者仅显示**本计划各任务 Files 列出的文件**与既有未提交改动（约 33 个）+ 未跟踪 `.review/`、`docs/`（若 Step 5 未提交）——任何 `frontend/bindings/**` 不出现

- [ ] **Step 5: 提交计划文档（若 Task 1 未随行提交）**

```bash
git add docs/superpowers/plans/2026-09-24-skills-reconciliation-engine.md
git commit -m "docs: skills reconciliation engine implementation plan"
```

---

## Self-Review（已执行）

1. **规格覆盖**：D1 字节守卫 → Task 1/4；D2 分叉三选 → Task 5（adopt/overwrite）+ Task 4（keep）+ Task 9（三按钮 UI）；D3 断链分级 → Task 4（convert 修 broken/wrong）+ Task 6（clean 只删孤儿）；D4 导入询问 → Task 10；D5 copy 模式感知 → Task 1 分类器 + 测试；检测层 → Task 3；ToggleAgent 数据丢失守卫 → Task 6；共享目录去重 → Task 3/6 测试断言；ack 指纹漂移 → Task 2/3 测试。
2. **无占位符**：所有锚点均为已验证的唯一原文（含行号语义），所有命令为可直接执行形态，所有代码为完整实现体。
3. **类型一致性**：Go `ReconcileItem` json tag ↔ TS `ReconcileItem` 字段一一对应；5 个 kind 值两侧字面量一致；方法名链 `Store.X` ↔ `App.XSkill(s)`（`ScanSkillConflicts`↔`ScanConflicts` 为唯一有意差异，两侧在 Interfaces 中显式标注）↔ `api.skills.x` ↔ `store.x` 均已逐一核对。
4. **Review Focus 五项**均在归属任务中有对应测试名（见头部 Review Focus 章节）。
5. **边界诚实**：`ScanConflicts` 对只读路径失败（ReadDir err）选择跳过而非报错；`adopt` 的 rollback 为 best-effort + log（`backupPath==""` 即 SSOT 本就不在，无回滚需要）；symlink 权限不足测试 `t.Skipf`；能力 ID 用 `t.Fatalf` 自证而非假设。
