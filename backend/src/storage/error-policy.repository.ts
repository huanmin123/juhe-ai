import { notifyGatewayRuntimeCacheInvalidation } from '../shared/gateway-cache-invalidation.js'
import { OPENAI_PROTOCOL_CODE } from '../domain/provider-protocol.js'
import { getBusinessDatabase, newId, nowIso } from './database.js'
import { isOpenAIProtocolProviderCode } from './provider.repository.js'

export type ErrorPolicyScopeType = 'global' | 'protocol' | 'provider' | 'client' | 'model'
export type ErrorPolicyAction = 'retry_next' | 'temp_unschedulable' | 'rate_limited' | 'error_disabled'
export type ErrorPolicyRecoveryStrategy = 'duration' | 'daily' | 'weekly'
export type ErrorPolicyModelMatchType = 'exact' | 'prefix' | 'contains'

export interface ErrorPolicyMatch {
  statusCodes?: number[]
  errorCodes?: string[]
  errorTypes?: string[]
  keywords?: string[]
}

export interface ErrorPolicySummary {
  id: string
  editable: boolean
  name: string
  enabled: boolean
  priority: number
  scopeType: ErrorPolicyScopeType
  protocolCode?: string
  providerCode?: string
  clientProfile?: string
  modelPattern?: string
  modelMatchType?: ErrorPolicyModelMatchType
  match: ErrorPolicyMatch
  action: ErrorPolicyAction
  resetStrategy?: ErrorPolicyRecoveryStrategy
  durationHours?: number
  dailyResetHour?: number
  weeklyResetDay?: number
  weeklyResetHour?: number
  notes?: string
  createdAt?: string
  updatedAt?: string
}

export interface ErrorPolicyInput {
  name?: string
  enabled?: boolean
  priority?: number
  scopeType: ErrorPolicyScopeType
  protocolCode?: string | null
  providerCode?: string | null
  clientProfile?: string | null
  modelPattern?: string | null
  modelMatchType?: ErrorPolicyModelMatchType | null
  match?: ErrorPolicyMatch
  action?: ErrorPolicyAction
  resetStrategy?: ErrorPolicyRecoveryStrategy | null
  durationHours?: number | null
  dailyResetHour?: number | null
  weeklyResetDay?: number | null
  weeklyResetHour?: number | null
  notes?: string | null
}

interface ErrorPolicyRow {
  id: string
  name: string
  enabled: number
  priority: number
  scope_type: string
  protocol_code: string | null
  provider_code: string | null
  client_profile: string | null
  model_pattern: string | null
  model_match_type: string | null
  match_json: string
  action: string
  reset_strategy: string | null
  duration_hours: number | null
  daily_reset_hour: number | null
  weekly_reset_day: number | null
  weekly_reset_hour: number | null
  notes: string | null
  created_at: string
  updated_at: string
}

export const maxErrorPolicyDefinitions = 200

const errorPolicyInputKeys = new Set([
  'name',
  'enabled',
  'priority',
  'scopeType',
  'protocolCode',
  'providerCode',
  'clientProfile',
  'modelPattern',
  'modelMatchType',
  'match',
  'action',
  'resetStrategy',
  'durationHours',
  'dailyResetHour',
  'weeklyResetDay',
  'weeklyResetHour',
  'notes'
])

const errorPolicyMatchKeys = [
  'statusCodes',
  'errorCodes',
  'errorTypes',
  'keywords'
] as const

const policyActions = new Set<ErrorPolicyAction>([
  'retry_next',
  'temp_unschedulable',
  'rate_limited',
  'error_disabled'
])

export interface ErrorPolicyListResult {
  policies: ErrorPolicySummary[]
}

export function listErrorPolicies(): ErrorPolicyListResult {
  return {
    policies: listErrorPolicyRows().map(policyFromRow)
  }
}

