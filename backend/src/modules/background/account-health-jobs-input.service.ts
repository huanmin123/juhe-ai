import type { AccountSummary } from '../../domain/types.js'
import { resolveJ1AccountHealthProbeProtocol } from '../../storage/account-health-jobs-input.repository.js'
import { resolveOpenAIAccountModelMapping } from '../gateway/protocols/openai-v1/model-mapping.js'
import { encryptJson } from '../../storage/crypto.js'
import { accountApiKeyEntries } from '../../storage/account-api-key-rotation.js'
import { parseRfc3339Instant, requiredRfc3339Instant } from '../../shared/rfc3339.js'

import { publishAccountHealthJobsInput, publishAccountHealthJobsRequest } from './account-health-jobs-input.protocol.js'
import type { KeyModelFenceReference } from '../gateway/runtime/key-model-redis-store.js'

type FrozenEndpointMode =
  | 'chat_json' | 'chat_sse' | 'responses_json' | 'responses_sse' | 'images_json'
  | 'messages_json' | 'messages_sse'
  | 'generate_content_json' | 'generate_content_sse' | 'interactions_json' | 'interactions_sse'

export interface AccountHealthJobsInputSettings {
  intervalHours: number
  jitterMinutes: number
  failureThreshold: number
  failureRetryMs?: number
  cooldownNeutralBaseMs?: number
  cooldownNeutralMaxMs?: number
  cooldownFailureBackoffMs?: number
  maxPauseMinutes?: number
  maxRecoveryHours?: number
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
  keyModelFence?: KeyModelFenceReference
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
  if (source.keyModelFence) {
    if (source.keyModelFence.dispatchRevision !== dispatchRevision || !/^[a-f0-9]{64}$/u.test(source.keyModelFence.capabilityHash) || !source.keyModelFence.keyFingerprint.trim() || !source.keyModelFence.ownerId.trim()) {
      throw new Error('J1 key-model fence 无效')
    }
    payload.key_model_fence = {
      capability_hash: source.keyModelFence.capabilityHash,
      key_fingerprint: source.keyModelFence.keyFingerprint.trim(),
      dispatch_revision: source.keyModelFence.dispatchRevision,
      owner_id: source.keyModelFence.ownerId.trim()
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
  const resolvedProtocol = resolveJ1AccountHealthProbeProtocol(account)
  if (!resolvedProtocol) throw new Error(`J1 未冻结的协议 profile/type/endpoint mode：${account.providerProtocolProfileId ?? account.providerCode}/${account.type}/${account.healthCheckEndpointMode}`)
  const endpointMode = frozenEndpointMode(account.healthCheckEndpointMode)
  const healthModel = account.healthCheckModel.trim()
  if (!healthModel) throw new Error('J1 healthCheckModel 缺失')
  const probeTarget = resolveJ1ProbeTarget(account, resolvedProtocol, endpointMode, healthModel)

  const sourceConfigRevision = account.accessType === 'authorized'
    ? positiveInteger(account.sourceConfigRevision, 'authorized sourceConfigRevision')
    : undefined
  if (source.sourceConfigRevision !== undefined && source.sourceConfigRevision !== sourceConfigRevision) {
    throw new Error('J1 sourceConfigRevision 与账户 source fence 不一致')
  }
  const authorizationExpiresAt = optionalRfc3339Instant(account.authorizationExpiresAt, 'J1 authorizationExpiresAt')
  const authorizationInstanceSourceAccountExpiresAt = optionalRfc3339Instant(
    account.authorizationInstanceSourceAccountExpiresAt,
    'J1 authorizationInstanceSourceAccountExpiresAt'
  )
  const authorizationInstanceSourceAccountCooldownUntil = optionalRfc3339Instant(
    account.authorizationInstanceSourceAccountCooldownUntil,
    'J1 authorizationInstanceSourceAccountCooldownUntil'
  )
  if (!(source.expiresAt instanceof Date) || !Number.isFinite(source.expiresAt.getTime()) || source.expiresAt <= now) {
    throw new Error('J1 input expiresAt 必须在未来')
  }
  const authorizationEligible = account.accessType !== 'authorized'
    || (account.groupBindStatus === 'bound'
      && account.authorizationStatus === 'active'
      && account.authorizationQuotaExceeded !== true
      && !isExpired(authorizationExpiresAt)
      && account.authorizationInstanceSourceAccountStatus === 'active'
      && account.authorizationInstanceSourceAccountSchedulable === true
      && !isExpired(authorizationInstanceSourceAccountExpiresAt)
      && !isFuture(authorizationInstanceSourceAccountCooldownUntil))
  const cooldownFence = cooldownInputFence(account, sourceConfigRevision, dispatchRevision)
  const cooldownUntil = optionalRfc3339Instant(account.cooldownUntil, 'J1 cooldownUntil')
  const payload: Record<string, unknown> = {
    account_id: account.id,
    input_version: inputVersion,
    config_revision: configRevision,
    dispatch_revision: dispatchRevision,
    provider: probeTarget.protocol,
    ...(account.providerProtocolProfileId ? { provider_protocol_profile_id: account.providerProtocolProfileId } : {}),
    ...(account.protocolCode ? { protocol_code: account.protocolCode } : {}),
    ...(account.protocolVersion ? { protocol_version: account.protocolVersion } : {}),
    type: account.type,
    endpoint_mode: probeTarget.endpointMode,
    health_model: probeTarget.model,
    base_url: accountBaseUrl(account, probeTarget.protocol),
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
      ...(cooldownUntil === undefined ? {} : { cooldown_until: cooldownUntil }),
	  temporary_unavailable_continuous_probe_enabled: account.temporaryUnavailableContinuousProbeEnabled !== false
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
    const expiresAt = parseFutureDate(account.credentials.expires_at, 'J1 OAuth credentials.expires_at')
    if (!accessToken || !expiresAt) throw new Error('J1 OAuth account 缺少有效 access token 或 expires_at')
    payload.oauth_access = { kind: 'oauth_access', ciphertext: encryptJson({ access_token: accessToken }) }
    payload.oauth_expires_at = expiresAt.toISOString()
    const oauthAccountId = optionalString(account.credentials.account_id) ?? optionalString(account.credentials.chatgpt_user_id)
    if (oauthAccountId) payload.oauth_account_id = oauthAccountId
    const quotaProjectId = optionalString(account.credentials.quota_project_id)
    if (quotaProjectId) payload.oauth_quota_project_id = quotaProjectId
    const oauthType = optionalString(account.credentials.oauth_type)
    if (oauthType) payload.oauth_type = oauthType
    const projectId = optionalString(account.credentials.project_id)
    if (projectId) payload.oauth_project_id = projectId
  }
  return publishAccountHealthJobsInput({
    root: source.root,
    accountId: account.id,
    payload,
    signingKey: source.signingKey
  })
}

function resolveJ1ProbeTarget(
  account: AccountSummary,
  protocol: 'openai' | 'anthropic' | 'gemini',
  sourceMode: FrozenEndpointMode,
  sourceModel: string
): { protocol: 'openai' | 'anthropic' | 'gemini'; endpointMode: FrozenEndpointMode; model: string } {
  if (account.providerProtocolProfileId !== 'profile_hybrid_openai_chat_v1') {
    return { protocol, endpointMode: sourceMode, model: sourceModel }
  }
  const sourceFamily = sourceEndpointFamilyForJ1Mode(sourceMode)
  if (!sourceFamily) throw new Error(`J1 hybrid 账户的健康检查模式无法解析为模型映射源协议：${sourceMode}`)
  const mapping = resolveOpenAIAccountModelMapping(account, sourceModel, sourceFamily)
  if (!mapping) throw new Error(`J1 hybrid 账户缺少 ${sourceModel}/${sourceFamily} 的冻结模型映射`)
  const targetProtocol = protocolForJ1EndpointFamily(mapping.upstreamEndpointFamily)
  if (!targetProtocol) throw new Error(`J1 hybrid 账户的目标协议不受支持：${mapping.upstreamEndpointFamily}`)
  return {
    protocol: targetProtocol,
    endpointMode: endpointModeForJ1TargetFamily(mapping.upstreamEndpointFamily, sourceMode),
    model: mapping.upstreamModel
  }
}

function sourceEndpointFamilyForJ1Mode(mode: FrozenEndpointMode): 'chat_completions' | 'responses' | 'messages' | 'generate_content' | 'stream_generate_content' | undefined {
  if (mode === 'chat_json' || mode === 'chat_sse') return 'chat_completions'
  if (mode === 'responses_json' || mode === 'responses_sse') return 'responses'
  if (mode === 'messages_json' || mode === 'messages_sse') return 'messages'
  if (mode === 'generate_content_json') return 'generate_content'
  if (mode === 'generate_content_sse') return 'stream_generate_content'
  return undefined
}

function protocolForJ1EndpointFamily(family: string): 'openai' | 'anthropic' | 'gemini' | undefined {
  if (family === 'chat_completions' || family === 'responses') return 'openai'
  if (family === 'messages') return 'anthropic'
  if (family === 'generate_content') return 'gemini'
  return undefined
}

function endpointModeForJ1TargetFamily(family: string, sourceMode: FrozenEndpointMode): FrozenEndpointMode {
  const stream = sourceMode === 'chat_sse' || sourceMode === 'responses_sse' || sourceMode === 'messages_sse' || sourceMode === 'generate_content_sse' || sourceMode === 'interactions_sse'
  if (family === 'chat_completions') return stream ? 'chat_sse' : 'chat_json'
  if (family === 'responses') return stream ? 'responses_sse' : 'responses_json'
  if (family === 'messages') return stream ? 'messages_sse' : 'messages_json'
  if (family === 'generate_content') return stream ? 'generate_content_sse' : 'generate_content_json'
  throw new Error(`J1 hybrid 账户目标 endpoint family 不受支持：${family}`)
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
  if (
    value === 'chat_json' || value === 'chat_sse' || value === 'responses_json' || value === 'responses_sse' || value === 'images_json'
    || value === 'messages_json' || value === 'messages_sse'
    || value === 'generate_content_json' || value === 'generate_content_sse' || value === 'interactions_json' || value === 'interactions_sse'
  ) return value
  throw new Error(`J1 未冻结的探活 endpoint mode：${value}`)
}

function accountBaseUrl(account: AccountSummary, protocol: 'openai' | 'anthropic' | 'gemini'): string {
  // GPT OAuth is a Codex credential, not an OpenAI API-key credential. The
  // OAuth writer historically stores api.openai.com/v1 as a compatibility
  // field, but J1 must always use the Codex backend endpoint for this profile.
  if ((account.providerProtocolProfileId === 'profile_gpt_openai_v1' || (!account.providerProtocolProfileId && account.providerCode === 'gpt')) && account.type === 'oauth') {
    return 'https://chatgpt.com/backend-api/codex'
  }
  const configured = optionalString(account.credentials.base_url)
  if (configured) return configured.replace(/\/+$/u, '')
  if (account.providerProtocolProfileId === 'profile_hybrid_openai_chat_v1') {
    throw new Error('J1 hybrid 账户缺少冻结的 base_url')
  }
  switch (account.providerProtocolProfileId) {
    case 'profile_gpt_openai_v1':
      return account.type === 'oauth' ? 'https://chatgpt.com/backend-api/codex' : 'https://api.openai.com'
    case 'profile_xai_openai_v1':
      return account.type === 'oauth' ? 'https://cli-chat-proxy.grok.com/v1' : 'https://api.x.ai/v1'
    case 'profile_deepseek_openai_v1':
      return 'https://api.deepseek.com'
    case 'profile_glm_general_openai_v1':
      return 'https://open.bigmodel.cn/api/paas/v4'
    case 'profile_glm_coding_openai_v1':
      return 'https://open.bigmodel.cn/api/coding/paas/v4'
    case 'profile_glm_coding_anthropic_v1':
      return 'https://open.bigmodel.cn/api/anthropic'
    case 'profile_deepseek_anthropic_v1':
      return 'https://api.deepseek.com/anthropic'
    case 'profile_anthropic_anthropic_v1':
    case 'profile_hybrid_anthropic_messages_v1':
      return 'https://api.anthropic.com'
    case 'profile_gemini_native_v1beta':
      return account.type === 'google_oauth' && (account.credentials.oauth_type === 'code_assist' || account.credentials.oauth_type === 'google_one')
        ? 'https://cloudcode-pa.googleapis.com'
        : 'https://generativelanguage.googleapis.com'
    case 'profile_gemini_openai_chat_v1beta':
      return 'https://generativelanguage.googleapis.com/v1beta/openai'
    case 'profile_hybrid_gemini_native_v1beta':
      return 'https://generativelanguage.googleapis.com'
    default:
      return protocol === 'anthropic'
        ? 'https://api.anthropic.com'
        : protocol === 'gemini'
          ? 'https://generativelanguage.googleapis.com'
          : account.type === 'oauth'
            ? 'https://chatgpt.com/backend-api/codex'
            : 'https://api.openai.com'
  }
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
    cooldown_failure_backoff_ms: positiveInteger(settings.cooldownFailureBackoffMs ?? 3_000, 'cooldownFailureBackoffMs'),
    max_pause_minutes: positiveInteger(settings.maxPauseMinutes ?? 2, 'maxPauseMinutes'),
    max_recovery_hours: positiveInteger(settings.maxRecoveryHours ?? 12, 'maxRecoveryHours')
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

function parseFutureDate(value: unknown, label: string): Date | undefined {
  const text = optionalString(value)
  if (text === undefined) return undefined
  const parsed = parseRfc3339Instant(requiredRfc3339Instant(text, label))
  return parsed && parsed > new Date() ? parsed : undefined
}

function isExpired(value: string | undefined): boolean {
  return value !== undefined && new Date(requiredRfc3339Instant(value, 'J1 authorization expiry')).getTime() <= Date.now()
}

function isFuture(value: string | undefined): boolean {
  return value !== undefined && new Date(requiredRfc3339Instant(value, 'J1 authorization cooldownUntil')).getTime() > Date.now()
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
  const normalizedObservation = requiredRfc3339Instant(observation, 'J1 cooldownRetestObservationStartedAt')
  if (account.cooldownUntil === undefined) {
    throw new Error('J1 冷却账户缺少有效 cooldownUntil')
  }
  requiredRfc3339Instant(account.cooldownUntil, 'J1 cooldownUntil')
  if (sourceConfigRevision === undefined && account.cooldownRetestSourceConfigRevision !== undefined) {
    throw new Error('J1 owner cooldown 不得携带 sourceConfigRevision')
  }
  if (sourceConfigRevision !== undefined && account.cooldownRetestSourceConfigRevision !== sourceConfigRevision) {
    throw new Error('J1 authorized cooldown sourceConfigRevision 与账户 fence 不一致')
  }
  return {
    observation_started_at: normalizedObservation,
    generation,
    ...(sourceConfigRevision === undefined ? {} : { source_config_revision: sourceConfigRevision })
  }
}

function optionalRfc3339Instant(value: unknown, label: string): string | undefined {
  return value === undefined ? undefined : requiredRfc3339Instant(value, label)
}
