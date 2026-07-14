import type { agents as AgentsNS, config as ConfigNS } from '../../wailsjs/go/models'
import type {
  MarketSource as ApiMarketSource,
  McpServer as ApiMcpServer,
  MarketServer as ApiMarketServer,
  SearchResultServers as ApiSearchResultServers,
  Skill as ApiSkill,
  Settings as ApiSettings,
} from '@/lib/api'

export type WailsAgent = AgentsNS.Agent
export type WailsSettings = ConfigNS.Settings
export type AgentVariant = 'cli' | 'desktop' | 'ide' | 'config'
export type AgentStatus =
  | 'detected'
  | 'enabled'
  | 'disabled'
  | 'not_found'
  | 'error'

export interface Agent {
  id: string
  name: string
  type: string
  variant: AgentVariant
  status: AgentStatus
  configPath: string
  configFormat: 'json' | 'toml'
  mcpCount: number
  detectedAt: string
  lastScannedAt: string
  error?: string
}

export type McpTransport = 'stdio' | 'sse' | 'http'

// Re-export from api.ts to eliminate type duplication
export type MarketSource = ApiMarketSource
export type McpServer = ApiMcpServer
export type MarketServer = ApiMarketServer
export type SearchResultServers = ApiSearchResultServers
export type Skill = ApiSkill
export type AppSettings = ApiSettings

export type WindowAction = 'minimize' | 'exit'
export type UpdateCheckResult = import('@/lib/api').UpdateCheckResult