import type {
  AccountSummary,
  ApiKeySummary,
  GroupSummary,
  ResourceAuthorizationSummary,
  SystemAccountSummary,
  SystemTeamSummary
} from '../../../domain/types.js'
import { GPT_VENDOR_CODE } from '../../../domain/provider-protocol.js'
import type { CreatedExternalIntegrationSourceAuthorization } from '../../../storage/external-integration-source-types.js'
import type { UsageRecordInput } from '../../../storage/usage-records.repository.js'

export type SqlValue = string | number | null

export interface MockdataOptions {
  days: number
  dailyRequests: number
  help: boolean
}

export interface MockSystemAccounts {
  admin: SystemAccountSummary
  manager: SystemAccountSummary
  ops: SystemAccountSummary
  dev: SystemAccountSummary
  tester: SystemAccountSummary
  finance: SystemAccountSummary
  viewer: SystemAccountSummary
  disabled: SystemAccountSummary
}

export interface MockGroups {
  main: GroupSummary
  highConcurrency: GroupSummary
  backup: GroupSummary
  oauth: GroupSummary
  experiment: GroupSummary
  empty: GroupSummary
  managerMain: GroupSummary
  managerHighConcurrency: GroupSummary
  adminGrantedDev: GroupSummary
  adminGrantedOps: GroupSummary
  adminGrantedTester: GroupSummary
  managerDefault: GroupSummary
  devDefault: GroupSummary
  opsDefault: GroupSummary
  testerDefault: GroupSummary
  financeDefault: GroupSummary
  viewerDefault: GroupSummary
}

export interface MockAccounts {
  primary: AccountSummary
  proxied: AccountSummary
  normal: AccountSummary
  standardClient: AccountSummary
  multiKeyPool: AccountSummary
  image: AccountSummary
  burstFast: AccountSummary
  burstImage: AccountSummary
  burstFallback: AccountSummary
  fallback: AccountSummary
  oauth: AccountSummary
  oauthBackup: AccountSummary
  pendingTest: AccountSummary
  disabled: AccountSummary
  unschedulable: AccountSummary
  scheduledInactive: AccountSummary
  rateLimited: AccountSummary
  temporary: AccountSummary
  error: AccountSummary
  expired: AccountSummary
  managerPrimary: AccountSummary
  managerBurst: AccountSummary
  devShared: AccountSummary
  opsShared: AccountSummary
  testerShared: AccountSummary
}

export type ApiKeyWithSecret = ApiKeySummary & { key: string }

export interface MockApiKeys {
  adminMain: ApiKeyWithSecret
  adminHighConcurrency: ApiKeyWithSecret
  adminHighFrequency: ApiKeyWithSecret
  adminRoundRobin: ApiKeyWithSecret
  adminWeighted: ApiKeyWithSecret
  adminScheduled: ApiKeyWithSecret
  adminBackup: ApiKeyWithSecret
  adminOAuth: ApiKeyWithSecret
  adminAuthorizedGroups: ApiKeyWithSecret
  adminDisabled: ApiKeyWithSecret
  adminExpired: ApiKeyWithSecret
  managerMain: ApiKeyWithSecret
  managerHighConcurrency: ApiKeyWithSecret
  devGroupAuthorized: ApiKeyWithSecret
  testerTeamAuthorized: ApiKeyWithSecret
  opsAccountAuthorized: ApiKeyWithSecret
  financeAuthorized: ApiKeyWithSecret
  viewerAuthorized: ApiKeyWithSecret
}

export interface MockTeams {
  devTeam: SystemTeamSummary
  opsTeam: SystemTeamSummary
  disabledTeam: SystemTeamSummary
}

export interface MockExternalSources {
  primary: CreatedExternalIntegrationSourceAuthorization
  readonly: CreatedExternalIntegrationSourceAuthorization
}

export interface CreatedMockdata {
  users: MockSystemAccounts
  groups: MockGroups
  accounts: MockAccounts
  apiKeys: MockApiKeys
  teams: MockTeams
  authorizations: ResourceAuthorizationSummary[]
  externalSources: MockExternalSources
  responseInspectionPolicies: number
  customProviderModels: number
}

export interface UsageRecordSeed extends UsageRecordInput {
  id: string
  createdAt: string
}

export interface DerivedCacheCounts {
  usageRecords: number
  accountQualityAccounts: number
  clientIpRecords: number
}

export interface RecordCleanupMockdataCounts {
  accountTargets: number
  apiKeyTargets: number
}

export interface ClientIpPolicyMockdataCounts {
  policies: number
  policyHits: number
}

export interface ExtraMockdataCounts {
  publicApiLogs: number
  accountCleanupTargets: number
  apiKeyCleanupTargets: number
  clientIpAggregatedRecords: number
  clientIpPolicies: number
  clientIpPolicyHits: number
}

export interface AccountMetricRow {
  sample_count: number
  cpu_percent_sum: number
  cpu_percent_max: number | null
  memory_used_percent_sum: number
  memory_used_percent_max: number | null
  process_rss_bytes_sum: number
  process_rss_bytes_max: number | null
  process_heap_used_bytes_sum: number
  process_heap_used_bytes_max: number | null
  event_loop_lag_ms_sum: number
  event_loop_lag_ms_max: number | null
  network_rx_bytes_per_sec_sum: number
  network_rx_bytes_per_sec_max: number | null
  network_rx_bytes_per_sec_count: number
  network_tx_bytes_per_sec_sum: number
  network_tx_bytes_per_sec_max: number | null
  network_tx_bytes_per_sec_count: number
  network_rx_total_bytes_max: number | null
  network_tx_total_bytes_max: number | null
  db_file_bytes_max: number | null
  stats_lag_seconds_max: number | null
}

