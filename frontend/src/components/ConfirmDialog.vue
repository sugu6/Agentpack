<script setup lang="ts">
import { useConfirm } from '@/composables/useConfirm'
import { AlertDialog, AlertDialogContent, AlertDialogHeader, AlertDialogTitle, AlertDialogDescription, AlertDialogFooter, AlertDialogCancel, AlertDialogAction } from '@/components/ui'
import { PhWarning } from '@phosphor-icons/vue'

const { visible, options, resolve } = useConfirm()
</script>

<template>
  <AlertDialog v-model:open="visible" @update:open="(v) => { if (!v) resolve(false) }">
    <AlertDialogContent class="max-w-md">
      <AlertDialogHeader>
        <AlertDialogTitle class="flex items-center gap-2">
          <PhWarning v-if="options.variant === 'destructive'" :size="18" weight="duotone" class="text-destructive" />
          {{ options.title }}
        </AlertDialogTitle>
        <AlertDialogDescription>{{ options.message }}</AlertDialogDescription>
      </AlertDialogHeader>
      <AlertDialogFooter class="mt-2">
        <AlertDialogCancel @click="resolve(false)">
          {{ options.cancelText }}
        </AlertDialogCancel>
        <AlertDialogAction
          :class="options.variant === 'destructive' ? '!bg-destructive !text-destructive-foreground hover:!bg-destructive/90 !border !border-destructive' : '!border !border-input !bg-background !text-foreground hover:!bg-accent hover:!text-accent-foreground'"
          @click="resolve(true)"
        >
          {{ options.confirmText }}
        </AlertDialogAction>
      </AlertDialogFooter>
    </AlertDialogContent>
  </AlertDialog>
</template>
