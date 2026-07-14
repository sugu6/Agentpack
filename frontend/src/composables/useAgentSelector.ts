import { computed, ref } from 'vue'
import { useAgentsStore } from '@/stores/agents'

export function useAgentSelector(options?: { defaultAllSelected?: boolean }) {
  const { defaultAllSelected = true } = options ?? {}
  const agentsStore = useAgentsStore()

  const showDialog = ref(false)
  const selectedAgentIds = ref<Set<string>>(new Set())

  const activeGroups = computed(() => agentsStore.mergedGroups.filter(g => g.status === 'enabled'))
  const allAgentIds = computed(() => activeGroups.value.flatMap(g => g.ids))
  const allSelected = computed(() => allAgentIds.value.length > 0 && selectedAgentIds.value.size === allAgentIds.value.length)
  const someSelected = computed(() => selectedAgentIds.value.size > 0 && selectedAgentIds.value.size < allAgentIds.value.length)

  function isGroupSelected(group: { ids: string[] }): boolean {
    return group.ids.every(id => selectedAgentIds.value.has(id))
  }

  function toggleGroup(group: { ids: string[] }, val: boolean) {
    const next = new Set(selectedAgentIds.value)
    for (const id of group.ids) {
      if (val) next.add(id)
      else next.delete(id)
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