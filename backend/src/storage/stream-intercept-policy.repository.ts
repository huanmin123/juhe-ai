import { notifyGatewayRuntimeCacheInvalidation } from '../shared/gateway-cache-invalidation.js'
import { getBusinessDatabase, newId, nowIso } from './database.js'

export type StreamInterceptPolicyExecutionMode = 'intercept' | 'dry_run'
export type StreamInterceptPolicyDataHandling = 'discard_event' | 'discard_stream' | 'replace_with_failure'
export type StreamInterceptPolicyAccountSwitch = 'none' | 'request_next_account' | 'avoid_account_ttl' | 'avoid_upstream_bucket_ttl'
export type StreamInterceptPolicyAccountState = 'none' | 'runtime_avoidance'
export type StreamInterceptPolicyAction =
  | 'observe'
  | 'drop_event'
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
  notes?: string
  createdAt?: string
  updatedAt?: string
}

export interface StreamInterceptPolicyInput {
  name?: string
  enabled?: boolean
  priority?: number
  providerCode: string
  match?: StreamInterceptPolicyMatch
  action?: StreamInterceptPolicyAction
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
  notes: string | null
  created_at: string
  updated_at: string
}

export const maxManagementStreamInterceptPolicies = 100

const policyActions = new Set<StreamInterceptPolicyAction>([
  'observe',
  'drop_event',
  'retry_no_avoidance',
  'retry_next_account',
  'avoid_account_ttl',
  'avoid_upstream_bucket_ttl'
])
const streamInterceptPolicyInputKeys = new Set([
  'name',
  'enabled',
  'priority',
  'providerCode',
  'match',
  'action',
  'notes'
])
const streamInterceptPolicyMatchKeys = [
  'eventTypes',
  'dataTypes',
  'errorCodes',
  'errorTypes',
  'textIncludes',
  'textExcludes',
  'jsonPathsExists'
] as const

const systemDefaultRules: StreamInterceptPolicySummary[] = [
  {
    id: 'default_openai_response_failed',
    defaultRule: true,
    editable: false,
    name: 'OpenAI response.failed',
    enabled: true,
    priority: 1,
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
    priority: 2,
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
    priority: 3,
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
    priority: 4,
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
    priority: 5,
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
        AND provider_code = ?
      ORDER BY priority ASC, updated_at DESC, id ASC
      LIMIT ?
    `)
    .all('openai', maxManagementStreamInterceptPolicies) as unknown as StreamInterceptPolicyRow[]
  return [
    ...listStreamInterceptPolicyDefaultRules().filter((policy) => policy.enabled),
    ...rows.map(policyFromRow)
  ]
}

export function createStreamInterceptPolicy(input: StreamInterceptPolicyInput): StreamInterceptPolicySummary {
  assertKnownInputKeys(input, streamInterceptPolicyInputKeys, '流式拦截策略')
  assertManagementPolicyCapacity()
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
        action, notes, created_at, updated_at
      ) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
    `)
    .run(
      policy.id,
      policy.name,
      policy.enabled ? 1 : 0,
      policy.priority,
      policy.providerCode,
      JSON.stringify(policy.match),
      policy.action,
      policy.notes ?? null,
      policy.createdAt ?? now,
      policy.updatedAt ?? now
    )
  notifyGatewayRuntimeCacheInvalidation('stream_intercept_policy_created')
  return policy
}

