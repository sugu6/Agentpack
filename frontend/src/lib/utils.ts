import { clsx, type ClassValue } from 'clsx'
import { twMerge } from 'tailwind-merge'

export function cn(...inputs: ClassValue[]) {
  return twMerge(clsx(inputs))
}

export function transportLabel(transport?: string): string {
  if (!transport || transport === 'stdio') return 'Stdio'
  if (transport === 'sse') return 'SSE'
  if (transport === 'streamable-http') return 'Streamable HTTP'
  return transport
}

// 判断是否为用户取消文件/目录选择
export function isFilePickCancelled(e: unknown): boolean {
  const msg = (e instanceof Error ? e.message : String(e ?? '')).toLowerCase()
  return msg === '' || msg.includes('cancel')
}
