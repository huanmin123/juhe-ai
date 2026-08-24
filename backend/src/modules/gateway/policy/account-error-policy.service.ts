import type { AccountStatus } from '../../../domain/types.js'
import {
  EXPLICIT_ACCOUNT_ERROR_POLICY_COOLDOWN_CODE,
  SYSTEM_QUOTA_EXPLICIT_RESET_COOLDOWN_CODE,
  SYSTEM_QUOTA_GENERIC_COOLDOWN_CODE,
} from '../../../domain/account-runtime-provenance.js'
import { runtimeConfig } from '../../../config/runtime.js'
import {
  markAccountCooldown,
  markAccountCooldownAsync,
  markAccountDisabledByFailure,
  markAccountDisabledByFailureAsync,
  markAuthorizedAccountBindingCooldownByContext,
  markAuthorizedAccountBindingCooldownByContextAsync,
  markAuthorizedAccountBindingDisabledByFailure,
  markAuthorizedAccountBindingDisabledByFailureAsync,
  recordAccountRuntimeSuccessObservation,
  recordAccountRuntimeSuccessObservationAsync,
  type AccountRuntimeFailureObservationGuard,
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
import {
  SYSTEM_INSUFFICIENT_QUOTA_ERROR_POLICY_RULE_ID,
  systemInsufficientQuotaRuleMatches
} from '../../accounts/account-error-policy-system-rules.js'
import {
  normalizeQuotaRecoveryPolicy,
  quotaRecoveryCooldownUntil,
  type QuotaRecoveryPolicy
} from '../../accounts/quota-recovery-policy.js'
import { requiredRfc3339Instant } from '../../../shared/rfc3339.js'
import { isAccountApiKeyPoolIsolationEnabled } from '../../../storage/account-api-key-rotation.js'
import {
  extractApiKeyQuotaRecoveryHint,
  type ApiKeyQuotaRecoveryHint,
  type ApiKeyQuotaRecoveryMode
} from './api-key-quota-recovery.js'

export type CooldownAccountStatus = 'rate_limited' | 'temporary_unavailable'

export interface GatewaySettings {
  gatewayTextRawBodyLimitMegabytes: number
  accountCircuitConfirmationFailuresRequired: number
  gatewayUserRequestLimitPerMinute?: number
  gatewayUserRequestLimitPerDay?: number
  gatewayUserRequestLimitPerWeek?: number
  gatewayUserRequestLimitPerMonth?: number
  usageStatsTimezone?: string
  defaultTemporaryUnschedulableMinutes: number
  temporaryUnschedulableRetryIntervalSeconds: number
  temporaryUnschedulableRetryAttempts: number
  streamCircuitBreakerEnabled: boolean
  textFirstResponseTimeoutSeconds: number
  textStreamIdleTimeoutSeconds: number
  textUncommittedAttemptMaxLifetimeSeconds: number
  imageFirstResponseTimeoutSeconds: number
  imageStreamIdleTimeoutSeconds: number
  imageUncommittedAttemptMaxLifetimeSeconds: number
  imageRequestWallTimeoutSeconds: number
  noAvailableAccountWaitTimeoutSeconds: number
  streamFailureThresholdCount: number
  streamFailureThresholdWindowMinutes: number
}

export interface AccountErrorPolicyAccount {
  id: string
  dispatchRevision?: number
  providerCode: string
  protocolCode?: string
  protocolVersion?: string
  type?: string
  credentials: Record<string, unknown>
  apiKeys?: string[]
  selectedApiKeyFingerprint?: string
  selectedApiKeyRecoveryStartedAt?: string
  accountAccessType?: 'owner' | 'account_authorized' | 'group_authorized'
  bindingSystemAccountId?: string
  groupOwnerSystemAccountId?: string
  boundGroupId?: string
  accountAuthorizationId?: string
  status?: AccountStatus
  cooldownUntil?: string
  lastErrorCode?: string
  lastErrorMessage?: string
  streamFailureCount?: number
  streamFailureWindowStartedAt?: string
  quotaRecoveryGeneration?: string
}

export interface AccountErrorPolicyDecision {
  action: 'retry_next' | 'cooldown' | 'disable'
  ruleName?: string
  ruleId?: string
  ruleSource?: 'system' | 'account'
  cooldownUntil?: string
  cooldownStatus?: CooldownAccountStatus
  keyScoped?: boolean
  quotaRecoveryMode?: ApiKeyQuotaRecoveryMode
  quotaRecoveryHintSource?: ApiKeyQuotaRecoveryHint['source']
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
    accountCircuitConfirmationFailuresRequired: numberSetting(settings.accountCircuitConfirmationFailuresRequired, 'accountCircuitConfirmationFailuresRequired', 1, 5),
    gatewayUserRequestLimitPerMinute: numberSetting(settings.gatewayUserRequestLimitPerMinute, 'gatewayUserRequestLimitPerMinute', 0, 1_000_000_000),
    gatewayUserRequestLimitPerDay: numberSetting(settings.gatewayUserRequestLimitPerDay, 'gatewayUserRequestLimitPerDay', 0, 1_000_000_000),
    gatewayUserRequestLimitPerWeek: numberSetting(settings.gatewayUserRequestLimitPerWeek, 'gatewayUserRequestLimitPerWeek', 0, 1_000_000_000),
    gatewayUserRequestLimitPerMonth: numberSetting(settings.gatewayUserRequestLimitPerMonth, 'gatewayUserRequestLimitPerMonth', 0, 1_000_000_000),
    usageStatsTimezone: typeof settings.usageStatsTimezone === 'string' && settings.usageStatsTimezone.trim()
      ? settings.usageStatsTimezone.trim()
      : 'UTC',
    defaultTemporaryUnschedulableMinutes: numberSetting(settings.defaultTemporaryUnschedulableMinutes, 'defaultTemporaryUnschedulableMinutes', 1, 1440),
    temporaryUnschedulableRetryIntervalSeconds: numberSetting(settings.temporaryUnschedulableRetryIntervalSeconds, 'temporaryUnschedulableRetryIntervalSeconds', 0, 3600),
    temporaryUnschedulableRetryAttempts: numberSetting(settings.temporaryUnschedulableRetryAttempts, 'temporaryUnschedulableRetryAttempts', 0, 10),
    streamCircuitBreakerEnabled: true,
    textFirstResponseTimeoutSeconds: numberSetting(settings.textFirstResponseTimeoutSeconds, 'textFirstResponseTimeoutSeconds', 10, 3600),
    textStreamIdleTimeoutSeconds: numberSetting(settings.textStreamIdleTimeoutSeconds, 'textStreamIdleTimeoutSeconds', 1, 3600),
    textUncommittedAttemptMaxLifetimeSeconds: numberSetting(settings.textUncommittedAttemptMaxLifetimeSeconds, 'textUncommittedAttemptMaxLifetimeSeconds', 60, 86400),
    imageFirstResponseTimeoutSeconds: numberSetting(settings.imageFirstResponseTimeoutSeconds, 'imageFirstResponseTimeoutSeconds', 10, 3600),
    imageStreamIdleTimeoutSeconds: numberSetting(settings.imageStreamIdleTimeoutSeconds, 'imageStreamIdleTimeoutSeconds', 1, 3600),
    imageUncommittedAttemptMaxLifetimeSeconds: numberSetting(settings.imageUncommittedAttemptMaxLifetimeSeconds, 'imageUncommittedAttemptMaxLifetimeSeconds', 60, 86400),
    imageRequestWallTimeoutSeconds: numberSetting(settings.imageRequestWallTimeoutSeconds, 'imageRequestWallTimeoutSeconds', 60, 86400),
    noAvailableAccountWaitTimeoutSeconds: numberSetting(settings.noAvailableAccountWaitTimeoutSeconds, 'noAvailableAccountWaitTimeoutSeconds', 10, 3600),
    streamFailureThresholdCount: numberSetting(settings.streamFailureThresholdCount, 'streamFailureThresholdCount', 1, 100),
    streamFailureThresholdWindowMinutes: numberSetting(settings.streamFailureThresholdWindowMinutes, 'streamFailureThresholdWindowMinutes', 1, 1440)
  }
}

