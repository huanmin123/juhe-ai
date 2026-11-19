import type {
  ApiKeyAvailabilitySchedule,
  ApiKeyMutationResult,
  ApiKeyQuotaLimits,
  ApiKeySummary
} from '@/types/domain'
import type { ApiKeyMutationPayload } from '@/api/domains/apiKeys'
import { BUILTIN_SECURITY_POLICY_ID } from '@/api/domains/securityPolicies'
import { SCHEDULING_PREFERENCE_BUILTIN_CHAT_COST_FIRST_ID } from '@/api/domains/schedulingPreferences'

/** 单 IP 并发限制契约上限（API Key 单 IP 并发限制设计 §2.1）。 */
export const apiKeyClientIpConcurrencyLimitMax = 1_000_000

export function isValidApiKeyClientIpConcurrencyLimit(value: unknown): value is number {
  return typeof value === 'number'
    && Number.isInteger(value)
    && value >= 0
    && value <= apiKeyClientIpConcurrencyLimitMax
}

export interface ApiKeyEditableSnapshot {
  name: string
  routeStrategyId?: string
  /** 绑定调度偏好；undefined 表示历史「沿用策略路由（迁移期）」或 purpose=chat，delta 中清空时回内置 ID。 */
  schedulingPreferenceId?: string
  /** 绑定安全策略；undefined 归一为内置「默认安全策略」（空规则 = 原样转发）。 */
  securityPolicyId?: string
  /** 单 IP 并发上限：0 = 不限制；1..1000000 生效，超限立即返回 429。 */
  clientIpConcurrencyLimit: number
  status: 'active' | 'disabled'
  expiresAt: string | null
  description: string
  quotaLimits: ApiKeyQuotaLimits
  availabilitySchedule: ApiKeyAvailabilitySchedule | null
}

export function buildApiKeyCreatePayload(
  snapshot: ApiKeyEditableSnapshot,
  options: { routeStrategyTouched: boolean }
): ApiKeyMutationPayload {
  const description = snapshot.description.trim()
  return {
    name: snapshot.name,
    ...(options.routeStrategyTouched && snapshot.routeStrategyId
      ? { routeStrategyId: snapshot.routeStrategyId }
      : {}),
    // 契约要求创建总是提交显式偏好 ID：预选内置行（显式默认）或用户选择的偏好。
    schedulingPreferenceId: snapshot.schedulingPreferenceId || SCHEDULING_PREFERENCE_BUILTIN_CHAT_COST_FIRST_ID,
    // 安全策略无迁移期：创建缺省内置 ID（null = 默认策略），未绑定与显式默认同义。
    securityPolicyId: snapshot.securityPolicyId || BUILTIN_SECURITY_POLICY_ID,
    // 单 IP 并发限制创建恒发归一化整数：默认 0（关闭）也显式提交，避免后端缺省路径差异。
    clientIpConcurrencyLimit: isValidApiKeyClientIpConcurrencyLimit(snapshot.clientIpConcurrencyLimit)
      ? snapshot.clientIpConcurrencyLimit
      : 0,
    ...(snapshot.status === 'disabled' ? { status: snapshot.status } : {}),
    ...(snapshot.expiresAt ? { expiresAt: snapshot.expiresAt } : {}),
    ...(description ? { description } : {}),
    ...(Object.keys(snapshot.quotaLimits).length ? { quotaLimits: snapshot.quotaLimits } : {}),
    ...(snapshot.availabilitySchedule ? { availabilitySchedule: snapshot.availabilitySchedule } : {})
  }
}

const editableFields = [
  'name',
  'routeStrategyId',
  'schedulingPreferenceId',
  'securityPolicyId',
  'clientIpConcurrencyLimit',
  'status',
  'expiresAt',
  'description',
  'quotaLimits',
  'availabilitySchedule'
] as const

const rowPatchFields = [
  'revision',
  'name',
  'description',
  'keyPrefix',
  'keySuffix',
  'status',
  'routeStrategyId',
  'routeStrategyName',
  'routeStrategyMode',
  'routeStrategyStatus',
  'schedulingPreferenceId',
  'schedulingPreferenceName',
  'schedulingPreferenceBuiltIn',
  'securityPolicyId',
  'securityPolicyName',
  'securityPolicyBuiltIn',
  'clientIpConcurrencyLimit',
  'expiresAt',
  'quotaLimits',
  'availabilitySchedule'
] as const

export function buildApiKeyMutationPatch(
  baseline: ApiKeyEditableSnapshot,
  current: ApiKeyEditableSnapshot
): ApiKeyMutationPayload {
  const patch: ApiKeyMutationPayload = {}
  const writablePatch = patch as Record<string, unknown>
  for (const field of editableFields) {
    if (stableValue(baseline[field]) === stableValue(current[field])) continue
    // 偏好清空不是「缺失保持原值」：契约要求显式回默认（内置 ID），避免空串/undefined 被丢弃后沿用旧绑定。
    writablePatch[field] = field === 'schedulingPreferenceId'
      ? (current.schedulingPreferenceId || SCHEDULING_PREFERENCE_BUILTIN_CHAT_COST_FIRST_ID)
      : field === 'securityPolicyId'
        ? (current.securityPolicyId || BUILTIN_SECURITY_POLICY_ID)
        : current[field]
  }
  return patch
}

export function hasApiKeyMutationChanges(patch: ApiKeyMutationPayload): boolean {
  return editableFields.some((field) => Object.prototype.hasOwnProperty.call(patch, field))
}

export function mergeApiKeyMutationResult(
  current: ApiKeySummary,
  result: ApiKeyMutationResult
): ApiKeySummary {
  if (current.id !== result.id) return current
  const next = { ...current, revision: result.revision }
  const writableNext = next as unknown as Record<string, unknown>
  const rowPatch = result.rowPatch as unknown as Record<string, unknown>
  for (const field of rowPatchFields) {
    if (!Object.prototype.hasOwnProperty.call(rowPatch, field)) continue
    const value = rowPatch[field]
    if (value === undefined) continue
    if (value === null && (field === 'description' || field === 'expiresAt' || field === 'availabilitySchedule'
      || field === 'schedulingPreferenceId' || field === 'schedulingPreferenceName' || field === 'schedulingPreferenceBuiltIn'
      || field === 'securityPolicyId' || field === 'securityPolicyName' || field === 'securityPolicyBuiltIn')) {
      delete writableNext[field]
      continue
    }
    writableNext[field] = value
  }
  next.revision = result.revision
  return next
}

function stableValue(value: unknown): string {
  if (value === undefined) return 'undefined'
  if (value === null || typeof value !== 'object') return JSON.stringify(value) ?? String(value)
  if (Array.isArray(value)) return `[${value.map(stableValue).join(',')}]`
  const record = value as Record<string, unknown>
  return `{${Object.keys(record).sort().map((key) => `${JSON.stringify(key)}:${stableValue(record[key])}`).join(',')}}`
}
