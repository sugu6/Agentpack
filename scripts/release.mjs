#!/usr/bin/env node
// 发版脚本：更新版本号 + 转换 CHANGELOG
//
// 用法: node scripts/release.mjs <version>
// 例如: node scripts/release.mjs 0.1.3
//
// 功能:
// 1. 验证版本号格式 (X.Y.Z)
// 2. 更新 build/config.yml 的 version
// 3. 更新 frontend/package.json 的 version
// 4. 更新平台版本文件 (Info.plist/Info.dev.plist, info.json, wails.exe.manifest, msix template.xml/app_manifest.xml, nfpm.yaml)
// 5. 处理 CHANGELOG.md 和 CHANGELOG_EN.md:
//    a. 如果 [Unreleased] 有内容: 转换为新版本节 + 添加新 [Unreleased]
//    b. 如果 [Unreleased] 为空且目标版本节已存在: 跳过转换（用户已手动维护）
//    c. 如果 [Unreleased] 为空且目标版本节不存在: 报错
//    d. 修正底部 compare 链接的 repo URL（从 git remote 获取）
//    e. 添加新版本的 compare 链接
// 6. 输出变更摘要

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

// 用本地时区（Asia/Shanghai 等）计算发版日期，避免 toISOString() 返回 UTC 导致日期比本地提前一天
const now = new Date()
const today = `${now.getFullYear()}-${String(now.getMonth() + 1).padStart(2, '0')}-${String(now.getDate()).padStart(2, '0')}`

// 脚本实际修改过的文件（用于 commit + tag）
const changedFiles = []