export async function readGatewaySettingsAsync(): Promise<GatewaySettings> {
  if (runtimeConfig.databaseDriver !== 'postgres') {
    return withGatewaySettingsLocalDatabaseAccess(() => readGatewaySettings())
  }
  const settings = await getSettingsAsync()
  return gatewaySettingsFromRawSettings(settings)
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
    upstreamErrorSummary?: string
    upstreamErrorSummaryResolved?: boolean
    traceId?: string
    observedAt?: string
    dispatchRevision?: number
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
    const observation = accountRuntimeSuccessObservationInput(account, input)
    if (observation) {
      const result = recordAccountRuntimeSuccessObservation(observation)
      return { action: 'none', changed: result.changed, accountStatus: result.accountStatus ?? account.status }
    }
    return { action: 'none', changed: false, accountStatus: account.status }
  }

  if (!input.policyDecision) return { action: 'none', changed: false, accountStatus: account.status }
  if (input.policyDecision.keyScoped) {
    return {
      action: 'cooldown',
      changed: false,
      accountStatus: account.status,
      reason: 'api_key_key_scoped_quota_recovery'
    }
  }
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
    upstreamErrorSummary?: string
    upstreamErrorSummaryResolved?: boolean
    traceId?: string
    observedAt?: string
    dispatchRevision?: number
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
    const observation = accountRuntimeSuccessObservationInput(account, input)
    if (observation) {
      const result = await recordAccountRuntimeSuccessObservationAsync(observation)
      return { action: 'none', changed: result.changed, accountStatus: result.accountStatus ?? account.status }
    }
    return { action: 'none', changed: false, accountStatus: account.status }
  }

  if (!input.policyDecision) return { action: 'none', changed: false, accountStatus: account.status }
  if (input.policyDecision.keyScoped) {
    return {
      action: 'cooldown',
      changed: false,
      accountStatus: account.status,
      reason: 'api_key_key_scoped_quota_recovery'
    }
  }
  return await applyExplicitAccountErrorPolicyDecisionAsync(account, input, input.policyDecision)
}

