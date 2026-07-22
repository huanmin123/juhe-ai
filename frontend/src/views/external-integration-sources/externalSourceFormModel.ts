import type { Dayjs } from 'dayjs'

import { formatServerDateTimeInput, parseStrictDatePickerValue } from '@/shared/formatters'
import type {
  ExternalIntegrationRateLimitRule,
  ExternalIntegrationScopeOption,
  ExternalIntegrationSourcePayload,
  ExternalIntegrationSourceStatus,
  ExternalIntegrationSourceSummary
} from '@/types/domain'

const defaultPublicScope = 'juhe_ai_public:group_list:read'

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

export function createEmptySourceForm(scopeOptions: ExternalIntegrationScopeOption[] = []): ExternalSourceForm {
  return {
    name: '',
    status: 'active',
    scopes: defaultCreateSourceScopes(scopeOptions),
    rateLimits: [],
    expiresAt: null,
    notes: ''
  }
}

export function createSourceFormFromRecord(record: ExternalIntegrationSourceSummary): ExternalSourceForm {
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

function defaultCreateSourceScopes(scopeOptions: ExternalIntegrationScopeOption[]): string[] {
  return scopeOptions.some((item) => item.value === defaultPublicScope)
    ? [defaultPublicScope]
    : []
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