// --- 1. 更新 build/config.yml (Wails v3) ---
function updateBuildConfig() {
  let content = readFileSync('build/config.yml', 'utf8')
  const pattern = /^(  version:\s*")[^"]*(")/m
  const match = content.match(pattern)
  if (!match) {
    console.log('build/config.yml: version field not found, skipping')
    return
  }
  const oldVersion = match[0].slice(match[1].length, -match[2].length)
  if (oldVersion === version) {
    console.log(`build/config.yml: already ${version}, skipping`)
    return
  }
  content = content.replace(pattern, `$1${version}$2`)
  writeFileSync('build/config.yml', content)
  changedFiles.push('build/config.yml')
  console.log(`build/config.yml: ${oldVersion} -> ${version}`)
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
  changedFiles.push('frontend/package.json')
  console.log(`frontend/package.json: ${oldVersion} -> ${version}`)
}

// --- 3. 更新平台版本文件 (plist / info.json / manifest / msix / nfpm) ---
function updatePlatformVersionFiles() {
  const fourPart = `${version}.0`

  function patch(file, pattern, replacement, label) {
    let content = readFileSync(file, 'utf8')
    if (!pattern.test(content)) {
      console.log(`${file}: ${label} not found, skipping`)
      return
    }
    const newContent = content.replace(pattern, replacement)
    if (newContent === content) {
      console.log(`${file}: ${label} already ${version}, skipping`)
      return
    }
    writeFileSync(file, newContent)
    changedFiles.push(file)
    console.log(`${file}: ${label} -> ${version}`)
  }

  // macOS Info.plist / Info.dev.plist (3-part version)
  for (const file of ['build/darwin/Info.plist', 'build/darwin/Info.dev.plist']) {
    patch(file, /(<key>CFBundleShortVersionString<\/key>\s*<string>)[^<]*(<\/string>)/, `$1${version}$2`, 'CFBundleShortVersionString')
    patch(file, /(<key>CFBundleVersion<\/key>\s*<string>)[^<]*(<\/string>)/, `$1${version}$2`, 'CFBundleVersion')
  }

  // Windows info.json (3-part version, tab-indented JSON)
  {
    const file = 'build/windows/info.json'
    const info = JSON.parse(readFileSync(file, 'utf8'))
    let changed = false
    if (info.fixed && info.fixed.file_version !== version) {
      info.fixed.file_version = version
      changed = true
    }
    if (info.info && info.info['0000'] && info.info['0000'].ProductVersion !== version) {
      info.info['0000'].ProductVersion = version
      changed = true
    }
    if (changed) {
      writeFileSync(file, JSON.stringify(info, null, '\t') + '\n')
      changedFiles.push(file)
      console.log(`${file}: file_version/ProductVersion -> ${version}`)
    } else {
      console.log(`${file}: already ${version}, skipping`)
    }
  }

  // Windows wails.exe.manifest (3-part, app assemblyIdentity only)
  patch('build/windows/wails.exe.manifest', /(name="com\.sugu6\.agentpack" version=")[^"]*(")/, `$1${version}$2`, 'assemblyIdentity version')

  // Windows MSIX template.xml / app_manifest.xml (4-part version)
  patch('build/windows/msix/template.xml', /(Version=")\d+\.\d+\.\d+\.0(")/, `$1${fourPart}$2`, 'MSIX Version')
  patch('build/windows/msix/app_manifest.xml', /(Version=")\d+\.\d+\.\d+\.0(")/, `$1${fourPart}$2`, 'MSIX Version')

  // Linux nfpm.yaml (3-part, top-level version)
  patch('build/linux/nfpm/nfpm.yaml', /^(version:\s*")[^"]*(")/m, `$1${version}$2`, 'version')

  // Windows NSIS 安装器版本 (wails_tools.nsh 的 INFO_PRODUCTVERSION)
  // 该文件记录安装包的文件版本（VIProductVersion/VIFileVersion），
  // 若不同步会导致安装包文件版本停留在上一版本。
  patch('build/windows/nsis/wails_tools.nsh', /(!define INFO_PRODUCTVERSION ")[^"]*(")/, `$1${version}$2`, 'INFO_PRODUCTVERSION')
}

// --- 4. 处理 CHANGELOG ---
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

  // 发版时始终将目标版本节的发版日期刷新为今天（含用户已手动维护但日期滞后的场景）
  const datePattern = new RegExp(`(^## \\[${version}\\]\\s*-\\s*)\\d{4}-\\d{2}-\\d{2}`, 'm')
  const dateMatch = content.match(datePattern)
  const currentDate = dateMatch && dateMatch[0].match(/\d{4}-\d{2}-\d{2}/)[0]
  if (currentDate !== today) {
    content = content.replace(datePattern, `$1${today}`)
    modified = true
    console.log(`${file}: [${version}] release date ${currentDate ?? '(none)'} -> ${today}`)
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
    changedFiles.push(file)
  }
}

// --- 5. git commit + tag（发版统一经过脚本打标签） ---
function gitCommitAndTag() {
  if (changedFiles.length === 0) {
    console.log('No files changed; skipping commit and tag.')
    return
  }
  // 去重（CHANGELOG 可能被重复 push）并过滤相对 HEAD 无实际改动的文件
  const files = [...new Set(changedFiles)].filter((f) => {
    try {
      execSync(`git diff --quiet HEAD -- ${JSON.stringify(f)}`, { stdio: 'ignore' })
      return false // 无改动
    } catch {
      return true // 有改动，或文件被脚本改后尚未提交
    }
  })
  if (files.length === 0) {
    console.log('No modifications relative to HEAD; skipping commit and tag.')
    return
  }

  console.log(`\n=== Commit & Tag v${version} ===\n`)
  execSync(`git add ${files.map((f) => JSON.stringify(f)).join(' ')}`, { stdio: 'inherit' })
  execSync(`git commit -m "release: v${version}"`, { stdio: 'inherit' })
  // git tag 统一走脚本，创建轻量标签（与历史 v* 标签一致）；已存在则强制更新
  execSync(`git tag -f v${version}`, { stdio: 'inherit' })
  console.log(`Created tag v${version}. Push with: git push origin v${version}`)
}

// --- 执行 ---
console.log(`\n=== Release v${version} ===\n`)
updateBuildConfig()
updatePackageJson()
updatePlatformVersionFiles()
updateChangelog('CHANGELOG.md')
updateChangelog('CHANGELOG_EN.md')
gitCommitAndTag()
console.log(`\nDone.`)
