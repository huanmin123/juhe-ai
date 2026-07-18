import type { AccountStatus } from '../../../domain/types.js'
import { runtimeConfig } from '../../../config/runtime.js'
import {
  clearAccountFailureStateResult,
  clearAccountFailureStateResultAsync,
  clearAuthorizedAccountBindingFailureStateByContext,
  clearAuthorizedAccountBindingFailureStateByContextAsync,
  markAccountCooldown,
  markAccountCooldownAsync,
  markAccountDisabledByFailure,
  markAccountDisabledByFailureAsync,
  markAuthorizedAccountBindingCooldownByContext,
  markAuthorizedAccountBindingCooldownByContextAsync,
  markAuthorizedAccountBindingDisabledByFailure,
  markAuthorizedAccountBindingDisabledByFailureAsync,
  type AuthorizedAccountBindingRuntimeTarget
} from '../../../storage/repositories.js'
import {
  getSettings,
  getSettingsAsync,
  getSettingsReadOnly
} from '../../../storage/settings.repository.js'
import type { OpenAIGatewayTrafficSource } from '../usage/traffic-source.js'
import { sanitizeDiagnosticPayload } from '../diagnostics/diagnostic-sanitizer.js'
import { parseGatewayProtocolErrorPayload } from '../protocols/registry.js'
import {
  normalizeAccountErrorHandlingRules,
  type AccountErrorHandlingRule
} from '../../accounts/account-error-policy-validation.js'
import { publishAccountRuntimeChange } from '../../page-data/page-data-change.publisher.js'

export type CooldownAccountStatus = 'rate_limited' | 'temporary_unavailable'

export interface GatewaySettings {
  gatewayTextRawBodyLimitMegabytes: number
  defaultTemporaryUnschedulableMinutes: number
  temporaryUnschedulableRetryIntervalSeconds: number
  temporaryUnschedulableRetryAttempts: number
  streamCircuitBreakerEnabled: boolean
  streamRequestTimeoutSeconds: number
  streamIdleTimeoutSeconds: number
  streamClientTotalWaitTimeoutSeconds: number
  streamMaxLifetimeSeconds: number
  streamFailureThresholdCount: number
  streamFailureThresholdWindowMinutes: number
}

export interface AccountErrorPolicyAccount {
  id: string
  providerCode: string
  protocolCode?: string
  protocolVersion?: string
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
  return gatewaySettingsFromRawSettings(settings)
}

export function readGatewaySettingsReadOnly(): GatewaySettings {
  return gatewaySettingsFromRawSettings(getSettingsReadOnly())
}

function gatewaySettingsFromRawSettings(settings: Record<string, unknown>): GatewaySettings {
  return {
    gatewayTextRawBodyLimitMegabytes: numberSetting(settings.gatewayTextRawBodyLimitMegabytes, 'gatewayTextRawBodyLimitMegabytes', 1, 64),
    defaultTemporaryUnschedulableMinutes: numberSetting(settings.defaultTemporaryUnschedulableMinutes, 'defaultTemporaryUnschedulableMinutes', 1, 1440),
    temporaryUnschedulableRetryIntervalSeconds: numberSetting(settings.temporaryUnschedulableRetryIntervalSeconds, 'temporaryUnschedulableRetryIntervalSeconds', 0, 3600),
    temporaryUnschedulableRetryAttempts: numberSetting(settings.temporaryUnschedulableRetryAttempts, 'temporaryUnschedulableRetryAttempts', 0, 10),
    streamCircuitBreakerEnabled: true,
    streamRequestTimeoutSeconds: numberSetting(settings.streamRequestTimeoutSeconds, 'streamRequestTimeoutSeconds', 10, 3600),
    streamIdleTimeoutSeconds: numberSetting(settings.streamIdleTimeoutSeconds, 'streamIdleTimeoutSeconds', 1, 3600),
    streamClientTotalWaitTimeoutSeconds: numberSetting(settings.streamClientTotalWaitTimeoutSeconds, 'streamClientTotalWaitTimeoutSeconds', 10, 3600),
    streamMaxLifetimeSeconds: numberSetting(settings.streamMaxLifetimeSeconds, 'streamMaxLifetimeSeconds', 60, 86400),
    streamFailureThresholdCount: numberSetting(settings.streamFailureThresholdCount, 'streamFailureThresholdCount', 1, 100),
    streamFailureThresholdWindowMinutes: numberSetting(settings.streamFailureThresholdWindowMinutes, 'streamFailureThresholdWindowMinutes', 1, 1440)
  }
}

