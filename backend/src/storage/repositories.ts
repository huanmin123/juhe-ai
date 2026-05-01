import type { AccountStatus, AccountSummary, AccountType, ApiKeySummary, GroupSummary, ProviderCode, ProviderDefinition } from '../domain/types.js'
import { createApiKey, decryptJson, encryptJson, hashSecret, maskSecret } from './crypto.js'
import { getDatabase, newId, nowIso } from './database.js'

interface ProviderRow {
  id: string
  code: ProviderCode
  name: string
  enabled: number
  base_url: string
  account_types_json: string
  capabilities_json: string
}

interface AccountRow {
  id: string
  provider_code: ProviderCode
  name: string
  notes: string | null
  type: AccountType
  status: AccountStatus
  credential_mask: string
  credentials_encrypted: string
  proxy_profile_id: string | null
  concurrency_limit: number
  passthrough_enabled: number
  error_policy_id: string | null
  priority: number
  schedulable: number
  last_used_at: string | null
  cooldown_until: string | null
  last_error_message: string | null
  stream_failure_count: number
  stream_failure_window_started_at: string | null
}

interface AccountFailureRow {
  id: string
  stream_failure_count: number
  stream_failure_window_started_at: string | null
}

interface GatewayApiKeyRow {
  id: string
  group_id: string
  status: 'active' | 'disabled'
  expires_at: string | null
}

interface GroupAccountRow {
  account_id: string
}

interface GroupRow {
  id: string
  name: string
  description: string | null
  enabled: number
}

interface ApiKeyRow {
  id: string
  name: string
  key_prefix: string
  key_secret_encrypted: string | null
  status: 'active' | 'disabled'
  group_id: string
  expires_at: string | null
}

interface ProxyRow {
  id: string
  name: string
  type: string
  host: string
  port: number
  username: string | null
  password_encrypted?: string | null
  enabled: number
  test_status: string
  last_tested_at: string | null
}

export interface ProxyProfileSummary {
  id: string
  name: string
  type: string
  host: string
  port: number
  username?: string
  enabled: boolean
  testStatus: string
  lastTestedAt?: string
}

export interface UsageRecordSummary {
  id: string
  requestId: string
  apiKeyId?: string
  groupId?: string
  accountId?: string
  providerCode?: string
  model?: string
  stream: boolean
  statusCode?: number
  success: boolean
  durationMs?: number
  inputTokens?: number
  outputTokens?: number
  cacheReadTokens?: number
  costUsd?: number
  errorCode?: string
  errorMessage?: string
  createdAt: string
}

export interface OpenAIAccountSecret {
  id: string
  name: string
  type: AccountType
  baseUrl: string
  apiKey: string
  refreshToken?: string
  clientId?: string
  proxyUrl?: string
  expiresAt?: string
  credentials: Record<string, unknown>
}

export interface AccountUsageSummary {
  requestCount: number
  clientCount: number
  inputTokens: number
  outputTokens: number
  cacheReadTokens: number
  totalTokens: number
  totalCost: number
  lastUsedAt?: string
}

export interface MigrationAccountInput {
  name: string
  description?: string
  baseUrl: string
  apiKey: string
}

export interface MigrationOAuthAccountInput {
  name: string
  description?: string
  accessToken: string
  refreshToken: string
  idToken?: string
  expiresAt?: string
  clientId?: string
  email?: string
  chatgptAccountId?: string
  chatgptUserId?: string
  organizationId?: string
  planType?: string
  proxyProfileId?: string
}

interface AccountUsageAggregateRow {
  account_id: string
  request_count: number
  client_count: number
  input_tokens: number
  output_tokens: number
  cache_read_tokens: number
  total_cost: number
  last_used_at: string | null
}

export interface MigrationResult {
  imported: number
  skipped: number
  importedApiKey?: number
  importedOAuth?: number
  skippedApiKey?: number
  skippedOAuth?: number
  accountIds: string[]
  apiKey?: string
  gatewayApiKeyId?: string
  gatewayApiKeyName?: string
  gatewayApiKeyCreated?: boolean
  groupId: string
  groupName: string
}

function parseJsonArray(value: string): string[] {
  const parsed = JSON.parse(value) as unknown
  return Array.isArray(parsed) ? parsed.map(String) : []
}

function optionalString(value: unknown): string | undefined {
  return typeof value === 'string' && value.length > 0 ? value : undefined
}

function boolInt(value: unknown, fallback: boolean): number {
  return typeof value === 'boolean' ? (value ? 1 : 0) : fallback ? 1 : 0
}

function runDelete(sql: string, id: string): boolean {
  const result = getDatabase().prepare(sql).run(id)
  return result.changes > 0
}

function accountFingerprint(providerCode: string, type: string, baseUrl: string, secret: string): string {
  return hashSecret(`${providerCode}:${type}:${baseUrl.trim().replace(/\/+$/, '')}:${secret.trim()}`)
}

