import type { AccountStatus } from '../../domain/types.js'
import { runtimeConfig } from '../../config/runtime.js'
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
import { calculateOpenAICodexRateLimitResetAt } from './openai-codex-usage.service.js'
import type { OpenAIGatewayTrafficSource } from './openai-gateway-traffic-source.js'

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
  providerCode?: string
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
    defaultTemporaryUnschedulableMinutes: numberSetting(settings.defaultTemporaryUnschedulableMinutes, 5, 1, 1440),
    temporaryUnschedulableRetryIntervalSeconds: numberSetting(settings.temporaryUnschedulableRetryIntervalSeconds, 3, 0, 3600),
    temporaryUnschedulableRetryAttempts: numberSetting(settings.temporaryUnschedulableRetryAttempts, 3, 0, 10),
    streamCircuitBreakerEnabled: booleanSetting(settings.streamCircuitBreakerEnabled, true),
    streamRequestTimeoutSeconds: numberSetting(settings.streamRequestTimeoutSeconds, 180, 10, 3600),
    streamIdleTimeoutSeconds: numberSetting(settings.streamIdleTimeoutSeconds, 60, 1, 3600),
    streamFailureThresholdCount: numberSetting(settings.streamFailureThresholdCount, 3, 1, 100),
    streamFailureThresholdWindowMinutes: numberSetting(settings.streamFailureThresholdWindowMinutes, 10, 1, 1440)
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
    if (decision) {
      const updated = applyAccountErrorPolicySideEffect(account, statusCode, decision, settings, upstreamSummary)
      return {
        action: decision.action,
        changed: Boolean(updated),
        accountStatus: updated?.status,
        reason: accountErrorPolicyReason(statusCode, decision, upstreamSummary)
      }
    }
  }

  return {
    action: 'none',
    changed: false,
    accountStatus: account.status,
    reason: statusCode !== undefined
      ? '未配置处理策略的上游状态码 ' + statusCode
      : '未配置处理策略的上游异常：' + (input.errorMessage ?? '请求失败')
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

  const codexOAuthResetAt = openAIOAuthCodexResetAt(account, statusCode, headers, bodyText)
  if (codexOAuthResetAt) {
    return {
      action: 'cooldown',
      ruleName: 'OpenAI OAuth 官方限额',
      cooldownUntil: codexOAuthResetAt,
      cooldownStatus: 'rate_limited'
    }
  }

  const rules = accountErrorRules(account.credentials)

  for (const rule of rules
    .filter((item) => item.enabled !== false)
    .sort((left, right) => numericRuleValue(left.priority, Number.MAX_SAFE_INTEGER) - numericRuleValue(right.priority, Number.MAX_SAFE_INTEGER))) {
    if (!hasErrorPolicyRuleMatcher(rule) || !matchesErrorPolicyRule(rule, statusCode, bodyText, errorPayload)) {
      continue
    }
    const action = normalizePolicyAction(rule.action)
    const ruleName = typeof rule.name === 'string' ? rule.name : '账号错误处理规则'
    if (action === 'cooldown') {
      return {
        action,
        ruleName,
        cooldownMinutes: numericRuleValue(rule.durationMinutes ?? rule.duration_minutes, settings.defaultTemporaryUnschedulableMinutes),
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
    const minutes = Math.max(1, decision.cooldownMinutes ?? settings.defaultTemporaryUnschedulableMinutes)
    const until = decision.cooldownUntil ?? new Date(Date.now() + minutes * 60_000).toISOString()
    const authorizedTarget = authorizedAccountBindingRuntimeTarget(account)
    return authorizedTarget
      ? markAuthorizedAccountBindingCooldownByContext({
          ...authorizedTarget,
          cooldownUntil: until,
          reason,
          status: decision.cooldownStatus ?? 'temporary_unavailable'
        })
      : markAccountCooldown(account.id, until, reason, decision.cooldownStatus ?? 'temporary_unavailable')
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
  const systemAccountId = account.bindingSystemAccountId ?? account.groupOwnerSystemAccountId
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
    parts.push(code)
  }
  if (message && message !== code) {
    parts.push(message)
  }
  return parts.length > 0 ? parts.join('；') : undefined
}

function stringValue(value: unknown): string {
  return typeof value === 'string' && value.trim() ? value.trim() : ''
}

function accountErrorRules(credentials: Record<string, unknown>): Array<Record<string, unknown>> {
  return Array.isArray(credentials.error_handling_rules)
    ? credentials.error_handling_rules.filter((item): item is Record<string, unknown> => typeof item === 'object' && item !== null && !Array.isArray(item))
    : []
}

function openAIOAuthCodexResetAt(account: AccountErrorPolicyAccount, statusCode: number, headers: Headers, bodyText: string): string | undefined {
  // OpenAI OAuth 是官方接入路径，Codex 限额会返回可解析的 reset 信息。
  // 这属于供应商官方账号语义，不依赖每个账号的 error_handling_rules 默认配置。
  if ((account.providerCode ?? 'openai') !== 'openai' || statusCode !== 429 || account.type !== 'oauth') return undefined
  return calculateOpenAICodexRateLimitResetAt(headers, bodyText)
}

function resolveAccountErrorRuleCooldownUntil(rule: Record<string, unknown>): string | undefined {
  if (policyCooldownStatus(rule.action) !== 'rate_limited') return undefined
  const now = new Date()
  const strategy = String(rule.reset_strategy ?? rule.recovery_strategy ?? 'daily')
  if (strategy === 'duration') {
    const hours = Math.max(1, numericRuleValue(rule.duration_hours ?? rule.durationHours, 5))
    return new Date(now.getTime() + hours * 60 * 60_000).toISOString()
  }
  if (strategy === 'weekly') {
    const weekday = Math.min(Math.max(numericRuleValue(rule.weekly_reset_day ?? rule.weeklyResetDay, 1), 0), 6)
    const hour = Math.min(Math.max(numericRuleValue(rule.weekly_reset_hour ?? rule.weeklyResetHour, 0), 0), 23)
    return nextWeeklyReset(now, weekday, hour).toISOString()
  }
  const hour = Math.min(Math.max(numericRuleValue(rule.daily_reset_hour ?? rule.dailyResetHour, 0), 0), 23)
  return nextDailyReset(now, hour).toISOString()
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

function errorPolicyRuleSpecs(rule: Record<string, unknown>): {
  statusSpec: unknown
  keywordSpec: unknown
  codeSpec: unknown
  typeSpec: unknown
} {
  const match = typeof rule.match === 'object' && rule.match !== null && !Array.isArray(rule.match)
    ? rule.match as Record<string, unknown>
    : {}
  return {
    statusSpec: rule.statusCode ?? rule.status_code ?? rule.statusCodes ?? rule.status_codes ?? match.statusCode ?? match.status_code ?? match.statusCodes ?? match.status_codes,
    keywordSpec: rule.keywords ?? rule.bodyKeywords ?? rule.body_keywords ?? match.keywords ?? match.bodyKeywords ?? match.body_keywords,
    codeSpec: rule.errorCode ?? rule.error_code ?? rule.errorCodes ?? rule.error_codes ?? match.errorCode ?? match.error_code ?? match.errorCodes ?? match.error_codes,
    typeSpec: rule.errorType ?? rule.error_type ?? rule.errorTypes ?? rule.error_types ?? match.errorType ?? match.error_type ?? match.errorTypes ?? match.error_types
  }
}

function hasErrorPolicyRuleMatcher(rule: Record<string, unknown>): boolean {
  const { statusSpec, keywordSpec, codeSpec, typeSpec } = errorPolicyRuleSpecs(rule)
  return listRuleValues(statusSpec).length > 0
    || listRuleValues(keywordSpec).length > 0
    || listRuleValues(codeSpec).length > 0
    || listRuleValues(typeSpec).length > 0
}

function matchesErrorPolicyRule(rule: Record<string, unknown>, statusCode: number, bodyText: string, errorPayload: Record<string, unknown>): boolean {
  const { statusSpec, keywordSpec, codeSpec, typeSpec } = errorPolicyRuleSpecs(rule)

  if (statusSpec !== undefined && !matchesStatusList(statusCode, statusSpec)) return false
  if (keywordSpec !== undefined && !matchesTextList(bodyText, keywordSpec)) return false
  if (codeSpec !== undefined && !matchesValueList(errorPayload.code, codeSpec)) return false
  if (typeSpec !== undefined && !matchesValueList(errorPayload.type, typeSpec)) return false
  return true
}

function matchesStatusList(statusCode: number, spec: unknown): boolean {
  const items = listRuleValues(spec)
  if (!items.length) return true
  return items.some((item) => {
    const token = item.toLowerCase()
    if (token === '*' || token === 'all') return true
    const range = token.match(/^(\d{3})\s*-\s*(\d{3})$/)
    if (range) return statusCode >= Number(range[1]) && statusCode <= Number(range[2])
    const family = token.match(/^([1-5])xx$/)
    if (family) return Math.floor(statusCode / 100) === Number(family[1])
    return Number(token) === statusCode
  })
}

function matchesTextList(text: string, spec: unknown): boolean {
  const items = listRuleValues(spec)
  if (!items.length) return true
  const normalized = text.toLowerCase()
  return items.some((item) => normalized.includes(item.toLowerCase()))
}

function matchesValueList(value: unknown, spec: unknown): boolean {
  const items = listRuleValues(spec)
  if (!items.length) return true
  const normalized = String(value ?? '').toLowerCase()
  return Boolean(normalized) && items.some((item) => normalized === item.toLowerCase())
}

function listRuleValues(spec: unknown): string[] {
  if (Array.isArray(spec)) {
    return spec.flatMap((item) => listRuleValues(item))
  }
  if (typeof spec === 'number') {
    return [String(spec)]
  }
  if (typeof spec !== 'string') {
    return []
  }
  return spec
    .split(/[,;，；\n\/]+/)
    .map((item) => item.trim())
    .filter(Boolean)
}

function normalizePolicyAction(value: unknown): AccountErrorPolicyDecision['action'] {
  if (value === 'retry_next' || value === 'switch_account' || value === 'next') return 'retry_next'
  if (value === 'temp_unschedulable' || value === 'temporary_unavailable' || value === 'overloaded' || value === 'rate_limited') return 'cooldown'
  if (value === 'error_disabled' || value === 'disable') return 'disable'
  return 'cooldown'
}

function policyCooldownStatus(value: unknown): CooldownAccountStatus {
  const token = String(value ?? '').toLowerCase()
  return token.includes('rate') ? 'rate_limited' : 'temporary_unavailable'
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

function booleanSetting(value: unknown, fallback: boolean): boolean {
  return typeof value === 'boolean' ? value : fallback
}

function numericRuleValue(value: unknown, fallback: number): number {
  const number = typeof value === 'number' ? value : typeof value === 'string' ? Number(value) : NaN
  return Number.isFinite(number) ? number : fallback
}

function numberSetting(value: unknown, fallback: number, min: number, max: number): number {
  const number = typeof value === 'number' ? value : typeof value === 'string' ? Number(value) : NaN
  if (!Number.isFinite(number)) return fallback
  return Math.min(Math.max(Math.trunc(number), min), max)
}

function assertLocalGatewayDatabaseAccess(operation: string): void {
  if (runtimeConfig.processRole === 'server') {
    throw new Error(`server 角色禁止直接同步访问 SQLite：${operation} 必须通过 DB service`)
  }
}
