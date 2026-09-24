import { describe, expect, it } from 'vitest'
import { isFilePickCancelled } from '@/lib/utils'

describe('isFilePickCancelled', () => {
  it('treats empty or missing message as cancellation', () => {
    expect(isFilePickCancelled(new Error(''))).toBe(true)
    expect(isFilePickCancelled(null)).toBe(true)
    expect(isFilePickCancelled(undefined)).toBe(true)
    expect(isFilePickCancelled('')).toBe(true)
  })

  it('matches cancel-like messages case-insensitively', () => {
    expect(isFilePickCancelled(new Error('cancelled by user'))).toBe(true)
    expect(isFilePickCancelled(new Error('Operation CANCEL'))).toBe(true)
    expect(isFilePickCancelled('User Canceled the dialog')).toBe(true)
  })

  it('rejects real errors', () => {
    expect(isFilePickCancelled(new Error('permission denied'))).toBe(false)
    expect(isFilePickCancelled(new Error('disk full'))).toBe(false)
  })
})