export function listProviders(): ProviderDefinition[] {
  const rows = getDatabase().prepare('SELECT * FROM providers ORDER BY name ASC').all() as unknown as ProviderRow[]
  return rows.map((row) => ({
    id: row.id,
    code: row.code,
    name: row.name,
    enabled: row.enabled === 1,
    baseUrl: row.base_url,
    accountTypes: parseJsonArray(row.account_types_json) as AccountType[],
    capabilities: parseJsonArray(row.capabilities_json)
  }))
}

export function listAccounts(): AccountSummary[] {
  const rows = getDatabase().prepare('SELECT * FROM accounts ORDER BY updated_at DESC').all() as unknown as AccountRow[]
  const usageByAccount = loadAccountUsageSummaries()
  return rows.map((row) => ({
    id: row.id,
    providerCode: row.provider_code,
    name: row.name,
    notes: row.notes ?? undefined,
    type: row.type,
    credentials: decryptJson<Record<string, unknown>>(row.credentials_encrypted),
    status: row.status,
    concurrencyLimit: row.concurrency_limit,
    currentConcurrency: 0,
    priority: row.priority,
    proxyProfileId: row.proxy_profile_id ?? undefined,
    passthroughEnabled: row.passthrough_enabled === 1,
    errorPolicyId: row.error_policy_id ?? undefined,
    schedulable: row.schedulable === 1,
    cooldownUntil: row.cooldown_until ?? undefined,
    lastErrorMessage: row.last_error_message ?? undefined,
    lastUsedAt: row.last_used_at ?? usageByAccount.get(row.id)?.lastUsedAt,
    usage: usageByAccount.get(row.id) ?? {
      requestCount: 0,
      clientCount: 0,
      inputTokens: 0,
      outputTokens: 0,
      cacheReadTokens: 0,
      totalTokens: 0,
      totalCost: 0
    }
  }))
}

export function createAccount(input: Record<string, unknown>): AccountSummary {
  const now = nowIso()
  const id = newId('acc')
  const credentials = typeof input.credentials === 'object' && input.credentials !== null ? input.credentials as Record<string, unknown> : {}
  const credentialMap = credentials as Record<string, unknown>
  const accountType = input.type === 'oauth' ? 'oauth' : 'api_key'
  const credentialSource = accountType === 'oauth'
    ? credentialMap.refresh_token ?? credentialMap.access_token ?? ''
    : credentialMap.api_key ?? ''
  const baseUrl = String(credentialMap.base_url ?? 'https://api.openai.com/v1')
  const credentialFingerprint = typeof credentialSource === 'string' && credentialSource.trim()
    ? accountFingerprint('openai', accountType, baseUrl, credentialSource)
    : null
  const account: AccountSummary = {
    id,
    providerCode: 'openai',
    name: String(input.name ?? '未命名 OpenAI 账户'),
    notes: optionalString(input.notes),
    type: accountType,
    credentials,
    status: input.status === 'disabled' || input.status === 'error' ? input.status : 'active',
    concurrencyLimit: Number(input.concurrencyLimit ?? input.concurrency_limit ?? 1),
    currentConcurrency: 0,
    priority: Number(input.priority ?? input.prioritiy ?? input.priority_level ?? 0),
    proxyProfileId: optionalString(input.proxyProfileId ?? input.proxy_profile_id),
    passthroughEnabled: Boolean(input.passthroughEnabled ?? input.passthrough_enabled),
    errorPolicyId: optionalString(input.errorPolicyId ?? input.error_policy_id),
    schedulable: input.schedulable !== false,
    cooldownUntil: undefined,
    lastErrorMessage: undefined,
    lastUsedAt: undefined,
    usage: {
      requestCount: 0,
      clientCount: 0,
      inputTokens: 0,
      outputTokens: 0,
      cacheReadTokens: 0,
      totalTokens: 0,
      totalCost: 0
    }
  }

  getDatabase()
    .prepare(`
      INSERT INTO accounts (
        id, provider_code, name, type, status, credentials_encrypted, credential_fingerprint, credential_mask,
        proxy_profile_id, concurrency_limit, passthrough_enabled, error_policy_id,
        priority, schedulable, notes, cooldown_until, last_error_message, stream_failure_count, stream_failure_window_started_at, created_at, updated_at
      ) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
    `)
    .run(
      account.id,
      account.providerCode,
      account.name,
      account.type,
      account.status,
      encryptJson(credentials),
      credentialFingerprint,
      maskSecret(credentialSource),
      account.proxyProfileId ?? null,
      account.concurrencyLimit,
      account.passthroughEnabled ? 1 : 0,
      account.errorPolicyId ?? null,
      account.priority,
      account.schedulable ? 1 : 0,
      optionalString(input.notes) ?? null,
      null,
      null,
      0,
      null,
      now,
      now
    )

  return account
}

export function findAccountByFingerprint(providerCode: string, type: string, baseUrl: string, apiKey: string): AccountSummary | undefined {
  const fingerprint = accountFingerprint(providerCode, type, baseUrl, apiKey)
  const row = getDatabase().prepare('SELECT id FROM accounts WHERE credential_fingerprint = ?').get(fingerprint) as unknown as { id?: string } | undefined
  if (!row?.id) {
    return undefined
  }
  return listAccounts().find((account) => account.id === row.id)
}

