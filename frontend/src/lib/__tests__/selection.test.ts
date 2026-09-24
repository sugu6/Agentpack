import { describe, expect, it } from 'vitest'
import { isGroupFullySelected, toggleGroupMembers } from '@/lib/selection'

// 语义取自 useAgentSelector / SkillsView / McpScanDialog 三处同构实现：
// 只按 active/capable 成员参与判断与增删，disabled 变体被跳过。

describe('isGroupFullySelected', () => {
  const isActive = (id: string) => id !== 'disabled-1'

  it('returns false when selected set is undefined', () => {
    expect(isGroupFullySelected(undefined, ['a'], isActive)).toBe(false)
  })

  it('returns false when no active member is selected', () => {
    expect(isGroupFullySelected(new Set(), ['a'], isActive)).toBe(false)
    expect(isGroupFullySelected(new Set(['x']), ['a'], isActive)).toBe(false)
  })

  it('returns false when group has no active member', () => {
    expect(isGroupFullySelected(new Set(['disabled-1']), ['disabled-1'], isActive)).toBe(false)
  })

  it('returns true only when every ACTIVE member is selected', () => {
    expect(isGroupFullySelected(new Set(['a', 'disabled-1']), ['a', 'disabled-1'], isActive)).toBe(true)
    // disabled 成员即使未选中也不影响判定
    expect(isGroupFullySelected(new Set(['a']), ['a', 'disabled-1'], isActive)).toBe(true)
    // active 成员缺失即 false
    expect(isGroupFullySelected(new Set(['a']), ['a', 'b'], isActive)).toBe(false)
  })
})

describe('toggleGroupMembers', () => {
  const canAdd = (id: string) => id !== 'off'

  it('enable: adds only addable ids and returns a NEW set', () => {
    const original = new Set(['x'])
    const out = toggleGroupMembers(original, ['a', 'off'], true, canAdd)
    expect(out).not.toBe(original)
    expect(original.has('a')).toBe(false) // 原集合不被修改
    expect([...out].sort()).toEqual(['a', 'x'])
  })

  it('disable: removes ids regardless of canAdd', () => {
    const out = toggleGroupMembers(new Set(['a', 'off']), ['a', 'off'], false, canAdd)
    expect(out.size).toBe(0)
  })

  it('does not add ids rejected by canAdd, and returns a NEW set', () => {
    const original = new Set(['x'])
    const out = toggleGroupMembers(original, ['off'], true, canAdd)
    expect([...out]).toEqual(['x'])
    expect(out).not.toBe(original)
  })
})
