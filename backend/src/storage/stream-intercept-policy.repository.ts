import { notifyGatewayRuntimeCacheInvalidation } from '../shared/gateway-cache-invalidation.js'
import { getDatabase, newId, nowIso } from './database.js'

export type StreamInterceptPolicyExecutionMode = 'intercept' | 'dry_run'
export type StreamInterceptPolicyDataHandling = 'discard_event' | 'discard_stream' | 'replace_with_failure'
export type StreamInterceptPolicyAccountSwitch = 'none' | 'request_next_account' | 'avoid_account_ttl' | 'avoid_upstream_bucket_ttl'
export type StreamInterceptPolicyAccountState = 'none' | 'runtime_avoidance'

export interface StreamInterceptPolicyMatch {
  eventTypes?: string[]
  dataTypes?: string[]
  errorCodes?: string[]
  errorTypes?: string[]
  textIncludes?: string[]
  textExcludes?: string[]
  jsonPathsExists?: string[]
}

export interface StreamInterceptPolicySummary {
  id: string
  defaultRule: boolean
  editable: boolean
  name: string
  enabled: boolean
  executionMode: StreamInterceptPolicyExecutionMode
  priority: number
  providerCode: string
  match: StreamInterceptPolicyMatch
  dataHandling: StreamInterceptPolicyDataHandling
  retryEnabled: boolean
  accountSwitch: StreamInterceptPolicyAccountSwitch
  accountState: StreamInterceptPolicyAccountState
  avoidanceTtlSeconds?: number
  notes?: string
  createdAt?: string
  updatedAt?: string
}

export interface StreamInterceptPolicyInput {
  name?: string
  enabled?: boolean
  executionMode?: StreamInterceptPolicyExecutionMode
  priority?: number
  providerCode?: string
  match?: StreamInterceptPolicyMatch
  dataHandling?: StreamInterceptPolicyDataHandling
  retryEnabled?: boolean
  accountSwitch?: StreamInterceptPolicyAccountSwitch
  accountState?: StreamInterceptPolicyAccountState
  avoidanceTtlSeconds?: number | null
  notes?: string | null
}

interface StreamInterceptPolicyRow {
  id: string
  name: string
  enabled: number
  execution_mode: string
  priority: number
  provider_code: string
  match_json: string
  data_handling: string
  retry_enabled: number
  account_switch: string
  account_state: string
  avoidance_ttl_seconds: number | null
  notes: string | null
  created_at: string
  updated_at: string
}

const executionModes = new Set<StreamInterceptPolicyExecutionMode>(['intercept', 'dry_run'])
const dataHandlings = new Set<StreamInterceptPolicyDataHandling>(['discard_event', 'discard_stream', 'replace_with_failure'])
const accountSwitches = new Set<StreamInterceptPolicyAccountSwitch>(['none', 'request_next_account', 'avoid_account_ttl', 'avoid_upstream_bucket_ttl'])
const accountStates = new Set<StreamInterceptPolicyAccountState>(['none', 'runtime_avoidance'])