export function updateAccount(id: string, input: Record<string, unknown>): AccountSummary | undefined {
  const current = listAccounts().find((account) => account.id === id)
  if (!current) {
    return undefined
  }

  const credentials = typeof input.credentials === 'object' && input.credentials !== null
    ? input.credentials as Record<string, unknown>
    : current.credentials
  const credentialSource = current.type === 'oauth'
    ? credentials.refresh_token ?? credentials.access_token ?? ''
    : credentials.api_key ?? ''
  const baseUrl = String(credentials.base_url ?? 'https://api.openai.com/v1')
  const credentialFingerprint = typeof credentialSource === 'string' && credentialSource.trim()
    ? accountFingerprint(current.providerCode, current.type, baseUrl, credentialSource)
    : null

  const next: AccountSummary = {
    ...current,
    name: typeof input.name === 'string' ? input.name : current.name,
    notes: optionalString(input.notes) ?? current.notes,
    credentials,
    status: input.status === 'active' || input.status === 'disabled' || input.status === 'error' ? input.status : current.status,
    concurrencyLimit: Number(input.concurrencyLimit ?? input.concurrency_limit ?? current.concurrencyLimit),
    priority: Number(input.priority ?? input.prioritiy ?? input.priority_level ?? current.priority),
    proxyProfileId: optionalString(input.proxyProfileId ?? input.proxy_profile_id) ?? current.proxyProfileId,
    passthroughEnabled: typeof input.passthroughEnabled === 'boolean' ? input.passthroughEnabled : current.passthroughEnabled,
    errorPolicyId: optionalString(input.errorPolicyId ?? input.error_policy_id) ?? current.errorPolicyId,
    schedulable: typeof input.schedulable === 'boolean' ? input.schedulable : current.schedulable,
    cooldownUntil: current.cooldownUntil,
    lastErrorMessage: current.lastErrorMessage,
    lastUsedAt: current.lastUsedAt,
    usage: current.usage
  }

  getDatabase()
    .prepare(`
      UPDATE accounts
      SET name = ?, notes = ?, status = ?, credentials_encrypted = ?, credential_fingerprint = ?, credential_mask = ?,
          proxy_profile_id = ?, concurrency_limit = ?, passthrough_enabled = ?,
          error_policy_id = ?, priority = ?, schedulable = ?, updated_at = ?
      WHERE id = ?
    `)
    .run(
      next.name,
      next.notes ?? null,
      next.status,
      encryptJson(credentials),
      credentialFingerprint,
      maskSecret(credentialSource),
      next.proxyProfileId ?? null,
      next.concurrencyLimit,
      next.passthroughEnabled ? 1 : 0,
      next.errorPolicyId ?? null,
      next.priority,
      next.schedulable ? 1 : 0,
      nowIso(),
      id
    )

  return next
}

export function deleteAccount(id: string): boolean {
  const result = getDatabase().prepare('DELETE FROM accounts WHERE id = ?').run(id)
  return result.changes > 0
}

export function clearAccountFailureState(id: string): AccountSummary | undefined {
  const current = listAccounts().find((account) => account.id === id)
  if (!current) {
    return undefined
  }

  getDatabase()
    .prepare(`
      UPDATE accounts
      SET cooldown_until = NULL,
          last_error_message = NULL,
          stream_failure_count = 0,
          stream_failure_window_started_at = NULL,
          updated_at = ?
      WHERE id = ?
    `)
    .run(nowIso(), id)

  return listAccounts().find((account) => account.id === id)
}

export function markAccountCooldown(id: string, until: string, reason: string): AccountSummary | undefined {
  const current = listAccounts().find((account) => account.id === id)
  if (!current) {
    return undefined
  }

  getDatabase()
    .prepare(`
      UPDATE accounts
      SET cooldown_until = ?,
          last_error_message = ?,
          stream_failure_count = 0,
          stream_failure_window_started_at = NULL,
          updated_at = ?
      WHERE id = ?
    `)
    .run(until, reason || null, nowIso(), id)

  return listAccounts().find((account) => account.id === id)
}

export function markAccountDisabledByFailure(id: string, reason: string): AccountSummary | undefined {
  const current = listAccounts().find((account) => account.id === id)
  if (!current) {
    return undefined
  }

  getDatabase()
    .prepare(`
      UPDATE accounts
      SET status = 'error',
          schedulable = 0,
          cooldown_until = NULL,
          last_error_message = ?,
          stream_failure_count = 0,
          stream_failure_window_started_at = NULL,
          updated_at = ?
      WHERE id = ?
    `)
    .run(reason || null, nowIso(), id)

  return listAccounts().find((account) => account.id === id)
}

