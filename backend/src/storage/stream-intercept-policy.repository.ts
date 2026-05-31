import { notifyGatewayRuntimeCacheInvalidation } from '../shared/gateway-cache-invalidation.js'
import { getBusinessDatabase, newId, nowIso } from './database.js'

export type StreamInterceptPolicyExecutionMode = 'intercept' | 'dry_run'
export type StreamInterceptPolicyDataHandling = 'discard_event' | 'discard_stream' | 'replace_with_failure'
export type StreamInterceptPolicyAccountSwitch = 'none' | 'request_next_account' | 'avoid_account_ttl' | 'avoid_upstream_bucket_ttl'
export type StreamInterceptPolicyAccountState = 'none' | 'runtime_avoidance'
export type StreamInterceptPolicyAction =
  | 'observe'
  | 'drop_event'
  | 'fail_stream'
  | 'retry_no_avoidance'
  | 'retry_next_account'
  | 'avoid_account_ttl'
  | 'avoid_upstream_bucket_ttl'

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
  priority: number
  providerCode: string
  match: StreamInterceptPolicyMatch
  action: StreamInterceptPolicyAction
  avoidanceTtlSeconds?: number
  notes?: string
  createdAt?: string
  updatedAt?: string
}

export interface StreamInterceptPolicyInput {
  name?: string
  enabled?: boolean
  priority?: number
  providerCode?: string
  match?: StreamInterceptPolicyMatch
  action?: StreamInterceptPolicyAction
  avoidanceTtlSeconds?: number | null
  notes?: string | null
}

interface StreamInterceptPolicyRow {
  id: string
  name: string
  enabled: number
  priority: number
  provider_code: string
  match_json: string
  action: string
  avoidance_ttl_seconds: number | null
  notes: string | null
  created_at: string
  updated_at: string
}

const policyActions = new Set<StreamInterceptPolicyAction>([
  'observe',
  'drop_event',
  'fail_stream',
  'retry_no_avoidance',
  'retry_next_account',
  'avoid_account_ttl',
  'avoid_upstream_bucket_ttl'
])