export interface ProcessMetricRow {
  sample_count: number
  event_loop_lag_ms_sum: number
  event_loop_lag_ms_max: number | null
}

export const idPrefix = 'mockdata_'
export const tracePrefix = 'mockdata-'
export const namePrefix = '造数-'
export const mockPassword = 'mockdata123456'
export const apiKeyAuthorizedGroupBindingRule = 'API Key 可以绑定当前系统账户自己的分组，也可以绑定有效授权给当前系统账户的分组；授权暂停、过期、回收或归还后，该授权分组号池保留配置但运行时不可用。'
export const dayMs = 24 * 60 * 60 * 1000
export const minuteMs = 60 * 1000
export const defaultDays = 31
export const defaultDailyRequests = 120
export const adminUsername = 'admin'
export const providerCode = GPT_VENDOR_CODE

export function publicApiLogStatus(index: number, method: string): number {
  if (index % 29 === 0) return 401
  if (index % 23 === 0) return 403
  if (index % 19 === 0) return 429
  if (index % 17 === 0) return 400
  if (index % 13 === 0) return 500
  return method === 'POST' && index % 4 === 0 ? 201 : 200
}

export function publicApiLogErrorCode(status: number): string {
  if (status === 401) return 'external_source_token_invalid'
  if (status === 403) return 'external_source_scope_denied'
  if (status === 429) return 'external_source_rate_limited'
  if (status === 400) return 'bad_request'
  return 'public_api_internal_error'
}

export function publicApiLogErrorMessage(status: number): string {
  if (status === 401) return 'Mockdata 模拟来源 Token 无效'
  if (status === 403) return 'Mockdata 模拟来源系统缺少接口权限'
  if (status === 429) return 'Mockdata 模拟公开接口触发限流'
  if (status === 400) return 'Mockdata 模拟公开接口参数无效'
  return 'Mockdata 模拟公开接口内部错误'
}

export function tableStorageValues(
  role: string,
  tableName: string,
  dayIndex: number,
  sampledAt: string,
  rowCount: number,
  totalBytes: number
): SqlValue[] {
  const tableBytes = Math.floor(totalBytes * 0.72)
  const indexBytes = totalBytes - tableBytes
  return [
    `${idPrefix}storage_table_${role}_${tableName}_${String(dayIndex + 1).padStart(2, '0')}`,
    role,
    tableName,
    sampledAt,
    rowCount,
    tableBytes,
    indexBytes,
    totalBytes,
    Math.ceil(totalBytes / 4096),
    3 + (dayIndex % 6),
    30_000 + dayIndex * 120,
    1 + (dayIndex % 8),
    500_000 + dayIndex * 10_000,
    10 + (dayIndex % 40),
    sampledAt
  ]
}

export function emptyMetricRow(): AccountMetricRow {
  return {
    sample_count: 0,
    cpu_percent_sum: 0,
    cpu_percent_max: null,
    memory_used_percent_sum: 0,
    memory_used_percent_max: null,
    process_rss_bytes_sum: 0,
    process_rss_bytes_max: null,
    process_heap_used_bytes_sum: 0,
    process_heap_used_bytes_max: null,
    event_loop_lag_ms_sum: 0,
    event_loop_lag_ms_max: null,
    network_rx_bytes_per_sec_sum: 0,
    network_rx_bytes_per_sec_max: null,
    network_rx_bytes_per_sec_count: 0,
    network_tx_bytes_per_sec_sum: 0,
    network_tx_bytes_per_sec_max: null,
    network_tx_bytes_per_sec_count: 0,
    network_rx_total_bytes_max: null,
    network_tx_total_bytes_max: null,
    db_file_bytes_max: null,
    stats_lag_seconds_max: null
  }
}

export function chunks<T>(items: T[], size: number): T[][] {
  const output: T[][] = []
  for (let index = 0; index < items.length; index += size) {
    output.push(items.slice(index, index + size))
  }
  return output
}

export function weightedIndex(seed: number, length: number): number {
  if (length <= 1) return 0
  return Math.floor(pseudoRandom(seed, 1) * length) % length
}

export function pseudoRandom(seed: number, salt: number): number {
  const value = Math.sin(seed * 12.9898 + salt * 78.233) * 43758.5453
  return value - Math.floor(value)
}

export function boundedInteger(value: string | undefined, fallback: number, min: number, max: number): number {
  const number = Number(value)
  if (!Number.isFinite(number)) return fallback
  return Math.min(Math.max(Math.trunc(number), min), max)
}

export function roundCost(value: number): number {
  return Math.round(value * 1_000_000) / 1_000_000
}

export function roundNumber(value: number, digits: number): number {
  const factor = 10 ** digits
  return Math.round(value * factor) / factor
}

export function numeric(value: unknown): number | undefined {
  const number = typeof value === 'number' ? value : typeof value === 'string' ? Number(value) : NaN
  return Number.isFinite(number) ? number : undefined
}