export function recordAccountStreamFailure(input: {
  accountId: string
  thresholdCount: number
  thresholdWindowMinutes: number
  action: 'cooldown' | 'disable' | 'none'
  cooldownMinutes: number
  reason: string
}): { count: number; triggered: boolean; account?: AccountSummary } {
  const row = getDatabase().prepare('SELECT id, stream_failure_count, stream_failure_window_started_at FROM accounts WHERE id = ?').get(input.accountId) as unknown as AccountFailureRow | undefined
  if (!row) {
    return { count: 0, triggered: false }
  }

  const now = new Date()
  const nowIsoValue = now.toISOString()
  const thresholdMs = Math.max(1, input.thresholdWindowMinutes) * 60_000
  const startedAt = row.stream_failure_window_started_at ? new Date(row.stream_failure_window_started_at) : undefined
  const windowValid = startedAt !== undefined && !Number.isNaN(startedAt.getTime()) && now.getTime() - startedAt.getTime() < thresholdMs
  const count = windowValid ? Math.max(0, row.stream_failure_count) + 1 : 1
  const windowStartedAt = windowValid ? row.stream_failure_window_started_at : nowIsoValue

  getDatabase()
    .prepare(`
      UPDATE accounts
      SET stream_failure_count = ?,
          stream_failure_window_started_at = ?,
          last_error_message = ?,
          updated_at = ?
      WHERE id = ?
    `)
    .run(count, windowStartedAt, input.reason || null, nowIsoValue, input.accountId)

  const triggered = count >= Math.max(1, input.thresholdCount) && input.action !== 'none'
  if (!triggered) {
    return { count, triggered: false, account: listAccounts().find((item) => item.id === input.accountId) }
  }

  if (input.action === 'cooldown') {
    const until = new Date(now.getTime() + Math.max(1, input.cooldownMinutes) * 60_000).toISOString()
    markAccountCooldown(input.accountId, until, input.reason)
  } else {
    markAccountDisabledByFailure(input.accountId, input.reason)
  }

  getDatabase()
    .prepare(`
      UPDATE accounts
      SET stream_failure_count = 0,
          stream_failure_window_started_at = NULL,
          updated_at = ?
      WHERE id = ?
    `)
    .run(nowIsoValue, input.accountId)

  return { count, triggered: true, account: listAccounts().find((item) => item.id === input.accountId) }
}

export function listGroups(): GroupSummary[] {
  const database = getDatabase()
  const rows = database.prepare('SELECT * FROM groups ORDER BY updated_at DESC').all() as unknown as GroupRow[]
  const accountRows = database.prepare('SELECT group_id, account_id FROM group_accounts WHERE enabled = 1').all() as Array<{ group_id: string; account_id: string }>
  return rows.map((row) => ({
    id: row.id,
    name: row.name,
    description: row.description ?? undefined,
    enabled: row.enabled === 1,
    accountIds: accountRows.filter((item) => item.group_id === row.id).map((item) => item.account_id)
  }))
}

export function createGroup(input: Record<string, unknown>): GroupSummary {
  const now = nowIso()
  const group: GroupSummary = {
    id: newId('grp'),
    name: String(input.name ?? '未命名分组'),
    description: optionalString(input.description),
    enabled: input.enabled !== false,
    accountIds: []
  }
  getDatabase()
    .prepare('INSERT INTO groups (id, name, description, enabled, created_at, updated_at) VALUES (?, ?, ?, ?, ?, ?)')
    .run(group.id, group.name, group.description ?? null, group.enabled ? 1 : 0, now, now)
  return group
}

export function findGroupByName(name: string): GroupSummary | undefined {
  return listGroups().find((group) => group.name === name)
}

export function updateGroup(id: string, input: Record<string, unknown>): GroupSummary | undefined {
  const current = listGroups().find((group) => group.id === id)
  if (!current) {
    return undefined
  }
  const next: GroupSummary = {
    ...current,
    name: typeof input.name === 'string' ? input.name : current.name,
    description: optionalString(input.description) ?? current.description,
    enabled: typeof input.enabled === 'boolean' ? input.enabled : current.enabled
  }
  getDatabase()
    .prepare('UPDATE groups SET name = ?, description = COALESCE(?, description), enabled = ?, updated_at = ? WHERE id = ?')
    .run(next.name, optionalString(input.description) ?? null, next.enabled ? 1 : 0, nowIso(), id)
  return next
}

export function deleteGroup(id: string): boolean {
  return runDelete('DELETE FROM groups WHERE id = ?', id)
}

export function setGroupAccounts(groupId: string, accountIds: string[]): GroupSummary | undefined {
  const database = getDatabase()
  const current = listGroups().find((group) => group.id === groupId)
  if (!current) {
    return undefined
  }
  const now = nowIso()
  database.prepare('DELETE FROM group_accounts WHERE group_id = ?').run(groupId)
  const statement = database.prepare('INSERT INTO group_accounts (group_id, account_id, weight, enabled, created_at, updated_at) VALUES (?, ?, ?, ?, ?, ?)')
  for (const accountId of accountIds) {
    statement.run(groupId, accountId, 1, 1, now, now)
  }
  return { ...current, accountIds }
}

export function addAccountToGroup(groupId: string, accountId: string, weight = 1): GroupSummary | undefined {
  const database = getDatabase()
  const current = listGroups().find((group) => group.id === groupId)
  if (!current) {
    return undefined
  }
  const now = nowIso()
  database
    .prepare(`
      INSERT INTO group_accounts (group_id, account_id, weight, enabled, created_at, updated_at)
      VALUES (?, ?, ?, 1, ?, ?)
      ON CONFLICT(group_id, account_id) DO UPDATE SET enabled = 1, updated_at = excluded.updated_at
    `)
    .run(groupId, accountId, weight, now, now)
  return listGroups().find((group) => group.id === groupId)
}

