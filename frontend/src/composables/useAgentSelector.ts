import { computed, ref } from 'vue'
import { useAgentsStore } from '@/stores/agents'
import { isGroupFullySelected, toggleGroupMembers } from '@/lib/selection'

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
    return isGroupFullySelected(selectedAgentIds.value, group.ids, id => !!agentsStore.activeIds.get(id))
  }

  function toggleGroup(group: { ids: string[] }, val: boolean) {
    selectedAgentIds.value = toggleGroupMembers(selectedAgentIds.value, group.ids, val, id => !!agentsStore.activeIds.get(id))
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