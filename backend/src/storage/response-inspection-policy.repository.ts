import { notifyGatewayRuntimeCacheInvalidation } from '../shared/gateway-cache-invalidation.js'
import { ANTHROPIC_PROTOCOL_CODE, GEMINI_PROTOCOL_CODE, GPT_VENDOR_CODE, OPENAI_PROTOCOL_CODE } from '../domain/provider-protocol.js'
import { runtimeConfig } from '../config/runtime.js'
import { getBusinessDatabase, newId, nowIso } from './database.js'
import type { DatabaseClient } from './database-client.js'
import { createPostgresDatabaseClient, createSqliteDatabaseClient } from './database-client.js'
import { getPostgresPool } from './postgres-client.js'
import { isProtocolProviderCode, isProtocolProviderCodeAsync } from './provider.repository.js'
import { requestSqliteReadWorker, sqliteReadWorkerPoolEnabled } from './sqlite-read-worker-pool.js'
export type ResponseInspectionPolicyScopeType = 'protocol' | 'provider'
export type ResponseInspectionPolicySource = 'system_default' | 'management' | 'account'
export type ResponseInspectionPolicyClientProfile =
  | 'codex'
  | 'generic_openai'
  | 'claude_code'
  | 'generic_anthropic'
  | 'generic_gemini'
  | 'gemini_cli'
export type ResponseInspectionPolicyAction =
  | 'observe'
  | 'drop_event'
  | 'retry_no_avoidance'
  | 'retry_next_account'
  | 'avoid_account_ttl'
  | 'avoid_upstream_bucket_ttl'

export type ResponseInspectionPolicyExecutionMode = 'intercept' | 'dry_run'
export type ResponseInspectionPolicyDataHandling = 'pass' | 'discard_event' | 'discard_response' | 'replace_with_failure'
export type ResponseInspectionPolicyAccountSwitch = 'none' | 'request_next_account' | 'avoid_account_ttl' | 'avoid_upstream_bucket_ttl'
export type ResponseInspectionPolicyAccountState = 'none' | 'runtime_avoidance'

export interface ResponseInspectionPolicyMatch {
  clientProfiles?: ResponseInspectionPolicyClientProfile[]
  outputTextIncludes?: string[]
  outputTextExcludes?: string[]
  errorCodes?: string[]
  errorTypes?: string[]
  errorMessageIncludes?: string[]
  finishReasons?: string[]
  jsonPathsExists?: string[]
  rawTextIncludes?: string[]
}

export interface ResponseInspectionPolicySummary {
  id: string
  defaultRule: boolean
  editable: boolean
  name: string
  enabled: boolean
  priority: number
  scopeType: ResponseInspectionPolicyScopeType
  protocolCode: string
  providerCode?: string
  match: ResponseInspectionPolicyMatch
  action: ResponseInspectionPolicyAction
  notes?: string
  createdAt?: string
  updatedAt?: string
}

export interface ResponseInspectionPolicyInput {
  name?: string
  enabled?: boolean
  priority?: number
  scopeType: ResponseInspectionPolicyScopeType
  protocolCode: string
  providerCode?: string | null
  match?: ResponseInspectionPolicyMatch
  action?: ResponseInspectionPolicyAction
  notes?: string | null
}

interface ResponseInspectionPolicyRow {
  id: string
  name: string
  enabled: number
  priority: number
  scope_type: string
  protocol_code: string
  provider_code: string | null
  match_json: string
  action: string
  notes: string | null
  created_at: string
  updated_at: string
}

export interface ResponseInspectionPolicyListResult {
  defaultRules: ResponseInspectionPolicySummary[]
  policies: ResponseInspectionPolicySummary[]
}

export const maxManagementResponseInspectionPolicies = 100

const policyActions = new Set<ResponseInspectionPolicyAction>([
  'observe',
  'drop_event',
  'retry_no_avoidance',
  'retry_next_account',
  'avoid_account_ttl',
  'avoid_upstream_bucket_ttl'
])

const inputKeys = new Set([
  'name',
  'enabled',
  'priority',
  'scopeType',
  'protocolCode',
  'providerCode',
  'match',
  'action',
  'notes'
])