export function listApiKeys(): ApiKeySummary[] {
  const rows = getDatabase().prepare('SELECT * FROM api_keys ORDER BY updated_at DESC').all() as unknown as ApiKeyRow[]
  return rows.map((row) => ({
    id: row.id,
    name: row.name,
    keyPrefix: row.key_prefix,
    key: decryptApiKeySecret(row.key_secret_encrypted),
    status: row.status,
    groupId: row.group_id,
    expiresAt: row.expires_at ?? undefined
  }))
}

function decryptApiKeySecret(value: string | null | undefined): string {
  if (!value) {
    return ''
  }
  const decrypted = decryptJson<{ key?: unknown }>(value)
  return typeof decrypted.key === 'string' ? decrypted.key : ''
}

export function createApiKeyRecord(input: Record<string, unknown>): ApiKeySummary & { key: string } {
  const now = nowIso()
  const key = createApiKey()
  const keyPrefix = key.slice(0, 8)
  const record: ApiKeySummary & { key: string } = {
    id: newId('key'),
    name: String(input.name ?? '未命名 API Key'),
    keyPrefix,
    status: input.status === 'disabled' ? 'disabled' : 'active',
    groupId: String(input.groupId ?? input.group_id ?? 'grp_default_openai'),
    expiresAt: optionalString(input.expiresAt ?? input.expires_at),
    key
  }
  getDatabase()
    .prepare(`
      INSERT INTO api_keys (id, name, key_hash, key_prefix, key_secret_encrypted, status, group_id, expires_at, scopes_json, created_at, updated_at)
      VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
    `)
    .run(record.id, record.name, hashSecret(key), record.keyPrefix, encryptJson({ key }), record.status, record.groupId, record.expiresAt ?? null, JSON.stringify(input.scopes ?? []), now, now)
  return record
}
function findApiKeyByGroupAndName(groupId: string, name: string): ApiKeySummary | undefined {
  return listApiKeys().find((apiKey) => apiKey.groupId === groupId && apiKey.name === name)
}

export function validateGatewayApiKey(key: string): GatewayApiKeyRow | undefined {
  const row = getDatabase().prepare('SELECT id, group_id, status, expires_at FROM api_keys WHERE key_hash = ?').get(hashSecret(key)) as unknown as GatewayApiKeyRow | undefined
  if (!row || row.status !== 'active') {
    return undefined
  }
  if (row.expires_at && Date.parse(row.expires_at) <= Date.now()) {
    return undefined
  }
  return row
}

export function updateApiKey(id: string, input: Record<string, unknown>): ApiKeySummary | undefined {
  const current = listApiKeys().find((apiKey) => apiKey.id === id)
  if (!current) {
    return undefined
  }
  const next: ApiKeySummary = {
    ...current,
    name: typeof input.name === 'string' ? input.name : current.name,
    status: input.status === 'disabled' ? 'disabled' : input.status === 'active' ? 'active' : current.status,
    groupId: typeof input.groupId === 'string' ? input.groupId : typeof input.group_id === 'string' ? input.group_id : current.groupId,
    expiresAt: optionalString(input.expiresAt ?? input.expires_at) ?? current.expiresAt
  }
  getDatabase()
    .prepare('UPDATE api_keys SET name = ?, status = ?, group_id = ?, expires_at = ?, updated_at = ? WHERE id = ?')
    .run(next.name, next.status, next.groupId, next.expiresAt ?? null, nowIso(), id)
  return next
}

export function deleteApiKey(id: string): boolean {
  return runDelete('DELETE FROM api_keys WHERE id = ?', id)
}

export function listProxies(): ProxyProfileSummary[] {
  const rows = getDatabase().prepare('SELECT * FROM proxy_profiles ORDER BY updated_at DESC').all() as unknown as ProxyRow[]
  return rows.map(proxySummaryFromRow)
}

export function findOrCreateLocalSocksProxy(): ProxyProfileSummary {
  const row = getDatabase()
    .prepare('SELECT * FROM proxy_profiles WHERE type IN (?, ?) AND host = ? AND port = ? ORDER BY created_at ASC LIMIT 1')
    .get('socks5', 'socks5h', '127.0.0.1', 7897) as unknown as ProxyRow | undefined
  if (row) {
    return proxySummaryFromRow(row)
  }
  return createProxy({
    name: 'OpenAI OAuth 本地代理 7897',
    type: 'socks5h',
    host: '127.0.0.1',
    port: 7897,
    enabled: true
  })
}

function proxySummaryFromRow(row: ProxyRow): ProxyProfileSummary {
  return {
    id: row.id,
    name: row.name,
    type: row.type,
    host: row.host,
    port: row.port,
    username: row.username ?? undefined,
    enabled: row.enabled === 1,
    testStatus: row.test_status,
    lastTestedAt: row.last_tested_at ?? undefined
  }
}

