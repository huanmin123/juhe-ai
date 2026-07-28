import type { Dayjs } from 'dayjs'

import { formatServerDateTimeInput, parseStrictDatePickerValue } from '@/shared/formatters'
import type {
  ExternalIntegrationRateLimitRule,
  ExternalIntegrationScopeOption,
  ExternalIntegrationSourceListItem,
  ExternalIntegrationSourcePayload,
  ExternalIntegrationSourceStatus
} from '@/types/domain'

const defaultPublicScope = 'juhe_ai_public:group_list:read'

export const DEFAULT_EXTERNAL_INTEGRATION_SCOPE_OPTIONS: ExternalIntegrationScopeOption[] = [
  { value: 'juhe_ai_public:api_key_list:read', label: 'GET API Key 列表' },
  { value: 'juhe_ai_public:route_strategy_list:read', label: 'GET 路由策略列表' },
  { value: defaultPublicScope, label: 'GET 分组列表' },
  { value: 'juhe_ai_public:account_list:read', label: 'GET 账号列表' },
  { value: 'juhe_ai_public:api_key_add:write', label: 'POST API Key 新增' },
  { value: 'juhe_ai_public:api_key_update:write', label: 'POST API Key 修改' },
  { value: 'juhe_ai_public:api_key_delete:write', label: 'POST API Key 删除' },
  { value: 'juhe_ai_public:route_strategy_add:write', label: 'POST 路由策略新增' },
  { value: 'juhe_ai_public:route_strategy_update:write', label: 'POST 路由策略修改' },
  { value: 'juhe_ai_public:route_strategy_delete:write', label: 'POST 路由策略删除' },
  { value: 'juhe_ai_public:group_add:write', label: 'POST 分组新增' },
  { value: 'juhe_ai_public:group_update:write', label: 'POST 分组修改' },
  { value: 'juhe_ai_public:group_delete:write', label: 'POST 分组删除' },
  { value: 'juhe_ai_public:account_add:write', label: 'POST 账号新增' },
  { value: 'juhe_ai_public:account_update:write', label: 'POST 账号修改' },
  { value: 'juhe_ai_public:account_delete:write', label: 'POST 账号删除' }
]

export const DEFAULT_EXTERNAL_INTEGRATION_SELECTED_SCOPES = [defaultPublicScope] as const

export interface ExternalSourceForm {
  name: string
  status: ExternalIntegrationSourceStatus
  scopes: string[]
  rateLimits: ExternalIntegrationRateLimitRule[]
  expiresAt: Dayjs | null
  notes: string
}

export const externalSourceStatusOptions: Array<{ label: string; value: ExternalIntegrationSourceStatus }> = [
  { label: '启用', value: 'active' },
  { label: '停用', value: 'disabled' }
]

export function createEmptySourceForm(): ExternalSourceForm {
  return {
    name: '',
    status: 'active',
    scopes: [...DEFAULT_EXTERNAL_INTEGRATION_SELECTED_SCOPES],
    rateLimits: [],
    expiresAt: null,
    notes: ''
  }
}

export function createSourceFormFromRecord(record: ExternalIntegrationSourceListItem): ExternalSourceForm {
  return {
    name: record.name,
    status: record.status,
    scopes: [...record.scopes],
    rateLimits: normalizeRateLimits(record.rateLimits),
    expiresAt: parseStrictDatePickerValue(record.expiresAt, '来源授权过期时间') ?? null,
    notes: record.notes ?? ''
  }
}

export function buildSourcePayload(form: ExternalSourceForm): ExternalIntegrationSourcePayload {
  return {
    name: form.name.trim(),
    status: form.status,
    scopes: [...form.scopes],
    rateLimits: normalizeRateLimits(form.rateLimits),
    expiresAt: formatServerDateTimeInput(form.expiresAt),
    notes: form.notes.trim() || null
  }
}

export function createDefaultRateLimit(): ExternalIntegrationRateLimitRule {
  return { windowSeconds: 60, maxRequests: 10 }
}

export function normalizeRateLimits(rules: ExternalIntegrationRateLimitRule[]): ExternalIntegrationRateLimitRule[] {
  return rules.map((rule, index) => ({
    windowSeconds: normalizeRateLimitInteger(rule.windowSeconds, 1, 86400, `第 ${index + 1} 条限频窗口`),
    maxRequests: normalizeRateLimitInteger(rule.maxRequests, 1, 100000, `第 ${index + 1} 条限频次数`)
  }))
}

export function formatRateLimits(rules: ExternalIntegrationRateLimitRule[]): string {
  return rules.length ? rules.map((rule) => `${rule.windowSeconds}s/${rule.maxRequests}次`).join('，') : '不限制'
}

function normalizeRateLimitInteger(value: unknown, min: number, max: number, label: string): number {
  if (typeof value !== 'number' || !Number.isInteger(value)) {
    throw new Error(`${label}必须是整数`)
  }
  if (value < min || value > max) {
    throw new Error(`${label}必须在 ${min} 到 ${max} 之间`)
  }
  return value
}
