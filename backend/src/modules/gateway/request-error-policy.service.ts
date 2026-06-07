import type { AccountStatus } from '../../domain/types.js'
import { runtimeConfig } from '../../config/runtime.js'
import {
  clearAccountFailureStateResult,
  clearAuthorizedAccountBindingFailureStateByContext,
  getSettings,
  markAccountCooldown,
  markAccountTemporaryUnavailable,
  markAccountDisabledByFailure,
  markAuthorizedAccountBindingCooldownByContext,
  markAuthorizedAccountBindingTemporaryUnavailableByContext,
  markAuthorizedAccountBindingDisabledByFailure,
  type AuthorizedAccountBindingRuntimeTarget
} from '../../storage/repositories.js'
import type { OpenAIGatewayTrafficSource } from './openai-gateway-traffic-source.js'
import { sanitizeDiagnosticPayload } from './payload-sanitizer.js'
import type { ErrorPolicyAction, ErrorPolicySummary } from '../../storage/error-policy.repository.js'

export type CooldownAccountStatus = 'rate_limited' | 'temporary_unavailable'

export interface GatewaySettings {
  defaultTemporaryUnschedulableMinutes: number
  temporaryUnschedulableRetryIntervalSeconds: number
  temporaryUnschedulableRetryAttempts: number
  streamCircuitBreakerEnabled: boolean
  streamRequestTimeoutSeconds: number
  streamIdleTimeoutSeconds: number
  streamFailureThresholdCount: number
  streamFailureThresholdWindowMinutes: number
}

export interface RequestErrorPolicyAccount {
  id: string
  providerCode: string
  type?: string
  credentials: Record<string, unknown>
  accountAccessType?: 'owner' | 'account_authorized' | 'group_authorized'
  bindingSystemAccountId?: string
  groupOwnerSystemAccountId?: string
  boundGroupId?: string
  accountAuthorizationId?: string
  status?: AccountStatus
  cooldownUntil?: string
  lastErrorMessage?: string
  streamFailureCount?: number
  streamFailureWindowStartedAt?: string
}

export interface RequestErrorPolicyDecision {
  action: 'retry_next' | 'cooldown' | 'disable'
  ruleName?: string
  cooldownUntil?: string
  cooldownStatus?: CooldownAccountStatus
}

export interface AccountErrorHandlingResult {
  action: 'none' | 'retry_next' | 'cooldown' | 'disable'
  changed: boolean
  accountStatus?: AccountStatus
  reason?: string
}

export interface GatewayErrorPolicyRuntimeContext {
  protocolCode?: string
  providerCode?: string
  clientProfile?: string
  model?: string
}

export function readGatewaySettings(): GatewaySettings {
  assertLocalGatewayDatabaseAccess('readGatewaySettings')
  const settings = getSettings()
  return {
    defaultTemporaryUnschedulableMinutes: numberSetting(settings.defaultTemporaryUnschedulableMinutes, 'defaultTemporaryUnschedulableMinutes', 1, 1440),
    temporaryUnschedulableRetryIntervalSeconds: numberSetting(settings.temporaryUnschedulableRetryIntervalSeconds, 'temporaryUnschedulableRetryIntervalSeconds', 0, 3600),
    temporaryUnschedulableRetryAttempts: numberSetting(settings.temporaryUnschedulableRetryAttempts, 'temporaryUnschedulableRetryAttempts', 0, 10),
    streamCircuitBreakerEnabled: booleanSetting(settings.streamCircuitBreakerEnabled, 'streamCircuitBreakerEnabled'),
    streamRequestTimeoutSeconds: numberSetting(settings.streamRequestTimeoutSeconds, 'streamRequestTimeoutSeconds', 10, 3600),
    streamIdleTimeoutSeconds: numberSetting(settings.streamIdleTimeoutSeconds, 'streamIdleTimeoutSeconds', 1, 3600),
    streamFailureThresholdCount: numberSetting(settings.streamFailureThresholdCount, 'streamFailureThresholdCount', 1, 100),
    streamFailureThresholdWindowMinutes: numberSetting(settings.streamFailureThresholdWindowMinutes, 'streamFailureThresholdWindowMinutes', 1, 1440)
  }
}

