// 组选择的同构纯逻辑，供 useAgentSelector / SkillsView / McpScanDialog 共用：
// 只按 active/capable 成员参与判断与增删，disabled 变体不可选、不计入选中状态。

/**
 * 组内所有 ACTIVE 成员均被选中才算整组选中。
 * selected 为 undefined（无该 key 的选择集）或组内无 active 成员时返回 false。
 */
export function isGroupFullySelected(
  selected: Set<string> | undefined,
  ids: string[],
  isActive: (id: string) => boolean,
): boolean {
  if (!selected) return false
  const active = ids.filter(isActive)
  return active.length > 0 && active.every(id => selected.has(id))
}

/**
 * 返回应用组开关后的新集合（不修改原集合）。
 * 启用时仅添加 canAdd 通过的成员；禁用时无条件移除组内 id。
 */
export function toggleGroupMembers(
  selected: Set<string>,
  ids: string[],
  enabled: boolean,
  canAdd: (id: string) => boolean,
): Set<string> {
  const next = new Set(selected)
  for (const id of ids) {
    if (enabled) {
      if (canAdd(id)) next.add(id)
    } else {
      next.delete(id)
    }
  }
  return next
}