export async function readGatewaySettingsAsync(): Promise<GatewaySettings> {
  if (runtimeConfig.databaseDriver !== 'postgres') {
    return withGatewaySettingsLocalDatabaseAccess(() => readGatewaySettings())
  }
  const settings = await getSettingsAsync()
  return {
    gatewayTextRawBodyLimitMegabytes: numberSetting(settings.gatewayTextRawBodyLimitMegabytes, 'gatewayTextRawBodyLimitMegabytes', 1, 64),
    defaultTemporaryUnschedulableMinutes: numberSetting(settings.defaultTemporaryUnschedulableMinutes, 'defaultTemporaryUnschedulableMinutes', 1, 1440),
    temporaryUnschedulableRetryIntervalSeconds: numberSetting(settings.temporaryUnschedulableRetryIntervalSeconds, 'temporaryUnschedulableRetryIntervalSeconds', 0, 3600),
    temporaryUnschedulableRetryAttempts: numberSetting(settings.temporaryUnschedulableRetryAttempts, 'temporaryUnschedulableRetryAttempts', 0, 10),
    streamCircuitBreakerEnabled: true,
    streamRequestTimeoutSeconds: numberSetting(settings.streamRequestTimeoutSeconds, 'streamRequestTimeoutSeconds', 10, 3600),
    streamIdleTimeoutSeconds: numberSetting(settings.streamIdleTimeoutSeconds, 'streamIdleTimeoutSeconds', 1, 3600),
    streamClientTotalWaitTimeoutSeconds: numberSetting(settings.streamClientTotalWaitTimeoutSeconds, 'streamClientTotalWaitTimeoutSeconds', 10, 3600),
    streamMaxLifetimeSeconds: numberSetting(settings.streamMaxLifetimeSeconds, 'streamMaxLifetimeSeconds', 60, 86400),
    streamFailureThresholdCount: numberSetting(settings.streamFailureThresholdCount, 'streamFailureThresholdCount', 1, 100),
    streamFailureThresholdWindowMinutes: numberSetting(settings.streamFailureThresholdWindowMinutes, 'streamFailureThresholdWindowMinutes', 1, 1440)
  }
}

function withGatewaySettingsLocalDatabaseAccess<T>(operation: () => T): T {
  if (runtimeConfig.processRole !== 'server') {
    return operation()
  }
  const previousProcessRole = runtimeConfig.processRole
  try {
    runtimeConfig.processRole = 'db-service'
    return operation()
  } finally {
    runtimeConfig.processRole = previousProcessRole
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
    traceId?: string
    settings?: GatewaySettings
    trafficSource?: OpenAIGatewayTrafficSource
    policyDecision?: AccountErrorPolicyDecision
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

  if (!input.policyDecision) return { action: 'none', changed: false, accountStatus: account.status }
  return applyExplicitAccountErrorPolicyDecision(account, input, input.policyDecision)
}

export async function applyAccountErrorHandlingAsync(
  account: AccountErrorPolicyAccount,
  input: {
    success: boolean
    statusCode?: number
    headers?: Headers | Record<string, string | string[]>
    bodyText?: string
    errorMessage?: string
    traceId?: string
    settings?: GatewaySettings
    trafficSource?: OpenAIGatewayTrafficSource
    policyDecision?: AccountErrorPolicyDecision
  }
): Promise<AccountErrorHandlingResult> {
  if (runtimeConfig.databaseDriver !== 'postgres') {
    return withGatewaySettingsLocalDatabaseAccess(() => applyAccountErrorHandling(account, input))
  }
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
      ? await clearAuthorizedAccountBindingFailureStateByContextAsync(authorizedTarget, { allowErrorRestore: false })
      : await clearAccountFailureStateResultAsync(account.id, undefined, { allowErrorRestore: false })
    return { action: 'none', changed: result.changed, accountStatus: result.account?.status ?? account.status }
  }

  if (!input.policyDecision) return { action: 'none', changed: false, accountStatus: account.status }
  return await applyExplicitAccountErrorPolicyDecisionAsync(account, input, input.policyDecision)
}