export function listActiveErrorPoliciesForGateway(input: {
  protocolCode?: string
  providerCode?: string
}): ErrorPolicySummary[] {
  const protocolCode = normalizeOptionalProtocolCode(input.protocolCode, '协议编码')
  const providerCode = normalizeOptionalProviderCode(input.providerCode, '供应商编码')
  const rows = getBusinessDatabase()
    .prepare(`
      SELECT *
      FROM error_policies
      WHERE enabled = 1
        AND (
          scope_type = 'global'
          OR (protocol_code = ? AND scope_type IN ('protocol', 'client'))
          OR (protocol_code = ? AND scope_type = 'provider' AND provider_code = ?)
          OR (
            protocol_code = ?
            AND scope_type = 'model'
            AND (provider_code IS NULL OR provider_code = ?)
          )
        )
      ORDER BY
        CASE scope_type
          WHEN 'model' THEN 0
          WHEN 'client' THEN 1
          WHEN 'provider' THEN 2
          WHEN 'protocol' THEN 3
          ELSE 4
        END ASC,
        priority ASC,
        updated_at DESC,
        id ASC
      LIMIT ?
    `)
    .all(
      protocolCode ?? '',
      protocolCode ?? '',
      providerCode ?? '',
      protocolCode ?? '',
      providerCode ?? '',
      maxErrorPolicyDefinitions
    ) as unknown as ErrorPolicyRow[]
  return rows.map(policyFromRow)
}

export function createErrorPolicy(input: ErrorPolicyInput): ErrorPolicySummary {
  assertKnownInputKeys(input, errorPolicyInputKeys, '请求错误策略')
  assertManagementPolicyCapacity()
  const now = nowIso()
  const policy = normalizePolicyInput(input, {
    id: newId('ep'),
    createdAt: now,
    updatedAt: now
  })
  getBusinessDatabase()
    .prepare(`
      INSERT INTO error_policies (
        id, name, enabled, priority, scope_type, protocol_code, provider_code,
        client_profile, model_pattern, model_match_type, match_json, action,
        reset_strategy, duration_hours, daily_reset_hour, weekly_reset_day,
        weekly_reset_hour, notes, created_at, updated_at
      ) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
    `)
    .run(
      policy.id,
      policy.name,
      policy.enabled ? 1 : 0,
      policy.priority,
      policy.scopeType,
      policy.protocolCode ?? null,
      policy.providerCode ?? null,
      policy.clientProfile ?? null,
      policy.modelPattern ?? null,
      policy.modelMatchType ?? null,
      JSON.stringify(policy.match),
      policy.action,
      policy.resetStrategy ?? null,
      policy.durationHours ?? null,
      policy.dailyResetHour ?? null,
      policy.weeklyResetDay ?? null,
      policy.weeklyResetHour ?? null,
      policy.notes ?? null,
      policy.createdAt ?? now,
      policy.updatedAt ?? now
    )
  notifyGatewayRuntimeCacheInvalidation('error_policy_created')
  return policy
}