const systemDefaultRules: StreamInterceptPolicySummary[] = [
  {
    id: 'default_openai_response_failed',
    defaultRule: true,
    editable: false,
    name: 'OpenAI response.failed',
    enabled: true,
    priority: 10,
    providerCode: 'openai',
    match: {
      eventTypes: ['response.failed']
    },
    action: 'retry_no_avoidance',
    notes: 'OpenAI SSE response.failed 默认规则；是否写客户端可重试错误码由运行时客户端能力决定。'
  },
  {
    id: 'default_openai_event_error',
    defaultRule: true,
    editable: false,
    name: 'OpenAI event:error',
    enabled: true,
    priority: 11,
    providerCode: 'openai',
    match: {
      eventTypes: ['error']
    },
    action: 'retry_no_avoidance',
    notes: 'OpenAI SSE event:error 默认规则；是否写客户端可重试错误码由运行时客户端能力决定。'
  },
  {
    id: 'default_openai_data_error',
    defaultRule: true,
    editable: false,
    name: 'OpenAI data.error',
    enabled: true,
    priority: 12,
    providerCode: 'openai',
    match: {
      jsonPathsExists: ['error']
    },
    action: 'retry_no_avoidance',
    notes: 'OpenAI SSE data.error 默认规则；是否写客户端可重试错误码由运行时客户端能力决定。'
  },
  {
    id: 'default_openai_response_error',
    defaultRule: true,
    editable: false,
    name: 'OpenAI response.error',
    enabled: true,
    priority: 13,
    providerCode: 'openai',
    match: {
      jsonPathsExists: ['response.error']
    },
    action: 'retry_no_avoidance',
    notes: 'OpenAI SSE response.error 默认规则；是否写客户端可重试错误码由运行时客户端能力决定。'
  },
  {
    id: 'default_openai_cyber_policy',
    defaultRule: true,
    editable: false,
    name: 'OpenAI cyber_policy',
    enabled: true,
    priority: 14,
    providerCode: 'openai',
    match: {
      errorCodes: ['cyber_policy']
    },
    action: 'retry_no_avoidance',
    notes: '安全拦截过滤替换为可重试失败事件'
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
  const rows = getBusinessDatabase()
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
  getBusinessDatabase()
    .prepare(`
      INSERT INTO stream_intercept_policies (
        id, name, enabled, priority, provider_code, match_json,
        action, avoidance_ttl_seconds, notes, created_at, updated_at
      ) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
    `)
    .run(
      policy.id,
      policy.name,
      policy.enabled ? 1 : 0,
      policy.priority,
      policy.providerCode,
      JSON.stringify(policy.match),
      policy.action,
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
  const result = getBusinessDatabase()
    .prepare(`
      UPDATE stream_intercept_policies
      SET name = ?,
          enabled = ?,
          priority = ?,
          provider_code = ?,
          match_json = ?,
          action = ?,
          avoidance_ttl_seconds = ?,
          notes = ?,
          updated_at = ?
      WHERE id = ?
    `)
    .run(
      policy.name,
      policy.enabled ? 1 : 0,
      policy.priority,
      policy.providerCode,
      JSON.stringify(policy.match),
      policy.action,
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
  const result = getBusinessDatabase().prepare('DELETE FROM stream_intercept_policies WHERE id = ?').run(id)
  const deleted = Number(result.changes ?? 0) > 0
  if (deleted) {
    notifyGatewayRuntimeCacheInvalidation('stream_intercept_policy_deleted')
  }
  return deleted
}

function listStreamInterceptPolicyRows(): StreamInterceptPolicyRow[] {
  return getBusinessDatabase()
    .prepare(`
      SELECT *
      FROM stream_intercept_policies
      ORDER BY priority ASC, updated_at DESC, id ASC
    `)
    .all() as unknown as StreamInterceptPolicyRow[]
}

function findStreamInterceptPolicyRow(id: string): StreamInterceptPolicyRow | undefined {
  return getBusinessDatabase()
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
  const action = normalizeSetValue(input.action, policyActions, fallback?.action, 'avoid_account_ttl')
  return {
    id: metadata.id,
    defaultRule: false,
    editable: true,
    name: stringValue(input.name) || fallback?.name || '未命名流式拦截策略',
    enabled: typeof input.enabled === 'boolean' ? input.enabled : fallback?.enabled ?? true,
    priority: normalizePriority(input.priority, fallback?.priority),
    providerCode: normalizeProviderCode(input.providerCode, fallback?.providerCode),
    match: normalizeMatch(input.match ?? fallback?.match),
    action,
    avoidanceTtlSeconds: normalizePolicyTtl(input.avoidanceTtlSeconds, action, fallback?.avoidanceTtlSeconds),
    notes: optionalString(input.notes) ?? fallback?.notes,
    createdAt: metadata.createdAt,
    updatedAt: metadata.updatedAt
  }
}

function policyFromRow(row: StreamInterceptPolicyRow): StreamInterceptPolicySummary {
  const action = normalizeSetValue(row.action, policyActions, undefined, 'avoid_account_ttl')
  return {
    id: row.id,
    defaultRule: false,
    editable: true,
    name: row.name,
    enabled: row.enabled === 1,
    priority: normalizePriority(row.priority, 100),
    providerCode: normalizeProviderCode(row.provider_code, 'openai'),
    match: normalizeMatch(parseJsonObject(row.match_json)),
    action,
    avoidanceTtlSeconds: normalizePolicyTtl(row.avoidance_ttl_seconds, action, undefined),
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

function normalizePolicyTtl(value: unknown, action: StreamInterceptPolicyAction, fallback: number | undefined): number | undefined {
  if (!actionUsesTtl(action)) return undefined
  return normalizeTtl(value, fallback ?? 300)
}

export function actionUsesTtl(action: StreamInterceptPolicyAction): boolean {
  return action === 'avoid_account_ttl' || action === 'avoid_upstream_bucket_ttl'
}

export function streamInterceptPolicyActionRuntime(action: StreamInterceptPolicyAction): {
  executionMode: StreamInterceptPolicyExecutionMode
  dataHandling: StreamInterceptPolicyDataHandling
  retryEnabled: boolean
  accountSwitch: StreamInterceptPolicyAccountSwitch
  accountState: StreamInterceptPolicyAccountState
} {
  switch (action) {
    case 'observe':
      return {
        executionMode: 'dry_run',
        dataHandling: 'discard_stream',
        retryEnabled: false,
        accountSwitch: 'none',
        accountState: 'none'
      }
    case 'drop_event':
      return {
        executionMode: 'intercept',
        dataHandling: 'discard_event',
        retryEnabled: false,
        accountSwitch: 'none',
        accountState: 'none'
      }
    case 'fail_stream':
      return {
        executionMode: 'intercept',
        dataHandling: 'replace_with_failure',
        retryEnabled: false,
        accountSwitch: 'none',
        accountState: 'none'
      }
    case 'retry_no_avoidance':
      return {
        executionMode: 'intercept',
        dataHandling: 'replace_with_failure',
        retryEnabled: true,
        accountSwitch: 'none',
        accountState: 'none'
      }
    case 'retry_next_account':
      return {
        executionMode: 'intercept',
        dataHandling: 'replace_with_failure',
        retryEnabled: true,
        accountSwitch: 'request_next_account',
        accountState: 'none'
      }
    case 'avoid_account_ttl':
      return {
        executionMode: 'intercept',
        dataHandling: 'replace_with_failure',
        retryEnabled: true,
        accountSwitch: 'avoid_account_ttl',
        accountState: 'runtime_avoidance'
      }
    case 'avoid_upstream_bucket_ttl':
      return {
        executionMode: 'intercept',
        dataHandling: 'replace_with_failure',
        retryEnabled: true,
        accountSwitch: 'avoid_upstream_bucket_ttl',
        accountState: 'none'
      }
  }
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
