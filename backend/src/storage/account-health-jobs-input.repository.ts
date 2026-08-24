import type { AccountSummary } from '../domain/types.js'
import type { AccessScope } from './access-scope.js'
import { findAccountForTest, findAccountForTestAsync } from './repositories.js'
import { resolveOpenAIAccountModelMapping } from '../modules/gateway/protocols/openai-v1/model-mapping.js'
import { getBusinessDatabase } from './database.js'
import { getPostgresPool } from './postgres-client.js'
import { rfc3339InstantMilliseconds } from '../shared/rfc3339.js'

const frozenEndpointModes = new Set([
  'chat_json', 'chat_sse', 'responses_json', 'responses_sse', 'images_json',
  'messages_json', 'messages_sse',
  'generate_content_json', 'generate_content_sse', 'interactions_json', 'interactions_sse'
])
const acceptedStatuses = new Set(['active', 'pending_test', 'temporary_unavailable', 'rate_limited'])
const inputPublisherAccess: AccessScope = { systemAccountId: 'sys_admin', role: 'super_admin' }

export type J1ProbeProtocol = 'openai' | 'anthropic' | 'gemini'

// Legacy helper retained for callers that only need the historical OpenAI-v1
// check. New admission must use resolveJ1AccountHealthProbeProtocol().
export function isJ1OpenAIProviderCode(value: string): boolean {
  return value === 'gpt' || value === 'openai'
}

// This checks the common mode/type vocabulary only. The profile-specific
// resolver below remains the authoritative admission decision.
export function isJ1AccountHealthEndpointModeEligible(accountType: string, endpointMode: string): boolean {
  return frozenEndpointModes.has(endpointMode)
    && (accountType === 'api_key' || accountType === 'oauth' || accountType === 'google_oauth')
}

// The protocol profile, account type and health-check mode are all frozen into
// the J1 input. Do not infer an upstream protocol from a provider code alone:
// hybrid accounts in particular use their selected profile as the dispatch
// contract.
export function resolveJ1AccountHealthProbeProtocol(account: Pick<AccountSummary, 'providerCode' | 'providerProtocolProfileId' | 'protocolCode' | 'protocolVersion' | 'type' | 'healthCheckEndpointMode' | 'healthCheckModel' | 'credentials' | 'modelMappings'>): J1ProbeProtocol | undefined {
  const profile = account.providerProtocolProfileId?.trim()
  const type = account.type
  const mode = account.healthCheckEndpointMode
  const openAIAllModes = mode === 'chat_json' || mode === 'chat_sse' || mode === 'responses_json' || mode === 'responses_sse' || mode === 'images_json'
  const openAIChatModes = mode === 'chat_json' || mode === 'chat_sse'
  const responseModes = mode === 'responses_json' || mode === 'responses_sse'
  const messagesModes = mode === 'messages_json' || mode === 'messages_sse'
  const geminiModes = mode === 'generate_content_json' || mode === 'generate_content_sse' || mode === 'interactions_json' || mode === 'interactions_sse'
  if (!profile) {
    return isJ1OpenAIProviderCode(account.providerCode)
      && (type === 'api_key' || type === 'oauth')
      && (type !== 'oauth' ? openAIAllModes : responseModes)
      ? 'openai'
      : undefined
  }
  const expectedProtocol = profile.startsWith('profile_gemini_') || profile === 'profile_hybrid_gemini_native_v1beta'
    ? { code: 'gemini', version: 'v1beta' }
    : profile.includes('anthropic') || profile === 'profile_hybrid_anthropic_messages_v1'
      ? { code: 'anthropic', version: 'v1' }
      : { code: 'openai', version: 'v1' }
  if ((account.protocolCode && account.protocolCode !== expectedProtocol.code) || (account.protocolVersion && account.protocolVersion !== expectedProtocol.version)) return undefined
  switch (profile) {
    case 'profile_gpt_openai_v1':
      return account.providerCode === 'gpt' && (type === 'api_key' ? openAIAllModes : type === 'oauth' && responseModes) ? 'openai' : undefined
    case 'profile_openai_openai_v1':
      return account.providerCode === 'openai' && type === 'api_key' && openAIAllModes ? 'openai' : undefined
    case 'profile_xai_openai_v1':
      return account.providerCode === 'xai' && (type === 'api_key' ? openAIAllModes : type === 'oauth' && responseModes) ? 'openai' : undefined
    case 'profile_deepseek_openai_v1':
      return account.providerCode === 'deepseek' && type === 'api_key' && openAIAllModes ? 'openai' : undefined
    case 'profile_glm_general_openai_v1':
    case 'profile_glm_coding_openai_v1':
      return account.providerCode === 'glm' && type === 'api_key' && openAIChatModes ? 'openai' : undefined
    case 'profile_gemini_openai_chat_v1beta':
      return account.providerCode === 'gemini' && type === 'api_key' && openAIChatModes ? 'openai' : undefined
    case 'profile_hybrid_openai_chat_v1':
      if (account.providerCode !== 'hybrid' || type !== 'api_key') return undefined
      return resolveHybridJ1Protocol(account, mode, account.healthCheckModel)
    case 'profile_hybrid_anthropic_messages_v1':
      return account.providerCode === 'hybrid' && type === 'api_key' && messagesModes ? 'anthropic' : undefined
    case 'profile_hybrid_gemini_native_v1beta':
      return account.providerCode === 'hybrid' && type === 'api_key' && geminiModes ? 'gemini' : undefined
    case 'profile_anthropic_anthropic_v1':
      return account.providerCode === 'anthropic' && (type === 'api_key' || type === 'oauth') && messagesModes ? 'anthropic' : undefined
    case 'profile_deepseek_anthropic_v1':
      return account.providerCode === 'deepseek' && type === 'api_key' && messagesModes ? 'anthropic' : undefined
    case 'profile_glm_coding_anthropic_v1':
      return account.providerCode === 'glm' && type === 'api_key' && messagesModes ? 'anthropic' : undefined
    case 'profile_gemini_native_v1beta':
      if (account.providerCode !== 'gemini' || (type !== 'api_key' && type !== 'google_oauth') || !geminiModes) return undefined
      if (type === 'google_oauth' && (account.credentials.oauth_type === 'code_assist' || account.credentials.oauth_type === 'google_one') && !mode.startsWith('generate_content_')) return undefined
      return 'gemini'
    default:
      return undefined
  }
}