export function decideAccountErrorPolicy(
  account: AccountErrorPolicyAccount,
  statusCode: number,
  headers: Headers,
  body: Buffer,
  settings: GatewaySettings,
  bodyFacts?: {
    bodyText: string
    errorPayload: Record<string, unknown>
  }
): AccountErrorPolicyDecision | undefined {
  if (statusCode >= 200 && statusCode <= 299) return undefined
  const bodyText = bodyFacts?.bodyText ?? body.toString('utf8')
  const payload = bodyFacts?.errorPayload ?? accountErrorPolicyPayload(bodyText, headers, account)
  const errorCode = stringValue(payload.code).toLowerCase()
  const errorType = stringValue(payload.type).toLowerCase()
  const systemSearchableText = [stringValue(payload.message), bodyText]
    .filter((value): value is string => Boolean(value?.trim()))
    .join('\n')
    .toLowerCase()
  if (systemInsufficientQuotaRuleMatches({ statusCode, errorCode, errorType, searchableText: systemSearchableText })) {
    const apiKeyGenericRecovery = account.type === 'api_key'
    const recoveryHint = apiKeyGenericRecovery
      ? extractApiKeyQuotaRecoveryHint({ bodyText, headers })
      : undefined
    const apiKeyPoolIsolation = isAccountApiKeyPoolIsolationEnabled({
      providerCode: account.providerCode,
      protocolCode: account.protocolCode,
      protocolVersion: account.protocolVersion,
      type: account.type,
      credentials: account.apiKeys?.length
        ? { ...account.credentials, api_keys: account.apiKeys }
        : account.credentials
    }) && Boolean(account.selectedApiKeyFingerprint)
    const recoveryMode = apiKeyGenericRecovery
      ? recoveryHint?.mode ?? 'generic'
      : recoveryHint?.mode
    const configuredPolicy = quotaRecoveryPolicyFromCredentials(account.credentials)
    const recoveryAccountType = account.type === 'api_key' ? 'api_key' : account.type === 'google_oauth' ? 'google_oauth' : 'oauth'
    // Runtime recovery timestamps are mutable observations, not a new policy
    // generation. Keep the seed identical to the background retest seed so
    // gateway and worker decisions do not move the due time between stages.
    const recoverySeed = [
      account.id,
      account.selectedApiKeyFingerprint?.trim() || 'account'
    ].join(':')
    const cooldownUntil = recoveryHint?.cooldownUntil
      ?? quotaRecoveryCooldownUntil({
        policy: configuredPolicy,
        accountType: recoveryAccountType,
        seed: recoverySeed
      })
    return {
      action: 'cooldown',
      ruleId: SYSTEM_INSUFFICIENT_QUOTA_ERROR_POLICY_RULE_ID,
      ruleName: '上游额度不足',
      ruleSource: 'system',
      cooldownStatus: 'rate_limited',
      cooldownUntil: cooldownUntil
        ?? accountErrorRuleCooldownUntil({
          enabled: true,
          name: '上游额度不足',
          priority: 1,
          action: 'rate_limited',
          reset_strategy: 'daily',
          daily_reset_hour: 0
        }, new Date()),
      keyScoped: apiKeyPoolIsolation,
      quotaRecoveryMode: recoveryMode,
      quotaRecoveryHintSource: recoveryHint?.source
    }
  }
  const rules = normalizeAccountErrorHandlingRules(account.credentials.error_handling_rules)
    .filter((rule) => rule.enabled)
    .sort((left, right) => left.priority - right.priority)
  const searchableText = bodyText.toLowerCase()
  const rule = rules.find((candidate) => accountErrorRuleMatches(candidate, statusCode, errorCode, errorType, searchableText))
  if (!rule) return undefined
  if (rule.action === 'retry_next') return { action: 'retry_next', ruleName: rule.name, ruleSource: 'account' }
  if (rule.action === 'error_disabled') return { action: 'disable', ruleName: rule.name, ruleSource: 'account' }
  if (rule.action === 'rate_limited') {
    return {
      action: 'cooldown',
      ruleName: rule.name,
      ruleSource: 'account',
      cooldownStatus: 'rate_limited',
      cooldownUntil: accountErrorRuleCooldownUntil(rule, new Date())
    }
  }
  return {
    action: 'cooldown',
    ruleName: rule.name,
    ruleSource: 'account',
    cooldownStatus: 'temporary_unavailable',
    cooldownUntil: new Date(Date.now() + settings.defaultTemporaryUnschedulableMinutes * 60_000).toISOString()
  }
}

