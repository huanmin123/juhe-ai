import type { AccountSummary } from '../../domain/types.js'
import { isJ1OpenAIProviderCode } from '../../storage/account-health-jobs-input.repository.js'
import { encryptJson } from '../../storage/crypto.js'
import { accountApiKeyEntries } from '../../storage/account-api-key-rotation.js'

import { publishAccountHealthJobsInput, publishAccountHealthJobsRequest } from './account-health-jobs-input.protocol.js'

type FrozenEndpointMode = 'chat_json' | 'responses_json' | 'responses_sse' | 'images_json'

export interface AccountHealthJobsInputSettings {
  intervalHours: number
  jitterMinutes: number
  failureThreshold: number
  failureRetryMs?: number
  cooldownNeutralBaseMs?: number
  cooldownNeutralMaxMs?: number
  cooldownFailureBackoffMs?: number
}

export interface AccountHealthJobsInputSource {
  account: AccountSummary
  dispatchRevision: number
  inputVersion: number
  signingKey: string
  root: string
  settings: AccountHealthJobsInputSettings
  expiresAt: Date
  proxyUrl?: string
  sourceConfigRevision?: number
}

export interface AccountHealthJobsInputTombstone {
  accountId: string
  inputVersion: number
  configRevision: number
  dispatchRevision: number
  signingKey: string
  root: string
  reason: string
}

export interface AccountHealthJobsProbeRequestSource {
  account: Pick<AccountSummary, 'id' | 'configRevision' | 'dispatchRevision'>
  inputVersion: number
  root: string
  signingKey: string
  requestId: string
  reason: string
  deadline: Date
  sourceFence?: {
    stateKey: string
    accountId: string
    sourceGeneration: number
    sourceFenceId: string
    runtimeKey: string
    probeGeneration: number
    configRevision: number
  }
}

export function publishAccountHealthJobsProbeRequest(source: AccountHealthJobsProbeRequestSource): string {
  const accountId = source.account.id.trim()
  const requestId = source.requestId.trim()
  const reason = source.reason.trim()
  if (!accountId || !requestId || !reason) throw new Error('J1 request 缺少 accountId、requestId 或 reason')
  if (!(source.deadline instanceof Date) || !Number.isFinite(source.deadline.getTime()) || source.deadline <= new Date()) {
    throw new Error('J1 request deadline 必须在未来')
  }
  const configRevision = positiveInteger(source.account.configRevision, 'account configRevision')
  const dispatchRevision = positiveInteger(source.account.dispatchRevision, 'account dispatchRevision')
  const payload: Record<string, unknown> = {
    request_id: requestId,
    account_id: accountId,
    reason,
    input_version: positiveInteger(source.inputVersion, 'J1 request inputVersion'),
    config_revision: configRevision,
    dispatch_revision: dispatchRevision,
    deadline: source.deadline.toISOString(),
    mutate_account: source.sourceFence === undefined
  }
  if (source.sourceFence) {
    if (source.sourceFence.accountId !== accountId || source.sourceFence.configRevision !== configRevision) {
      throw new Error('J1 source fence 与账户 revision 不一致')
    }
    payload.source_fence = {
      state_key: requiredText(source.sourceFence.stateKey, 'sourceFence.stateKey'),
      account_id: accountId,
      source_generation: positiveInteger(source.sourceFence.sourceGeneration, 'sourceFence.sourceGeneration'),
      source_fence_id: requiredText(source.sourceFence.sourceFenceId, 'sourceFence.sourceFenceId'),
      runtime_key: requiredText(source.sourceFence.runtimeKey, 'sourceFence.runtimeKey'),
      probe_generation: positiveInteger(source.sourceFence.probeGeneration, 'sourceFence.probeGeneration'),
      config_revision: configRevision
    }
  }
  return publishAccountHealthJobsRequest({ root: source.root, requestId, payload, signingKey: source.signingKey })
}