function resolveHybridJ1Protocol(account: Pick<AccountSummary, 'providerCode' | 'providerProtocolProfileId' | 'modelMappings'>, mode: string, model: string | undefined): J1ProbeProtocol | undefined {
  const sourceEndpointFamily = mode === 'chat_json' || mode === 'chat_sse'
    ? 'chat_completions'
    : mode === 'responses_json' || mode === 'responses_sse'
      ? 'responses'
      : mode === 'messages_json' || mode === 'messages_sse'
        ? 'messages'
        : mode === 'generate_content_json'
          ? 'generate_content'
          : mode === 'generate_content_sse'
            ? 'stream_generate_content'
          : undefined
  const sourceModel = typeof model === 'string' ? model.trim() : ''
  if (!sourceEndpointFamily || !sourceModel) return undefined
  const mapping = resolveOpenAIAccountModelMapping(account, sourceModel, sourceEndpointFamily)
  if (!mapping) return undefined
  if (mapping.upstreamEndpointFamily === 'chat_completions' || mapping.upstreamEndpointFamily === 'responses') return 'openai'
  if (mapping.upstreamEndpointFamily === 'messages') return 'anthropic'
  if (mapping.upstreamEndpointFamily === 'generate_content') return 'gemini'
  return undefined
}

export interface AccountHealthJobsInputRevisions {
  configRevision: number
  dispatchRevision: number
}

// Read adapter for immutable J1 input production.  This is intentionally not a
// due-candidate scan and has no health state mutation or scheduling side effect.
export function findAccountForAccountHealthJobsInput(accountId: string): AccountSummary | undefined {
  // AccountSummary intentionally redacts authorized-source secrets. The J1
  // publisher is an internal owner and must use the same audited secret read
  // boundary as account tests, otherwise authorized inputs would be emitted
  // without credentials.
  const account = findAccountForTest(accountId, inputPublisherAccess)
  return isEligibleForAccountHealthJobsInput(account) ? account : undefined
}