export function applyAccountErrorHandling(
  account: RequestErrorPolicyAccount,
  input: {
    success: boolean
    statusCode?: number
    headers?: Headers | Record<string, string | string[]>
    bodyText?: string
    errorMessage?: string
    settings?: GatewaySettings
    trafficSource?: OpenAIGatewayTrafficSource
    errorPolicies?: ErrorPolicySummary[]
    errorPolicyContext?: GatewayErrorPolicyRuntimeContext
  }
): AccountErrorHandlingResult {
  assertLocalGatewayDatabaseAccess('applyAccountErrorHandling')
  if (account.status === 'disabled' || account.status === 'error') {
    return { action: 'none', changed: false, accountStatus: account.status }
  }

  if (input.success) {
    if (account.status === 'rate_limited' && input.trafficSource === 'gateway') {
      return { action: 'none', changed: false, accountStatus: account.status }
    }
    const shouldClear = (account.status !== undefined && account.status !== 'active')
      || Boolean(account.cooldownUntil)
      || Boolean(account.lastErrorMessage)
      || Boolean(account.streamFailureCount)
      || Boolean(account.streamFailureWindowStartedAt)
    if (!shouldClear) {
      return { action: 'none', changed: false, accountStatus: account.status }
    }
    const authorizedTarget = authorizedAccountBindingRuntimeTarget(account)
    const result = authorizedTarget
      ? clearAuthorizedAccountBindingFailureStateByContext(authorizedTarget, { allowErrorRestore: false })
      : clearAccountFailureStateResult(account.id, undefined, { allowErrorRestore: false })
    return { action: 'none', changed: result.changed, accountStatus: result.account?.status ?? account.status }
  }

  const settings = input.settings ?? readGatewaySettings()
  const statusCode = input.statusCode
  const bodyText = input.bodyText ?? input.errorMessage ?? ''
  const headers = normalizeHeadersInput(input.headers)
  const upstreamSummary = requestErrorPolicyUpstreamSummary(bodyText, headers)

  if (statusCode !== undefined) {
    const decision = decideRequestErrorPolicy(account, statusCode, headers, Buffer.from(bodyText), settings, {
      policies: input.errorPolicies,
      context: input.errorPolicyContext
    })
    if (decision && decision.action !== 'retry_next') {
      const updated = applyRequestErrorPolicySideEffect(account, statusCode, decision, settings, upstreamSummary)
      return {
        action: decision.action,
        changed: Boolean(updated),
        accountStatus: updated?.status,
        reason: requestErrorPolicyReason(statusCode, decision, upstreamSummary)
      }
    }

    const reason = genericUpstreamResponseFailureReason(statusCode, upstreamSummary)
    const updated = applyAccountTemporaryUnavailableSideEffect(account, reason)
    return {
      action: 'cooldown',
      changed: Boolean(updated),
      accountStatus: updated?.status ?? account.status,
      reason
    }
  }

  const reason = genericUpstreamRequestFailureReason(input.errorMessage ?? bodyText)
  const updated = applyAccountTemporaryUnavailableSideEffect(account, reason)
  return {
    action: 'cooldown',
    changed: Boolean(updated),
    accountStatus: updated?.status ?? account.status,
    reason
  }
}

export function decideRequestErrorPolicy(
  account: RequestErrorPolicyAccount,
  statusCode: number,
  headers: Headers,
  body: Buffer,
  _settings: GatewaySettings,
  options: {
    policies?: ErrorPolicySummary[]
    context?: GatewayErrorPolicyRuntimeContext
  } = {}
): RequestErrorPolicyDecision | undefined {
  if (statusCode >= 200 && statusCode <= 299) {
    return undefined
  }
  const bodyText = body.toString('utf8')
  const errorPayload = parseErrorPayload(bodyText, headers)

  const rules = runtimeErrorPolicyRules(options.policies ?? [], account, options.context)

  for (const rule of rules
    .filter((item) => item.enabled)
    .sort((left, right) => scopeSpecificity(right) - scopeSpecificity(left) || left.priority - right.priority || left.id.localeCompare(right.id))) {
    if (!hasErrorPolicyRuleMatcher(rule) || !matchesErrorPolicyRule(rule, statusCode, bodyText, errorPayload)) {
      continue
    }
    const action = normalizePolicyAction(rule.action)
    const ruleName = rule.name
    if (action === 'cooldown') {
      const cooldownStatus = policyCooldownStatus(rule.action)
      return {
        action,
        ruleName,
        cooldownUntil: cooldownStatus === 'rate_limited' ? resolveRequestErrorRuleCooldownUntil(rule) : undefined,
        cooldownStatus
      }
    }
    return { action, ruleName }
  }

  return undefined
}

