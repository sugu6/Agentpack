<script setup lang="ts">
import { ref, watch } from 'vue'
import { useConfirm } from '@/composables/useConfirm'
import { AlertDialog, AlertDialogContent, AlertDialogHeader, AlertDialogTitle, AlertDialogDescription, AlertDialogFooter, AlertDialogCancel, AlertDialogAction } from '@/components/ui'
import { PhWarning } from '@phosphor-icons/vue'

const confirmStore = useConfirm()
const { resolve } = confirmStore

// 使用本地 ref 绑定 AlertDialog，避免 reka-ui 对 Pinia ref 的兼容问题
const localOpen = ref(false)

watch(() => confirmStore.visible, (v) => {
  localOpen.value = v
})

watch(() => localOpen.value, (v) => {
  if (!v) resolve(false)
})
</script>

<template>
  <AlertDialog v-model:open="localOpen">
    <AlertDialogContent class="max-w-md">
      <AlertDialogHeader>
        <AlertDialogTitle class="flex items-center gap-2">
          <PhWarning v-if="confirmStore.options.variant === 'destructive'" :size="18" weight="duotone" class="text-destructive" />
          {{ confirmStore.options.title }}
        </AlertDialogTitle>
        <AlertDialogDescription>{{ confirmStore.options.message }}</AlertDialogDescription>
      </AlertDialogHeader>
      <AlertDialogFooter class="mt-2">
        <AlertDialogCancel @click="resolve(false)">
          {{ confirmStore.options.cancelText }}
        </AlertDialogCancel>
        <AlertDialogAction
          :class="confirmStore.options.variant === 'destructive' ? '!bg-destructive !text-destructive-foreground hover:!bg-destructive/90 !border !border-destructive' : '!border !border-input !bg-background !text-foreground hover:!bg-accent hover:!text-accent-foreground'"
          @click="resolve(true)"
        >
          {{ confirmStore.options.confirmText }}
        </AlertDialogAction>
      </AlertDialogFooter>
    </AlertDialogContent>
  </AlertDialog>
</template>