const systemDefaultRules: StreamInterceptPolicySummary[] = [
  {
    id: 'default_openai_response_failed',
    defaultRule: true,
    editable: false,
    name: 'OpenAI response.failed',
    enabled: true,
    executionMode: 'intercept',
    priority: 10,
    providerCode: 'openai',
    match: {
      eventTypes: ['response.failed']
    },
    dataHandling: 'replace_with_failure',
    retryEnabled: true,
    accountSwitch: 'request_next_account',
    accountState: 'none',
    notes: 'OpenAI SSE response.failed 默认规则；是否写客户端可重试错误码由运行时客户端能力决定。'
  },
  {
    id: 'default_openai_event_error',
    defaultRule: true,
    editable: false,
    name: 'OpenAI event:error',
    enabled: true,
    executionMode: 'intercept',
    priority: 11,
    providerCode: 'openai',
    match: {
      eventTypes: ['error']
    },
    dataHandling: 'replace_with_failure',
    retryEnabled: true,
    accountSwitch: 'request_next_account',
    accountState: 'none',
    notes: 'OpenAI SSE event:error 默认规则；是否写客户端可重试错误码由运行时客户端能力决定。'
  },
  {
    id: 'default_openai_data_error',
    defaultRule: true,
    editable: false,
    name: 'OpenAI data.error',
    enabled: true,
    executionMode: 'intercept',
    priority: 12,
    providerCode: 'openai',
    match: {
      jsonPathsExists: ['error']
    },
    dataHandling: 'replace_with_failure',
    retryEnabled: true,
    accountSwitch: 'request_next_account',
    accountState: 'none',
    notes: 'OpenAI SSE data.error 默认规则；是否写客户端可重试错误码由运行时客户端能力决定。'
  },
  {
    id: 'default_openai_response_error',
    defaultRule: true,
    editable: false,
    name: 'OpenAI response.error',
    enabled: true,
    executionMode: 'intercept',
    priority: 13,
    providerCode: 'openai',
    match: {
      jsonPathsExists: ['response.error']
    },
    dataHandling: 'replace_with_failure',
    retryEnabled: true,
    accountSwitch: 'request_next_account',
    accountState: 'none',
    notes: 'OpenAI SSE response.error 默认规则；是否写客户端可重试错误码由运行时客户端能力决定。'
  },
  {
    id: 'default_openai_cyber_policy',
    defaultRule: true,
    editable: false,
    name: 'OpenAI cyber_policy',
    enabled: true,
    executionMode: 'intercept',
    priority: 14,
    providerCode: 'openai',
    match: {
      errorCodes: ['cyber_policy']
    },
    dataHandling: 'replace_with_failure',
    retryEnabled: true,
    accountSwitch: 'request_next_account',
    accountState: 'none',
    notes: '生产确认过的 OpenAI 流内策略错误；客户端支持专用重试信号时可替换为可重试失败事件。'
  }
]

export interface StreamInterceptPolicyListResult {
  defaultRules: StreamInterceptPolicySummary[]
  policies: StreamInterceptPolicySummary[]
}

export function listStreamInterceptPolicyDefaultRules(): StreamInterceptPolicySummary[] {
  return systemDefaultRules.map(clonePolicy)
}

export function listStreamInterceptPolicies(): StreamInterceptPolicyListResult {
  return {
    defaultRules: listStreamInterceptPolicyDefaultRules(),
    policies: listStreamInterceptPolicyRows().map(policyFromRow)
  }
}

export function listActiveStreamInterceptPoliciesForGateway(): StreamInterceptPolicySummary[] {
  const rows = getDatabase()
    .prepare(`
      SELECT *
      FROM stream_intercept_policies
      WHERE enabled = 1
      ORDER BY priority ASC, updated_at DESC, id ASC
    `)
    .all() as unknown as StreamInterceptPolicyRow[]
  return [
    ...listStreamInterceptPolicyDefaultRules().filter((policy) => policy.enabled),
    ...rows.map(policyFromRow)
  ]
}

export function createStreamInterceptPolicy(input: StreamInterceptPolicyInput): StreamInterceptPolicySummary {
  const now = nowIso()
  const policy = normalizePolicyInput(input, {
    id: newId('sip'),
    createdAt: now,
    updatedAt: now
  })
  getDatabase()
    .prepare(`
      INSERT INTO stream_intercept_policies (
        id, name, enabled, execution_mode, priority, provider_code, match_json,
        data_handling, retry_enabled, account_switch, account_state, avoidance_ttl_seconds, notes, created_at, updated_at
      ) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
    `)
    .run(
      policy.id,
      policy.name,
      policy.enabled ? 1 : 0,
      policy.executionMode,
      policy.priority,
      policy.providerCode,
      JSON.stringify(policy.match),
      policy.dataHandling,
      policy.retryEnabled ? 1 : 0,
      policy.accountSwitch,
      policy.accountState,
      policy.avoidanceTtlSeconds ?? null,
      policy.notes ?? null,
      policy.createdAt ?? now,
      policy.updatedAt ?? now
    )
  notifyGatewayRuntimeCacheInvalidation('stream_intercept_policy_created')
  return policy
}

