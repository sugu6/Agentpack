#!/usr/bin/env node
// 发版脚本：更新版本号 + 转换 CHANGELOG
//
// 用法: node scripts/release.mjs <version>
// 例如: node scripts/release.mjs 0.1.3
//
// 功能:
// 1. 验证版本号格式 (X.Y.Z)
// 2. 更新 wails.json 的 productVersion
// 3. 更新 frontend/package.json 的 version
// 4. 处理 CHANGELOG.md 和 CHANGELOG_EN.md:
//    a. 如果 [Unreleased] 有内容: 转换为新版本节 + 添加新 [Unreleased]
//    b. 如果 [Unreleased] 为空且目标版本节已存在: 跳过转换（用户已手动维护）
//    c. 如果 [Unreleased] 为空且目标版本节不存在: 报错
//    d. 修正底部 compare 链接的 repo URL（从 git remote 获取）
//    e. 添加新版本的 compare 链接
// 5. 输出变更摘要

import { readFileSync, writeFileSync } from 'fs'
import { execSync } from 'child_process'

const version = process.argv[2]

if (!version) {
  console.error('Usage: node scripts/release.mjs <version>')
  console.error('Example: node scripts/release.mjs 0.1.3')
  process.exit(1)
}

// 验证版本号格式
if (!/^\d+\.\d+\.\d+$/.test(version)) {
  console.error(`Error: Invalid version format "${version}" (expected X.Y.Z)`)
  process.exit(1)
}

// 从 git remote 获取 repo URL
function getRepoUrl() {
  try {
    const remote = execSync('git remote get-url origin', { encoding: 'utf8' }).trim()
    return remote.replace(/\.git$/, '')
  } catch {
    return null
  }
}

const repoUrl = getRepoUrl()
if (!repoUrl) {
  console.error('Error: Cannot determine git remote URL')
  process.exit(1)
}

const today = new Date().toISOString().slice(0, 10)

// --- 1. 更新 wails.json ---
function updateWailsJson() {
  const wails = JSON.parse(readFileSync('wails.json', 'utf8'))
  const oldVersion = wails.info.productVersion
  if (oldVersion === version) {
    console.log(`wails.json: already ${version}, skipping`)
    return
  }
  wails.info.productVersion = version
  writeFileSync('wails.json', JSON.stringify(wails, null, 2) + '\n')
  console.log(`wails.json: ${oldVersion} -> ${version}`)
}

// --- 2. 更新 frontend/package.json ---
function updatePackageJson() {
  const pkg = JSON.parse(readFileSync('frontend/package.json', 'utf8'))
  const oldVersion = pkg.version
  if (oldVersion === version) {
    console.log(`frontend/package.json: already ${version}, skipping`)
    return
  }
  pkg.version = version
  writeFileSync('frontend/package.json', JSON.stringify(pkg, null, 2) + '\n')
  console.log(`frontend/package.json: ${oldVersion} -> ${version}`)
}

// --- 3. 处理 CHANGELOG ---
function updateChangelog(file) {
  let content = readFileSync(file, 'utf8')
  let modified = false

  // 检查目标版本节是否已存在
  const versionSectionExists = new RegExp(`## \\[${version}\\]`).test(content)

  // 检查 [Unreleased] 节是否有内容
  // 注意：用 [ \t]*\n 而非 \s*\n，避免贪婪匹配消耗空行导致下一个版本节内容被误匹配
  const unreleasedMatch = content.match(/## \[Unreleased\][ \t]*\n([\s\S]*?)(?=\n## \[)/)
  const hasUnreleasedContent = unreleasedMatch && unreleasedMatch[1].trim()

  if (hasUnreleasedContent) {
    // 将 [Unreleased] 内容转为新版本节，顶部添加新的空 [Unreleased]
    content = content.replace(
      /## \[Unreleased\][ \t]*\n/,
      `## [Unreleased]\n\n## [${version}] - ${today}\n`
    )
    modified = true
    console.log(`${file}: [Unreleased] -> [${version}]`)
  } else if (versionSectionExists) {
    console.log(`${file}: [${version}] section already exists, skipping conversion`)
  } else {
    console.error(`Error: [Unreleased] section in ${file} is empty and [${version}] section does not exist.`)
    console.error(`Please add changelog entries under [Unreleased] before releasing.`)
    process.exit(1)
  }

  // 修正底部 compare 链接的 repo URL
  // 匹配 https://github.com/owner/repo 格式，替换为正确的 repoUrl
  const wrongRepoPattern = /https:\/\/github\.com\/[^/\s"')]+\/[^/\s"')]+/g
  const fixedContent = content.replace(wrongRepoPattern, repoUrl)
  if (fixedContent !== content) {
    content = fixedContent
    modified = true
    console.log(`${file}: fixed repo URLs -> ${repoUrl}`)
  }

  // 添加新版本的 compare 链接（如果不存在）
  const versionLinkPattern = new RegExp(`^\\[${version}\\]:`, 'm')
  if (!versionLinkPattern.test(content)) {
    // 找到前一个版本号（[Unreleased] 后面的第一个版本号，排除当前版本）
    const versionMatches = content.matchAll(/\[(\d+\.\d+\.\d+)\]/g)
    let prevVersion = null
    for (const m of versionMatches) {
      const v = m[1]
      if (v !== version) {
        prevVersion = v
        break
      }
    }

    const compareUrl = prevVersion
      ? `${repoUrl}/compare/v${prevVersion}...v${version}`
      : `${repoUrl}/releases/tag/v${version}`

    // 在 [Unreleased] 链接行后插入新版本链接
    const unreleasedLinkPattern = /^(\[Unreleased\]:[^\n]*\n)/m
    if (unreleasedLinkPattern.test(content)) {
      content = content.replace(
        unreleasedLinkPattern,
        `$1[${version}]: ${compareUrl}\n`
      )
      modified = true
      console.log(`${file}: added compare link for [${version}]`)
    }
  }

  // 更新或添加 [Unreleased] 链接
  const unreleasedLinkRegex = /^\[Unreleased\]:[^\n]*$/m
  const newUnreleasedLink = `[Unreleased]: ${repoUrl}/compare/v${version}...HEAD`
  if (unreleasedLinkRegex.test(content)) {
    content = content.replace(unreleasedLinkRegex, newUnreleasedLink)
    modified = true
  } else {
    // 底部没有 [Unreleased] 链接，在最后一个版本链接后添加
    content = content.trimEnd() + '\n' + newUnreleasedLink + '\n'
    modified = true
    console.log(`${file}: added [Unreleased] compare link`)
  }

  if (modified) {
    writeFileSync(file, content)
  }
}

// --- 执行 ---
console.log(`\n=== Release v${version} ===\n`)
updateWailsJson()
updatePackageJson()
updateChangelog('CHANGELOG.md')
updateChangelog('CHANGELOG_EN.md')
console.log(`\nDone. Review changes, then commit and tag.`)
