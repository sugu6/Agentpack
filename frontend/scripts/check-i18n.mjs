// 校验 zh-CN.json 与 en.json 键集一致性
// 缺失键或多余键均报错退出
import zhCN from '../src/locales/zh-CN.json' with { type: 'json' }
import en from '../src/locales/en.json' with { type: 'json' }

function collectKeys(obj, prefix = '') {
  const keys = new Set()
  for (const [k, v] of Object.entries(obj)) {
    const path = prefix ? `${prefix}.${k}` : k
    if (v && typeof v === 'object' && !Array.isArray(v)) {
      for (const sub of collectKeys(v, path)) keys.add(sub)
    } else {
      keys.add(path)
    }
  }
  return keys
}

const zhKeys = collectKeys(zhCN)
const enKeys = collectKeys(en)

const missingInEn = [...zhKeys].filter(k => !enKeys.has(k))
const missingInZh = [...enKeys].filter(k => !zhKeys.has(k))

if (missingInEn.length > 0) {
  console.error('Keys missing in en.json:')
  missingInEn.forEach(k => console.error('  - ' + k))
}
if (missingInZh.length > 0) {
  console.error('Keys missing in zh-CN.json:')
  missingInZh.forEach(k => console.error('  - ' + k))
}
if (missingInEn.length > 0 || missingInZh.length > 0) {
  process.exit(1)
}
console.log(`i18n key consistency check passed (${zhKeys.size} keys)`)
