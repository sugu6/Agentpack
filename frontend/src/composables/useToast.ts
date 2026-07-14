import { toast } from 'vue-sonner'
import { useI18n } from 'vue-i18n'

// 统一的 toast 封装：默认文案中文，统一时长与位置
// - success: 2.5s
// - error:   4s（用户需要时间阅读错误）
// - warning: 3.5s
// - info:    3s
// 所有 toast 都关闭 closeButton，避免与 destructive 操作的对话框重复

export interface ToastOptions {
  description?: string
  duration?: number
  id?: string | number
}

const SUCCESS_DURATION = 2500
const ERROR_DURATION = 4000
const WARNING_DURATION = 3500
const INFO_DURATION = 3000

export function useToast() {
  const { t } = useI18n()

  function success(message: string, opts: ToastOptions = {}) {
    toast.success(message, {
      duration: SUCCESS_DURATION,
      ...opts,
    })
  }

  function error(message: string, opts: ToastOptions = {}) {
    toast.error(message, {
      duration: ERROR_DURATION,
      ...opts,
    })
  }

  function warning(message: string, opts: ToastOptions = {}) {
    toast.warning(message, {
      duration: WARNING_DURATION,
      ...opts,
    })
  }

  function info(message: string, opts: ToastOptions = {}) {
    toast.info(message, {
      duration: INFO_DURATION,
      ...opts,
    })
  }

  // 从 unknown 错误中提取可读消息
  function fromError(e: unknown, fallback = t('common.operationFailed')): string {
    if (e instanceof Error) return e.message || fallback
    if (typeof e === 'string') return e
    return fallback
  }

  return { success, error, warning, info, fromError, toast }
}