const clientProfiles = ['codex', 'generic_openai', 'claude_code', 'generic_anthropic', 'generic_gemini', 'gemini_cli'] as const satisfies readonly ResponseInspectionPolicyClientProfile[]
const clientProfileValues = new Set<ResponseInspectionPolicyClientProfile>(clientProfiles)
const supportedResponseInspectionProtocolCodes = new Set([OPENAI_PROTOCOL_CODE, ANTHROPIC_PROTOCOL_CODE, GEMINI_PROTOCOL_CODE])
const codexCompactionContractMismatchErrorCode = 'codex_compaction_contract_mismatch'
const businessSchemaName = 'juhe_business'

const matchKeys = [
  'clientProfiles',
  'outputTextIncludes',
  'outputTextExcludes',
  'errorCodes',
  'errorTypes',
  'errorMessageIncludes',
  'finishReasons',
  'jsonPathsExists',
  'rawTextIncludes'
] as const

const positiveMatchKeys = [
  'outputTextIncludes',
  'errorCodes',
  'errorTypes',
  'errorMessageIncludes',
  'finishReasons',
  'jsonPathsExists',
  'rawTextIncludes'
] as const

const systemDefaultRules: ResponseInspectionPolicySummary[] = [
  {
    id: 'default_openai_error_object',
    defaultRule: true,
    editable: false,
    name: 'OpenAI error 对象',
    enabled: true,
    priority: 1,
    scopeType: 'protocol',
    protocolCode: OPENAI_PROTOCOL_CODE,
    match: {
      jsonPathsExists: ['error']
    },
    action: 'retry_no_avoidance',
    notes: 'OpenAI v1 JSON / SSE data.error 默认检查规则；是否允许客户端专用重试由运行时客户端能力门控。'
  },
  {
    id: 'default_openai_response_error',
    defaultRule: true,
    editable: false,
    name: 'OpenAI response.error',
    enabled: true,
    priority: 2,
    scopeType: 'protocol',
    protocolCode: OPENAI_PROTOCOL_CODE,
    match: {
      jsonPathsExists: ['response.error']
    },
    action: 'retry_no_avoidance',
    notes: 'OpenAI v1 Responses response.error 默认检查规则。'
  },
  {
    id: 'default_openai_failed_status',
    defaultRule: true,
    editable: false,
    name: 'OpenAI failed 状态',
    enabled: true,
    priority: 3,
    scopeType: 'protocol',
    protocolCode: OPENAI_PROTOCOL_CODE,
    match: {
      finishReasons: ['failed']
    },
    action: 'retry_no_avoidance',
    notes: 'OpenAI v1 Responses failed 状态默认检查规则。'
  },
  {
    id: 'default_codex_response_incomplete',
    defaultRule: true,
    editable: false,
    name: 'Codex response.incomplete',
    enabled: true,
    priority: 4,
    scopeType: 'protocol',
    protocolCode: OPENAI_PROTOCOL_CODE,
    match: {
      clientProfiles: ['codex'],
      finishReasons: ['incomplete']
    },
    action: 'retry_no_avoidance',
    notes: 'Codex 客户端会把 Responses response.incomplete 当成可重试流式错误；网关在写下游前拦截为统一可重试失败，避免服务端误判成功。'
  },
  {
    id: 'default_codex_compaction_contract',
    defaultRule: true,
    editable: false,
    name: 'Codex compact 输出契约',
    enabled: true,
    priority: 5,
    scopeType: 'protocol',
    protocolCode: OPENAI_PROTOCOL_CODE,
    match: {
      clientProfiles: ['codex'],
      errorCodes: [codexCompactionContractMismatchErrorCode]
    },
    action: 'retry_next_account',
    notes: 'Codex Remote Compaction V2 要求返回恰好 1 个 compaction output item；不满足时在下游写出前触发重试或可重试失败。'
  },
  {
    id: 'default_gpt_cyber_policy',
    defaultRule: true,
    editable: false,
    name: 'GPT cyber_policy',
    enabled: true,
    priority: 6,
    scopeType: 'provider',
    protocolCode: OPENAI_PROTOCOL_CODE,
    providerCode: GPT_VENDOR_CODE,
    match: {
      errorCodes: ['cyber_policy']
    },
    action: 'retry_no_avoidance',
    notes: 'GPT 供应商 cyber_policy 规则，适用于该供应商的所有下游客户端；不能扩散为所有 OpenAI-compatible 供应商语义。'
  },
  {
    id: 'default_anthropic_error_object',
    defaultRule: true,
    editable: false,
    name: 'Anthropic error 对象',
    enabled: true,
    priority: 1,
    scopeType: 'protocol',
    protocolCode: ANTHROPIC_PROTOCOL_CODE,
    match: {
      jsonPathsExists: ['error']
    },
    action: 'retry_no_avoidance',
    notes: 'Anthropic Messages JSON / SSE event:error 默认检查规则；错误类型只作为响应语义输入，不直接写账号状态。'
  },
  {
    id: 'default_gemini_cli_retryable_error',
    defaultRule: true,
    editable: false,
    name: 'Gemini CLI 可重试错误',
    enabled: true,
    priority: 1,
    scopeType: 'protocol',
    protocolCode: GEMINI_PROTOCOL_CODE,
    match: {
      clientProfiles: ['gemini_cli'],
      errorTypes: ['RESOURCE_EXHAUSTED', 'UNAVAILABLE', 'DEADLINE_EXCEEDED', 'INTERNAL', 'CANCELLED']
    },
    action: 'retry_next_account',
    notes: 'gemini-cli 已知会把 429、499、5xx 和超时类 Google canonical error 当作可重试错误；该规则只在 gemini_cli 客户端画像下请求下一个账号，不扩散到普通 Gemini 客户端。'
  },
  {
    id: 'default_gemini_error_object',
    defaultRule: true,
    editable: false,
    name: 'Gemini error 对象',
    enabled: true,
    priority: 20,
    scopeType: 'protocol',
    protocolCode: GEMINI_PROTOCOL_CODE,
    match: {
      jsonPathsExists: ['error']
    },
    action: 'retry_no_avoidance',
    notes: 'Gemini JSON / SSE error 默认检查规则；错误状态只作为响应语义输入，不直接写账号状态。'
  }
]

