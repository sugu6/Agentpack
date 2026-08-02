# Skills 来源回填（skills.sh）设计

日期：2026-08-02
状态：已批准（用户确认设计后进入实现）

## 背景

`~/.agents/.skill-lock.json` 中有 11 个已安装技能缺少 GitHub 仓库来源（`source` 为空），
导致这些技能无法使用"检查更新/更新"功能（`CheckUpdates`/`UpdateSkill` 仅处理
`RepoOwner`/`RepoName` 非空的技能）。

skills.sh 公开搜索 API（`https://skills.sh/api/search`）可检索同名技能，并返回下载量
（`installs`）与来源仓库（`source`，格式 `owner/repo`）。实测确认当前缺来源的技能
（如 `karpathy-guidelines`、`debugger`、`ui-ux-pro-max`）均可精确搜索到对应条目，
方案可行。

## 目标

- 对当前缺仓库来源的技能，从 skills.sh 搜索匹配，按下载量（installs）优先，
  把仓库地址写入 lock 文件，使其具备后续更新能力。
- 不覆盖已有正确来源；无法精确匹配的技能保持原样，不写错误关联。
- 功能可重复执行：未来新安装的技能缺来源时也可再次回填。

## 非目标

- 不实现技能自动更新或安装。
- 不回填 `fullPath`（由现有"检测时从远程树推断并回写 lock"机制自动补全）。
- 不修改 skills.sh 搜索接口本身。

## 匹配规则

1. 每个技能目录名调用 `GET /api/search?q=<directory>&limit=20`。
2. 只接受 `skillId` 与目录名**精确相等**（大小写不敏感）的条目。
   不做子串匹配：否则通用名（如 `debugger`）会被高下载量的无关条目
   （如 `argent-metro-debugger`）错误抢占。
3. 过滤非 GitHub 来源：`owner` 含 `.`（如 `smithery.ai`）的条目跳过。
4. 多个命中取 `installs` 最大的。
5. 无精确命中（如通用名 `shared`）则跳过，不写入。

## 写入内容

命中条目写入 lock（`WriteAgentsLock`）：

- `source`：`owner/repo`
- `sourceType`：`github`
- `sourceURL`：`https://github.com/owner/repo`
- `branch`：`main`

`fullPath` 不在本次写入；由现有检测期树推断机制（`resolveSkillDirInTree` +
`persistSkillRepoInfo`）在后续检查/更新时自动补全。

## 架构与数据流

- `market` 包：新增 `BackfillSkillSources(ctx, dirs []string) (map[string]BackfillMatch, error)`，
  复用现有 `NewHTTPClient`，并发 5，单个查询超时 15 秒。返回每个目录名的最优匹配
  （owner/repo/installs）。
- `App.BackfillSkillSources() (BackfillResult, error)`：收集 `skillsStore.List()` 中
  `RepoOwner==""` 的技能目录 → 调用 market 回填查询 → 对命中者调 `WriteAgentsLock` →
  返回 `{Matched, Unmatched, Failed}` 统计。
- 前端：设置页 Skills 区域新增"从 skills.sh 回填来源"按钮，调用后显示结果 toast
  并刷新技能列表（复用 `settings` store 的 `markSkillReposChanged`/事件机制）。

## 错误处理

- 单个技能查询失败：记录日志并继续处理其余技能，不计入失败阻断。
- 全部查询失败：返回错误，前端显示失败 toast。
- lock 写入失败：计入失败列表，不影响其他技能。

## 测试

httptest 模拟 skills.sh API，覆盖：

- 精确匹配成功；
- 多个命中按 installs 排序取最大；
- 非 GitHub 来源（域名）过滤；
- 防抢匹配（`debugger` 不会被 `argent-metro-debugger` 抢占）；
- 无精确命中跳过；
- lock 写入内容断言（source/sourceType/sourceURL/branch）。

## 范围

单次功能，无新外部依赖；沿用现有 skills.sh fetcher、lock 写入与设置页结构。