export function applyRequestErrorPolicySideEffect(
  account: RequestErrorPolicyAccount,
  statusCode: number,
  decision: RequestErrorPolicyDecision,
  settings: GatewaySettings,
  upstreamSummary?: string
): { status: AccountStatus } | undefined {
  const reason = requestErrorPolicyReason(statusCode, decision, upstreamSummary)
  if (decision.action === 'cooldown') {
    return applyAccountCooldownSideEffect(account, settings, reason, {
      cooldownUntil: decision.cooldownUntil,
      cooldownStatus: decision.cooldownStatus
    })
  }
  if (decision.action === 'disable') {
    const authorizedTarget = authorizedAccountBindingRuntimeTarget(account)
    return authorizedTarget
      ? markAuthorizedAccountBindingDisabledByFailure({ ...authorizedTarget, reason })
      : markAccountDisabledByFailure(account.id, reason)
  }
  return undefined
}

function authorizedAccountBindingRuntimeTarget(account: RequestErrorPolicyAccount): AuthorizedAccountBindingRuntimeTarget | undefined {
  if (account.accountAccessType !== 'account_authorized') {
    return undefined
  }
  const systemAccountId = account.bindingSystemAccountId
  if (!systemAccountId || !account.boundGroupId || !account.accountAuthorizationId) {
    return undefined
  }
  return {
    accountId: account.id,
    systemAccountId,
    groupId: account.boundGroupId,
    accountAuthorizationId: account.accountAuthorizationId
  }
}

export function parseErrorPayload(text: string, headers: Headers): Record<string, unknown> {
  const trimmed = text.trim()
  if (!headers.get('content-type')?.includes('json') && !trimmed.startsWith('{')) return {}
  try {
    const payload = JSON.parse(trimmed) as Record<string, unknown>
    const error = typeof payload.error === 'object' && payload.error !== null ? payload.error as Record<string, unknown> : payload
    return {
      code: error.code ?? payload.code,
      type: error.type ?? payload.type,
      message: error.message ?? payload.message
    }
  } catch {
    return {}
  }
}

export function requestErrorPolicyReason(statusCode: number, decision: RequestErrorPolicyDecision, upstreamSummary?: string): string {
  const base = decision.ruleName
    ? '命中错误处理策略：' + decision.ruleName + '（HTTP ' + statusCode + '）'
    : '命中错误处理策略 HTTP ' + statusCode
  return upstreamSummary ? `${base}；${upstreamSummary}`.slice(0, 1000) : base
}

function requestErrorPolicyUpstreamSummary(bodyText: string, headers: Headers): string | undefined {
  const errorPayload = parseErrorPayload(bodyText, headers)
  const parts: string[] = []
  const code = stringValue(errorPayload.code)
  const message = stringValue(errorPayload.message)
  if (code) {
    parts.push(sanitizeDiagnosticPayload(code))
  }
  if (message && message !== code) {
    parts.push(sanitizeDiagnosticPayload(message))
  }
  return parts.length > 0 ? parts.join('；') : undefined
}

function stringValue(value: unknown): string {
  return typeof value === 'string' && value.trim() ? value.trim() : ''
}

function applyAccountTemporaryUnavailableSideEffect(
  account: RequestErrorPolicyAccount,
  reason: string
): { status: AccountStatus } | undefined {
  const authorizedTarget = authorizedAccountBindingRuntimeTarget(account)
  return authorizedTarget
    ? markAuthorizedAccountBindingTemporaryUnavailableByContext({ ...authorizedTarget, reason })
    : markAccountTemporaryUnavailable(account.id, reason)
}