export function decideAccountErrorPolicy(
  account: AccountErrorPolicyAccount,
  statusCode: number,
  headers: Headers,
  body: Buffer,
  settings: GatewaySettings
): AccountErrorPolicyDecision | undefined {
  if (statusCode >= 200 && statusCode <= 299) return undefined
  const rules = normalizeAccountErrorHandlingRules(account.credentials.error_handling_rules)
    .filter((rule) => rule.enabled)
    .sort((left, right) => left.priority - right.priority)
  const payload = accountErrorPolicyPayload(body.toString('utf8'), headers, account)
  const errorCode = stringValue(payload.code).toLowerCase()
  const errorType = stringValue(payload.type).toLowerCase()
  const searchableText = body.toString('utf8').toLowerCase()
  const rule = rules.find((candidate) => accountErrorRuleMatches(candidate, statusCode, errorCode, errorType, searchableText))
  if (!rule) return undefined
  if (rule.action === 'retry_next') return { action: 'retry_next', ruleName: rule.name }
  if (rule.action === 'error_disabled') return { action: 'disable', ruleName: rule.name }
  if (rule.action === 'rate_limited') {
    return {
      action: 'cooldown',
      ruleName: rule.name,
      cooldownStatus: 'rate_limited',
      cooldownUntil: accountErrorRuleCooldownUntil(rule, new Date())
    }
  }
  return {
    action: 'cooldown',
    ruleName: rule.name,
    cooldownStatus: 'temporary_unavailable',
    cooldownUntil: new Date(Date.now() + settings.defaultTemporaryUnschedulableMinutes * 60_000).toISOString()
  }
}

function accountErrorPolicyPayload(
  bodyText: string,
  headers: Headers,
  account: AccountErrorPolicyAccount
): Record<string, unknown> {
  return parseErrorPayload(bodyText, headers, account)
}

function accountErrorRuleMatches(
  rule: AccountErrorHandlingRule,
  statusCode: number,
  errorCode: string,
  errorType: string,
  searchableText: string
): boolean {
  return (!rule.status_codes?.length || rule.status_codes.includes(statusCode))
    && (!rule.error_codes?.length || rule.error_codes.some((value) => value.toLowerCase() === errorCode))
    && (!rule.error_types?.length || rule.error_types.some((value) => value.toLowerCase() === errorType))
    && (!rule.keywords?.length || rule.keywords.some((value) => searchableText.includes(value.toLowerCase())))
}

function accountErrorRuleCooldownUntil(rule: AccountErrorHandlingRule, now: Date): string | undefined {
  if (rule.reset_strategy === 'duration') {
    return new Date(now.getTime() + Math.max(1, rule.duration_hours ?? 1) * 3_600_000).toISOString()
  }
  const target = new Date(now)
  target.setMinutes(0, 0, 0)
  target.setHours(rule.reset_strategy === 'weekly' ? rule.weekly_reset_hour ?? 0 : rule.daily_reset_hour ?? 0)
  if (rule.reset_strategy === 'weekly') {
    const daysAhead = ((rule.weekly_reset_day ?? 0) - target.getDay() + 7) % 7
    target.setDate(target.getDate() + daysAhead)
  }
  if (target.getTime() <= now.getTime()) {
    target.setDate(target.getDate() + (rule.reset_strategy === 'weekly' ? 7 : 1))
  }
  return target.toISOString()
}

function applyExplicitAccountErrorPolicyDecision(
  account: AccountErrorPolicyAccount,
  input: {
    statusCode?: number
    headers?: Headers | Record<string, string | string[]>
    bodyText?: string
    errorMessage?: string
    traceId?: string
  },
  decision: AccountErrorPolicyDecision
): AccountErrorHandlingResult {
  if (decision.action === 'retry_next') {
    return { action: 'retry_next', changed: false, accountStatus: account.status }
  }
  const reason = explicitAccountErrorPolicyReason(account, input, decision)
  const authorizedTarget = authorizedAccountBindingRuntimeTarget(account)
  const updated = decision.action === 'disable'
    ? authorizedTarget
      ? markAuthorizedAccountBindingDisabledByFailure({ ...authorizedTarget, reason })
      : markAccountDisabledByFailure(account.id, reason)
    : authorizedTarget
      ? markAuthorizedAccountBindingCooldownByContext({
          ...authorizedTarget,
          status: decision.cooldownStatus,
          cooldownUntil: decision.cooldownUntil,
          reason,
          traceId: input.traceId
        })
      : markAccountCooldown(
          account.id,
          decision.cooldownUntil,
          reason,
          decision.cooldownStatus,
          undefined,
          input.traceId
        )
  if (updated) {
    void publishAccountRuntimeChange({
      accountId: account.id,
      ownerSystemAccountIds: [account.bindingSystemAccountId ?? '', account.groupOwnerSystemAccountId ?? ''],
      fieldMask: ['status', 'schedulable', 'cooldownUntil', 'lastErrorCode', 'lastErrorMessage']
    })
  }
  return {
    action: decision.action,
    changed: Boolean(updated),
    accountStatus: updated?.status ?? account.status,
    reason
  }
}