export async function findAccountForAccountHealthJobsInputAsync(accountId: string): Promise<AccountSummary | undefined> {
  const account = await findAccountForTestAsync(accountId, inputPublisherAccess)
  return isEligibleForAccountHealthJobsInput(account) ? account : undefined
}

// AccountSummary deliberately omits dispatchRevision from its management DTO.
// The input producer still needs both business fences before it publishes an
// immutable snapshot, so this narrow read adapter keeps that internal field
// out of the public summary while preserving the outbox CAS contract.
export function findAccountHealthJobsInputRevisions(accountId: string): AccountHealthJobsInputRevisions | undefined {
  const id = accountId.trim()
  if (!id) return undefined
  const row = getBusinessDatabase().prepare(`
    SELECT config_revision, dispatch_revision
    FROM accounts
    WHERE id = ? AND deleted_at IS NULL
  `).get(id) as { config_revision?: number, dispatch_revision?: number } | undefined
  return normalizeRevisions(row?.config_revision, row?.dispatch_revision)
}

export async function findAccountHealthJobsInputRevisionsAsync(accountId: string): Promise<AccountHealthJobsInputRevisions | undefined> {
  const id = accountId.trim()
  if (!id) return undefined
  const pool = await getPostgresPool()
  const result = await pool.query(`
    SELECT config_revision, dispatch_revision
    FROM juhe_business.accounts
    WHERE id = $1 AND deleted_at IS NULL
  `, [id])
  const row = result.rows[0] as { config_revision?: number, dispatch_revision?: number } | undefined
  return normalizeRevisions(row?.config_revision, row?.dispatch_revision)
}

function isEligibleForAccountHealthJobsInput(account: AccountSummary | undefined): account is AccountSummary {
  if (!account) return false
  if (!resolveJ1AccountHealthProbeProtocol(account)) return false
  if (!acceptedStatuses.has(account.status)) return false
  if (account.status !== 'pending_test' && !account.schedulable) return false
  if (account.accountExpiresAt && (rfc3339InstantMilliseconds(account.accountExpiresAt) === undefined || rfc3339InstantMilliseconds(account.accountExpiresAt)! <= Date.now())) return false
  if (!account.boundGroupId || account.groupBindStatus !== 'bound') return false
  if (account.accessType !== 'authorized') return true
  if (!account.accountAuthorizationId || !account.bindingSystemAccountId) return false
  if (account.authorizationStatus !== 'active' || account.authorizationQuotaExceeded) return false
  if (account.authorizationExpiresAt && (rfc3339InstantMilliseconds(account.authorizationExpiresAt) === undefined || rfc3339InstantMilliseconds(account.authorizationExpiresAt)! <= Date.now())) return false
  if (account.authorizationInstanceSourceAccountStatus !== 'active') return false
  if (!account.authorizationInstanceSourceAccountSchedulable) return false
  if (account.authorizationInstanceSourceAccountLastErrorCode === 'account_expired') return false
  if (account.authorizationInstanceSourceAccountExpiresAt && (rfc3339InstantMilliseconds(account.authorizationInstanceSourceAccountExpiresAt) === undefined || rfc3339InstantMilliseconds(account.authorizationInstanceSourceAccountExpiresAt)! <= Date.now())) return false
  if (account.authorizationInstanceSourceAccountCooldownUntil && (rfc3339InstantMilliseconds(account.authorizationInstanceSourceAccountCooldownUntil) === undefined || rfc3339InstantMilliseconds(account.authorizationInstanceSourceAccountCooldownUntil)! > Date.now())) return false
  return true
}

function normalizeRevisions(configRevision: unknown, dispatchRevision: unknown): AccountHealthJobsInputRevisions | undefined {
  const config = positiveSafeInteger(configRevision)
  const dispatch = positiveSafeInteger(dispatchRevision)
  if (config === undefined || dispatch === undefined) return undefined
  return { configRevision: config, dispatchRevision: dispatch }
}

function positiveSafeInteger(value: unknown): number | undefined {
  const parsed = typeof value === 'number'
    ? value
    : typeof value === 'bigint'
      ? Number(value)
      : typeof value === 'string' && /^\d+$/u.test(value)
        ? Number(value)
        : Number.NaN
  return Number.isSafeInteger(parsed) && parsed >= 1 ? parsed : undefined
}
