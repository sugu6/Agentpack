<script setup lang="ts">
import { Tooltip, TooltipContent, TooltipProvider, TooltipTrigger } from '@/components/ui'
import { agentLogoUrl, agentLogoInvertClass } from '@/composables/useAgentHelpers'
import { PhTerminal, PhMonitor } from '@phosphor-icons/vue'

const props = defineProps<{
  agentId: string
  agentName: string
  modelValue: boolean
  disabled?: boolean
  badge?: 'terminal' | 'monitor' | null
}>()

const emit = defineEmits<{
  (e: 'update:modelValue', val: boolean): void
}>()

function toggle() {
  if (props.disabled) return
  emit('update:modelValue', !props.modelValue)
}
</script>

<template>
  <TooltipProvider>
    <Tooltip>
      <TooltipTrigger as-child>
        <button
          type="button"
          :disabled="disabled"
          :class="[
            'relative inline-flex items-center justify-center rounded-lg transition-all duration-150',
            'h-8 w-8',
            modelValue
              ? 'bg-primary/10 text-primary ring-1 ring-primary/30 shadow-sm'
              : 'ring-1 ring-border/40 text-muted-foreground',
            disabled
              ? 'cursor-not-allowed opacity-30'
              : 'cursor-pointer hover:bg-primary/10 hover:ring-1 hover:ring-primary/30',
          ]"
          @click="toggle"
        >
          <img
            :src="agentLogoUrl(agentId)"
            :alt="agentName"
            :class="[
              'h-4 w-4 object-contain',
              agentLogoInvertClass(agentId),
            ]"
          />
          <span
            v-if="badge"
            :class="[
              'absolute -bottom-px -right-px flex items-center justify-center rounded-sm border',
              'h-[14px] w-[14px]',
              modelValue
                ? 'bg-primary border-primary/30 text-primary-foreground'
                : 'bg-muted-foreground/10 border-border/40 text-muted-foreground/60',
            ]"
          >
            <PhTerminal v-if="badge === 'terminal'" :size="9" weight="bold" />
            <PhMonitor v-else-if="badge === 'monitor'" :size="9" weight="bold" />
          </span>
        </button>
      </TooltipTrigger>
      <TooltipContent side="top">
        {{ agentName }}
      </TooltipContent>
    </Tooltip>
  </TooltipProvider>
</template>
