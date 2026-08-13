import { computed, ref } from 'vue'
import { useAgentsStore } from '@/stores/agents'

export function useAgentSelector(options?: { defaultAllSelected?: boolean }) {
  const { defaultAllSelected = true } = options ?? {}
  const agentsStore = useAgentsStore()

  const showDialog = ref(false)
  const selectedAgentIds = ref<Set<string>>(new Set())

  const activeGroups = computed(() => agentsStore.mergedGroups.filter(g => g.status === 'enabled'))
  // 组内可能含 disabled 变体成员（如 CLI enabled + Desktop disabled）。
  // 后端 validateAgentIDs 只接受 enabled/detected，提交的 id 必须按成员级过滤，
  // 否则整组勾选会把 disabled 成员也提交导致操作整体失败。
  const allAgentIds = computed(() =>
    activeGroups.value.flatMap(g => g.ids.filter(id => agentsStore.activeIds.get(id)))
  )
  const allSelected = computed(() => allAgentIds.value.length > 0 && selectedAgentIds.value.size === allAgentIds.value.length)
  const someSelected = computed(() => selectedAgentIds.value.size > 0 && selectedAgentIds.value.size < allAgentIds.value.length)

  function isGroupSelected(group: { ids: string[] }): boolean {
    // 只按组内 active 成员判断：disabled 成员不可选、不计入选中状态
    const active = group.ids.filter(id => agentsStore.activeIds.get(id))
    return active.length > 0 && active.every(id => selectedAgentIds.value.has(id))
  }

  function toggleGroup(group: { ids: string[] }, val: boolean) {
    const next = new Set(selectedAgentIds.value)
    for (const id of group.ids) {
      if (val) {
        if (agentsStore.activeIds.get(id)) next.add(id)
      } else {
        next.delete(id)
      }
    }
    selectedAgentIds.value = next
  }

  function toggleSelectAll(checked: boolean) {
    selectedAgentIds.value = checked ? new Set(allAgentIds.value) : new Set()
  }

  function openDialog() {
    selectedAgentIds.value = defaultAllSelected ? new Set(allAgentIds.value) : new Set()
    showDialog.value = true
  }

  return {
    showDialog,
    selectedAgentIds,
    activeGroups,
    allAgentIds,
    allSelected,
    someSelected,
    isGroupSelected,
    toggleGroup,
    toggleSelectAll,
    openDialog,
  }
}