export function updateErrorPolicy(id: string, input: ErrorPolicyInput): ErrorPolicySummary | undefined {
  assertKnownInputKeys(input, errorPolicyInputKeys, '请求错误策略')
  const current = findErrorPolicyRow(id)
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
      UPDATE error_policies
      SET name = ?,
          enabled = ?,
          priority = ?,
          scope_type = ?,
          protocol_code = ?,
          provider_code = ?,
          client_profile = ?,
          model_pattern = ?,
          model_match_type = ?,
          match_json = ?,
          action = ?,
          reset_strategy = ?,
          duration_hours = ?,
          daily_reset_hour = ?,
          weekly_reset_day = ?,
          weekly_reset_hour = ?,
          notes = ?,
          updated_at = ?
      WHERE id = ?
    `)
    .run(
      policy.name,
      policy.enabled ? 1 : 0,
      policy.priority,
      policy.scopeType,
      policy.protocolCode ?? null,
      policy.providerCode ?? null,
      policy.clientProfile ?? null,
      policy.modelPattern ?? null,
      policy.modelMatchType ?? null,
      JSON.stringify(policy.match),
      policy.action,
      policy.resetStrategy ?? null,
      policy.durationHours ?? null,
      policy.dailyResetHour ?? null,
      policy.weeklyResetDay ?? null,
      policy.weeklyResetHour ?? null,
      policy.notes ?? null,
      policy.updatedAt ?? now,
      id
    )
  if (Number(result.changes ?? 0) > 0) {
    notifyGatewayRuntimeCacheInvalidation('error_policy_updated')
  }
  return policy
}

export function deleteErrorPolicy(id: string): boolean {
  const result = getBusinessDatabase().prepare('DELETE FROM error_policies WHERE id = ?').run(id)
  const deleted = Number(result.changes ?? 0) > 0
  if (deleted) {
    notifyGatewayRuntimeCacheInvalidation('error_policy_deleted')
  }
  return deleted
}

function listErrorPolicyRows(): ErrorPolicyRow[] {
  return getBusinessDatabase()
    .prepare(`
      SELECT *
      FROM error_policies
      ORDER BY
        CASE scope_type
          WHEN 'global' THEN 0
          WHEN 'protocol' THEN 1
          WHEN 'provider' THEN 2
          WHEN 'client' THEN 3
          WHEN 'model' THEN 4
          ELSE 5
        END ASC,
        protocol_code ASC,
        provider_code ASC,
        client_profile ASC,
        model_pattern ASC,
        priority ASC,
        updated_at DESC,
        id ASC
      LIMIT ?
    `)
    .all(maxErrorPolicyDefinitions) as unknown as ErrorPolicyRow[]
}

function findErrorPolicyRow(id: string): ErrorPolicyRow | undefined {
  return getBusinessDatabase()
    .prepare('SELECT * FROM error_policies WHERE id = ?')
    .get(id) as unknown as ErrorPolicyRow | undefined
}

function assertManagementPolicyCapacity(): void {
  const rows = getBusinessDatabase()
    .prepare('SELECT id FROM error_policies LIMIT ?')
    .all(maxErrorPolicyDefinitions) as Array<{ id: string }>
  if (rows.length >= maxErrorPolicyDefinitions) {
    throw new Error(`请求错误策略不能超过 ${maxErrorPolicyDefinitions} 条`)
  }
}

function normalizePolicyInput(
  input: ErrorPolicyInput,
  metadata: {
    id: string
    createdAt: string
    updatedAt: string
    fallback?: ErrorPolicySummary
  }
): ErrorPolicySummary {
  const fallback = metadata.fallback
  const scopeType = normalizeScopeType(input.scopeType, fallback?.scopeType)
  const scope = normalizeScopeFields(input, scopeType, fallback)
  const action = input.action === undefined && fallback
    ? fallback.action
    : normalizePolicyAction(input.action)
  const recovery = normalizeRecoveryFields(input, action, fallback)
  return {
    id: metadata.id,
    editable: true,
    name: normalizePolicyName(input.name, fallback?.name),
    enabled: normalizeBooleanInput(input.enabled, fallback?.enabled ?? true, '启用状态'),
    priority: normalizePriority(input.priority, fallback?.priority),
    scopeType,
    ...scope,
    match: normalizeMatch(input.match === undefined ? fallback?.match : input.match),
    action,
    ...recovery,
    notes: normalizeOptionalTextInput(input.notes, fallback?.notes, 1000, '备注'),
    createdAt: metadata.createdAt,
    updatedAt: metadata.updatedAt
  }
}

function policyFromRow(row: ErrorPolicyRow): ErrorPolicySummary {
  return {
    id: row.id,
    editable: true,
    name: row.name,
    enabled: row.enabled === 1,
    priority: normalizePriority(row.priority, 1),
    scopeType: normalizeScopeType(row.scope_type),
    protocolCode: row.protocol_code ?? undefined,
    providerCode: row.provider_code ?? undefined,
    clientProfile: row.client_profile ?? undefined,
    modelPattern: row.model_pattern ?? undefined,
    modelMatchType: normalizeOptionalModelMatchType(row.model_match_type, undefined),
    match: normalizeMatch(parseJsonObject(row.match_json)),
    action: normalizePolicyAction(row.action),
    resetStrategy: normalizeOptionalRecoveryStrategy(row.reset_strategy, undefined),
    durationHours: normalizeOptionalPositiveInt(row.duration_hours, undefined, '恢复小时数'),
    dailyResetHour: normalizeOptionalHour(row.daily_reset_hour, undefined, '每天恢复时间'),
    weeklyResetDay: normalizeOptionalWeekday(row.weekly_reset_day, undefined, '每周恢复日'),
    weeklyResetHour: normalizeOptionalHour(row.weekly_reset_hour, undefined, '每周恢复时间'),
    notes: row.notes ?? undefined,
    createdAt: row.created_at,
    updatedAt: row.updated_at
  }
}

function normalizeScopeFields(
  input: ErrorPolicyInput,
  scopeType: ErrorPolicyScopeType,
  fallback?: ErrorPolicySummary
): Pick<ErrorPolicySummary, 'protocolCode' | 'providerCode' | 'clientProfile' | 'modelPattern' | 'modelMatchType'> {
  if (scopeType === 'global') {
    assertNoScopeField(input.protocolCode, '全局层请求错误策略不能绑定协议')
    assertNoScopeField(input.providerCode, '全局层请求错误策略不能绑定供应商')
    assertNoScopeField(input.clientProfile, '全局层请求错误策略不能绑定客户端')
    assertNoScopeField(input.modelPattern, '全局层请求错误策略不能绑定模型')
    return {}
  }
  const protocolCode = normalizeProtocolCode(input.protocolCode === undefined ? fallback?.protocolCode : input.protocolCode)
  if (scopeType === 'protocol') {
    assertNoScopeField(input.providerCode, '协议层请求错误策略不能绑定供应商')
    assertNoScopeField(input.clientProfile, '协议层请求错误策略不能绑定客户端')
    assertNoScopeField(input.modelPattern, '协议层请求错误策略不能绑定模型')
    return { protocolCode }
  }
  if (scopeType === 'provider') {
    const providerCode = normalizeRequiredProviderCode(input.providerCode === undefined ? fallback?.providerCode : input.providerCode, protocolCode)
    assertNoScopeField(input.clientProfile, '供应商层请求错误策略不能绑定客户端')
    assertNoScopeField(input.modelPattern, '供应商层请求错误策略不能绑定模型')
    return { protocolCode, providerCode }
  }
  if (scopeType === 'client') {
    assertNoScopeField(input.providerCode, '客户端层请求错误策略不能绑定供应商')
    assertNoScopeField(input.modelPattern, '客户端层请求错误策略不能绑定模型')
    const clientProfile = normalizeRequiredText(input.clientProfile === undefined ? fallback?.clientProfile : input.clientProfile, '客户端标识', 80)
    return { protocolCode, clientProfile }
  }
  const providerCode = normalizeOptionalProviderCode(input.providerCode === undefined ? fallback?.providerCode : input.providerCode, '供应商编码')
  const modelPattern = normalizeRequiredText(input.modelPattern === undefined ? fallback?.modelPattern : input.modelPattern, '模型匹配值', 120)
  const modelMatchType = normalizeOptionalModelMatchType(input.modelMatchType === undefined ? fallback?.modelMatchType : input.modelMatchType, 'prefix') ?? 'prefix'
  if (providerCode && protocolCode === OPENAI_PROTOCOL_CODE && !isOpenAIProtocolProviderCode(providerCode)) {
    throw new Error('模型层请求错误策略绑定供应商时，只能选择 OpenAI v1 协议下已启用的供应商')
  }
  assertNoScopeField(input.clientProfile, '模型层请求错误策略不能绑定客户端')
  return { protocolCode, providerCode, modelPattern, modelMatchType }
}

function normalizeRecoveryFields(
  input: ErrorPolicyInput,
  action: ErrorPolicyAction,
  fallback?: ErrorPolicySummary
): Pick<ErrorPolicySummary, 'resetStrategy' | 'durationHours' | 'dailyResetHour' | 'weeklyResetDay' | 'weeklyResetHour'> {
  if (action !== 'rate_limited') {
    return {}
  }
  const resetStrategy = normalizeRequiredRecoveryStrategy(input.resetStrategy === undefined ? fallback?.resetStrategy : input.resetStrategy)
  if (resetStrategy === 'duration') {
    return {
      resetStrategy,
      durationHours: normalizeRequiredPositiveInt(input.durationHours === undefined ? fallback?.durationHours : input.durationHours, '恢复小时数')
    }
  }
  if (resetStrategy === 'weekly') {
    return {
      resetStrategy,
      weeklyResetDay: normalizeRequiredWeekday(input.weeklyResetDay === undefined ? fallback?.weeklyResetDay : input.weeklyResetDay, '每周恢复日'),
      weeklyResetHour: normalizeRequiredHour(input.weeklyResetHour === undefined ? fallback?.weeklyResetHour : input.weeklyResetHour, '每周恢复时间')
    }
  }
  return {
    resetStrategy,
    dailyResetHour: normalizeRequiredHour(input.dailyResetHour === undefined ? fallback?.dailyResetHour : input.dailyResetHour, '每天恢复时间')
  }
}

function normalizeMatch(value: unknown): ErrorPolicyMatch {
  const record = objectValue(value)
  if (!record) {
    throw new Error('请求错误策略匹配条件必须是对象')
  }
  assertOnlyKeys(record, errorPolicyMatchKeys, '请求错误策略匹配条件')
  const match = {
    statusCodes: normalizeStatusCodes(record.statusCodes),
    errorCodes: normalizeTextList(record.errorCodes, 50, 120, '错误码'),
    errorTypes: normalizeTextList(record.errorTypes, 50, 120, '错误类型'),
    keywords: normalizeTextList(record.keywords, 50, 200, '关键词')
  }
  const hasMatcher = [
    match.statusCodes,
    match.errorCodes,
    match.errorTypes,
    match.keywords
  ].some((items) => Array.isArray(items) && items.length > 0)
  if (!hasMatcher) {
    throw new Error('至少需要填写一个匹配条件')
  }
  return match
}

function normalizeStatusCodes(value: unknown): number[] | undefined {
  if (value === undefined) return undefined
  if (!Array.isArray(value)) {
    throw new Error('状态码必须是数字数组')
  }
  if (value.length > 50) {
    throw new Error('状态码不能超过 50 项')
  }
  const seen = new Set<number>()
  const output: number[] = []
  for (const item of value) {
    if (typeof item !== 'number' || !Number.isInteger(item) || item < 100 || item > 599) {
      throw new Error('状态码必须是 100 到 599 之间的整数')
    }
    if (item >= 200 && item <= 299) {
      throw new Error('请求错误策略不能匹配 2xx 成功状态码')
    }
    if (seen.has(item)) {
      throw new Error('状态码不能重复')
    }
    seen.add(item)
    output.push(item)
  }
  return output.length ? output : undefined
}

function normalizeTextList(value: unknown, maxItems: number, maxLength: number, label: string): string[] | undefined {
  if (value === undefined) return undefined
  if (!Array.isArray(value)) {
    throw new Error(`${label}必须是字符串数组`)
  }
  if (value.length > maxItems) {
    throw new Error(`${label}不能超过 ${maxItems} 项`)
  }
  const seen = new Set<string>()
  const output: string[] = []
  for (const item of value) {
    if (typeof item !== 'string') {
      throw new Error(`${label}必须是字符串数组`)
    }
    const text = item.trim()
    if (!text) {
      throw new Error(`${label}不能为空`)
    }
    if (text.length > maxLength) {
      throw new Error(`${label}单项不能超过 ${maxLength} 个字符`)
    }
    if (/^\d+$/.test(text) && Number(text) >= 200 && Number(text) <= 299) {
      throw new Error(`${label}不能填写 2xx 成功码，例如 200`)
    }
    if (seen.has(text)) {
      throw new Error(`${label}不能重复`)
    }
    seen.add(text)
    output.push(text)
  }
  return output.length ? output : undefined
}

function normalizeScopeType(value: unknown, fallback?: ErrorPolicyScopeType): ErrorPolicyScopeType {
  if (value === undefined) {
    if (fallback) return fallback
    throw new Error('请求错误策略作用层级不能为空')
  }
  if (value === 'global' || value === 'protocol' || value === 'provider' || value === 'client' || value === 'model') {
    return value
  }
  throw new Error('请求错误策略作用层级无效')
}

function normalizePolicyAction(value: unknown): ErrorPolicyAction {
  if (policyActions.has(value as ErrorPolicyAction)) {
    return value as ErrorPolicyAction
  }
  throw new Error('请求错误策略动作无效')
}

function normalizePolicyName(value: unknown, fallback?: string): string {
  return normalizeRequiredText(value === undefined ? fallback : value, '策略名称', 100)
}

function normalizeProtocolCode(value: unknown): string {
  return normalizeRequiredText(value, '协议编码', 80).toLowerCase()
}

function normalizeOptionalProtocolCode(value: unknown, label: string): string | undefined {
  return normalizeOptionalText(value, label, 80)?.toLowerCase()
}

function normalizeRequiredProviderCode(value: unknown, protocolCode: string): string {
  const providerCode = normalizeRequiredText(value, '供应商编码', 80).toLowerCase()
  if (protocolCode === OPENAI_PROTOCOL_CODE && !isOpenAIProtocolProviderCode(providerCode)) {
    throw new Error('供应商层请求错误策略只能选择 OpenAI v1 协议下已启用的供应商')
  }
  return providerCode
}

function normalizeOptionalProviderCode(value: unknown, label: string): string | undefined {
  return normalizeOptionalText(value, label, 80)?.toLowerCase()
}

function normalizeRequiredText(value: unknown, label: string, maxLength: number): string {
  const text = normalizeOptionalText(value, label, maxLength)
  if (!text) {
    throw new Error(`${label}不能为空`)
  }
  return text
}

function normalizeOptionalText(value: unknown, label: string, maxLength: number): string | undefined {
  if (value === undefined || value === null) return undefined
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

function assertNoScopeField(value: unknown, message: string): void {
  if (normalizeOptionalText(value, '作用层级字段', 120)) {
    throw new Error(message)
  }
}

function normalizePriority(value: unknown, fallback = 1): number {
  if (value === undefined) return fallback
  if (typeof value !== 'number' || !Number.isInteger(value)) {
    throw new Error('请求错误策略优先级必须是整数')
  }
  if (value < 1 || value > 9999) {
    throw new Error('请求错误策略优先级必须在 1 到 9999 之间')
  }
  return value
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
  return normalizeOptionalText(value, label, maxLength)
}

function normalizeOptionalModelMatchType(value: unknown, fallback?: ErrorPolicyModelMatchType): ErrorPolicyModelMatchType | undefined {
  if (value === undefined || value === null) return fallback
  if (value === 'exact' || value === 'prefix' || value === 'contains') return value
  throw new Error('模型匹配方式无效')
}

function normalizeRequiredRecoveryStrategy(value: unknown): ErrorPolicyRecoveryStrategy {
  const strategy = normalizeOptionalRecoveryStrategy(value, undefined)
  if (!strategy) {
    throw new Error('限流策略必须选择恢复策略')
  }
  return strategy
}

function normalizeOptionalRecoveryStrategy(value: unknown, fallback?: ErrorPolicyRecoveryStrategy): ErrorPolicyRecoveryStrategy | undefined {
  if (value === undefined || value === null) return fallback
  if (value === 'duration' || value === 'daily' || value === 'weekly') return value
  throw new Error('恢复策略无效')
}

function normalizeRequiredPositiveInt(value: unknown, label: string): number {
  const numberValue = normalizeOptionalPositiveInt(value, undefined, label)
  if (numberValue === undefined) throw new Error(`${label}必须是大于 0 的整数`)
  return numberValue
}

function normalizeOptionalPositiveInt(value: unknown, fallback: number | undefined, label: string): number | undefined {
  if (value === undefined || value === null) return fallback
  if (typeof value !== 'number' || !Number.isInteger(value) || value <= 0) {
    throw new Error(`${label}必须是大于 0 的整数`)
  }
  return value
}

function normalizeRequiredHour(value: unknown, label: string): number {
  const numberValue = normalizeOptionalHour(value, undefined, label)
  if (numberValue === undefined) throw new Error(`${label}必须是 0-23 的整数`)
  return numberValue
}

function normalizeOptionalHour(value: unknown, fallback: number | undefined, label: string): number | undefined {
  if (value === undefined || value === null) return fallback
  if (typeof value !== 'number' || !Number.isInteger(value) || value < 0 || value > 23) {
    throw new Error(`${label}必须是 0-23 的整数`)
  }
  return value
}

function normalizeRequiredWeekday(value: unknown, label: string): number {
  const numberValue = normalizeOptionalWeekday(value, undefined, label)
  if (numberValue === undefined) throw new Error(`${label}必须是 0-6 的整数`)
  return numberValue
}

function normalizeOptionalWeekday(value: unknown, fallback: number | undefined, label: string): number | undefined {
  if (value === undefined || value === null) return fallback
  if (typeof value !== 'number' || !Number.isInteger(value) || value < 0 || value > 6) {
    throw new Error(`${label}必须是 0-6 的整数`)
  }
  return value
}

function parseJsonObject(value: string): Record<string, unknown> | undefined {
  const parsed = JSON.parse(value) as unknown
  const object = objectValue(parsed)
  if (!object) {
    throw new Error('请求错误策略 match_json 必须是对象')
  }
  return object
}

function objectValue(value: unknown): Record<string, unknown> | undefined {
  return typeof value === 'object' && value !== null && !Array.isArray(value)
    ? value as Record<string, unknown>
    : undefined
}

function assertKnownInputKeys(input: ErrorPolicyInput, allowedKeys: ReadonlySet<string>, label: string): void {
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