export function listResponseInspectionPolicyDefaultRules(): ResponseInspectionPolicySummary[] {
  return systemDefaultRules.map(clonePolicy)
}

export function listResponseInspectionPolicies(): ResponseInspectionPolicyListResult {
  return {
    defaultRules: listResponseInspectionPolicyDefaultRules(),
    policies: listResponseInspectionPolicyRows().map(policyFromRow)
  }
}

export async function listResponseInspectionPoliciesAsync(): Promise<ResponseInspectionPolicyListResult> {
  if (sqliteReadWorkerPoolEnabled()) {
    return requestSqliteReadWorker({
      type: 'list_response_inspection_policies_read_only'
    })
  }
  if (runtimeConfig.databaseDriver !== 'postgres') {
    return listResponseInspectionPolicies()
  }
  return {
    defaultRules: listResponseInspectionPolicyDefaultRules(),
    policies: (await listResponseInspectionPolicyRowsAsync()).map(policyFromRow)
  }
}

export function listActiveResponseInspectionPoliciesForGateway(input: {
  protocolCode: string
  providerCode?: string
}): ResponseInspectionPolicySummary[] {
  const protocolCode = normalizeGatewayPolicyProtocolCode(input.protocolCode)
  if (!protocolCode) {
    return []
  }
  const providerCode = normalizeOptionalText(input.providerCode, '供应商编码')
  const scopeFilter = providerCode
    ? `AND (
        (scope_type = 'protocol' AND provider_code IS NULL)
        OR (scope_type = 'provider' AND provider_code = ?)
      )`
    : `AND scope_type = 'protocol'
      AND provider_code IS NULL`
  const params = providerCode
    ? [protocolCode, providerCode, maxManagementResponseInspectionPolicies]
    : [protocolCode, maxManagementResponseInspectionPolicies]
  const rows = getBusinessDatabase()
    .prepare(`
      SELECT *
      FROM response_inspection_policies
      WHERE enabled = 1
        AND protocol_code = ?
        ${scopeFilter}
      ORDER BY CASE scope_type WHEN 'provider' THEN 0 ELSE 1 END ASC, priority ASC, updated_at DESC, id ASC
      LIMIT ?
    `)
    .all(...params) as unknown as ResponseInspectionPolicyRow[]
  return [
    ...listResponseInspectionPolicyDefaultRules().filter((policy) => policyMatchesGatewayScope(policy, protocolCode, providerCode)),
    ...rows.map(policyFromRow)
  ]
}