export function createProxy(input: Record<string, unknown>): ProxyProfileSummary {
  const now = nowIso()
  const proxy: ProxyProfileSummary = {
    id: newId('proxy'),
    name: String(input.name ?? '未命名代理'),
    type: String(input.type ?? 'http'),
    host: String(input.host ?? ''),
    port: Number(input.port ?? 0),
    username: optionalString(input.username),
    enabled: input.enabled !== false,
    testStatus: 'unknown'
  }
  getDatabase()
    .prepare(`
      INSERT INTO proxy_profiles (id, name, type, host, port, username, password_encrypted, enabled, test_status, created_at, updated_at)
      VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
    `)
    .run(proxy.id, proxy.name, proxy.type, proxy.host, proxy.port, proxy.username ?? null, input.password ? encryptJson({ password: input.password }) : null, proxy.enabled ? 1 : 0, proxy.testStatus, now, now)
  return proxy
}

export function updateProxy(id: string, input: Record<string, unknown>): ProxyProfileSummary | undefined {
  const current = listProxies().find((proxy) => proxy.id === id)
  if (!current) {
    return undefined
  }
  const next: ProxyProfileSummary = {
    ...current,
    name: typeof input.name === 'string' ? input.name : current.name,
    type: typeof input.type === 'string' ? input.type : current.type,
    host: typeof input.host === 'string' ? input.host : current.host,
    port: Number(input.port ?? current.port),
    username: optionalString(input.username) ?? current.username,
    enabled: typeof input.enabled === 'boolean' ? input.enabled : current.enabled
  }
  getDatabase()
    .prepare(`
      UPDATE proxy_profiles
      SET name = ?, type = ?, host = ?, port = ?, username = ?, enabled = ?, updated_at = ?
      WHERE id = ?
    `)
    .run(next.name, next.type, next.host, next.port, next.username ?? null, next.enabled ? 1 : 0, nowIso(), id)
  return next
}

export function deleteProxy(id: string): boolean {
  return runDelete('DELETE FROM proxy_profiles WHERE id = ?', id)
}

function proxyUrlForProfile(proxyProfileId?: string | null): string | undefined {
  if (!proxyProfileId) return undefined
  const row = getDatabase()
    .prepare('SELECT type, host, port, username, password_encrypted FROM proxy_profiles WHERE id = ? AND enabled = 1')
    .get(proxyProfileId) as unknown as ProxyRow | undefined
  if (!row) return undefined
  const protocol = row.type === 'socks5h' ? 'socks5h' : row.type === 'socks5' ? 'socks5h' : row.type
  const credentials = row.username
    ? `${encodeURIComponent(row.username)}${proxyPassword(row) ? `:${encodeURIComponent(proxyPassword(row) ?? '')}` : ''}@`
    : ''
  return `${protocol}://${credentials}${row.host}:${row.port}`
}

function proxyPassword(row: ProxyRow): string | undefined {
  if (!row.password_encrypted) return undefined
  const decrypted = decryptJson<{ password?: unknown }>(row.password_encrypted)
  return typeof decrypted.password === 'string' ? decrypted.password : undefined
}

export function listUsageRecords(): UsageRecordSummary[] {
  const rows = getDatabase().prepare('SELECT * FROM usage_records ORDER BY created_at DESC LIMIT 200').all() as Array<Record<string, unknown>>
  return rows.map((row) => ({
    id: String(row.id),
    requestId: String(row.request_id),
    apiKeyId: optionalString(row.api_key_id),
    groupId: optionalString(row.group_id),
    accountId: optionalString(row.account_id),
    providerCode: optionalString(row.provider_code),
    model: optionalString(row.model),
    stream: row.stream === 1,
    statusCode: typeof row.status_code === 'number' ? row.status_code : undefined,
    success: row.success === 1,
    durationMs: typeof row.duration_ms === 'number' ? row.duration_ms : undefined,
    inputTokens: typeof row.input_tokens === 'number' ? row.input_tokens : undefined,
    outputTokens: typeof row.output_tokens === 'number' ? row.output_tokens : undefined,
    cacheReadTokens: typeof row.cache_read_tokens === 'number' ? row.cache_read_tokens : undefined,
    costUsd: typeof row.cost_usd === 'number' ? row.cost_usd : undefined,
    errorCode: optionalString(row.error_code),
    errorMessage: optionalString(row.error_message),
    createdAt: String(row.created_at)
  }))
}

export function selectOpenAIAccountForGroup(groupId: string): OpenAIAccountSecret | undefined {
  return listOpenAIAccountsForGroup(groupId)[0]
}

