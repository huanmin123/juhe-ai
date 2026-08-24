import {
  normalizeAccountErrorHandlingRules,
  type AccountErrorHandlingRule
} from './account-error-policy-validation.js'

export const SYSTEM_INSUFFICIENT_QUOTA_ERROR_POLICY_RULE_ID = 'system.upstream_insufficient_quota'

export type AccountErrorPolicyRuleSource = 'system' | 'account'

export interface EffectiveAccountErrorHandlingRule extends AccountErrorHandlingRule {
  id: string
  source: AccountErrorPolicyRuleSource
  inherited: boolean
  editable: boolean
}

/**
 * This registry is intentionally code-defined.  It is never persisted into an
 * account credential or system setting, so upgrades take effect consistently
 * for every account without altering customer configuration.
 */
const systemRules: readonly EffectiveAccountErrorHandlingRule[] = [
  {
    id: SYSTEM_INSUFFICIENT_QUOTA_ERROR_POLICY_RULE_ID,
    source: 'system',
    inherited: true,
    editable: false,
    enabled: true,
    name: '上游额度不足',
    priority: 1,
    action: 'rate_limited',
    reset_strategy: 'daily',
    daily_reset_hour: 0,
    status_codes: [403],
    error_codes: [
      'insufficient_user_quota',
      'insufficient_quota',
      'insufficient_balance',
      'quota_exceeded',
      'quota_exhausted',
      'wallet_balance_exhausted',
      'pre_consume_token_quota_failed'
    ],
    keywords: [
      '余额不足',
      '额度不足',
      'insufficient balance',
      'insufficient quota',
      'credit balance too low',
      'wallet balance exhausted'
    ],
    description: '仅在 HTTP 403 且明确额度不足时进入限流中；支持该语义的 API Key 供应商 explicit reset 优先，无可靠时间时按账户策略稳定错峰复测；OAuth / Google OAuth 不消费 API Key reset 字段，默认 UTC daily 并支持账户策略调整。'
  }
]

const insufficientQuotaStableCodes = new Set(
  systemRules[0]!.error_codes!.map(normalizeErrorIdentifier)
)

const insufficientQuotaTextMarkers = systemRules[0]!.keywords!.map((value) => value.toLowerCase())

const nonQuota403ErrorIdentifiers = new Set([
  'content_policy_violation',
  'content_policy_blocked',
  'prompt_guard_blocked',
  'client_restricted',
  'permission_denied',
  'access_denied',
  'forbidden'
])

export function systemAccountErrorHandlingRules(): EffectiveAccountErrorHandlingRule[] {
  return systemRules.map(cloneEffectiveRule)
}

export function effectiveAccountErrorHandlingRules(value: unknown): EffectiveAccountErrorHandlingRule[] {
  const accountRules = normalizeAccountErrorHandlingRules(value)
    .sort((left, right) => left.priority - right.priority)
    .map((rule, index) => ({
      ...cloneRule(rule),
      id: `account.${index + 1}`,
      source: 'account' as const,
      inherited: false,
      editable: true
    }))
  return [...systemAccountErrorHandlingRules(), ...accountRules]
}

/**
 * The system rule is deliberately stricter than generic account-rule
 * matching: its code markers and high-confidence text markers are
 * alternatives, rather than requiring both configured lists to match.
 */
export function systemInsufficientQuotaRuleMatches(input: {
  statusCode: number
  errorCode?: string
  errorType?: string
  searchableText?: string
}): boolean {
  if (input.statusCode !== 403) return false
  const errorCode = normalizeErrorIdentifier(input.errorCode)
  const errorType = normalizeErrorIdentifier(input.errorType)
  if (nonQuota403ErrorIdentifiers.has(errorCode) || nonQuota403ErrorIdentifiers.has(errorType)) {
    return false
  }
  if (insufficientQuotaStableCodes.has(errorCode) || insufficientQuotaStableCodes.has(errorType)) {
    return true
  }
  const text = input.searchableText?.toLowerCase() ?? ''
  if ([...nonQuota403ErrorIdentifiers].some((identifier) => text.includes(identifier.replaceAll('_', ' ')) || text.includes(identifier))) {
    return false
  }
  return insufficientQuotaTextMarkers.some((marker) => text.includes(marker))
}

function cloneEffectiveRule(rule: EffectiveAccountErrorHandlingRule): EffectiveAccountErrorHandlingRule {
  return {
    ...cloneRule(rule),
    id: rule.id,
    source: rule.source,
    inherited: rule.inherited,
    editable: rule.editable
  }
}

function cloneRule(rule: AccountErrorHandlingRule): AccountErrorHandlingRule {
  const copied = Object.fromEntries(
    Object.entries(rule).filter(([, value]) => value !== undefined)
  ) as AccountErrorHandlingRule
  return {
    ...copied,
    ...(rule.status_codes ? { status_codes: [...rule.status_codes] } : {}),
    ...(rule.error_codes ? { error_codes: [...rule.error_codes] } : {}),
    ...(rule.error_types ? { error_types: [...rule.error_types] } : {}),
    ...(rule.keywords ? { keywords: [...rule.keywords] } : {})
  }
}

function normalizeErrorIdentifier(value: string | undefined): string {
  return (value ?? '').trim().toLowerCase().replace(/[\s-]+/g, '_')
}