export async function listActiveResponseInspectionPoliciesForGatewayAsync(input: {
  protocolCode: string
  providerCode?: string
}): Promise<ResponseInspectionPolicySummary[]> {
  if (sqliteReadWorkerPoolEnabled()) {
    return requestSqliteReadWorker({
      type: 'list_active_response_inspection_policies_read_only',
      input
    })
  }
  if (runtimeConfig.databaseDriver !== 'postgres') {
    return listActiveResponseInspectionPoliciesForGateway(input)
  }
  const protocolCode = normalizeGatewayPolicyProtocolCode(input.protocolCode)
  if (!protocolCode) {
    return []
  }
  const providerCode = normalizeOptionalText(input.providerCode, '供应商编码')
  const scopeFilter = providerCode
    ? `AND (
        (scope_type = 'protocol' AND provider_code IS NULL)
        OR (scope_type = 'provider' AND provider_code = ?)
      )`
    : `AND scope_type = 'protocol'
      AND provider_code IS NULL`
  const params = providerCode
    ? [protocolCode, providerCode, maxManagementResponseInspectionPolicies]
    : [protocolCode, maxManagementResponseInspectionPolicies]
  const client = await getResponseInspectionPolicyDatabaseClient()
  const rows = await client.query<ResponseInspectionPolicyRow>(`
    SELECT *
    FROM ${responseInspectionPoliciesTable(client)}
    WHERE enabled = 1
      AND protocol_code = ?
      ${scopeFilter}
    ORDER BY CASE scope_type WHEN 'provider' THEN 0 ELSE 1 END ASC, priority ASC, updated_at DESC, id ASC
    LIMIT ?
  `, params)
  return [
    ...listResponseInspectionPolicyDefaultRules().filter((policy) => policyMatchesGatewayScope(policy, protocolCode, providerCode)),
    ...rows.map(policyFromRow)
  ]
}

function normalizeGatewayPolicyProtocolCode(value: unknown): string | undefined {
  const text = requiredText(value, '协议编码', 80)
  return supportedResponseInspectionProtocolCodes.has(text) ? text : undefined
}

export function createResponseInspectionPolicy(input: ResponseInspectionPolicyInput): ResponseInspectionPolicySummary {
  assertKnownInputKeys(input, inputKeys, '响应检查策略')
  assertManagementPolicyCapacity()
  const now = nowIso()
  const policy = normalizePolicyInput(input, {
    id: newId('rip'),
    createdAt: now,
    updatedAt: now
  })
  getBusinessDatabase()
    .prepare(`
      INSERT INTO response_inspection_policies (
        id, name, enabled, priority, scope_type, protocol_code, provider_code, match_json,
        action, notes, created_at, updated_at
      ) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
    `)
    .run(
      policy.id,
      policy.name,
      policy.enabled ? 1 : 0,
      policy.priority,
      policy.scopeType,
      policy.protocolCode,
      policy.providerCode ?? null,
      JSON.stringify(policy.match),
      policy.action,
      policy.notes ?? null,
      policy.createdAt ?? now,
      policy.updatedAt ?? now
    )
  notifyGatewayRuntimeCacheInvalidation('response_inspection_policy_created')
  return policy
}

export async function createResponseInspectionPolicyAsync(input: ResponseInspectionPolicyInput): Promise<ResponseInspectionPolicySummary> {
  if (runtimeConfig.databaseDriver !== 'postgres') {
    return createResponseInspectionPolicy(input)
  }
  assertKnownInputKeys(input, inputKeys, '响应检查策略')
  await assertManagementPolicyCapacityAsync()
  const now = nowIso()
  const policy = await normalizePolicyInputAsync(input, {
    id: newId('rip'),
    createdAt: now,
    updatedAt: now
  })
  const client = await getResponseInspectionPolicyDatabaseClient()
  await client.execute(`
    INSERT INTO ${responseInspectionPoliciesTable(client)} (
      id, name, enabled, priority, scope_type, protocol_code, provider_code, match_json,
      action, notes, created_at, updated_at
    ) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
  `, [
    policy.id,
    policy.name,
    policy.enabled ? 1 : 0,
    policy.priority,
    policy.scopeType,
    policy.protocolCode,
    policy.providerCode ?? null,
    JSON.stringify(policy.match),
    policy.action,
    policy.notes ?? null,
    policy.createdAt ?? now,
    policy.updatedAt ?? now
  ])
  notifyGatewayRuntimeCacheInvalidation('response_inspection_policy_created')
  return policy
}

