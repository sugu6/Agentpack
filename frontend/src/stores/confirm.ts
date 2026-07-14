import { defineStore } from 'pinia'
import { readonly, ref } from 'vue'

export interface ConfirmOptions {
  title: string
  message: string
  confirmText?: string
  cancelText?: string
  variant?: 'default' | 'destructive'
}

let confirmCounter = 0
const CONFIRM_AUTO_TIMEOUT_MS = 5 * 60 * 1000

export const useConfirmStore = defineStore('confirm', () => {
  const visible = ref(false)
  const options = ref<ConfirmOptions>({ title: '', message: '' })
  const resolvers: Array<{ id: number; resolve: (value: boolean) => void }> = []
  let currentId = 0

  function confirm(opts: ConfirmOptions): Promise<boolean> {
    const id = ++confirmCounter
    // Reject any pending confirm before starting a new one
    while (resolvers.length > 0) {
      const { resolve: prevResolve } = resolvers.shift()!
      prevResolve(false)
    }
    options.value = {
      ...opts,
      confirmText: opts.confirmText || '确认',
      cancelText: opts.cancelText || '取消',
      variant: opts.variant || 'default',
    }
    currentId = id
    visible.value = true
    return new Promise<boolean>((resolve) => {
      const timer = setTimeout(() => {
        // Auto-resolve false on timeout (default 5 minutes)
        const idx = resolvers.findIndex(r => r.id === id)
        if (idx >= 0) {
          resolvers.splice(idx, 1)
          resolve(false)
          if (resolvers.length === 0) visible.value = false
        }
      }, CONFIRM_AUTO_TIMEOUT_MS)
      resolvers.push({ id, resolve: (v: boolean) => { clearTimeout(timer); resolve(v) } })
    })
  }

  function resolve(value: boolean) {
    visible.value = false
    // 只 resolve 当前可见的确认对话框
    const idx = resolvers.findIndex(r => r.id === currentId)
    if (idx >= 0) {
      const { resolve: resolver } = resolvers.splice(idx, 1)[0]
      resolver(value)
    }
  }

  function rejectAll() {
    visible.value = false
    for (const { resolve: resolver } of resolvers) {
      resolver(false)
    }
    resolvers.length = 0
  }

  return { visible, options: readonly(options), confirm, resolve, rejectAll }
})
