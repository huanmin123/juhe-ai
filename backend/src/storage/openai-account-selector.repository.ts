import type { AccountStatus, AccountType } from '../domain/types.js'
import { buildSystemAccountScopeClause, currentSystemAccountId } from './access-scope.js'
import { decryptJson } from './crypto.js'
import { getDatabase, nowIso } from './database.js'
import { resolveProxyUrlForProfile } from './proxy.repository.js'
import { refreshGroupAccountStatsCache } from './usage-stats.repository.js'

export interface OpenAIAccountSecret {
  id: string
  systemAccountId: string
  accountOwnerSystemAccountId: string
  groupOwnerSystemAccountId: string
  accountAccessType: 'owner' | 'account_authorized' | 'group_authorized'
  groupAccessType: 'owner' | 'authorized'
  accountAuthorizationId?: string
  groupAuthorizationId?: string
  name: string
  type: AccountType
  status: AccountStatus
  baseUrl: string
  apiKey: string
  refreshToken?: string
  clientId?: string
  proxyUrl?: string
  passthroughEnabled: boolean
  errorPolicyId?: string
  cooldownUntil?: string
  lastErrorMessage?: string
  expiresAt?: string
  credentials: Record<string, unknown>
}

export interface GroupUsageAccessMetadata {
  groupOwnerSystemAccountId: string
  groupAccessType: 'owner' | 'authorized'
  groupAuthorizationId?: string
}

interface GroupAccountRow {
  account_id: string
  account_authorization_id?: string | null
}

type ResourceAuthorizationRow = {
  id: string
}

export function selectOpenAIAccountForGroup(groupId: string, systemAccountId = currentSystemAccountId()): OpenAIAccountSecret | undefined {
  return listOpenAIAccountsForGroup(groupId, systemAccountId)[0]
}

export function resolveGroupUsageAccessMetadata(groupId: string, systemAccountId: string): GroupUsageAccessMetadata | undefined {
  const group = groupOwnerAndProvider(groupId)
  if (!group) return undefined
  if (group.systemAccountId === systemAccountId) {
    return { groupOwnerSystemAccountId: group.systemAccountId, groupAccessType: 'owner' }
  }
  const authorization = activeResourceAuthorization('group', groupId, systemAccountId)
  if (!authorization) return undefined
  return {
    groupOwnerSystemAccountId: group.systemAccountId,
    groupAccessType: 'authorized',
    groupAuthorizationId: authorization.id
  }
}

export function listOpenAIAccountsForGroup(groupId: string, systemAccountId = currentSystemAccountId()): OpenAIAccountSecret[] {
  const database = getDatabase()
  const now = nowIso()
  const groupAccess = resolveGroupUsageAccessMetadata(groupId, systemAccountId)
  if (!groupAccess) {
    return []
  }
  disableExpiredAccountsForSelection(groupAccess.groupOwnerSystemAccountId)
  const groupAccountRows = database
    .prepare(`
      SELECT group_accounts.account_id, group_accounts.account_authorization_id
      FROM group_accounts
      INNER JOIN accounts ON accounts.id = group_accounts.account_id
      WHERE group_accounts.group_id = ? AND group_accounts.enabled = 1
      ORDER BY accounts.priority ASC, group_accounts.weight DESC, group_accounts.created_at ASC
    `)
    .all(groupId) as unknown as GroupAccountRow[]

  const accounts: OpenAIAccountSecret[] = []
  for (const groupAccount of groupAccountRows) {
    const row = database
      .prepare(`
        SELECT id, system_account_id, name, type, status, credentials_encrypted, proxy_profile_id, passthrough_enabled, error_policy_id, cooldown_until, last_error_message
        FROM accounts
        WHERE id = ?
          AND provider_code = 'openai'
          AND type IN ('api_key', 'oauth')
          AND schedulable = 1
          AND (account_expires_at IS NULL OR account_expires_at > ?)
          AND status = 'active'
          AND (cooldown_until IS NULL OR cooldown_until <= ?)
      `)
      .get(groupAccount.account_id, now, now) as unknown as { id: string; system_account_id: string; name: string; type: AccountType; status: AccountStatus; credentials_encrypted: string; proxy_profile_id: string | null; passthrough_enabled: number; error_policy_id: string | null; cooldown_until: string | null; last_error_message: string | null } | undefined
    if (!row) {
      continue
    }
    const credentials = decryptJson<Record<string, unknown>>(row.credentials_encrypted)
    const apiKey = row.type === 'oauth'
      ? typeof credentials.access_token === 'string' ? credentials.access_token : ''
      : typeof credentials.api_key === 'string' ? credentials.api_key : ''
    if (!apiKey) {
      continue
    }
    const accountAccess = resolveOpenAIAccountAccess(row.id, row.system_account_id, systemAccountId, groupAccess, groupAccount.account_authorization_id ?? undefined)
    if (!accountAccess) {
      continue
    }
    if (!canScheduleAuthorizedAccount({
      accountId: row.id,
      accountAccessType: accountAccess.accountAccessType,
      authorizationId: accountAccess.accountAuthorizationId,
      systemAccountId
    })) {
      continue
    }
    accounts.push({
      id: row.id,
      systemAccountId: row.system_account_id,
      accountOwnerSystemAccountId: row.system_account_id,
      groupOwnerSystemAccountId: groupAccess.groupOwnerSystemAccountId,
      accountAccessType: accountAccess.accountAccessType,
      groupAccessType: groupAccess.groupAccessType,
      accountAuthorizationId: accountAccess.accountAuthorizationId,
      groupAuthorizationId: groupAccess.groupAuthorizationId,
      name: row.name,
      type: row.type,
      status: row.status,
      baseUrl: typeof credentials.base_url === 'string' && credentials.base_url ? credentials.base_url : 'https://api.openai.com/v1',
      apiKey,
      refreshToken: typeof credentials.refresh_token === 'string' ? credentials.refresh_token : undefined,
      clientId: typeof credentials.client_id === 'string' ? credentials.client_id : undefined,
      proxyUrl: resolveProxyUrlForProfile(row.proxy_profile_id),
      passthroughEnabled: row.passthrough_enabled === 1,
      errorPolicyId: row.error_policy_id ?? undefined,
      cooldownUntil: row.cooldown_until ?? undefined,
      lastErrorMessage: row.last_error_message ?? undefined,
      expiresAt: typeof credentials.expires_at === 'string' ? credentials.expires_at : undefined,
      credentials
    })
  }

  return accounts
}