export function updateResponseInspectionPolicy(id: string, input: ResponseInspectionPolicyInput): ResponseInspectionPolicySummary | undefined {
  assertKnownInputKeys(input, inputKeys, '响应检查策略')
  const current = findResponseInspectionPolicyRow(id)
  if (!current) return undefined
  const policy = normalizePolicyInput(input, {
    id,
    createdAt: current.created_at,
    updatedAt: nowIso()
  })
  getBusinessDatabase()
    .prepare(`
      UPDATE response_inspection_policies
      SET name = ?, enabled = ?, priority = ?, scope_type = ?, protocol_code = ?,
          provider_code = ?, match_json = ?, action = ?, notes = ?, updated_at = ?
      WHERE id = ?
    `)
    .run(
      policy.name,
      policy.enabled ? 1 : 0,
      policy.priority,
      policy.scopeType,
      policy.protocolCode,
      policy.providerCode ?? null,
      JSON.stringify(policy.match),
      policy.action,
      policy.notes ?? null,
      policy.updatedAt ?? nowIso(),
      id
    )
  notifyGatewayRuntimeCacheInvalidation('response_inspection_policy_updated')
  return policy
}

export async function updateResponseInspectionPolicyAsync(id: string, input: ResponseInspectionPolicyInput): Promise<ResponseInspectionPolicySummary | undefined> {
  if (runtimeConfig.databaseDriver !== 'postgres') {
    return updateResponseInspectionPolicy(id, input)
  }
  assertKnownInputKeys(input, inputKeys, '响应检查策略')
  const current = await findResponseInspectionPolicyRowAsync(id)
  if (!current) return undefined
  const policy = await normalizePolicyInputAsync(input, {
    id,
    createdAt: current.created_at,
    updatedAt: nowIso()
  })
  const client = await getResponseInspectionPolicyDatabaseClient()
  await client.execute(`
    UPDATE ${responseInspectionPoliciesTable(client)}
    SET name = ?, enabled = ?, priority = ?, scope_type = ?, protocol_code = ?,
        provider_code = ?, match_json = ?, action = ?, notes = ?, updated_at = ?
    WHERE id = ?
  `, [
    policy.name,
    policy.enabled ? 1 : 0,
    policy.priority,
    policy.scopeType,
    policy.protocolCode,
    policy.providerCode ?? null,
    JSON.stringify(policy.match),
    policy.action,
    policy.notes ?? null,
    policy.updatedAt ?? nowIso(),
    id
  ])
  notifyGatewayRuntimeCacheInvalidation('response_inspection_policy_updated')
  return policy
}

export function deleteResponseInspectionPolicy(id: string): boolean {
  const result = getBusinessDatabase().prepare('DELETE FROM response_inspection_policies WHERE id = ?').run(id)
  const deleted = result.changes > 0
  if (deleted) {
    notifyGatewayRuntimeCacheInvalidation('response_inspection_policy_deleted')
  }
  return deleted
}

export async function deleteResponseInspectionPolicyAsync(id: string): Promise<boolean> {
  if (runtimeConfig.databaseDriver !== 'postgres') {
    return deleteResponseInspectionPolicy(id)
  }
  const client = await getResponseInspectionPolicyDatabaseClient()
  const result = await client.execute(`DELETE FROM ${responseInspectionPoliciesTable(client)} WHERE id = ?`, [id])
  const deleted = result.changes > 0
  if (deleted) {
    notifyGatewayRuntimeCacheInvalidation('response_inspection_policy_deleted')
  }
  return deleted
}

