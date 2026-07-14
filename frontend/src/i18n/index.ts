import { createI18n } from 'vue-i18n'
import zhCN from '@/locales/zh-CN.json'
import en from '@/locales/en.json'

export type AppLanguage = 'zh-CN' | 'en'

const STORAGE_KEY = 'agentpack-language'

// 检测系统语言(仅基于 navigator.language,不读 localStorage 缓存)
// 非中文系统语言统一回退到英文
function detectSystemLanguage(): AppLanguage {
  const nav = (navigator.language || '').toLowerCase()
  if (nav.startsWith('zh')) return 'zh-CN'
  return 'en'
}

// 自动检测语言(用户未设置时)
// 优先 localStorage 缓存(启动早期 inline 脚本写入,避免闪烁),回退到系统检测
function detectLanguage(): AppLanguage {
  try {
    const cached = localStorage.getItem(STORAGE_KEY)
    if (cached === 'zh-CN' || cached === 'en') return cached
  } catch {}
  return detectSystemLanguage()
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
// "" (跟随系统) → detectSystemLanguage() (直接检测 navigator,不读 localStorage 缓存)
// "zh-CN" / "en" → 原值
// 其他 → detectSystemLanguage()
export function resolveLanguage(setting: string): AppLanguage {
  if (setting === 'zh-CN' || setting === 'en') return setting
  return detectSystemLanguage()
}