function applyAccountCooldownSideEffect(
  account: RequestErrorPolicyAccount,
  settings: GatewaySettings,
  reason: string,
  input: {
    cooldownUntil?: string
    cooldownStatus?: CooldownAccountStatus
  } = {}
): { status: AccountStatus } | undefined {
  const status = input.cooldownStatus ?? 'temporary_unavailable'
  if (status === 'temporary_unavailable') {
    return applyAccountTemporaryUnavailableSideEffect(account, reason)
  }
  const until = input.cooldownUntil ?? new Date(Date.now() + Math.max(1, settings.defaultTemporaryUnschedulableMinutes) * 60_000).toISOString()
  const authorizedTarget = authorizedAccountBindingRuntimeTarget(account)
  return authorizedTarget
    ? markAuthorizedAccountBindingCooldownByContext({
        ...authorizedTarget,
        cooldownUntil: until,
        reason,
        status
      })
    : markAccountCooldown(account.id, until, reason, status)
}

function genericUpstreamResponseFailureReason(statusCode: number, upstreamSummary?: string): string {
  const base = `上游调用失败：HTTP ${statusCode}`
  return upstreamSummary ? `${base}；${upstreamSummary}`.slice(0, 1000) : base
}

function genericUpstreamRequestFailureReason(message: string): string {
  return `上游请求异常：${sanitizeDiagnosticPayload(message || '请求失败')}`.slice(0, 1000)
}

function resolveRequestErrorRuleCooldownUntil(rule: ErrorPolicySummary): string | undefined {
  if (policyCooldownStatus(rule.action) !== 'rate_limited') return undefined
  const now = new Date()
  if (rule.resetStrategy === 'duration') {
    return new Date(now.getTime() + rule.durationHours! * 60 * 60_000).toISOString()
  }
  if (rule.resetStrategy === 'weekly') {
    return nextWeeklyReset(now, rule.weeklyResetDay!, rule.weeklyResetHour!).toISOString()
  }
  return nextDailyReset(now, rule.dailyResetHour!).toISOString()
}

function nextDailyReset(now: Date, hour: number): Date {
  const next = new Date(now)
  next.setHours(hour, 0, 0, 0)
  if (next <= now) next.setDate(next.getDate() + 1)
  return next
}

function nextWeeklyReset(now: Date, weekday: number, hour: number): Date {
  const next = new Date(now)
  next.setHours(hour, 0, 0, 0)
  const delta = (weekday - next.getDay() + 7) % 7
  next.setDate(next.getDate() + delta)
  if (next <= now) next.setDate(next.getDate() + 7)
  return next
}

function runtimeErrorPolicyRules(
  policies: ErrorPolicySummary[],
  account: RequestErrorPolicyAccount,
  context?: GatewayErrorPolicyRuntimeContext
): ErrorPolicySummary[] {
  return policies.filter((policy) => policy.enabled && policyMatchesRuntimeContext(policy, account, context))
}

function policyMatchesRuntimeContext(
  policy: ErrorPolicySummary,
  account: RequestErrorPolicyAccount,
  context?: GatewayErrorPolicyRuntimeContext
): boolean {
  const protocolCode = normalizeComparable(context?.protocolCode)
  const providerCode = normalizeComparable(context?.providerCode ?? account.providerCode)
  if (policy.scopeType === 'global') return true
  if (normalizeComparable(policy.protocolCode) !== protocolCode) return false
  if (policy.scopeType === 'protocol') return true
  if (policy.scopeType === 'provider') {
    return normalizeComparable(policy.providerCode) === providerCode
  }
  if (policy.scopeType === 'client') {
    return normalizeComparable(policy.clientProfile) === normalizeComparable(context?.clientProfile)
  }
  if (policy.providerCode && normalizeComparable(policy.providerCode) !== providerCode) return false
  return modelPatternMatches(context?.model, policy.modelPattern, policy.modelMatchType)
}

function modelPatternMatches(model: string | undefined, pattern: string | undefined, matchType: ErrorPolicySummary['modelMatchType']): boolean {
  const modelText = normalizeComparable(model)
  const patternText = normalizeComparable(pattern)
  if (!modelText || !patternText) return false
  if (matchType === 'exact') return modelText === patternText
  if (matchType === 'contains') return modelText.includes(patternText)
  return modelText.startsWith(patternText)
}