// BuildAccountHealthJobsInput is deliberately a pure Node-business boundary:
// it may package already-authorized account configuration, but it never
// schedules, retries, calls Go, Gateway, Redis or an upstream. Go validates
// and owns every task decision after this immutable file is published.
export function publishAccountHealthJobsInputFromAccount(source: AccountHealthJobsInputSource): string {
  const account = source.account
  const now = new Date()
  const configRevision = positiveInteger(account.configRevision, 'account configRevision')
  const dispatchRevision = positiveInteger(source.dispatchRevision, 'account dispatchRevision')
  const inputVersion = positiveInteger(source.inputVersion, 'account inputVersion')
  if (!isJ1OpenAIProviderCode(account.providerCode)) throw new Error(`J1 未冻结的 provider：${account.providerCode}`)
  if (account.type !== 'api_key' && account.type !== 'oauth') throw new Error(`J1 未冻结的账户类型：${account.type}`)
  const endpointMode = frozenEndpointMode(account.healthCheckEndpointMode)
  const healthModel = account.healthCheckModel.trim()
  if (!healthModel) throw new Error('J1 healthCheckModel 缺失')

  const sourceConfigRevision = account.accessType === 'authorized'
    ? positiveInteger(account.sourceConfigRevision, 'authorized sourceConfigRevision')
    : undefined
  if (source.sourceConfigRevision !== undefined && source.sourceConfigRevision !== sourceConfigRevision) {
    throw new Error('J1 sourceConfigRevision 与账户 source fence 不一致')
  }
  if (!(source.expiresAt instanceof Date) || !Number.isFinite(source.expiresAt.getTime()) || source.expiresAt <= now) {
    throw new Error('J1 input expiresAt 必须在未来')
  }
  const authorizationEligible = account.accessType !== 'authorized'
    || (account.groupBindStatus === 'bound'
      && account.authorizationStatus === 'active'
      && account.authorizationQuotaExceeded !== true
      && !isExpired(account.authorizationExpiresAt)
      && account.authorizationInstanceSourceAccountStatus === 'active'
      && account.authorizationInstanceSourceAccountSchedulable === true
      && !isExpired(account.authorizationInstanceSourceAccountExpiresAt)
      && !isFuture(account.authorizationInstanceSourceAccountCooldownUntil))
  const cooldownFence = cooldownInputFence(account, sourceConfigRevision, dispatchRevision)
  const cooldownUntil = account.cooldownUntil ? parseDate(account.cooldownUntil) : undefined
  const payload: Record<string, unknown> = {
    account_id: account.id,
    input_version: inputVersion,
    config_revision: configRevision,
    dispatch_revision: dispatchRevision,
    provider: 'openai',
    type: account.type,
    endpoint_mode: endpointMode,
    health_model: healthModel,
    base_url: accountBaseUrl(account),
    issued_at: now.toISOString(),
    expires_at: source.expiresAt.toISOString(),
    tls_policy_version: 'j1-direct-upstream-v1',
    allow_insecure_base_url: false,
    eligibility: {
      account_status: account.status,
      schedulable: account.schedulable,
      bound_group: Boolean(account.boundGroupId),
      authorization_eligible: authorizationEligible,
      ...(sourceConfigRevision === undefined ? {} : { source_config_revision: sourceConfigRevision }),
      ...(cooldownUntil === undefined ? {} : { cooldown_until: cooldownUntil.toISOString() })
    },
    schedule: normalizedSchedule(source.settings)
  }
  if (cooldownFence) payload.cooldown_fence = cooldownFence
  if (source.proxyUrl?.trim()) payload.proxy = { kind: 'proxy_url', ciphertext: encryptJson({ url: source.proxyUrl.trim() }) }
  if (account.type === 'api_key') {
    const entries = accountApiKeyEntries(account.credentials)
    if (!entries.length) throw new Error('J1 API Key account 缺少 Key')
    payload.key_set_fingerprint = accountApiKeyPoolFingerprint(entries.map((entry) => entry.fingerprint))
    payload.api_keys = entries.map((entry) => ({
      index: entry.index,
      fingerprint: entry.fingerprint,
      credential: { kind: 'api_key', ciphertext: encryptJson({ api_key: entry.key }) }
    }))
  } else {
    const accessToken = optionalString(account.credentials.access_token)
    const expiresAt = parseFutureDate(account.credentials.expires_at)
    if (!accessToken || !expiresAt) throw new Error('J1 OAuth account 缺少有效 access token 或 expires_at')
    payload.oauth_access = { kind: 'oauth_access', ciphertext: encryptJson({ access_token: accessToken }) }
    payload.oauth_expires_at = expiresAt.toISOString()
    const oauthAccountId = optionalString(account.credentials.account_id) ?? optionalString(account.credentials.chatgpt_user_id)
    if (oauthAccountId) payload.oauth_account_id = oauthAccountId
  }
  return publishAccountHealthJobsInput({
    root: source.root,
    accountId: account.id,
    payload,
    signingKey: source.signingKey
  })
}

// A tombstone replaces, rather than deletes, the previous input. Deletion can
// leave a Go process with an already-loaded snapshot; a signed ineligible
// version makes that snapshot stale without sending another upstream request.
export function publishAccountHealthJobsInputTombstone(input: AccountHealthJobsInputTombstone): string {
  const now = new Date()
  const payload = {
    account_id: input.accountId.trim(),
    input_version: positiveInteger(input.inputVersion, 'account inputVersion'),
    config_revision: positiveInteger(input.configRevision, 'account configRevision'),
    dispatch_revision: positiveInteger(input.dispatchRevision, 'account dispatchRevision'),
    issued_at: now.toISOString(),
    expires_at: new Date(now.getTime() + 24 * 60 * 60_000).toISOString(),
    eligibility: {
      account_status: 'disabled',
      schedulable: false,
      bound_group: false,
      authorization_eligible: false
    },
    tombstone_reason: input.reason.trim() || 'account_ineligible'
  }
  if (!payload.account_id) throw new Error('J1 tombstone account ID 缺失')
  return publishAccountHealthJobsInput({
    root: input.root,
    accountId: payload.account_id,
    payload,
    signingKey: input.signingKey
  })
}