export function listOpenAIAccountsForGroup(groupId: string): OpenAIAccountSecret[] {
  const database = getDatabase()
  const groupAccountRows = database
    .prepare('SELECT account_id FROM group_accounts WHERE group_id = ? AND enabled = 1 ORDER BY weight DESC, created_at ASC')
    .all(groupId) as unknown as GroupAccountRow[]

  const accounts: OpenAIAccountSecret[] = []
  for (const groupAccount of groupAccountRows) {
    const row = database
      .prepare(`
        SELECT id, name, type, credentials_encrypted, proxy_profile_id, cooldown_until
        FROM accounts
        WHERE id = ? AND provider_code = 'openai' AND type IN ('api_key', 'oauth') AND status = 'active' AND schedulable = 1 AND (cooldown_until IS NULL OR cooldown_until <= ?)
      `)
      .get(groupAccount.account_id, nowIso()) as unknown as { id: string; name: string; type: AccountType; credentials_encrypted: string; proxy_profile_id: string | null; cooldown_until: string | null } | undefined
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
    accounts.push({
      id: row.id,
      name: row.name,
      type: row.type,
      baseUrl: typeof credentials.base_url === 'string' && credentials.base_url ? credentials.base_url : 'https://api.openai.com/v1',
      apiKey,
      refreshToken: typeof credentials.refresh_token === 'string' ? credentials.refresh_token : undefined,
      clientId: typeof credentials.client_id === 'string' ? credentials.client_id : undefined,
      proxyUrl: proxyUrlForProfile(row.proxy_profile_id),
      expiresAt: typeof credentials.expires_at === 'string' ? credentials.expires_at : undefined,
      credentials
    })
  }

  return accounts
}

export function createUsageRecord(input: {
  requestId: string
  apiKeyId?: string
  groupId?: string
  accountId?: string
  providerCode?: string
  model?: string
  stream?: boolean
  statusCode?: number
  success: boolean
  durationMs?: number
  inputTokens?: number
  outputTokens?: number
  cacheReadTokens?: number
  costUsd?: number
  errorCode?: string
  errorMessage?: string
}): void {
  const now = nowIso()
  getDatabase()
    .prepare(`
      INSERT INTO usage_records (
        id, request_id, api_key_id, group_id, account_id, provider_code, model, stream,
        status_code, success, duration_ms, input_tokens, output_tokens, cache_read_tokens, cost_usd, error_code, error_message, created_at
      ) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
    `)
    .run(
      newId('usage'),
      input.requestId,
      input.apiKeyId ?? null,
      input.groupId ?? null,
      input.accountId ?? null,
      input.providerCode ?? null,
      input.model ?? null,
      input.stream ? 1 : 0,
      input.statusCode ?? null,
      input.success ? 1 : 0,
      input.durationMs ?? null,
      input.inputTokens ?? null,
      input.outputTokens ?? null,
      input.cacheReadTokens ?? null,
      input.costUsd ?? null,
      input.errorCode ?? null,
      input.errorMessage ?? null,
      now
    )

  if (input.accountId) {
    getDatabase()
      .prepare('UPDATE accounts SET last_used_at = ?, updated_at = ? WHERE id = ?')
      .run(now, now, input.accountId)
  }
}

export function resolveProxyUrlForProfile(proxyProfileId?: string | null): string | undefined {
  if (!proxyProfileId) return undefined
  const row = getDatabase()
    .prepare('SELECT type, host, port, username, password_encrypted FROM proxy_profiles WHERE id = ? AND enabled = 1')
    .get(proxyProfileId) as unknown as ProxyRow | undefined
  if (!row) return undefined
  const protocol = row.type === 'socks5h' ? 'socks5h' : row.type === 'socks5' ? 'socks5h' : row.type
  const credentials = row.username
    ? `${encodeURIComponent(row.username)}${proxyPassword(row) ? `:${encodeURIComponent(proxyPassword(row) ?? '')}` : ''}@`
    : ''
  return `${protocol}://${credentials}${row.host}:${row.port}`
}

function loadAccountUsageSummaries(): Map<string, AccountUsageSummary> {
  const rows = getDatabase().prepare(`
    SELECT
      account_id,
      COUNT(*) AS request_count,
      COUNT(DISTINCT api_key_id) AS client_count,
      COALESCE(SUM(COALESCE(input_tokens, 0)), 0) AS input_tokens,
      COALESCE(SUM(COALESCE(output_tokens, 0)), 0) AS output_tokens,
      COALESCE(SUM(COALESCE(cache_read_tokens, 0)), 0) AS cache_read_tokens,
      COALESCE(SUM(COALESCE(cost_usd, 0)), 0) AS total_cost,
      MAX(created_at) AS last_used_at
    FROM usage_records
    WHERE account_id IS NOT NULL
    GROUP BY account_id
  `).all() as unknown as AccountUsageAggregateRow[]

  const result = new Map<string, AccountUsageSummary>()
  for (const row of rows) {
    const inputTokens = Number(row.input_tokens ?? 0)
    const outputTokens = Number(row.output_tokens ?? 0)
    const cacheReadTokens = Number(row.cache_read_tokens ?? 0)
    result.set(row.account_id, {
      requestCount: Number(row.request_count ?? 0),
      clientCount: Number(row.client_count ?? 0),
      inputTokens,
      outputTokens,
      cacheReadTokens,
      totalTokens: inputTokens + outputTokens,
      totalCost: Number(row.total_cost ?? 0),
      lastUsedAt: row.last_used_at ?? undefined
    })
  }
  return result
}

