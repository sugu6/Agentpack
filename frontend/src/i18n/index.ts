import { createI18n } from 'vue-i18n'
import zhCN from '@/locales/zh-CN.json'
import en from '@/locales/en.json'

export type AppLanguage = 'zh-CN' | 'en'

const STORAGE_KEY = 'agentpack-language'

// 自动检测语言(用户未设置时)
// 非中文系统语言统一回退到英文
function detectLanguage(): AppLanguage {
  // 1. localStorage 缓存(启动早期 inline 脚本写入,避免闪烁)
  try {
    const cached = localStorage.getItem(STORAGE_KEY)
    if (cached === 'zh-CN' || cached === 'en') return cached
  } catch {}
  // 2. navigator.language,仅中文系 → zh-CN,其他全部 → en
  const nav = (navigator.language || '').toLowerCase()
  if (nav.startsWith('zh')) return 'zh-CN'
  return 'en'
}

export const i18n = createI18n({
  legacy: false,
  locale: detectLanguage(),
  fallbackLocale: 'en',
  messages: { 'zh-CN': zhCN, en },
})

export function setLanguage(lang: AppLanguage) {
  i18n.global.locale.value = lang
  try { localStorage.setItem(STORAGE_KEY, lang) } catch {}
  // 同步 document lang 属性(无障碍 + 浏览器原生控件语言提示)
  document.documentElement.lang = lang === 'zh-CN' ? 'zh-CN' : 'en'
}

// resolveLanguage: 将 Settings.Language 解析为最终语言
// "" (跟随系统) → detectLanguage()
// "zh-CN" / "en" → 原值
// 其他 → "en"(detectLanguage 也会回退到 en)
export function resolveLanguage(setting: string): AppLanguage {
  if (setting === 'zh-CN' || setting === 'en') return setting
  return detectLanguage()
}