export function updateStreamInterceptPolicy(id: string, input: StreamInterceptPolicyInput): StreamInterceptPolicySummary | undefined {
  assertKnownInputKeys(input, streamInterceptPolicyInputKeys, '流式拦截策略')
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
      LIMIT ?
    `)
    .all(maxManagementStreamInterceptPolicies) as unknown as StreamInterceptPolicyRow[]
}

function findStreamInterceptPolicyRow(id: string): StreamInterceptPolicyRow | undefined {
  return getBusinessDatabase()
    .prepare('SELECT * FROM stream_intercept_policies WHERE id = ?')
    .get(id) as unknown as StreamInterceptPolicyRow | undefined
}

function assertManagementPolicyCapacity(): void {
  const rows = getBusinessDatabase()
    .prepare('SELECT id FROM stream_intercept_policies LIMIT ?')
    .all(maxManagementStreamInterceptPolicies) as Array<{ id: string }>
  if (rows.length >= maxManagementStreamInterceptPolicies) {
    throw new Error(`管理端流式拦截策略不能超过 ${maxManagementStreamInterceptPolicies} 条`)
  }
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
  const action = input.action === undefined && fallback
    ? fallback.action
    : normalizePolicyAction(input.action)
  return {
    id: metadata.id,
    defaultRule: false,
    editable: true,
    name: normalizePolicyName(input.name, fallback?.name),
    enabled: normalizeBooleanInput(input.enabled, fallback?.enabled ?? true, '启用状态'),
    priority: normalizePriority(input.priority, fallback?.priority),
    providerCode: normalizeProviderCode(input.providerCode),
    match: normalizeMatch(input.match === undefined ? fallback?.match : input.match),
    action,
    notes: normalizeOptionalTextInput(input.notes, fallback?.notes, 1000, '备注'),
    createdAt: metadata.createdAt,
    updatedAt: metadata.updatedAt
  }
}

function policyFromRow(row: StreamInterceptPolicyRow): StreamInterceptPolicySummary {
  const action = normalizePolicyAction(row.action)
  return {
    id: row.id,
    defaultRule: false,
    editable: true,
    name: row.name,
    enabled: row.enabled === 1,
    priority: normalizePriority(row.priority, 1),
    providerCode: normalizeProviderCode(row.provider_code),
    match: normalizeMatch(parseJsonObject(row.match_json)),
    action,
    notes: row.notes ?? undefined,
    createdAt: row.created_at,
    updatedAt: row.updated_at
  }
}

function normalizeMatch(value: unknown): StreamInterceptPolicyMatch {
  const record = objectValue(value)
  if (!record) {
    throw new Error('流式拦截策略匹配条件必须是对象')
  }
  assertOnlyKeys(record, streamInterceptPolicyMatchKeys, '流式拦截策略匹配条件')
  const match = {
    eventTypes: normalizeTextList(record.eventTypes, 50, 120),
    dataTypes: normalizeTextList(record.dataTypes, 50, 120),
    errorCodes: normalizeTextList(record.errorCodes, 50, 120),
    errorTypes: normalizeTextList(record.errorTypes, 50, 120),
    textIncludes: normalizeTextList(record.textIncludes, 50, 200),
    textExcludes: normalizeTextList(record.textExcludes, 50, 200),
    jsonPathsExists: normalizeTextList(record.jsonPathsExists, 50, 120)
  }
  const hasMatcher = [
    match.eventTypes,
    match.dataTypes,
    match.errorCodes,
    match.errorTypes,
    match.textIncludes,
    match.jsonPathsExists
  ].some((items) => Array.isArray(items) && items.length > 0)
  if (!hasMatcher) {
    throw new Error('至少需要填写一个匹配条件')
  }
  return match
}

function normalizeProviderCode(value: unknown): string {
  if (value === undefined) {
    throw new Error('流式拦截策略供应商编码不能为空')
  }
  if (typeof value !== 'string') {
    throw new Error('流式拦截策略供应商编码必须是字符串')
  }
  const text = value.trim()
  if (!text) {
    throw new Error('流式拦截策略供应商编码不能为空')
  }
  if (text.length > 80) {
    throw new Error('流式拦截策略供应商编码不能超过 80 个字符')
  }
  return text
}

function normalizePriority(value: unknown, fallback = 1): number {
  if (value === undefined) return fallback
  if (typeof value !== 'number' || !Number.isInteger(value)) {
    throw new Error('流式拦截策略优先级必须是整数')
  }
  if (value < 1 || value > 9999) {
    throw new Error('流式拦截策略优先级必须在 1 到 9999 之间')
  }
  return value
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
  if (value === undefined) {
    return undefined
  }
  if (!Array.isArray(value)) {
    throw new Error('流式拦截策略匹配条件必须是字符串数组')
  }
  if (value.length > maxItems) {
    throw new Error(`流式拦截策略匹配条件不能超过 ${maxItems} 条`)
  }
  const seen = new Set<string>()
  const output: string[] = []
  for (const item of value) {
    if (typeof item !== 'string') {
      throw new Error('流式拦截策略匹配条件必须是字符串数组')
    }
    const text = item.trim()
    if (!text) {
      throw new Error('流式拦截策略匹配条件不能为空')
    }
    if (text.length > maxLength) {
      throw new Error(`流式拦截策略匹配条件不能超过 ${maxLength} 个字符`)
    }
    if (seen.has(text)) {
      throw new Error('流式拦截策略匹配条件不能重复')
    }
    seen.add(text)
    output.push(text)
  }
  return output.length ? output : undefined
}

function normalizePolicyAction(value: unknown): StreamInterceptPolicyAction {
  if (policyActions.has(value as StreamInterceptPolicyAction)) {
    return value as StreamInterceptPolicyAction
  }
  throw new Error('流式拦截策略动作无效')
}

function missingPolicyField(label: string): never {
  throw new Error(`${label}不能为空`)
}

function normalizePolicyName(value: unknown, fallback?: string): string {
  if (value === undefined) {
    return fallback ?? missingPolicyField('规则名称')
  }
  if (typeof value !== 'string') {
    throw new Error('规则名称必须是字符串')
  }
  const text = value.trim()
  if (!text) {
    throw new Error('规则名称不能为空')
  }
  if (text.length > 100) {
    throw new Error('规则名称不能超过 100 个字符')
  }
  return text
}

function normalizeBooleanInput(value: unknown, fallback: boolean, label: string): boolean {
  if (value === undefined) return fallback
  if (typeof value !== 'boolean') {
    throw new Error(`${label}必须是布尔值`)
  }
  return value
}

function normalizeOptionalTextInput(value: unknown, fallback: string | undefined, maxLength: number, label: string): string | undefined {
  if (value === undefined) return fallback
  if (value === null) return undefined
  if (typeof value !== 'string') {
    throw new Error(`${label}必须是字符串`)
  }
  const text = value.trim()
  if (!text) return undefined
  if (text.length > maxLength) {
    throw new Error(`${label}不能超过 ${maxLength} 个字符`)
  }
  return text
}

function parseJsonObject(value: string): Record<string, unknown> | undefined {
  const parsed = JSON.parse(value) as unknown
  const object = objectValue(parsed)
  if (!object) {
    throw new Error('流式拦截策略 match_json 必须是对象')
  }
  return object
}

function objectValue(value: unknown): Record<string, unknown> | undefined {
  return typeof value === 'object' && value !== null && !Array.isArray(value)
    ? value as Record<string, unknown>
    : undefined
}

function clonePolicy(policy: StreamInterceptPolicySummary): StreamInterceptPolicySummary {
  return {
    ...policy,
    match: { ...policy.match }
  }
}

function assertKnownInputKeys(input: StreamInterceptPolicyInput, allowedKeys: ReadonlySet<string>, label: string): void {
  const unknownKeys = Object.keys(input as unknown as Record<string, unknown>).filter((key) => !allowedKeys.has(key))
  if (unknownKeys.length) {
    throw new Error(`${label}包含未知字段：${unknownKeys.join('、')}`)
  }
}

function assertOnlyKeys(value: Record<string, unknown>, allowedKeys: readonly string[], label: string): void {
  const allowed = new Set(allowedKeys)
  const unknownKeys = Object.keys(value).filter((key) => !allowed.has(key))
  if (unknownKeys.length) {
    throw new Error(`${label}包含未知字段：${unknownKeys.join('、')}`)
  }
}
