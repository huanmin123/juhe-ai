import type { AccountStatus } from '../../domain/types.js'
import { clearAccountFailureState, getSettings, markAccountCooldown, markAccountDisabledByFailure } from '../../storage/repositories.js'
import { calculateOpenAICodexRateLimitResetAt } from './openai-codex-usage.service.js'

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
  type?: string
  credentials: Record<string, unknown>
}

export interface AccountErrorPolicyDecision {
  action: 'retry_next' | 'cooldown' | 'disable'
  ruleName?: string
  cooldownMinutes?: number
  cooldownUntil?: string
  cooldownStatus?: CooldownAccountStatus
}

export interface AccountErrorHandlingResult {
  action: 'none' | 'retry_next' | 'cooldown' | 'disable' | 'default_cooldown'
  changed: boolean
  accountStatus?: AccountStatus
  reason?: string
}

export function readGatewaySettings(): GatewaySettings {
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
  account: AccountErrorPolicyAccount & { status?: AccountStatus; cooldownUntil?: string; lastErrorMessage?: string },
  input: {
    success: boolean
    statusCode?: number
    headers?: Headers | Record<string, string | string[]>
    bodyText?: string
    errorMessage?: string
    settings?: GatewaySettings
  }
): AccountErrorHandlingResult {
  if (input.success) {
    const changed = (account.status !== undefined && account.status !== 'active') || Boolean(account.cooldownUntil) || Boolean(account.lastErrorMessage)
    const updated = clearAccountFailureState(account.id)
    return { action: 'none', changed, accountStatus: updated?.status ?? account.status }
  }

  const settings = input.settings ?? readGatewaySettings()
  const statusCode = input.statusCode
  const bodyText = input.bodyText ?? input.errorMessage ?? ''
  const headers = normalizeHeadersInput(input.headers)

  if (statusCode !== undefined) {
    const decision = decideAccountErrorPolicy(account, statusCode, headers, Buffer.from(bodyText), settings)
    if (decision) {
      const updated = applyAccountErrorPolicySideEffect(account, statusCode, decision, settings)
      return {
        action: decision.action,
        changed: Boolean(updated),
        accountStatus: updated?.status,
        reason: accountErrorPolicyReason(statusCode, decision)
      }
    }
  }

  const reason = statusCode !== undefined
    ? 'Unhandled upstream status ' + statusCode
    : 'Unhandled upstream exception: ' + (input.errorMessage ?? 'request failed')
  const updated = markDefaultTemporaryUnschedulable(account, settings, reason)
  return {
    action: 'default_cooldown',
    changed: Boolean(updated),
    accountStatus: updated?.status,
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
  const bodyText = body.toString('utf8')
  const errorPayload = parseErrorPayload(bodyText, headers)

  const codexOAuthResetAt = openAIOAuthCodexResetAt(account, statusCode, headers, bodyText)
  if (codexOAuthResetAt) {
    return {
      action: 'cooldown',
      ruleName: 'OpenAI OAuth Codex 429',
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
  settings: GatewaySettings
): { status: AccountStatus } | undefined {
  const reason = accountErrorPolicyReason(statusCode, decision)
  if (decision.action === 'cooldown') {
    const minutes = Math.max(1, decision.cooldownMinutes ?? settings.defaultTemporaryUnschedulableMinutes)
    const until = decision.cooldownUntil ?? new Date(Date.now() + minutes * 60_000).toISOString()
    return markAccountCooldown(account.id, until, reason, decision.cooldownStatus ?? 'temporary_unavailable')
  }
  if (decision.action === 'disable') {
    return markAccountDisabledByFailure(account.id, reason)
  }
  return undefined
}

export function markDefaultTemporaryUnschedulable(
  account: AccountErrorPolicyAccount,
  settings: GatewaySettings,
  reason: string
): { status: AccountStatus } | undefined {
  const minutes = Math.max(1, settings.defaultTemporaryUnschedulableMinutes)
  const until = new Date(Date.now() + minutes * 60_000).toISOString()
  return markAccountCooldown(account.id, until, reason, 'temporary_unavailable')
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

function accountErrorPolicyReason(statusCode: number, decision: AccountErrorPolicyDecision): string {
  return decision.ruleName
    ? 'Error policy matched: ' + decision.ruleName + ' (HTTP ' + statusCode + ')'
    : 'Error policy matched HTTP ' + statusCode
}

function accountErrorRules(credentials: Record<string, unknown>): Array<Record<string, unknown>> {
  return Array.isArray(credentials.error_handling_rules)
    ? credentials.error_handling_rules.filter((item): item is Record<string, unknown> => typeof item === 'object' && item !== null && !Array.isArray(item))
    : []
}

function openAIOAuthCodexResetAt(account: AccountErrorPolicyAccount, statusCode: number, headers: Headers, bodyText: string): string | undefined {
  if (statusCode !== 429 || account.type !== 'oauth') return undefined
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
  const timeZone = safeTimeZone(rule.reset_timezone ?? rule.timezone)
  if (strategy === 'weekly') {
    const weekday = Math.min(Math.max(numericRuleValue(rule.weekly_reset_day ?? rule.weeklyResetDay, 1), 0), 6)
    const hour = Math.min(Math.max(numericRuleValue(rule.weekly_reset_hour ?? rule.weeklyResetHour, 0), 0), 23)
    return nextWeeklyReset(now, weekday, hour, timeZone).toISOString()
  }
  const hour = Math.min(Math.max(numericRuleValue(rule.daily_reset_hour ?? rule.dailyResetHour, 0), 0), 23)
  return nextDailyReset(now, hour, timeZone).toISOString()
}

function nextDailyReset(now: Date, hour: number, timeZone?: string): Date {
  if (!timeZone) {
    const next = new Date(now)
    next.setHours(hour, 0, 0, 0)
    if (next <= now) next.setDate(next.getDate() + 1)
    return next
  }
  const parts = zonedDateParts(now, timeZone)
  let next = zonedTimeToDate(timeZone, parts.year, parts.month, parts.day, hour)
  if (next <= now) {
    const tomorrow = addDaysToLocalDate(parts.year, parts.month, parts.day, 1)
    next = zonedTimeToDate(timeZone, tomorrow.year, tomorrow.month, tomorrow.day, hour)
  }
  return next
}

function nextWeeklyReset(now: Date, weekday: number, hour: number, timeZone?: string): Date {
  if (!timeZone) {
    const next = new Date(now)
    next.setHours(hour, 0, 0, 0)
    const delta = (weekday - next.getDay() + 7) % 7
    next.setDate(next.getDate() + delta)
    if (next <= now) next.setDate(next.getDate() + 7)
    return next
  }
  const parts = zonedDateParts(now, timeZone)
  let delta = (weekday - parts.weekday + 7) % 7
  let target = addDaysToLocalDate(parts.year, parts.month, parts.day, delta)
  let next = zonedTimeToDate(timeZone, target.year, target.month, target.day, hour)
  if (next <= now) {
    delta += 7
    target = addDaysToLocalDate(parts.year, parts.month, parts.day, delta)
    next = zonedTimeToDate(timeZone, target.year, target.month, target.day, hour)
  }
  return next
}

function safeTimeZone(value: unknown): string | undefined {
  if (typeof value !== 'string' || !value.trim()) return undefined
  try {
    new Intl.DateTimeFormat('en-US', { timeZone: value }).format(new Date())
    return value
  } catch {
    return undefined
  }
}

function zonedDateParts(date: Date, timeZone: string): { year: number; month: number; day: number; weekday: number } {
  const formatter = new Intl.DateTimeFormat('en-US', {
    timeZone,
    year: 'numeric',
    month: '2-digit',
    day: '2-digit',
    weekday: 'short'
  })
  const parts = Object.fromEntries(formatter.formatToParts(date).map((part) => [part.type, part.value]))
  return {
    year: Number(parts.year),
    month: Number(parts.month),
    day: Number(parts.day),
    weekday: weekdayNumber(String(parts.weekday))
  }
}

function weekdayNumber(value: string): number {
  const key = value.slice(0, 3).toLowerCase()
  return ['sun', 'mon', 'tue', 'wed', 'thu', 'fri', 'sat'].indexOf(key)
}

function addDaysToLocalDate(year: number, month: number, day: number, days: number): { year: number; month: number; day: number } {
  const date = new Date(Date.UTC(year, month - 1, day + days, 12, 0, 0, 0))
  return { year: date.getUTCFullYear(), month: date.getUTCMonth() + 1, day: date.getUTCDate() }
}

function zonedTimeToDate(timeZone: string, year: number, month: number, day: number, hour: number): Date {
  const localAsUtcMs = Date.UTC(year, month - 1, day, hour, 0, 0, 0)
  let result = new Date(localAsUtcMs - timeZoneOffsetMs(timeZone, new Date(localAsUtcMs)))
  result = new Date(localAsUtcMs - timeZoneOffsetMs(timeZone, result))
  return result
}

function timeZoneOffsetMs(timeZone: string, date: Date): number {
  const formatter = new Intl.DateTimeFormat('en-US', {
    timeZone,
    hour12: false,
    year: 'numeric',
    month: '2-digit',
    day: '2-digit',
    hour: '2-digit',
    minute: '2-digit',
    second: '2-digit'
  })
  const parts = Object.fromEntries(formatter.formatToParts(date).map((part) => [part.type, part.value]))
  const asUtc = Date.UTC(Number(parts.year), Number(parts.month) - 1, Number(parts.day), Number(parts.hour), Number(parts.minute), Number(parts.second))
  return asUtc - date.getTime()
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
