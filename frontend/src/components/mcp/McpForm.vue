<script setup lang="ts">
import { ref, watch, nextTick } from 'vue'
import { useI18n } from 'vue-i18n'
import { useMcpStore } from '@/stores/mcp'
import { Dialog, DialogContent, DialogHeader, DialogTitle, DialogDescription, DialogFooter, DialogTrigger, Button, Input, Label, Tabs, TabsList, TabsTrigger } from '@/components/ui'
import { PhPlus, PhPencilSimple, PhCode } from '@phosphor-icons/vue'
import { useAgentSelector } from '@/composables/useAgentSelector'
import { agentLogoUrl, agentLogoInvertClass } from '@/composables/useAgentHelpers'
import type { McpServer } from '@/lib/api'
import { useToast } from '@/composables/useToast'

const props = defineProps<{
  mode: 'add' | 'edit'
  server?: McpServer
}>()

const emit = defineEmits<{
  (e: 'updated'): void
}>()

const { t } = useI18n()

function resolveTransport(type: string): 'stdio' | 'sse' | 'http' {
  if (type === 'local') return 'stdio'
  if (type === 'remote') return 'http'
  if (type === 'stdio' || type === 'sse' || type === 'http') return type
  return 'stdio'
}

function normalizeCommandArgs(srv: Record<string, any>): { command: string; args: string[] } {
  if (Array.isArray(srv.command)) {
    return {
      command: srv.command.length > 0 ? srv.command[0] : '',
      args: srv.command.length > 1 ? srv.command.slice(1) : (srv.args || []),
    }
  }
  return { command: srv.command || '', args: srv.args || [] }
}

const mcp = useMcpStore()
const { selectedAgentIds, activeGroups, allAgentIds, allSelected, someSelected, isGroupSelected, toggleGroup, toggleSelectAll } = useAgentSelector({ defaultAllSelected: false })
const toast = useToast()

const open = ref(false)
const formErrors = ref<string[]>([])
const entryMode = ref<'form' | 'json'>('form')
const jsonRaw = ref('')
const form = ref<{
  name: string
  description: string
  command: string
  args: string
  env: string
  transport: 'stdio' | 'sse' | 'http'
  url: string
}>({
  name: '',
  description: '',
  command: '',
  args: '',
  env: '',
  transport: 'stdio',
  url: '',
})

watch(
  () => props.server,
  (s) => {
    if (s) {
      form.value = {
        name: s.name,
        description: s.description || '',
        command: s.command,
        args: formatArgs(s.args || []),
        env: Object.entries(s.env || {})
          .map(([k, v]) => `${k}=${v}`)
          .join('\n'),
        transport: resolveTransport(s.transport || 'stdio'),
        url: s.url || '',
      }
    }
  },
  { immediate: true },
)

watch(open, (isOpen) => {
  if (isOpen) {
    if (props.mode === 'edit' && props.server?.boundAgents) {
      selectedAgentIds.value = new Set(props.server.boundAgents)
    } else {
      selectedAgentIds.value = new Set()
    }
    if (props.mode === 'add' && !props.server) {
      reset()
    }
  }
})

function reset() {
  form.value = {
    name: '',
    description: '',
    command: '',
    args: '',
    env: '',
    transport: 'stdio',
    url: '',
  }
  jsonRaw.value = ''
  entryMode.value = 'form'
  formErrors.value = []
}