function frozenEndpointMode(value: string): FrozenEndpointMode {
  if (value === 'chat_json' || value === 'responses_json' || value === 'responses_sse' || value === 'images_json') return value
  throw new Error(`J1 未冻结的探活 endpoint mode：${value}`)
}

function accountBaseUrl(account: AccountSummary): string {
  const configured = optionalString(account.credentials.base_url)
  if (configured) return configured.replace(/\/+$/u, '')
  return account.type === 'oauth' ? 'https://chatgpt.com/backend-api/codex' : 'https://api.openai.com'
}

function normalizedSchedule(settings: AccountHealthJobsInputSettings): Record<string, number> {
  const intervalHours = positiveInteger(settings.intervalHours, 'accountHealthCheckIntervalHours')
  const jitterMinutes = nonNegativeInteger(settings.jitterMinutes, 'accountHealthCheckJitterMinutes')
  return {
    health_interval_ms: intervalHours * 60 * 60 * 1_000,
    health_jitter_ms: jitterMinutes * 60 * 1_000,
    failure_threshold: positiveInteger(settings.failureThreshold, 'accountHealthCheckFailureThreshold'),
    failure_retry_ms: positiveInteger(settings.failureRetryMs ?? 5 * 60_000, 'failureRetryMs'),
    cooldown_neutral_base_ms: positiveInteger(settings.cooldownNeutralBaseMs ?? 30_000, 'cooldownNeutralBaseMs'),
    cooldown_neutral_max_ms: positiveInteger(settings.cooldownNeutralMaxMs ?? 15 * 60_000, 'cooldownNeutralMaxMs'),
    cooldown_failure_backoff_ms: positiveInteger(settings.cooldownFailureBackoffMs ?? 5 * 60_000, 'cooldownFailureBackoffMs')
  }
}

function accountApiKeyPoolFingerprint(fingerprints: string[]): string {
  let hash = 2166136261
  for (const fingerprint of [...fingerprints].sort()) {
    for (const character of fingerprint) {
      hash ^= character.charCodeAt(0)
      hash = Math.imul(hash, 16777619)
    }
    hash ^= 0
    hash = Math.imul(hash, 16777619)
  }
  return (hash >>> 0).toString(16).padStart(8, '0')
}

function positiveInteger(value: unknown, field: string): number {
  if (typeof value !== 'number' || !Number.isInteger(value) || value < 1) throw new Error(`${field} 必须是正整数`)
  return value
}

function nonNegativeInteger(value: unknown, field: string): number {
  if (typeof value !== 'number' || !Number.isInteger(value) || value < 0) throw new Error(`${field} 必须是非负整数`)
  return value
}

function optionalString(value: unknown): string | undefined {
  return typeof value === 'string' && value.trim() ? value.trim() : undefined
}

function requiredText(value: unknown, field: string): string {
  const text = optionalString(value)
  if (!text) throw new Error(`${field} 不能为空`)
  return text
}

function parseFutureDate(value: unknown): Date | undefined {
  const parsed = typeof value === 'string' ? new Date(value) : undefined
  return parsed && Number.isFinite(parsed.getTime()) && parsed > new Date() ? parsed : undefined
}

function isExpired(value: string | undefined): boolean {
  return value !== undefined && new Date(value).getTime() <= Date.now()
}

function isFuture(value: string | undefined): boolean {
  return value !== undefined && new Date(value).getTime() > Date.now()
}

function cooldownInputFence(
  account: AccountSummary,
  sourceConfigRevision: number | undefined,
  dispatchRevision: number
): Record<string, unknown> | undefined {
  if (account.status !== 'temporary_unavailable' && account.status !== 'rate_limited') return undefined
  const observation = requiredText(account.cooldownRetestObservationStartedAt, 'cooldownRetestObservationStartedAt')
  const generation = requiredText(account.cooldownRetestGeneration, 'cooldownRetestGeneration')
  if (account.cooldownRetestDispatchRevision !== undefined && account.cooldownRetestDispatchRevision !== dispatchRevision) {
    throw new Error('J1 cooldown dispatchRevision 与账户 fence 不一致')
  }
  const parsedObservation = parseDate(observation)
  if (!parsedObservation) throw new Error('J1 cooldownRetestObservationStartedAt 无效')
  if (account.cooldownUntil === undefined || !parseDate(account.cooldownUntil)) {
    throw new Error('J1 冷却账户缺少有效 cooldownUntil')
  }
  if (sourceConfigRevision === undefined && account.cooldownRetestSourceConfigRevision !== undefined) {
    throw new Error('J1 owner cooldown 不得携带 sourceConfigRevision')
  }
  if (sourceConfigRevision !== undefined && account.cooldownRetestSourceConfigRevision !== sourceConfigRevision) {
    throw new Error('J1 authorized cooldown sourceConfigRevision 与账户 fence 不一致')
  }
  return {
    observation_started_at: parsedObservation.toISOString(),
    generation,
    ...(sourceConfigRevision === undefined ? {} : { source_config_revision: sourceConfigRevision })
  }
}

function parseDate(value: string): Date | undefined {
  const parsed = new Date(value)
  return Number.isFinite(parsed.getTime()) ? parsed : undefined
}
