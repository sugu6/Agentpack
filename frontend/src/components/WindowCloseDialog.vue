<script setup lang="ts">
import { ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { Dialog, DialogContent, DialogHeader, DialogTitle, DialogDescription, DialogFooter, Button, RadioGroup, RadioGroupItem, Checkbox, Label } from '@/components/ui'
import { useSettingsStore } from '@/stores/settings'
import { useToast } from '@/composables/useToast'
import { api } from '@/lib/api'

const props = defineProps<{ open: boolean }>()
const emit = defineEmits<{ 'update:open': [value: boolean] }>()

const { t } = useI18n()
const settings = useSettingsStore()
const toast = useToast()

const action = ref<'minimize' | 'exit'>('minimize')
const dontRemind = ref(false)
// 防重入：confirm 里要等待 settings.update + 后端动作（quit/hideWindow），
// 双击或连点会在第一次完成前发起第二次调用（重复保存、并发 quit），
// 加 busy 守卫使后续点击失效
const busy = ref(false)

watch(() => props.open, (v) => {
  if (v) {
    action.value = 'minimize'
    dontRemind.value = false
  }
})

async function confirm() {
  if (busy.value) return
  busy.value = true
  try {
    if (dontRemind.value) {
      // 保存用户选择到设置（不再提醒 + 默认行为）
      const cfg = JSON.parse(JSON.stringify(settings.config))
      cfg.windowAction = action.value
      cfg.windowNoRemind = true
      await settings.update(cfg)
    }
    if (action.value === 'exit') {
      await api.system.quit()
    } else {
      await api.system.hideWindow()
    }
    // 动作执行成功后再关闭对话框：若设置保存或动作失败，
    // 保持对话框打开并 toast 报错，避免"关闭后动作被静默丢弃"。
    emit('update:open', false)
  } catch (e) {
    toast.error(toast.fromError(e, t('common.operationFailed')))
  } finally {
    busy.value = false
  }
}
</script>

<template>
  <Dialog :open="open" @update:open="(v) => emit('update:open', v)">
    <DialogContent class="max-w-lg p-6" @pointer-down-outside.prevent>
      <DialogHeader>
        <DialogTitle>{{ t('dialog.close.title') }}</DialogTitle>
        <DialogDescription>{{ t('dialog.close.description') }}</DialogDescription>
      </DialogHeader>
      <RadioGroup
        class="flex items-center justify-center gap-12 py-6"
        :model-value="action"
        @update:model-value="(v) => action = String(v) as 'minimize' | 'exit'"
      >
        <label class="flex items-center gap-2 cursor-pointer select-none">
          <RadioGroupItem value="minimize" />
          <span class="text-sm">{{ t('dialog.close.minimize') }}</span>
        </label>
        <label class="flex items-center gap-2 cursor-pointer select-none">
          <RadioGroupItem value="exit" />
          <span class="text-sm">{{ t('dialog.close.exit') }}</span>
        </label>
      </RadioGroup>
      <DialogFooter class="flex items-center justify-between sm:justify-between pt-2">
        <label class="flex items-center gap-2 cursor-pointer select-none">
          <Checkbox
            :model-value="dontRemind"
            @update:model-value="(v) => dontRemind = v === true"
          />
          <span class="text-sm text-muted-foreground">{{ t('settings.window.noRemind') }}</span>
        </label>
        <div class="flex gap-2">
          <Button variant="outline" size="sm" @click="emit('update:open', false)">{{ t('common.cancel') }}</Button>
          <Button size="sm" :disabled="busy" @click="confirm">{{ t('common.confirm') }}</Button>
        </div>
      </DialogFooter>
    </DialogContent>
  </Dialog>
</template>
