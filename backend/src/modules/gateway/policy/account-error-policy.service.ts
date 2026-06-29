import type { AccountStatus } from '../../../domain/types.js'
import { runtimeConfig } from '../../../config/runtime.js'
import {
  clearAccountFailureStateResult,
  clearAccountFailureStateResultAsync,
  clearAuthorizedAccountBindingFailureStateByContext,
  clearAuthorizedAccountBindingFailureStateByContextAsync,
  getSettings,
  getSettingsAsync,
  markAccountTemporaryUnavailable,
  markAccountTemporaryUnavailableAsync,
  markAuthorizedAccountBindingTemporaryUnavailableByContext,
  markAuthorizedAccountBindingTemporaryUnavailableByContextAsync,
  type AuthorizedAccountBindingRuntimeTarget
} from '../../../storage/repositories.js'
import type { OpenAIGatewayTrafficSource } from '../usage/traffic-source.js'
import { sanitizeDiagnosticPayload } from '../audit/payload-sanitizer.js'
import { parseGatewayProtocolErrorPayload } from '../protocols/registry.js'

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
  return {
    gatewayTextRawBodyLimitMegabytes: numberSetting(settings.gatewayTextRawBodyLimitMegabytes, 'gatewayTextRawBodyLimitMegabytes', 1, 64),
    defaultTemporaryUnschedulableMinutes: numberSetting(settings.defaultTemporaryUnschedulableMinutes, 'defaultTemporaryUnschedulableMinutes', 1, 1440),
    temporaryUnschedulableRetryIntervalSeconds: numberSetting(settings.temporaryUnschedulableRetryIntervalSeconds, 'temporaryUnschedulableRetryIntervalSeconds', 0, 3600),
    temporaryUnschedulableRetryAttempts: numberSetting(settings.temporaryUnschedulableRetryAttempts, 'temporaryUnschedulableRetryAttempts', 0, 10),
    streamCircuitBreakerEnabled: booleanSetting(settings.streamCircuitBreakerEnabled, 'streamCircuitBreakerEnabled'),
    streamRequestTimeoutSeconds: numberSetting(settings.streamRequestTimeoutSeconds, 'streamRequestTimeoutSeconds', 10, 3600),
    streamIdleTimeoutSeconds: numberSetting(settings.streamIdleTimeoutSeconds, 'streamIdleTimeoutSeconds', 1, 3600),
    streamClientTotalWaitTimeoutSeconds: numberSetting(settings.streamClientTotalWaitTimeoutSeconds, 'streamClientTotalWaitTimeoutSeconds', 10, 3600),
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
    streamCircuitBreakerEnabled: booleanSetting(settings.streamCircuitBreakerEnabled, 'streamCircuitBreakerEnabled'),
    streamRequestTimeoutSeconds: numberSetting(settings.streamRequestTimeoutSeconds, 'streamRequestTimeoutSeconds', 10, 3600),
    streamIdleTimeoutSeconds: numberSetting(settings.streamIdleTimeoutSeconds, 'streamIdleTimeoutSeconds', 1, 3600),
    streamClientTotalWaitTimeoutSeconds: numberSetting(settings.streamClientTotalWaitTimeoutSeconds, 'streamClientTotalWaitTimeoutSeconds', 10, 3600),
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

  const statusCode = input.statusCode
  const bodyText = input.bodyText ?? input.errorMessage ?? ''
  const headers = normalizeHeadersInput(input.headers)
  const upstreamSummary = accountErrorPolicyUpstreamSummary(account, bodyText, headers)

  if (statusCode !== undefined) {
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

export async function applyAccountErrorHandlingAsync(
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

  const statusCode = input.statusCode
  const bodyText = input.bodyText ?? input.errorMessage ?? ''
  const headers = normalizeHeadersInput(input.headers)
  const upstreamSummary = accountErrorPolicyUpstreamSummary(account, bodyText, headers)

  if (statusCode !== undefined) {
    const reason = genericUpstreamResponseFailureReason(statusCode, upstreamSummary)
    const updated = await applyAccountTemporaryUnavailableSideEffectAsync(account, reason)
    return {
      action: 'cooldown',
      changed: Boolean(updated),
      accountStatus: updated?.status ?? account.status,
      reason
    }
  }

  const reason = genericUpstreamRequestFailureReason(input.errorMessage ?? bodyText)
  const updated = await applyAccountTemporaryUnavailableSideEffectAsync(account, reason)
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
  void account
  void statusCode
  void headers
  void body
  void settings
  // 账号级策略不能脱离具体客户端画像按上游状态码、错误类型或错误码做业务判断。
  // 上游失败只作为泛化失败信号参与服务端切号、短期避让和事前确认。
  // 明确客户端画像下的可重试语义由 response inspection 管线负责。
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

function applyAccountTemporaryUnavailableSideEffect(
  account: AccountErrorPolicyAccount,
  reason: string
): { status: AccountStatus } | undefined {
  const authorizedTarget = authorizedAccountBindingRuntimeTarget(account)
  return authorizedTarget
    ? markAuthorizedAccountBindingTemporaryUnavailableByContext({ ...authorizedTarget, reason })
    : markAccountTemporaryUnavailable(account.id, reason)
}

async function applyAccountTemporaryUnavailableSideEffectAsync(
  account: AccountErrorPolicyAccount,
  reason: string
): Promise<{ status: AccountStatus } | undefined> {
  const authorizedTarget = authorizedAccountBindingRuntimeTarget(account)
  return authorizedTarget
    ? await markAuthorizedAccountBindingTemporaryUnavailableByContextAsync({ ...authorizedTarget, reason })
    : await markAccountTemporaryUnavailableAsync(account.id, reason)
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