export function responseInspectionPolicyActionRuntime(action: ResponseInspectionPolicyAction): {
  executionMode: ResponseInspectionPolicyExecutionMode
  dataHandling: ResponseInspectionPolicyDataHandling
  retryEnabled: boolean
  accountSwitch: ResponseInspectionPolicyAccountSwitch
  accountState: ResponseInspectionPolicyAccountState
} {
  switch (action) {
    case 'observe':
      return { executionMode: 'dry_run', dataHandling: 'pass', retryEnabled: false, accountSwitch: 'none', accountState: 'none' }
    case 'drop_event':
      return { executionMode: 'intercept', dataHandling: 'discard_event', retryEnabled: false, accountSwitch: 'none', accountState: 'none' }
    case 'retry_no_avoidance':
      return { executionMode: 'intercept', dataHandling: 'replace_with_failure', retryEnabled: true, accountSwitch: 'none', accountState: 'none' }
    case 'retry_next_account':
      return { executionMode: 'intercept', dataHandling: 'replace_with_failure', retryEnabled: true, accountSwitch: 'request_next_account', accountState: 'none' }
    case 'avoid_account_ttl':
      return { executionMode: 'intercept', dataHandling: 'replace_with_failure', retryEnabled: true, accountSwitch: 'avoid_account_ttl', accountState: 'runtime_avoidance' }
    case 'avoid_upstream_bucket_ttl':
      return { executionMode: 'intercept', dataHandling: 'replace_with_failure', retryEnabled: true, accountSwitch: 'avoid_upstream_bucket_ttl', accountState: 'none' }
  }
}

function listResponseInspectionPolicyRows(): ResponseInspectionPolicyRow[] {
  return getBusinessDatabase()
    .prepare(`
      SELECT *
      FROM response_inspection_policies
      ORDER BY priority ASC, updated_at DESC, id ASC
      LIMIT ?
    `)
    .all(maxManagementResponseInspectionPolicies) as unknown as ResponseInspectionPolicyRow[]
}

async function listResponseInspectionPolicyRowsAsync(): Promise<ResponseInspectionPolicyRow[]> {
  const client = await getResponseInspectionPolicyDatabaseClient()
  return await client.query<ResponseInspectionPolicyRow>(`
    SELECT *
    FROM ${responseInspectionPoliciesTable(client)}
    ORDER BY priority ASC, updated_at DESC, id ASC
    LIMIT ?
  `, [maxManagementResponseInspectionPolicies])
}

function findResponseInspectionPolicyRow(id: string): ResponseInspectionPolicyRow | undefined {
  return getBusinessDatabase()
    .prepare('SELECT * FROM response_inspection_policies WHERE id = ?')
    .get(id) as unknown as ResponseInspectionPolicyRow | undefined
}

async function findResponseInspectionPolicyRowAsync(id: string): Promise<ResponseInspectionPolicyRow | undefined> {
  const client = await getResponseInspectionPolicyDatabaseClient()
  return await client.one<ResponseInspectionPolicyRow>(`SELECT * FROM ${responseInspectionPoliciesTable(client)} WHERE id = ?`, [id])
}

function assertManagementPolicyCapacity(): void {
  const rows = getBusinessDatabase()
    .prepare('SELECT id FROM response_inspection_policies LIMIT ?')
    .all(maxManagementResponseInspectionPolicies + 1)
  if (rows.length >= maxManagementResponseInspectionPolicies) {
    throw new Error(`响应检查策略最多允许 ${maxManagementResponseInspectionPolicies} 条`)
  }
}

async function assertManagementPolicyCapacityAsync(): Promise<void> {
  const client = await getResponseInspectionPolicyDatabaseClient()
  const rows = await client.query(`SELECT id FROM ${responseInspectionPoliciesTable(client)} LIMIT ?`, [maxManagementResponseInspectionPolicies + 1])
  if (rows.length >= maxManagementResponseInspectionPolicies) {
    throw new Error(`响应检查策略最多允许 ${maxManagementResponseInspectionPolicies} 条`)
  }
}

