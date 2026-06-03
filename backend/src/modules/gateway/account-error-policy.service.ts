import type { AccountStatus } from '../../domain/types.js'
import { runtimeConfig } from '../../config/runtime.js'
import {
  normalizeAccountErrorHandlingRules,
  type AccountErrorHandlingRule,
  type AccountErrorHandlingRuleAction
} from '../accounts/account-error-policy-validation.js'
import {
  clearAccountFailureStateResult,
  clearAuthorizedAccountBindingFailureStateByContext,
  getSettings,
  markAccountCooldown,
  markAccountDisabledByFailure,
  markAuthorizedAccountBindingCooldownByContext,
  markAuthorizedAccountBindingDisabledByFailure,
  type AuthorizedAccountBindingRuntimeTarget
} from '../../storage/repositories.js'
import type { OpenAIGatewayTrafficSource } from './openai-gateway-traffic-source.js'
import { sanitizeDiagnosticPayload } from './payload-sanitizer.js'

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

export interface AccountErrorPolicyAccount {
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

export interface AccountErrorPolicyDecision {
  action: 'retry_next' | 'cooldown' | 'disable'
  ruleName?: string
  cooldownMinutes?: number
  cooldownUntil?: string
  cooldownStatus?: CooldownAccountStatus
}

export interface AccountErrorHandlingResult {
  action: 'none' | 'retry_next' | 'cooldown' | 'disable'
  changed: boolean
  accountStatus?: AccountStatus
  reason?: string
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
  account: AccountErrorPolicyAccount,
  input: {
    success: boolean
    statusCode?: number
    headers?: Headers | Record<string, string | string[]>
    bodyText?: string
    errorMessage?: string
    settings?: GatewaySettings
    trafficSource?: OpenAIGatewayTrafficSource
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
  const upstreamSummary = accountErrorPolicyUpstreamSummary(bodyText, headers)

  if (statusCode !== undefined) {
    const decision = decideAccountErrorPolicy(account, statusCode, headers, Buffer.from(bodyText), settings)
    if (decision && decision.action !== 'retry_next') {
      const updated = applyAccountErrorPolicySideEffect(account, statusCode, decision, settings, upstreamSummary)
      return {
        action: decision.action,
        changed: Boolean(updated),
        accountStatus: updated?.status,
        reason: accountErrorPolicyReason(statusCode, decision, upstreamSummary)
      }
    }

    const reason = genericUpstreamResponseFailureReason(statusCode, upstreamSummary)
    const updated = applyAccountTemporaryUnavailableSideEffect(account, settings, reason)
    return {
      action: 'cooldown',
      changed: Boolean(updated),
      accountStatus: updated?.status ?? account.status,
      reason
    }
  }

  const reason = genericUpstreamRequestFailureReason(input.errorMessage ?? bodyText)
  const updated = applyAccountTemporaryUnavailableSideEffect(account, settings, reason)
  return {
    action: 'cooldown',
    changed: Boolean(updated),
    accountStatus: updated?.status ?? account.status,
    reason
  }
}

export function decideAccountErrorPolicy(
  account: AccountErrorPolicyAccount,
  statusCode: number,
  headers: Headers,
  body: Buffer,
  settings: GatewaySettings
): AccountErrorPolicyDecision | undefined {
  if (statusCode >= 200 && statusCode <= 299) {
    return undefined
  }
  const bodyText = body.toString('utf8')
  const errorPayload = parseErrorPayload(bodyText, headers)

  const rules = accountErrorRules(account.credentials)

  for (const rule of rules
    .filter((item) => item.enabled)
    .sort((left, right) => left.priority - right.priority)) {
    if (!hasErrorPolicyRuleMatcher(rule) || !matchesErrorPolicyRule(rule, statusCode, bodyText, errorPayload)) {
      continue
    }
    const action = normalizePolicyAction(rule.action)
    const ruleName = rule.name
    if (action === 'cooldown') {
      return {
        action,
        ruleName,
        cooldownMinutes: rule.durationMinutes ?? settings.defaultTemporaryUnschedulableMinutes,
        cooldownUntil: resolveAccountErrorRuleCooldownUntil(rule),
        cooldownStatus: policyCooldownStatus(rule.action)
      }
    }
    return { action, ruleName }
  }

  return undefined
}

export function applyAccountErrorPolicySideEffect(
  account: AccountErrorPolicyAccount,
  statusCode: number,
  decision: AccountErrorPolicyDecision,
  settings: GatewaySettings,
  upstreamSummary?: string
): { status: AccountStatus } | undefined {
  const reason = accountErrorPolicyReason(statusCode, decision, upstreamSummary)
  if (decision.action === 'cooldown') {
    return applyAccountCooldownSideEffect(account, settings, reason, {
      cooldownMinutes: decision.cooldownMinutes,
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

function authorizedAccountBindingRuntimeTarget(account: AccountErrorPolicyAccount): AuthorizedAccountBindingRuntimeTarget | undefined {
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

function accountErrorPolicyReason(statusCode: number, decision: AccountErrorPolicyDecision, upstreamSummary?: string): string {
  const base = decision.ruleName
    ? '命中错误处理策略：' + decision.ruleName + '（HTTP ' + statusCode + '）'
    : '命中错误处理策略 HTTP ' + statusCode
  return upstreamSummary ? `${base}；${upstreamSummary}`.slice(0, 1000) : base
}

function accountErrorPolicyUpstreamSummary(bodyText: string, headers: Headers): string | undefined {
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

function accountErrorRules(credentials: Record<string, unknown>): AccountErrorHandlingRule[] {
  return normalizeAccountErrorHandlingRules(credentials.error_handling_rules)
}

function applyAccountTemporaryUnavailableSideEffect(
  account: AccountErrorPolicyAccount,
  settings: GatewaySettings,
  reason: string
): { status: AccountStatus } | undefined {
  return applyAccountCooldownSideEffect(account, settings, reason, {
    cooldownMinutes: settings.defaultTemporaryUnschedulableMinutes,
    cooldownStatus: 'temporary_unavailable'
  })
}

function applyAccountCooldownSideEffect(
  account: AccountErrorPolicyAccount,
  settings: GatewaySettings,
  reason: string,
  input: {
    cooldownMinutes?: number
    cooldownUntil?: string
    cooldownStatus?: CooldownAccountStatus
  } = {}
): { status: AccountStatus } | undefined {
  const minutes = Math.max(1, input.cooldownMinutes ?? settings.defaultTemporaryUnschedulableMinutes)
  const until = input.cooldownUntil ?? new Date(Date.now() + minutes * 60_000).toISOString()
  const status = input.cooldownStatus ?? 'temporary_unavailable'
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

function resolveAccountErrorRuleCooldownUntil(rule: AccountErrorHandlingRule): string | undefined {
  if (policyCooldownStatus(rule.action) !== 'rate_limited') return undefined
  const now = new Date()
  if (rule.reset_strategy === 'duration') {
    return new Date(now.getTime() + rule.duration_hours! * 60 * 60_000).toISOString()
  }
  if (rule.reset_strategy === 'weekly') {
    return nextWeeklyReset(now, rule.weekly_reset_day!, rule.weekly_reset_hour!).toISOString()
  }
  return nextDailyReset(now, rule.daily_reset_hour!).toISOString()
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

function errorPolicyRuleSpecs(rule: AccountErrorHandlingRule): {
  statusSpec: number[] | undefined
  keywordSpec: string[] | undefined
  codeSpec: string[] | undefined
  typeSpec: string[] | undefined
} {
  return {
    statusSpec: rule.status_codes,
    keywordSpec: rule.keywords,
    codeSpec: rule.error_codes,
    typeSpec: rule.error_types
  }
}

function hasErrorPolicyRuleMatcher(rule: AccountErrorHandlingRule): boolean {
  const { statusSpec, keywordSpec, codeSpec, typeSpec } = errorPolicyRuleSpecs(rule)
  return Boolean(statusSpec?.length || keywordSpec?.length || codeSpec?.length || typeSpec?.length)
}

function matchesErrorPolicyRule(rule: AccountErrorHandlingRule, statusCode: number, bodyText: string, errorPayload: Record<string, unknown>): boolean {
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

function normalizePolicyAction(value: AccountErrorHandlingRuleAction): AccountErrorPolicyDecision['action'] {
  if (value === 'retry_next') return 'retry_next'
  if (value === 'temp_unschedulable' || value === 'rate_limited') return 'cooldown'
  if (value === 'error_disabled') return 'disable'
  const exhaustive: never = value
  return exhaustive
}

function policyCooldownStatus(value: AccountErrorHandlingRuleAction): CooldownAccountStatus {
  return value === 'rate_limited' ? 'rate_limited' : 'temporary_unavailable'
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