export function updateStreamInterceptPolicy(id: string, input: StreamInterceptPolicyInput): StreamInterceptPolicySummary | undefined {
  const current = findStreamInterceptPolicyRow(id)
  if (!current) return undefined
  const now = nowIso()
  const policy = normalizePolicyInput(input, {
    id: current.id,
    createdAt: current.created_at,
    updatedAt: now,
    fallback: policyFromRow(current)
  })
  const result = getDatabase()
    .prepare(`
      UPDATE stream_intercept_policies
      SET name = ?,
          enabled = ?,
          execution_mode = ?,
          priority = ?,
          provider_code = ?,
          match_json = ?,
          data_handling = ?,
          retry_enabled = ?,
          account_switch = ?,
          account_state = ?,
          avoidance_ttl_seconds = ?,
          notes = ?,
          updated_at = ?
      WHERE id = ?
    `)
    .run(
      policy.name,
      policy.enabled ? 1 : 0,
      policy.executionMode,
      policy.priority,
      policy.providerCode,
      JSON.stringify(policy.match),
      policy.dataHandling,
      policy.retryEnabled ? 1 : 0,
      policy.accountSwitch,
      policy.accountState,
      policy.avoidanceTtlSeconds ?? null,
      policy.notes ?? null,
      policy.updatedAt ?? now,
      id
    )
  if (Number(result.changes ?? 0) > 0) {
    notifyGatewayRuntimeCacheInvalidation('stream_intercept_policy_updated')
  }
  return policy
}

export function deleteStreamInterceptPolicy(id: string): boolean {
  const result = getDatabase().prepare('DELETE FROM stream_intercept_policies WHERE id = ?').run(id)
  const deleted = Number(result.changes ?? 0) > 0
  if (deleted) {
    notifyGatewayRuntimeCacheInvalidation('stream_intercept_policy_deleted')
  }
  return deleted
}

function listStreamInterceptPolicyRows(): StreamInterceptPolicyRow[] {
  return getDatabase()
    .prepare(`
      SELECT *
      FROM stream_intercept_policies
      ORDER BY priority ASC, updated_at DESC, id ASC
    `)
    .all() as unknown as StreamInterceptPolicyRow[]
}

function findStreamInterceptPolicyRow(id: string): StreamInterceptPolicyRow | undefined {
  return getDatabase()
    .prepare('SELECT * FROM stream_intercept_policies WHERE id = ?')
    .get(id) as unknown as StreamInterceptPolicyRow | undefined
}

function normalizePolicyInput(
  input: StreamInterceptPolicyInput,
  metadata: {
    id: string
    createdAt: string
    updatedAt: string
    fallback?: StreamInterceptPolicySummary
  }
): StreamInterceptPolicySummary {
  const fallback = metadata.fallback
  const dataHandling = normalizeSetValue(input.dataHandling, dataHandlings, fallback?.dataHandling, 'discard_stream')
  const retryEnabled = typeof input.retryEnabled === 'boolean' ? input.retryEnabled : fallback?.retryEnabled ?? false
  return {
    id: metadata.id,
    defaultRule: false,
    editable: true,
    name: stringValue(input.name) || fallback?.name || '未命名流式拦截策略',
    enabled: typeof input.enabled === 'boolean' ? input.enabled : fallback?.enabled ?? true,
    executionMode: normalizeSetValue(input.executionMode, executionModes, fallback?.executionMode, 'intercept'),
    priority: normalizePriority(input.priority, fallback?.priority),
    providerCode: normalizeProviderCode(input.providerCode, fallback?.providerCode),
    match: normalizeMatch(input.match ?? fallback?.match),
    dataHandling,
    retryEnabled,
    accountSwitch: normalizeSetValue(input.accountSwitch, accountSwitches, fallback?.accountSwitch, 'none'),
    accountState: normalizeSetValue(input.accountState, accountStates, fallback?.accountState, 'none'),
    avoidanceTtlSeconds: normalizeTtl(input.avoidanceTtlSeconds, fallback?.avoidanceTtlSeconds),
    notes: optionalString(input.notes) ?? fallback?.notes,
    createdAt: metadata.createdAt,
    updatedAt: metadata.updatedAt
  }
}