function scopeSpecificity(policy: ErrorPolicySummary): number {
  if (policy.scopeType === 'model') return 5
  if (policy.scopeType === 'client') return 4
  if (policy.scopeType === 'provider') return 3
  if (policy.scopeType === 'protocol') return 2
  return 1
}

function errorPolicyRuleSpecs(rule: ErrorPolicySummary): {
  statusSpec: number[] | undefined
  keywordSpec: string[] | undefined
  codeSpec: string[] | undefined
  typeSpec: string[] | undefined
} {
  return {
    statusSpec: rule.match.statusCodes,
    keywordSpec: rule.match.keywords,
    codeSpec: rule.match.errorCodes,
    typeSpec: rule.match.errorTypes
  }
}

function hasErrorPolicyRuleMatcher(rule: ErrorPolicySummary): boolean {
  const { statusSpec, keywordSpec, codeSpec, typeSpec } = errorPolicyRuleSpecs(rule)
  return Boolean(statusSpec?.length || keywordSpec?.length || codeSpec?.length || typeSpec?.length)
}

function matchesErrorPolicyRule(rule: ErrorPolicySummary, statusCode: number, bodyText: string, errorPayload: Record<string, unknown>): boolean {
  const { statusSpec, keywordSpec, codeSpec, typeSpec } = errorPolicyRuleSpecs(rule)

  if (statusSpec !== undefined && !matchesStatusList(statusCode, statusSpec)) return false
  if (keywordSpec !== undefined && !matchesTextList(bodyText, keywordSpec)) return false
  if (codeSpec !== undefined && !matchesValueList(errorPayload.code, codeSpec)) return false
  if (typeSpec !== undefined && !matchesValueList(errorPayload.type, typeSpec)) return false
  return true
}

function matchesStatusList(statusCode: number, spec: number[]): boolean {
  return spec.length ? spec.includes(statusCode) : true
}

function matchesTextList(text: string, items: string[]): boolean {
  if (!items.length) return true
  const normalized = text.toLowerCase()
  return items.some((item) => normalized.includes(item.toLowerCase()))
}

function matchesValueList(value: unknown, items: string[]): boolean {
  if (!items.length) return true
  const normalized = String(value ?? '').toLowerCase()
  return Boolean(normalized) && items.some((item) => normalized === item.toLowerCase())
}

function normalizePolicyAction(value: ErrorPolicyAction): RequestErrorPolicyDecision['action'] {
  if (value === 'retry_next') return 'retry_next'
  if (value === 'temp_unschedulable' || value === 'rate_limited') return 'cooldown'
  if (value === 'error_disabled') return 'disable'
  const exhaustive: never = value
  return exhaustive
}

function policyCooldownStatus(value: ErrorPolicyAction): CooldownAccountStatus {
  return value === 'rate_limited' ? 'rate_limited' : 'temporary_unavailable'
}

function normalizeComparable(value: string | undefined): string | undefined {
  const text = value?.trim().toLowerCase()
  return text || undefined
}

function normalizeHeadersInput(headers?: Headers | Record<string, string | string[]>): Headers {
  if (headers instanceof Headers) return headers
  const output = new Headers()
  if (!headers) return output
  for (const [name, value] of Object.entries(headers)) {
    output.set(name, Array.isArray(value) ? value.join(', ') : value)
  }
  return output
}

function booleanSetting(value: unknown, key: string): boolean {
  if (typeof value !== 'boolean') {
    throw new Error(`系统设置 ${key} 必须是布尔值`)
  }
  return value
}

function numberSetting(value: unknown, key: string, min: number, max: number): number {
  if (typeof value !== 'number' || !Number.isFinite(value) || !Number.isInteger(value)) {
    throw new Error(`系统设置 ${key} 必须是整数`)
  }
  if (value < min || value > max) {
    throw new Error(`系统设置 ${key} 必须在 ${min} 到 ${max} 之间`)
  }
  return value
}

function assertLocalGatewayDatabaseAccess(operation: string): void {
  if (runtimeConfig.processRole === 'server') {
    throw new Error(`server 角色禁止直接同步访问 SQLite：${operation} 必须通过 DB service`)
  }
}