async function applyExplicitAccountErrorPolicyDecisionAsync(
  account: AccountErrorPolicyAccount,
  input: {
    statusCode?: number
    headers?: Headers | Record<string, string | string[]>
    bodyText?: string
    errorMessage?: string
    traceId?: string
  },
  decision: AccountErrorPolicyDecision
): Promise<AccountErrorHandlingResult> {
  if (decision.action === 'retry_next') {
    return { action: 'retry_next', changed: false, accountStatus: account.status }
  }
  const reason = explicitAccountErrorPolicyReason(account, input, decision)
  const authorizedTarget = authorizedAccountBindingRuntimeTarget(account)
  const updated = decision.action === 'disable'
    ? authorizedTarget
      ? await markAuthorizedAccountBindingDisabledByFailureAsync({ ...authorizedTarget, reason })
      : await markAccountDisabledByFailureAsync(account.id, reason)
    : authorizedTarget
      ? await markAuthorizedAccountBindingCooldownByContextAsync({
          ...authorizedTarget,
          status: decision.cooldownStatus,
          cooldownUntil: decision.cooldownUntil,
          reason,
          traceId: input.traceId
        })
      : await markAccountCooldownAsync(
          account.id,
          decision.cooldownUntil,
          reason,
          decision.cooldownStatus,
          undefined,
          input.traceId
        )
  if (updated) {
    await publishAccountRuntimeChange({
      accountId: account.id,
      ownerSystemAccountIds: [account.bindingSystemAccountId ?? '', account.groupOwnerSystemAccountId ?? ''],
      fieldMask: ['status', 'schedulable', 'cooldownUntil', 'lastErrorCode', 'lastErrorMessage']
    })
  }
  return {
    action: decision.action,
    changed: Boolean(updated),
    accountStatus: updated?.status ?? account.status,
    reason
  }
}

function explicitAccountErrorPolicyReason(
  account: AccountErrorPolicyAccount,
  input: {
    statusCode?: number
    headers?: Headers | Record<string, string | string[]>
    bodyText?: string
    errorMessage?: string
  },
  decision: AccountErrorPolicyDecision
): string {
  const bodyText = input.bodyText ?? input.errorMessage ?? ''
  const statusCode = input.statusCode
  const upstreamSummary = accountErrorPolicyUpstreamSummary(account, bodyText, normalizeHeadersInput(input.headers))
  const failure = statusCode === undefined
    ? genericUpstreamRequestFailureReason(input.errorMessage ?? bodyText)
    : genericUpstreamResponseFailureReason(statusCode, upstreamSummary)
  return `账户错误策略「${decision.ruleName ?? '未命名规则'}」命中；${failure}`.slice(0, 1000)
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

export function parseErrorPayload(
  text: string,
  headers: Headers,
  profile?: { protocolCode?: string; protocolVersion?: string }
): Record<string, unknown> {
  return parseGatewayProtocolErrorPayload(profile, text, headers)
}

function accountErrorPolicyUpstreamSummary(
  profile: { protocolCode?: string; protocolVersion?: string } | undefined,
  bodyText: string,
  headers: Headers
): string | undefined {
  const errorPayload = parseErrorPayload(bodyText, headers, profile)
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

function genericUpstreamResponseFailureReason(statusCode: number, upstreamSummary?: string): string {
  const base = `上游调用失败：HTTP ${statusCode}`
  return upstreamSummary ? `${base}；${upstreamSummary}`.slice(0, 1000) : base
}

function genericUpstreamRequestFailureReason(message: string): string {
  return `上游请求异常：${sanitizeDiagnosticPayload(message || '请求失败')}`.slice(0, 1000)
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