function policyFromRow(row: StreamInterceptPolicyRow): StreamInterceptPolicySummary {
  return {
    id: row.id,
    defaultRule: false,
    editable: true,
    name: row.name,
    enabled: row.enabled === 1,
    executionMode: normalizeSetValue(row.execution_mode, executionModes, undefined, 'intercept'),
    priority: normalizePriority(row.priority, 100),
    providerCode: normalizeProviderCode(row.provider_code, 'openai'),
    match: normalizeMatch(parseJsonObject(row.match_json)),
    dataHandling: normalizeSetValue(row.data_handling, dataHandlings, undefined, 'discard_stream'),
    retryEnabled: row.retry_enabled === 1,
    accountSwitch: normalizeSetValue(row.account_switch, accountSwitches, undefined, 'none'),
    accountState: normalizeSetValue(row.account_state, accountStates, undefined, 'none'),
    avoidanceTtlSeconds: normalizeTtl(row.avoidance_ttl_seconds, undefined),
    notes: row.notes ?? undefined,
    createdAt: row.created_at,
    updatedAt: row.updated_at
  }
}

function normalizeMatch(value: unknown): StreamInterceptPolicyMatch {
  const record = objectValue(value)
  if (!record) return {}
  return {
    eventTypes: normalizeTextList(record.eventTypes, 50, 120),
    dataTypes: normalizeTextList(record.dataTypes, 50, 120),
    errorCodes: normalizeTextList(record.errorCodes, 50, 120),
    errorTypes: normalizeTextList(record.errorTypes, 50, 120),
    textIncludes: normalizeTextList(record.textIncludes, 50, 200),
    textExcludes: normalizeTextList(record.textExcludes, 50, 200),
    jsonPathsExists: normalizeTextList(record.jsonPathsExists, 50, 120)
  }
}

function normalizeProviderCode(value: unknown, fallback = 'openai'): string {
  return typeof value === 'string' && value.trim() ? value.trim().slice(0, 80) : fallback
}

function normalizePriority(value: unknown, fallback = 100): number {
  const numberValue = Number(value)
  return Number.isFinite(numberValue) ? Math.max(1, Math.min(9999, Math.trunc(numberValue))) : fallback
}

function normalizeTtl(value: unknown, fallback: number | undefined): number | undefined {
  if (value === null) return undefined
  const numberValue = Number(value)
  if (!Number.isFinite(numberValue)) return fallback
  return Math.max(1, Math.min(86_400, Math.trunc(numberValue)))
}

function normalizeTextList(value: unknown, maxItems: number, maxLength: number): string[] | undefined {
  const source = Array.isArray(value)
    ? value
    : typeof value === 'string'
      ? value.split(/[,;，；\n]/)
      : []
  const seen = new Set<string>()
  const output: string[] = []
  for (const item of source) {
    const text = String(item).trim().slice(0, maxLength)
    if (!text || seen.has(text)) continue
    seen.add(text)
    output.push(text)
    if (output.length >= maxItems) break
  }
  return output.length ? output : undefined
}

function normalizeSetValue<T extends string>(value: unknown, values: Set<T>, fallback: T | undefined, defaultValue: T): T {
  return values.has(value as T) ? value as T : fallback ?? defaultValue
}

function parseJsonObject(value: string): Record<string, unknown> | undefined {
  try {
    return objectValue(JSON.parse(value) as unknown)
  } catch {
    return undefined
  }
}

function objectValue(value: unknown): Record<string, unknown> | undefined {
  return typeof value === 'object' && value !== null && !Array.isArray(value)
    ? value as Record<string, unknown>
    : undefined
}

function stringValue(value: unknown): string | undefined {
  return typeof value === 'string' && value.trim() ? value.trim().slice(0, 100) : undefined
}

function optionalString(value: unknown): string | undefined {
  if (value === null) return undefined
  return typeof value === 'string' && value.trim() ? value.trim().slice(0, 1000) : undefined
}

function clonePolicy(policy: StreamInterceptPolicySummary): StreamInterceptPolicySummary {
  return {
    ...policy,
    match: { ...policy.match }
  }
}