function stripJsonComments(text: string): string {
  return text
    .replace(/\/\/.*$/gm, '')
    .replace(/\/\*[\s\S]*?\*\//g, '')
    .replace(/^\s*[\r\n]/gm, '')
}

function jsonToForm(text: string): { name: string; server: Record<string, any> } | null {
  let parsed: any
  try {
    parsed = JSON.parse(stripJsonComments(text))
  } catch {
    formErrors.value = [t('mcp.errors.jsonInvalid')]
    return null
  }
  if (!parsed || typeof parsed !== 'object') {
    formErrors.value = [t('mcp.errors.jsonNotObject')]
    return null
  }
  let servers: Record<string, any> | undefined

  if (parsed.mcpServers && typeof parsed.mcpServers === 'object') {
    servers = parsed.mcpServers
  } else if (parsed.servers && typeof parsed.servers === 'object') {
    servers = parsed.servers
  } else if (parsed.mcp && typeof parsed.mcp === 'object') {
    if (parsed.mcp.servers && typeof parsed.mcp.servers === 'object') {
      servers = parsed.mcp.servers
    } else {
      servers = parsed.mcp
    }
  }

  if (!servers) {
    formErrors.value = [t('mcp.errors.noServersField')]
    return null
  }

  const entries = Object.entries(servers)
  if (entries.length === 0) {
    formErrors.value = [t('mcp.errors.noServers')]
    return null
  }

  const [name, srv] = entries[0]
  if (!srv || typeof srv !== 'object') {
    formErrors.value = [t('mcp.errors.serverInvalid', { name })]
    return null
  }

  return { name, server: srv }
}

function formToJson(): string {
  const srv: Record<string, any> = {}
  if (form.value.command) srv.command = form.value.command
  if (form.value.args) srv.args = parseArgs(form.value.args)
  if (form.value.env) {
    const env = parseEnvVars(form.value.env)
    if (Object.keys(env).length) srv.env = env
  }
  if (form.value.description) srv.description = form.value.description
  srv.type = form.value.transport
  if (form.value.url) srv.url = form.value.url
  const name = form.value.name || 'server-name'
  return JSON.stringify({ mcpServers: { [name]: srv } }, null, 2)
}

function switchToJson() {
  entryMode.value = 'json'
  jsonRaw.value = form.value.name ? formToJson() : ''
  formErrors.value = []
}

function switchToForm() {
  formErrors.value = []
  const result = jsonToForm(jsonRaw.value)
  if (!result) return

  const { name, server: srv } = result
  const transport = resolveTransport(srv.type)

  const { command, args } = normalizeCommandArgs(srv)

  const env = srv.env || srv.environment || {}
  const envStr = Object.entries(env)
    .map(([k, v]) => `${k}=${v}`)
    .join('\n')
  const url = srv.url || ''

  form.value = {
    name: name || '',
    description: srv.description || '',
    command,
    args: formatArgs(args),
    env: envStr,
    transport: transport as 'stdio' | 'sse' | 'http',
    url,
  }
  entryMode.value = 'form'
}

function formatJson() {
  try {
    jsonRaw.value = JSON.stringify(JSON.parse(stripJsonComments(jsonRaw.value)), null, 2)
  } catch {
    formErrors.value = [t('mcp.errors.formatJsonFailed')]
  }
}

function onEntryModeChange(val: string | number) {
  if (val === 'json' && entryMode.value === 'form') {
    switchToJson()
  } else if (val === 'form' && entryMode.value === 'json') {
    switchToForm()
  }
}

function parseArgs(input: string): string[] {
  const args: string[] = []
  const trimmed = input.trim()
  if (!trimmed) return args
  let current = ''
  let inDouble = false
  let inSingle = false
  let escaping = false
  for (let i = 0; i < trimmed.length; i++) {
    const ch = trimmed[i]
    if (escaping) {
      current += ch
      escaping = false
    } else if (ch === '\\') {
      escaping = true
    } else if (ch === '"' && !inSingle) {
      inDouble = !inDouble
    } else if (ch === "'" && !inDouble) {
      inSingle = !inSingle
    } else if (/\s/.test(ch) && !inDouble && !inSingle) {
      if (current) { args.push(current); current = '' }
    } else {
      current += ch
    }
  }
  if (escaping) current += '\\'
  if (current) args.push(current)
  return args
}

function formatArgs(args: string[]): string {
  return args
    .map((a) => {
      if (a === '') return '""'
      if (/[\s"'\\]/.test(a)) {
        return '"' + a.replace(/[\\"]/g, '\\$&') + '"'
      }
      return a
    })
    .join(' ')
}

function parseEnvVars(input: string): Record<string, string> {
  const env: Record<string, string> = {}
  for (const line of input.split(/\n/)) {
    const m = line.match(/^\s*([A-Za-z_][A-Za-z0-9_]*)\s*=\s*(.*)$/)
    if (!m) continue
    let val = m[2]
    if ((val.startsWith('"') && val.endsWith('"')) || (val.startsWith("'") && val.endsWith("'"))) {
      val = val.slice(1, -1)
    } else {
      const commentIdx = val.search(/(?<!\\)#/)
      if (commentIdx !== -1) {
        val = val.substring(0, commentIdx)
      }
    }
    env[m[1]] = val.trimEnd()
  }
  return env
}

async function submit() {
  formErrors.value = []
  const errors: string[] = []

  if (entryMode.value === 'json') {
    const result = jsonToForm(jsonRaw.value)
    if (!result) return
    const { name, server: srv } = result

    if (!name) errors.push(t('mcp.errors.nameRequired'))

    const transport = resolveTransport(srv.type)

    const { command, args } = normalizeCommandArgs(srv)

    if (!command && transport === 'stdio') errors.push(t('mcp.errors.commandRequired'))
    if ((transport === 'sse' || transport === 'http') && !srv.url) errors.push(t('mcp.errors.urlRequired'))
    if (srv.url && transport !== 'stdio') {
      try { new URL(srv.url) } catch { errors.push(t('mcp.errors.urlInvalid')) }
    }
    if (errors.length > 0) {
      formErrors.value = errors
      return
    }

    const env = srv.env || srv.environment || {}
    const targetAgentIds = [...selectedAgentIds.value]
    if (targetAgentIds.length === 0) {
      formErrors.value = [t('mcp.errors.noAgent')]
      return
    }

    const now = new Date().toISOString()
    const server: McpServer = {
      id: props.server?.id || `manual:${crypto.randomUUID()}`,
      name,
      description: srv.description || '',
      command,
      args,
      env: Object.keys(env).length ? env : undefined,
      transport,
      url: srv.url || undefined,
      source: 'manual',
      boundAgents: [],
      installedAt: props.server?.installedAt || now,
      updatedAt: now,
    }

    try {
      if (props.mode === 'edit' && props.server) {
        await mcp.update(props.server.id, server, targetAgentIds)
        toast.success(t('mcp.toast.updated', { name: server.name }))
      } else {
        await mcp.add(server, targetAgentIds)
        toast.success(t('mcp.toast.added', { name: server.name }))
      }
      formErrors.value = []
      emit('updated')
      open.value = false
      if (props.mode === 'add') reset()
    } catch (e) {
      const msg = toast.fromError(e, t('common.operationFailed'))
      formErrors.value = [t('mcp.errors.operationFailed')]
      toast.error(msg)
    }
    return
  }

  if (!form.value.name) errors.push(t('mcp.errors.nameRequired'))
  if (!form.value.command && form.value.transport === 'stdio') errors.push(t('mcp.errors.commandRequired'))
  if ((form.value.transport === 'sse' || form.value.transport === 'http') && !form.value.url) {
    errors.push(t('mcp.errors.urlRequired'))
  }
  if (form.value.url && form.value.transport !== 'stdio') {
    try { new URL(form.value.url) } catch { errors.push(t('mcp.errors.urlInvalid')) }
  }
  if (errors.length > 0) {
    formErrors.value = errors
    return
  }

  const args = parseArgs(form.value.args)
  const env = parseEnvVars(form.value.env)

  const targetAgentIds = [...selectedAgentIds.value]
  if (targetAgentIds.length === 0) {
    formErrors.value = [t('mcp.errors.noAgent')]
    return
  }

  const now = new Date().toISOString()
  const server: McpServer = {
    id: props.server?.id || `manual:${crypto.randomUUID()}`,
    name: form.value.name,
    description: form.value.description,
    command: form.value.command,
    args,
    env: Object.keys(env).length ? env : undefined,
    transport: form.value.transport,
    url: form.value.url || undefined,
    source: 'manual',
    boundAgents: [],
    installedAt: props.server?.installedAt || now,
    updatedAt: now,
  }

  try {
    if (props.mode === 'edit' && props.server) {
      await mcp.update(props.server.id, server, targetAgentIds)
      toast.success(t('mcp.toast.updated', { name: server.name }))
    } else {
      await mcp.add(server, targetAgentIds)
      toast.success(t('mcp.toast.added', { name: server.name }))
    }
    formErrors.value = []
    emit('updated')
    open.value = false
    if (props.mode === 'add') reset()
  } catch (e) {
    const msg = toast.fromError(e, t('common.operationFailed'))
    formErrors.value = [t('mcp.errors.operationFailed')]
    toast.error(msg)
  }
}
</script>

<template>
  <Dialog v-model:open="open">
    <DialogTrigger v-if="mode === 'add'" as-child>
      <Button size="sm">
        <PhPlus :size="14" />
        <span>{{ t('mcp.add') }}</span>
      </Button>
    </DialogTrigger>
    <DialogTrigger v-else as-child>
      <Button variant="outline" size="icon" :aria-label="t('common.edit')">
        <PhPencilSimple :size="14" />
      </Button>
    </DialogTrigger>
    <DialogContent class="max-w-2xl flex flex-col max-h-[85vh]">
      <DialogHeader>
        <DialogTitle>{{ mode === 'add' ? t('mcp.addServerTitle') : t('mcp.editServerTitle') }}</DialogTitle>
        <DialogDescription>
          {{ t('mcp.dialogDescription') }}
        </DialogDescription>
      </DialogHeader>
      <Tabs :model-value="entryMode" @update:model-value="onEntryModeChange" class="w-full">
        <TabsList class="w-full">
          <TabsTrigger value="form" class="flex-1">{{ t('mcp.tab.form') }}</TabsTrigger>
          <TabsTrigger value="json" class="flex-1">
            <PhCode :size="12" />JSON
          </TabsTrigger>
        </TabsList>
      </Tabs>

      <form @submit.prevent="submit" class="flex flex-col min-h-0 mt-4">
        <div :class="['min-h-0 flex-1 space-y-4 py-2 pl-1 pr-2 pb-6', entryMode === 'form' ? 'overflow-y-auto' : 'overflow-hidden']">
          <template v-if="entryMode === 'form'">
            <div class="grid grid-cols-2 gap-3">
              <div class="space-y-1.5">
                <Label for="mcp-name">{{ t('mcp.name') }}</Label>
                <Input id="mcp-name" v-model="form.name" name="mcp-name" autocomplete="off" placeholder="github" required />
              </div>
              <div class="space-y-1.5">
                <Label>{{ t('mcp.transport') }}</Label>
                <Tabs v-model="form.transport" class="w-full">
                  <TabsList class="w-full">
                    <TabsTrigger value="stdio" class="flex-1">STDIO</TabsTrigger>
                    <TabsTrigger value="sse" class="flex-1">SSE</TabsTrigger>
                    <TabsTrigger value="http" class="flex-1">HTTP</TabsTrigger>
                  </TabsList>
                </Tabs>
              </div>
            </div>

            <div class="space-y-1.5">
              <Label for="mcp-desc">{{ t('common.description') }}</Label>
              <Input id="mcp-desc" v-model="form.description" name="mcp-desc" autocomplete="off" :placeholder="t('mcp.descriptionPlaceholder')" />
            </div>

            <div class="space-y-1.5">
              <Label for="mcp-cmd">{{ t('mcp.command') }}</Label>
              <Input id="mcp-cmd" v-model="form.command" name="mcp-cmd" autocomplete="off" placeholder="npx" :required="form.transport === 'stdio'" class="font-mono" />
            </div>

            <div class="space-y-1.5">
              <Label for="mcp-args">{{ t('mcp.args') }}</Label>
              <Input id="mcp-args" v-model="form.args" name="mcp-args" autocomplete="off" placeholder="-y @modelcontextprotocol/server-github" class="font-mono" />
            </div>

            <div v-if="form.transport !== 'stdio'" class="space-y-1.5">
              <Label for="mcp-url">URL</Label>
              <Input id="mcp-url" v-model="form.url" name="mcp-url" autocomplete="off" placeholder="https://..." class="font-mono" />
            </div>

            <div class="space-y-1.5">
              <Label for="mcp-env">{{ t('mcp.env') }}</Label>
              <textarea
                id="mcp-env"
                name="mcp-env"
                v-model="form.env"
                autocomplete="off"
                placeholder="GITHUB_TOKEN=xxx&#10;DEBUG=true"
                style="min-height: 2.75rem"
                class="flex w-full rounded-md border border-input bg-transparent px-3 py-2 font-mono text-xs shadow-sm focus-visible:outline-none focus-visible:ring-1 focus-visible:ring-ring leading-[1.5]"
              />
            </div>
          </template>

          <template v-if="entryMode === 'json'">
              <div class="flex flex-col space-y-1.5">
                <Label>{{ t('mcp.jsonConfig') }}</Label>
                <textarea
                  v-model="jsonRaw"
                  autocomplete="off"
                  placeholder='{
  // GitHub MCP
  "mcpServers": {
    "github": {
      "command": "npx",
      "args": ["-y", "@modelcontextprotocol/server-github"],
      "env": { "GITHUB_TOKEN": "your_token" },
      "type": "stdio"
    }
  }
}'
                  class="flex w-full h-[240px] rounded-md border border-input bg-transparent px-3 py-2 font-mono text-xs shadow-sm focus-visible:outline-none focus-visible:ring-1 focus-visible:ring-ring leading-relaxed resize-y placeholder:text-muted-foreground/30"
                />
              </div>
          </template>

          <div class="space-y-2">
            <label class="flex items-center gap-2 text-sm font-medium">
              <input
                type="checkbox"
                :checked="allSelected"
                :indeterminate="someSelected"
                @change="(e: Event) => toggleSelectAll((e.target as HTMLInputElement).checked)"
                class="h-4 w-4"
              />
              {{ t('common.toggleAll') }}
              <span v-if="selectedAgentIds.size > 0" class="ml-auto text-xs text-muted-foreground">
                {{ selectedAgentIds.size }}/{{ allAgentIds.length }} {{ t('common.selected') }}
              </span>
            </label>
            <div class="flex flex-wrap gap-1.5">
              <button
                v-for="group in activeGroups"
                :key="group.id"
                type="button"
                :class="[
                  'flex items-center gap-1.5 rounded-full border px-2.5 py-1 text-xs transition-colors',
                  isGroupSelected(group)
                    ? 'border-primary bg-primary text-primary-foreground'
                    : 'border-border bg-background hover:bg-accent',
                ]"
                @click="toggleGroup(group, !isGroupSelected(group))"
              >
                <img
                  v-if="agentLogoUrl(group.id)"
                  :src="agentLogoUrl(group.id)"
                  :class="['h-3.5 w-3.5', agentLogoInvertClass(group.id)]"
                  alt=""
                />
                <span>{{ group.name }}</span>
                <span class="text-[10px] opacity-60">{{ group.ids.length }}</span>
              </button>
            </div>
          </div>

          <div v-if="formErrors.length > 0" class="rounded-md border border-destructive/50 bg-destructive/5 p-3">
            <p class="text-xs font-medium text-destructive">{{ t('mcp.errors.fixErrors') }}</p>
            <ul class="mt-1 space-y-0.5">
              <li v-for="err in formErrors" :key="err" class="text-xs text-destructive/80">
                {{ err }}
              </li>
            </ul>
          </div>
        </div>

        <div v-if="entryMode === 'json'" class="flex items-start gap-2 shrink-0 px-1">
          <Button type="button" variant="outline" size="sm" class="h-6 text-[11px]" @click="formatJson">
            {{ t('mcp.formatJson') }}
          </Button>
          <span class="text-[11px] text-muted-foreground/70">
            {{ t('mcp.jsonSupportHint') }}
            <button type="button" class="underline hover:text-foreground" @click="switchToForm">{{ t('mcp.switchToForm') }}</button>
            {{ t('mcp.parseHint') }}
          </span>
        </div>

        <DialogFooter class="shrink-0">
          <Button type="button" variant="outline" @click="open = false">{{ t('common.cancel') }}</Button>
          <Button type="submit">
            {{ mode === 'add' ? t('common.install') : t('common.save') }}
          </Button>
        </DialogFooter>
      </form>
    </DialogContent>
  </Dialog>
</template>