export function importOpenAIApiKeyAccounts(input: {
  accounts: MigrationAccountInput[]
  oauthAccounts?: MigrationOAuthAccountInput[]
  groupName?: string
  createGatewayApiKey?: boolean
  gatewayApiKeyName?: string
  dryRun?: boolean
}): MigrationResult {
  const groupName = input.groupName ?? '迁移 OpenAI 账户分组'
  const existingGroup = findGroupByName(groupName)
  const group = existingGroup ?? (input.dryRun ? { id: 'dry_run_group', name: groupName, enabled: true, accountIds: [] } : createGroup({ name: groupName, description: '从 sub2api 迁移的 OpenAI API Key 与 OAuth 账户' }))
  const defaultOAuthProxyId = input.dryRun ? undefined : findOrCreateLocalSocksProxy().id
  let imported = 0
  let skipped = 0
  let importedApiKey = 0
  let importedOAuth = 0
  let skippedApiKey = 0
  let skippedOAuth = 0
  const accountIds = new Set(group.accountIds)

  for (const account of input.accounts) {
    const existing = findAccountByFingerprint('openai', 'api_key', account.baseUrl, account.apiKey)
    if (existing) {
      skipped += 1
      skippedApiKey += 1
      accountIds.add(existing.id)
      continue
    }
    if (input.dryRun) {
      imported += 1
      importedApiKey += 1
      continue
    }
    const created = createAccount({
      name: account.name,
      type: 'api_key',
      credentials: {
        api_key: account.apiKey,
        base_url: account.baseUrl
      },
      status: 'active',
      concurrencyLimit: 1,
      passthroughEnabled: true,
      schedulable: true,
      notes: account.description
    })
    imported += 1
    importedApiKey += 1
    accountIds.add(created.id)
  }

  for (const account of input.oauthAccounts ?? []) {
    const existing = findAccountByFingerprint('openai', 'oauth', 'https://api.openai.com/v1', account.refreshToken)
    if (existing) {
      skipped += 1
      skippedOAuth += 1
      accountIds.add(existing.id)
      continue
    }
    if (input.dryRun) {
      imported += 1
      importedOAuth += 1
      continue
    }

    const credentials: Record<string, unknown> = {
      access_token: account.accessToken,
      refresh_token: account.refreshToken,
      base_url: 'https://api.openai.com/v1'
    }
    if (account.idToken) credentials.id_token = account.idToken
    if (account.expiresAt) credentials.expires_at = account.expiresAt
    if (account.clientId) credentials.client_id = account.clientId
    if (account.email) credentials.email = account.email
    if (account.chatgptAccountId) credentials.chatgpt_account_id = account.chatgptAccountId
    if (account.chatgptUserId) credentials.chatgpt_user_id = account.chatgptUserId
    if (account.organizationId) credentials.organization_id = account.organizationId
    if (account.planType) credentials.plan_type = account.planType

    const created = createAccount({
      name: account.name,
      type: 'oauth',
      credentials,
      status: 'active',
      concurrencyLimit: 3,
      proxyProfileId: account.proxyProfileId ?? defaultOAuthProxyId,
      passthroughEnabled: true,
      schedulable: true,
      notes: account.description
    })
    imported += 1
    importedOAuth += 1
    accountIds.add(created.id)
  }

  if (!input.dryRun) {
    for (const accountId of accountIds) {
      addAccountToGroup(group.id, accountId)
    }
  }

  const result: MigrationResult = {
    imported,
    skipped,
    importedApiKey,
    importedOAuth,
    skippedApiKey,
    skippedOAuth,
    accountIds: [...accountIds],
    groupId: group.id,
    groupName: group.name
  }

  if (input.createGatewayApiKey && !input.dryRun) {
    const gatewayApiKeyName = input.gatewayApiKeyName ?? '迁移 OpenAI 网关 Key'
    const existingGatewayApiKey = findApiKeyByGroupAndName(group.id, gatewayApiKeyName)
    result.gatewayApiKeyName = gatewayApiKeyName
    if (existingGatewayApiKey) {
      result.gatewayApiKeyId = existingGatewayApiKey.id
      result.apiKey = existingGatewayApiKey.key || undefined
      result.gatewayApiKeyCreated = false
    } else {
      const createdGatewayApiKey = createApiKeyRecord({
        name: gatewayApiKeyName,
        groupId: group.id,
        status: 'active',
      })
      result.apiKey = createdGatewayApiKey.key
      result.gatewayApiKeyId = createdGatewayApiKey.id
      result.gatewayApiKeyCreated = true
    }
  }

  return result
}

export function getSettings(): Record<string, unknown> {
  const rows = getDatabase().prepare('SELECT key, value_json FROM system_settings ORDER BY key ASC').all() as Array<{ key: string; value_json: string }>
  return Object.fromEntries(rows.filter((row) => row.key !== 'apiKeyPrefix').map((row) => [row.key, JSON.parse(row.value_json) as unknown]))
}

export function updateSettings(input: Record<string, unknown>): Record<string, unknown> {
  const statement = getDatabase().prepare('INSERT INTO system_settings (key, value_json, updated_at) VALUES (?, ?, ?) ON CONFLICT(key) DO UPDATE SET value_json = excluded.value_json, updated_at = excluded.updated_at')
  const now = nowIso()
  for (const [key, value] of Object.entries(input)) {
    if (key === 'apiKeyPrefix') {
      continue
    }
    statement.run(key, JSON.stringify(value), now)
  }
  return getSettings()
}