function quotaRecoveryPolicyFromCredentials(credentials: Record<string, unknown>): QuotaRecoveryPolicy | undefined {
  if (!Object.prototype.hasOwnProperty.call(credentials, 'quota_recovery_policy')) return undefined
  return normalizeQuotaRecoveryPolicy(credentials.quota_recovery_policy)
}

export function accountErrorPolicyCouldMatchStatus(
  account: AccountErrorPolicyAccount,
  statusCode: number
): boolean {
  if (statusCode >= 200 && statusCode <= 299) return false
  if (statusCode === 403) return true
  return normalizeAccountErrorHandlingRules(account.credentials.error_handling_rules)
    .some((rule) => rule.enabled && (!rule.status_codes?.length || rule.status_codes.includes(statusCode)))
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
    upstreamErrorSummary?: string
    upstreamErrorSummaryResolved?: boolean
    traceId?: string
    observedAt?: string
    dispatchRevision?: number
  },
  decision: AccountErrorPolicyDecision
): AccountErrorHandlingResult {
  if (decision.action === 'retry_next') {
    return { action: 'retry_next', changed: false, accountStatus: account.status }
  }
  const reason = explicitAccountErrorPolicyReason(account, input, decision)
  const authorizedTarget = authorizedAccountBindingRuntimeTarget(account)
  const runtimeFailureGuard = accountRuntimeFailureObservationGuard(account, input)
  const failureCode = decision.ruleSource === 'system'
    ? decision.quotaRecoveryMode === 'explicit_reset'
      ? SYSTEM_QUOTA_EXPLICIT_RESET_COOLDOWN_CODE
      : SYSTEM_QUOTA_GENERIC_COOLDOWN_CODE
    : EXPLICIT_ACCOUNT_ERROR_POLICY_COOLDOWN_CODE
  const updated = decision.action === 'disable'
    ? authorizedTarget
      ? markAuthorizedAccountBindingDisabledByFailure({ ...authorizedTarget, reason, runtimeFailureGuard })
      : markAccountDisabledByFailure(account.id, reason, runtimeFailureGuard)
    : authorizedTarget
      ? markAuthorizedAccountBindingCooldownByContext({
          ...authorizedTarget,
          status: decision.cooldownStatus,
          cooldownUntil: decision.cooldownUntil,
          reason,
          traceId: input.traceId,
          failureCode,
          runtimeFailureGuard
        })
      : markAccountCooldown(
          account.id,
          decision.cooldownUntil,
          reason,
          decision.cooldownStatus,
          undefined,
          input.traceId,
          undefined,
          runtimeFailureGuard,
          failureCode
        )
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
    upstreamErrorSummary?: string
    upstreamErrorSummaryResolved?: boolean
    traceId?: string
    observedAt?: string
    dispatchRevision?: number
  },
  decision: AccountErrorPolicyDecision
): Promise<AccountErrorHandlingResult> {
  if (decision.action === 'retry_next') {
    return { action: 'retry_next', changed: false, accountStatus: account.status }
  }
  const reason = explicitAccountErrorPolicyReason(account, input, decision)
  const authorizedTarget = authorizedAccountBindingRuntimeTarget(account)
  const runtimeFailureGuard = accountRuntimeFailureObservationGuard(account, input)
  const failureCode = decision.ruleSource === 'system'
    ? decision.quotaRecoveryMode === 'explicit_reset'
      ? SYSTEM_QUOTA_EXPLICIT_RESET_COOLDOWN_CODE
      : SYSTEM_QUOTA_GENERIC_COOLDOWN_CODE
    : EXPLICIT_ACCOUNT_ERROR_POLICY_COOLDOWN_CODE
  const updated = decision.action === 'disable'
    ? authorizedTarget
      ? await markAuthorizedAccountBindingDisabledByFailureAsync({ ...authorizedTarget, reason, runtimeFailureGuard })
      : await markAccountDisabledByFailureAsync(account.id, reason, runtimeFailureGuard)
    : authorizedTarget
      ? await markAuthorizedAccountBindingCooldownByContextAsync({
          ...authorizedTarget,
          status: decision.cooldownStatus,
          cooldownUntil: decision.cooldownUntil,
          reason,
          traceId: input.traceId,
          failureCode,
          runtimeFailureGuard
        })
      : await markAccountCooldownAsync(
          account.id,
          decision.cooldownUntil,
          reason,
          decision.cooldownStatus,
          undefined,
          input.traceId,
          undefined,
          runtimeFailureGuard,
          failureCode
        )
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
    upstreamErrorSummary?: string
    upstreamErrorSummaryResolved?: boolean
  },
  decision: AccountErrorPolicyDecision
): string {
  const bodyText = input.bodyText ?? input.errorMessage ?? ''
  const statusCode = input.statusCode
  const upstreamSummary = input.upstreamErrorSummaryResolved === true
    ? input.upstreamErrorSummary
    : input.upstreamErrorSummary
      ?? accountErrorPolicyUpstreamSummary(account, bodyText, normalizeHeadersInput(input.headers))
  const failure = statusCode === undefined
    ? genericUpstreamRequestFailureReason(input.errorMessage ?? bodyText)
    : genericUpstreamResponseFailureReason(statusCode, upstreamSummary)
  const policyLabel = decision.ruleSource === 'system' ? '系统继承错误策略' : '账户错误策略'
  return `${policyLabel}「${decision.ruleName ?? '未命名规则'}」命中；${failure}`.slice(0, 1000)
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

function accountRuntimeSuccessObservationInput(
  account: AccountErrorPolicyAccount,
  input: {
    observedAt?: string
    dispatchRevision?: number
    trafficSource?: OpenAIGatewayTrafficSource
  }
) {
  const observedAt = normalizedRuntimeObservationAt(input.observedAt)
  const expectedDispatchRevision = normalizedRuntimeDispatchRevision(input.dispatchRevision ?? account.dispatchRevision)
  if (!observedAt || !expectedDispatchRevision) return undefined
  return {
    accountId: account.id,
    expectedDispatchRevision,
    observedAt,
    authorizedBinding: authorizedAccountBindingRuntimeTarget(account)
  }
}

function accountRuntimeFailureObservationGuard(
  account: AccountErrorPolicyAccount,
  input: { observedAt?: string; dispatchRevision?: number }
): AccountRuntimeFailureObservationGuard | undefined {
  const observedAt = normalizedRuntimeObservationAt(input.observedAt)
  const expectedDispatchRevision = normalizedRuntimeDispatchRevision(input.dispatchRevision ?? account.dispatchRevision)
  if (!observedAt || !expectedDispatchRevision) return undefined
  return { expectedDispatchRevision, observedAt }
}

function normalizedRuntimeObservationAt(value: string | undefined): string | undefined {
  if (value === undefined) return undefined
  return requiredRfc3339Instant(value, '账户运行态 observedAt')
}

function normalizedRuntimeDispatchRevision(value: number | undefined): number | undefined {
  return Number.isSafeInteger(value) && (value ?? 0) > 0 ? value : undefined
}

export function parseErrorPayload(
  text: string,
  headers: Headers,
  profile?: { protocolCode?: string; protocolVersion?: string }
): Record<string, unknown> {
  return parseGatewayProtocolErrorPayload(profile, text, headers)
}

export function accountErrorPayloadSummary(errorPayload: Record<string, unknown>): string | undefined {
  const parts: string[] = []
  const code = stringValue(errorPayload.code)
  const message = stringValue(errorPayload.message)
  if (code) parts.push(sanitizeDiagnosticPayload(code))
  if (message && message !== code) parts.push(sanitizeDiagnosticPayload(message))
  return parts.length > 0 ? parts.join('；') : undefined
}

function accountErrorPolicyUpstreamSummary(
  profile: { protocolCode?: string; protocolVersion?: string } | undefined,
  bodyText: string,
  headers: Headers
): string | undefined {
  return accountErrorPayloadSummary(parseErrorPayload(bodyText, headers, profile))
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