function normalizePolicyInput(
  input: ResponseInspectionPolicyInput,
  options: {
    id: string
    createdAt: string
    updatedAt: string
  }
): ResponseInspectionPolicySummary {
  const scopeType = normalizeScopeType(input.scopeType)
  const protocolCode = normalizeProtocolCode(input.protocolCode)
  const providerCode = scopeType === 'provider'
    ? normalizeProviderCode(input.providerCode)
    : undefined
  if (scopeType === 'provider' && (providerCode === undefined || !isProtocolProviderCode(providerCode, protocolCode))) {
    throw new Error('响应检查策略供应商必须使用同协议启用档案')
  }
  if (scopeType === 'protocol' && input.providerCode) {
    throw new Error('协议层响应检查策略不能绑定供应商')
  }
  return {
    id: options.id,
    defaultRule: false,
    editable: true,
    name: requiredText(input.name, '规则名称', 100),
    enabled: input.enabled ?? true,
    priority: positiveInt(input.priority ?? 100, '优先级', 9999),
    scopeType,
    protocolCode,
    providerCode,
    match: normalizeMatch(input.match ?? {}),
    action: normalizeAction(input.action),
    notes: optionalText(input.notes, '备注', 1000),
    createdAt: options.createdAt,
    updatedAt: options.updatedAt
  }
}

async function normalizePolicyInputAsync(
  input: ResponseInspectionPolicyInput,
  options: {
    id: string
    createdAt: string
    updatedAt: string
  }
): Promise<ResponseInspectionPolicySummary> {
  const scopeType = normalizeScopeType(input.scopeType)
  const protocolCode = normalizeProtocolCode(input.protocolCode)
  const providerCode = scopeType === 'provider'
    ? normalizeProviderCode(input.providerCode)
    : undefined
  if (scopeType === 'provider' && (providerCode === undefined || !(await isProtocolProviderCodeAsync(providerCode, protocolCode)))) {
    throw new Error('响应检查策略供应商必须使用同协议启用档案')
  }
  if (scopeType === 'protocol' && input.providerCode) {
    throw new Error('协议层响应检查策略不能绑定供应商')
  }
  return {
    id: options.id,
    defaultRule: false,
    editable: true,
    name: requiredText(input.name, '规则名称', 100),
    enabled: input.enabled ?? true,
    priority: positiveInt(input.priority ?? 100, '优先级', 9999),
    scopeType,
    protocolCode,
    providerCode,
    match: normalizeMatch(input.match ?? {}),
    action: normalizeAction(input.action),
    notes: optionalText(input.notes, '备注', 1000),
    createdAt: options.createdAt,
    updatedAt: options.updatedAt
  }
}

function policyFromRow(row: ResponseInspectionPolicyRow): ResponseInspectionPolicySummary {
  return {
    id: row.id,
    defaultRule: false,
    editable: true,
    name: row.name,
    enabled: row.enabled === 1,
    priority: row.priority,
    scopeType: normalizeScopeType(row.scope_type),
    protocolCode: row.protocol_code,
    providerCode: row.provider_code ?? undefined,
    match: normalizeMatch(parseJsonObject(row.match_json)),
    action: normalizeAction(row.action),
    notes: row.notes ?? undefined,
    createdAt: row.created_at,
    updatedAt: row.updated_at
  }
}

function policyMatchesGatewayScope(
  policy: ResponseInspectionPolicySummary,
  protocolCode: string,
  providerCode: string | undefined
): boolean {
  if (!policy.enabled || policy.protocolCode !== protocolCode) return false
  if (policy.scopeType === 'protocol') return policy.providerCode === undefined
  return providerCode !== undefined && policy.providerCode === providerCode
}

function normalizeMatch(value: unknown): ResponseInspectionPolicyMatch {
  if (!value || typeof value !== 'object' || Array.isArray(value)) {
    return {}
  }
  const record = value as Record<string, unknown>
  assertKnownInputKeys(record, new Set(matchKeys), '响应检查策略匹配条件')
  const match: ResponseInspectionPolicyMatch = {}
  const normalizedClientProfiles = normalizeKnownStringList(
    record.clientProfiles,
    '响应检查策略clientProfiles',
    clientProfileValues
  ) as ResponseInspectionPolicyClientProfile[]
  if (normalizedClientProfiles.length > 0) {
    match.clientProfiles = normalizedClientProfiles
  }
  for (const key of matchKeys) {
    if (key === 'clientProfiles') {
      continue
    }
    const normalized = normalizeStringList(record[key], `响应检查策略${key}`)
    if (normalized.length > 0) {
      match[key] = normalized
    }
  }
  const hasMatcher = positiveMatchKeys.some((key) => (match[key]?.length ?? 0) > 0)
  if (!hasMatcher) {
    throw new Error('响应检查策略至少需要一个匹配条件')
  }
  return match
}

