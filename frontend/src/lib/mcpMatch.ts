import type { MarketServer, McpServer } from '@/lib/api'

/**
 * 归一化 stdio 命令：剥离 Windows cmd /c 包装 + npx 包名的 @latest 后缀。
 * 镜像后端 internal/mcp/store.go 的 normalizeCommand 逻辑，
 * 用于前端匹配市场条目与已安装 MCP（手动安装的条目 source="config"、sourceId=""，
 * 无法通过 source+sourceId 精确匹配）。
 */
export function normalizeCommand(cmd: string, args: string[] = []): { command: string; args: string[] } {
  let command = cmd
  let normalizedArgs = [...args]

  // 1. 剥离 Windows cmd /c 包装
  if (command === 'cmd' && normalizedArgs.length >= 2 && normalizedArgs[0] === '/c') {
    command = normalizedArgs[1]
    normalizedArgs = normalizedArgs.slice(2)
  }

  // 2. 剥离 npx 包名的 @latest 后缀（语义等价）
  if (command === 'npx' && normalizedArgs.length > 0) {
    normalizedArgs = normalizedArgs.map(trimLatestSuffix)
  }

  return { command, args: normalizedArgs }
}

function trimLatestSuffix(s: string): string {
  if (s.length > 7 && s.slice(-7) === '@latest') {
    return s.slice(0, -7)
  }
  return s
}

/**
 * 在已安装 MCP 列表中查找与市场条目匹配的服务器。
 * 匹配优先级：
 * 1. 精确 source + sourceId（managed 安装路径）
 * 2. 归一化 command + args 匹配（stdio，处理 cmd /c 包装与 @latest 差异）
 * 3. URL 匹配（http/sse）
 * 4. market 条目无 command/args 时（如 smithery 搜索结果）：
 *    检查 sourceId 是否出现在已安装 server 的 args 中（例：smithery sourceId="@upstash/context7-mcp"
 *    匹配手动安装的 args=["-y","@upstash/context7-mcp"]）
 */
export function matchInstalledServer(market: MarketServer, installed: McpServer[]): McpServer | undefined {
  // 1. source + sourceId 精确匹配
  if (market.source && market.sourceId) {
    const exact = installed.find(s => s.source === market.source && s.sourceId === market.sourceId)
    if (exact) return exact
  }

  // 2. command + args 归一化匹配（stdio）
  if (market.command) {
    const marketNorm = normalizeCommand(market.command, market.args ?? [])
    const cmdMatch = installed.find(s => {
      if (!s.command) return false
      const instNorm = normalizeCommand(s.command, s.args ?? [])
      return instNorm.command === marketNorm.command
        && JSON.stringify(instNorm.args) === JSON.stringify(marketNorm.args)
    })
    if (cmdMatch) return cmdMatch
  }

  // 3. URL 匹配（http/sse）
  if (market.url) {
    const urlMatch = installed.find(s => s.url && s.url === market.url)
    if (urlMatch) return urlMatch
  }

  // 4. sourceId 出现在 args 中（手动安装的兜底匹配）
  // 例：smithery sourceId="context7" 或 official sourceId="@upstash/context7-mcp"
  // 对应手动安装 args 中包含该标识符的情况
  if (market.sourceId && !market.command) {
    const argMatch = installed.find(s => {
      if (!s.args || s.args.length === 0) return false
      return s.args.some(a => a === market.sourceId || a === `@${market.sourceId}` || a.endsWith(`/${market.sourceId}`))
    })
    if (argMatch) return argMatch
  }

  return undefined
}
