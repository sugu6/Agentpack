import type { BadgeVariant } from '@/components/ui'
import { i18n } from '@/i18n'

const AGENT_LOGO_MAP: Record<string, string> = {
  'claude-code': 'https://thesvg.org/icons/claude/default.svg',
  'cursor': 'https://thesvg.org/icons/cursor/light.svg',
  'opencode': 'https://thesvg.org/icons/opencode/default.svg',
  'codex': 'https://thesvg.org/icons/codex/default.svg',
  'trae': 'https://thesvg.org/icons/trae/default.svg',
  'trae-cn': 'https://thesvg.org/icons/trae/default.svg',
}

const VARIANTS = ['-desktop', '-cli', '-ide', '-config']

export function agentLogoUrl(id: string): string {
  const direct = AGENT_LOGO_MAP[id]
  if (direct) return direct
  return AGENT_LOGO_MAP[agentBaseId(id)] ?? ''
}

const DARK_INVERT_IDS = new Set(['codex', 'opencode'])
const LIGHT_INVERT_IDS = new Set(['cursor'])

function agentBaseId(id: string): string {
  return VARIANTS.reduce((acc, suf) => acc.endsWith(suf) ? acc.slice(0, -suf.length) : acc, id)
}

export function agentLogoInvertClass(id: string): string {
  const base = agentBaseId(id)
  if (LIGHT_INVERT_IDS.has(base)) return 'dark:invert'
  if (DARK_INVERT_IDS.has(base)) return 'dark:brightness-0 dark:invert'
  return ''
}

export function statusVariant(status: string): BadgeVariant {
  if (status === 'enabled') return 'success'
  if (status === 'error') return 'destructive'
  return 'secondary'
}

export function statusLabel(status: string): string {
  const map: Record<string, string> = {
    enabled: 'agents.enabled',
    disabled: 'agents.disabled',
    not_found: 'agents.notFound',
    error: 'agents.error',
  }
  const key = map[status]
  return key ? i18n.global.t(key) : status
}

export function variantLabel(variant: string): string {
  const map: Record<string, string> = {
    cli: 'CLI',
    desktop: 'Desktop',
    ide: 'IDE',
    config: 'Config',
  }
  return map[variant] ?? variant
}

export function getVariantFromId(id: string): string {
  if (id.endsWith('-desktop')) return 'desktop'
  if (id === 'cursor' || id === 'trae' || id === 'trae-cn') return 'ide'
  return 'cli'
}

export function variantToBadge(variant: string): 'terminal' | 'monitor' | null {
  if (variant === 'desktop') return 'monitor'
  if (variant === 'cli') return 'terminal'
  return null
}

export function agentDisplayName(agent: { name: string; variant?: string; id: string }): string {
  return `${agent.name} (${variantLabel(agent.variant || getVariantFromId(agent.id))})`
}