function resolveOpenAIAccountAccess(
  accountId: string,
  accountOwnerSystemAccountId: string,
  callerSystemAccountId: string,
  groupAccess: GroupUsageAccessMetadata,
  boundAccountAuthorizationId?: string
): { accountAccessType: 'owner' | 'account_authorized' | 'group_authorized'; accountAuthorizationId?: string } | undefined {
  if (accountOwnerSystemAccountId === callerSystemAccountId) {
    return { accountAccessType: 'owner' }
  }
  if (groupAccess.groupAccessType === 'authorized') {
    return accountOwnerSystemAccountId === groupAccess.groupOwnerSystemAccountId
      ? { accountAccessType: 'group_authorized' }
      : undefined
  }
  const authorization = activeResourceAuthorization('account', accountId, callerSystemAccountId)
  if (boundAccountAuthorizationId && authorization?.id !== boundAccountAuthorizationId) {
    return undefined
  }
  return authorization ? { accountAccessType: 'account_authorized', accountAuthorizationId: authorization.id } : undefined
}

function canScheduleAuthorizedAccount(input: {
  accountId: string
  accountAccessType: 'owner' | 'account_authorized' | 'group_authorized'
  authorizationId?: string
  systemAccountId: string
}): boolean {
  if (input.accountAccessType === 'owner' || input.accountAccessType === 'group_authorized') {
    return true
  }
  if (!input.authorizationId) {
    return false
  }
  const authorization = activeResourceAuthorization('account', input.accountId, input.systemAccountId)
  return authorization?.id === input.authorizationId
}

function groupOwnerAndProvider(groupId: string): { systemAccountId: string } | undefined {
  const row = getDatabase().prepare('SELECT system_account_id FROM groups WHERE id = ?').get(groupId) as unknown as { system_account_id?: string } | undefined
  return row?.system_account_id ? { systemAccountId: row.system_account_id } : undefined
}

function activeResourceAuthorization(resourceType: 'account' | 'group', resourceId: string, granteeSystemAccountId: string): ResourceAuthorizationRow | undefined {
  const now = nowIso()
  return getDatabase()
    .prepare("SELECT id FROM resource_authorizations WHERE resource_type = ? AND resource_id = ? AND grantee_system_account_id = ? AND status = 'active' AND (expires_at IS NULL OR expires_at > ?) LIMIT 1")
    .get(resourceType, resourceId, granteeSystemAccountId, now) as unknown as ResourceAuthorizationRow | undefined
}

function disableExpiredAccountsForSelection(systemAccountId: string): void {
  const scope = buildSystemAccountScopeClause({ systemAccountId, role: 'user' })
  const now = nowIso()
  const result = getDatabase()
    .prepare(`
      UPDATE accounts
      SET status = 'disabled',
          schedulable = 0,
          cooldown_until = NULL,
          last_error_message = ?,
          updated_at = ?
      WHERE account_expires_at IS NOT NULL
        AND account_expires_at <= ?
        AND (
          status <> 'disabled'
          OR schedulable <> 0
          OR cooldown_until IS NOT NULL
          OR last_error_message IS NULL
        )${scope.clause}
    `)
    .run('账户套餐已过期，已自动停用', now, now, ...scope.params)
  if (Number(result.changes ?? 0) > 0) {
    refreshGroupAccountStatsCache()
  }
}
