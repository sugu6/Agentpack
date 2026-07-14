<script setup lang="ts">
import { ref, watch } from 'vue'
import { Dialog, DialogContent, DialogHeader, DialogTitle, DialogDescription, DialogFooter, Button, RadioGroup, RadioGroupItem, Checkbox, Label } from '@/components/ui'
import { useSettingsStore } from '@/stores/settings'
import { useToast } from '@/composables/useToast'
import { api } from '@/lib/api'

const props = defineProps<{ open: boolean }>()
const emit = defineEmits<{ 'update:open': [value: boolean] }>()

const settings = useSettingsStore()
const toast = useToast()

const action = ref<'minimize' | 'exit'>('minimize')
const dontRemind = ref(false)

watch(() => props.open, (v) => {
  if (v) {
    action.value = 'minimize'
    dontRemind.value = false
  }
})

async function confirm() {
  emit('update:open', false)
  try {
    if (dontRemind.value) {
      // 保存用户选择到设置
      const cfg = JSON.parse(JSON.stringify(settings.config))
      cfg.windowAction = action.value
      await settings.update(cfg)
    }
    if (action.value === 'exit') {
      await api.system.quit()
    } else {
      await api.system.hideWindow()
    }
  } catch (e) {
    toast.error(toast.fromError(e, '操作失败'))
  }
}
</script>

<template>
  <Dialog :open="open" @update:open="(v) => emit('update:open', v)">
    <DialogContent class="max-w-lg p-6" @pointer-down-outside.prevent>
      <DialogHeader>
        <DialogTitle>关闭窗口</DialogTitle>
        <DialogDescription>选择关闭窗口时的行为。</DialogDescription>
      </DialogHeader>
      <RadioGroup
        class="flex items-center justify-center gap-12 py-6"
        :model-value="action"
        @update:model-value="(v) => action = String(v) as 'minimize' | 'exit'"
      >
        <label class="flex items-center gap-2 cursor-pointer select-none">
          <RadioGroupItem value="minimize" />
          <span class="text-sm">最小化到系统托盘</span>
        </label>
        <label class="flex items-center gap-2 cursor-pointer select-none">
          <RadioGroupItem value="exit" />
          <span class="text-sm">退出程序</span>
        </label>
      </RadioGroup>
      <DialogFooter class="flex items-center justify-between sm:justify-between pt-2">
        <label class="flex items-center gap-2 cursor-pointer select-none">
          <Checkbox
            :model-value="dontRemind"
            @update:model-value="(v) => dontRemind = v === true"
          />
          <span class="text-sm text-muted-foreground">不再提醒</span>
        </label>
        <div class="flex gap-2">
          <Button variant="outline" size="sm" @click="emit('update:open', false)">取消</Button>
          <Button size="sm" @click="confirm">确认</Button>
        </div>
      </DialogFooter>
    </DialogContent>
  </Dialog>
</template>
