// 前端错误桥：把 console.error/warn、未捕获异常、未处理的 Promise 拒绝
// 写入独立的 frontend 日志通道（~/.agentpack/logs/frontend.log，经
// LogFrontend 服务方法），由"设置 → 日志与诊断 → 导出zip"收集，默认不上传任何数据。
//
// 约束：本模块挂在全局异常路径上，任何情况下都不能抛错或形成日志回环。
import { api } from './api'

const MAX_MESSAGE = 2000
const MAX_STACK = 4000
const MAX_EVENTS_PER_MINUTE = 60
const MAX_SAME_MESSAGE = 3
const RATE_WINDOW_MS = 60_000
const MESSAGE_KEY_LEN = 200

let sentInWindow = 0
let windowStart = Date.now()
const seen = new Map<string, number>()

function withinRateLimit(key: string): boolean {
  const now = Date.now()
  if (now - windowStart > RATE_WINDOW_MS) {
    windowStart = now
    sentInWindow = 0
    seen.clear()
  }
  if (sentInWindow >= MAX_EVENTS_PER_MINUTE) return false
  // 同一错误最多记录 MAX_SAME_MESSAGE 次：防止渲染/网络异常循环刷爆日志
  const n = (seen.get(key) ?? 0) + 1
  seen.set(key, n)
  if (n > MAX_SAME_MESSAGE) return false
  sentInWindow++
  return true
}

function clip(value: unknown, max: number): string {
  let s: string
  if (value == null) return ''
  if (typeof value === 'string') {
    s = value
  } else if (value instanceof Error) {
    s = value.stack || `${value.name}: ${value.message}`
  } else {
    try {
      s = JSON.stringify(value) ?? String(value)
    } catch {
      s = String(value)
    }
  }
  return s.length > max ? s.slice(0, max) + '…' : s
}

function findStack(args: unknown[]): string {
  const err = args.find((a) => a instanceof Error) as Error | undefined
  return err?.stack ?? ''
}

function report(level: 'error' | 'warn', message: string, stack = ''): void {
  if (message.trim() === '') return
  if (!withinRateLimit(`${level}:${message.slice(0, MESSAGE_KEY_LEN)}`)) return
  api.diagnostics.logFrontend(level, clip(message, MAX_MESSAGE), clip(stack, MAX_STACK))
}

// installLogBridge 在应用挂载前调用一次，包装全局错误入口并保留原始控制台行为。
export function installLogBridge(): void {
  const origError = console.error.bind(console)
  const origWarn = console.warn.bind(console)

  console.error = (...args: unknown[]) => {
    origError(...args)
    report('error', args.map((a) => clip(a, MAX_MESSAGE)).join(' '), findStack(args))
  }
  console.warn = (...args: unknown[]) => {
    origWarn(...args)
    report('warn', args.map((a) => clip(a, MAX_MESSAGE)).join(' '), findStack(args))
  }

  window.addEventListener('error', (e) => {
    if (e instanceof ErrorEvent) {
      report(
        'error',
        `${e.message} @ ${e.filename}:${e.lineno}:${e.colno}`,
        e.error instanceof Error ? e.error.stack ?? '' : '',
      )
    }
  })

  window.addEventListener('unhandledrejection', (e) => {
    const reason = (e as PromiseRejectionEvent).reason
    report(
      'error',
      `unhandledrejection: ${clip(reason, MAX_MESSAGE)}`,
      reason instanceof Error ? reason.stack ?? '' : '',
    )
  })
}