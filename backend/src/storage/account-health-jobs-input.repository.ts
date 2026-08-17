import type { AccountSummary } from '../domain/types.js'
import type { AccessScope } from './access-scope.js'
import { findAccountSummary, findAccountSummaryAsync } from './account-summary.repository.js'
import { getBusinessDatabase } from './database.js'
import { getPostgresPool } from './postgres-client.js'

const frozenEndpointModes = new Set(['chat_json', 'responses_json', 'images_json'])
const acceptedStatuses = new Set(['active', 'pending_test', 'temporary_unavailable', 'rate_limited'])
const inputPublisherAccess: AccessScope = { systemAccountId: 'sys_admin', role: 'super_admin' }

export interface AccountHealthJobsInputRevisions {
  configRevision: number
  dispatchRevision: number
}

// Read adapter for immutable J1 input production.  This is intentionally not a
// due-candidate scan and has no health state mutation or scheduling side effect.
export function findAccountForAccountHealthJobsInput(accountId: string): AccountSummary | undefined {
  const account = findAccountSummary(accountId, inputPublisherAccess)
  return isEligibleForAccountHealthJobsInput(account) ? account : undefined
}

export async function findAccountForAccountHealthJobsInputAsync(accountId: string): Promise<AccountSummary | undefined> {
  const account = await findAccountSummaryAsync(accountId, inputPublisherAccess)
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
  if (account.providerCode !== 'openai') return false
  if (account.type !== 'api_key' && account.type !== 'oauth') return false
  if (!frozenEndpointModes.has(account.healthCheckEndpointMode)) return false
  if (!acceptedStatuses.has(account.status)) return false
  if (account.status !== 'pending_test' && !account.schedulable) return false
  if (account.accountExpiresAt && Date.parse(account.accountExpiresAt) <= Date.now()) return false
  if (!account.boundGroupId || account.groupBindStatus !== 'bound') return false
  if (account.accessType !== 'authorized') return true
  if (!account.accountAuthorizationId || !account.bindingSystemAccountId) return false
  if (account.authorizationStatus !== 'active' || account.authorizationQuotaExceeded) return false
  if (account.authorizationExpiresAt && Date.parse(account.authorizationExpiresAt) <= Date.now()) return false
  if (account.authorizationInstanceSourceAccountStatus !== 'active') return false
  if (!account.authorizationInstanceSourceAccountSchedulable) return false
  if (account.authorizationInstanceSourceAccountExpiresAt && Date.parse(account.authorizationInstanceSourceAccountExpiresAt) <= Date.now()) return false
  if (account.authorizationInstanceSourceAccountCooldownUntil && Date.parse(account.authorizationInstanceSourceAccountCooldownUntil) > Date.now()) return false
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
