<script setup lang="ts">
import type { TabsListProps } from "reka-ui"
import type { HTMLAttributes } from "vue"
import { reactiveOmit } from "@vueuse/core"
import { TabsList } from "reka-ui"
import { cn } from "@/lib/utils"

const props = defineProps<TabsListProps & { class?: HTMLAttributes["class"] }>()

const delegatedProps = reactiveOmit(props, "class")
</script>

<template>
  <TabsList
    data-slot="tabs-list"
    v-bind="delegatedProps"
    :class="cn(
      'inline-flex items-center justify-center rounded-lg p-0.5 shadow-inner ring-1 ring-black/5 dark:ring-white/10',
      'bg-white/10 dark:bg-white/[0.07] text-muted-foreground',
      props.class,
    )"
  >
    <slot />
  </TabsList>
</template>