function normalizeAction(value: unknown): ResponseInspectionPolicyAction {
  if (policyActions.has(value as ResponseInspectionPolicyAction)) {
    return value as ResponseInspectionPolicyAction
  }
  throw new Error('响应检查策略动作无效')
}

function normalizeScopeType(value: unknown): ResponseInspectionPolicyScopeType {
  if (value === 'protocol' || value === 'provider') return value
  throw new Error('响应检查策略作用层级无效')
}

function normalizeProtocolCode(value: unknown): string {
  const text = requiredText(value, '协议编码', 80)
  if (!supportedResponseInspectionProtocolCodes.has(text)) {
    throw new Error('当前响应检查策略只支持 OpenAI v1、Anthropic v1 或 Gemini v1beta 协议')
  }
  return text
}

function normalizeProviderCode(value: unknown): string {
  return requiredText(value, '供应商编码', 80)
}

function normalizeOptionalText(value: unknown, label: string): string | undefined {
  if (value === undefined || value === null) return undefined
  return requiredText(value, label, 80)
}

function requiredText(value: unknown, label: string, max: number): string {
  if (typeof value !== 'string') throw new Error(`${label}无效`)
  const text = value.trim()
  if (!text) throw new Error(`${label}不能为空`)
  if (text.length > max) throw new Error(`${label}不能超过 ${max} 个字符`)
  return text
}

function optionalText(value: unknown, label: string, max: number): string | undefined {
  if (value === undefined || value === null) return undefined
  return requiredText(value, label, max)
}

function positiveInt(value: unknown, label: string, max: number): number {
  if (typeof value !== 'number' || !Number.isInteger(value) || value < 1 || value > max) {
    throw new Error(`${label}必须是 1-${max} 的整数`)
  }
  return value
}

function normalizeStringList(value: unknown, label: string): string[] {
  if (value === undefined) return []
  if (!Array.isArray(value)) throw new Error(`${label}必须是字符串数组`)
  if (value.length > 50) throw new Error(`${label}不能超过 50 项`)
  const output: string[] = []
  for (const item of value) {
    const text = requiredText(item, label, 200)
    if (!output.includes(text)) {
      output.push(text)
    }
  }
  return output
}

function normalizeKnownStringList<T extends string>(
  value: unknown,
  label: string,
  allowedValues: ReadonlySet<T>
): T[] {
  const items = normalizeStringList(value, label)
  for (const item of items) {
    if (!allowedValues.has(item as T)) {
      throw new Error(`${label}包含不支持的值：${item}`)
    }
  }
  return items as T[]
}

function parseJsonObject(text: string): Record<string, unknown> {
  try {
    const value = JSON.parse(text) as unknown
    return value && typeof value === 'object' && !Array.isArray(value) ? value as Record<string, unknown> : {}
  } catch {
    return {}
  }
}

function assertKnownInputKeys(value: object, allowedKeys: ReadonlySet<string>, label: string): void {
  for (const key of Object.keys(value)) {
    if (!allowedKeys.has(key)) {
      throw new Error(`${label}包含未知字段：${key}`)
    }
  }
}

function clonePolicy(policy: ResponseInspectionPolicySummary): ResponseInspectionPolicySummary {
  return {
    ...policy,
    match: { ...policy.match }
  }
}

async function getResponseInspectionPolicyDatabaseClient(): Promise<DatabaseClient> {
  if (runtimeConfig.databaseDriver === 'postgres') {
    return createPostgresDatabaseClient(await getPostgresPool())
  }
  return createSqliteDatabaseClient(getBusinessDatabase())
}

function responseInspectionPoliciesTable(client: DatabaseClient): string {
  return client.driver === 'postgres'
    ? client.dialect.qualifyTable(businessSchemaName, 'response_inspection_policies')
    : client.dialect.quoteIdentifier('response_inspection_policies')
}
