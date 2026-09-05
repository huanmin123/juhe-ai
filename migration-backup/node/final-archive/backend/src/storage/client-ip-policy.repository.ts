import type { SQLInputValue } from 'node:sqlite'

import { runtimeConfig } from '../config/runtime.js'
import { requiredRfc3339Instant, rfc3339InstantMilliseconds } from '../shared/rfc3339.js'
import {
  beginDatabaseTransaction,
  commitDatabaseTransaction,
  getStatsDatabase,
  newId,
  nowIso,
  rollbackDatabaseTransaction
} from './database.js'
import { createPostgresDatabaseClient, type DatabaseClient } from './database-client.js'
import { getPostgresPool } from './postgres-client.js'
import { chunkValues } from './query-utils.js'
import { requestSqliteReadWorker, sqliteReadWorkerPoolEnabled } from './sqlite-read-worker-pool.js'
import { dateKey, usageStatsTimezone, usageStatsTimezoneAsync } from './usage-stats-helpers.js'
import { normalizeIpHash } from './client-ip-normalization.js'

export type ClientIpPolicyStatus = 'active' | 'disabled'
export type ClientIpPolicyType = 'blacklist' | 'allowlist'

export interface ClientIpPolicySummary {
  id: string
  ipHash: string
  policyType: ClientIpPolicyType
  status: ClientIpPolicyStatus
  reason?: string
  expiresAt?: string
  createdBySystemAccountId: string
  createdAt: string
  updatedAt: string
  disabledAt?: string
  disabledBySystemAccountId?: string
  disabledReason?: string
}

export interface ActiveClientIpPolicy {
  id: string
  ipHash: string
  policyType: ClientIpPolicyType
  aggregateIpKey: string
  clientIp: string
  reason?: string
  expiresAt?: string
}

export interface ClientIpPolicyMutationInput {
  ipHash: string
  policyType?: ClientIpPolicyType
  reason?: string
  expiresAt?: string
  actorSystemAccountId: string
}

export interface ClientIpPolicyDisableInput {
  ipHash: string
  policyType?: ClientIpPolicyType
  reason?: string
  actorSystemAccountId: string
}

export interface ClientIpPolicyHitInput {
  ipHash: string
  policyId: string
  hitCount?: number
  hitAt?: string
}

const statsSchemaName = 'juhe_stats'

export function createClientIpPolicy(input: ClientIpPolicyMutationInput): ClientIpPolicySummary {
  const ipHash = normalizeIpHash(input.ipHash)
  if (!ipHash) {
    throw new Error('IP 标识无效')
  }
  const policyType = normalizeClientIpPolicyType(input.policyType)
  const database = getStatsDatabase()
  const registry = database.prepare('SELECT ip_hash FROM client_ip_registry WHERE ip_hash = ?').get(ipHash) as { ip_hash?: string } | undefined
  if (!registry) {
    throw new Error('IP 不存在')
  }
  const id = newId('ip_policy')
  const now = nowIso()
  const expiresAt = optionalRfc3339Instant(input.expiresAt, 'Client-IP 策略 expiresAt')
  const transactionStarted = beginDatabaseTransaction(database)
  try {
    database.prepare(`
      UPDATE client_ip_policies
      SET status = 'disabled',
        disabled_at = ?,
        disabled_by_system_account_id = ?,
        disabled_reason = ?,
        updated_at = ?
      WHERE ip_hash = ?
        AND status = 'active'
    `).run(now, input.actorSystemAccountId, activePolicyReplacementReason(policyType), now, ipHash)
    database.prepare(`
      INSERT INTO client_ip_policies (
        id, ip_hash, policy_type, status, reason, expires_at,
        created_by_system_account_id, created_at, updated_at
      ) VALUES (?, ?, ?, 'active', ?, ?, ?, ?, ?)
    `).run(
      id,
      ipHash,
      policyType,
      normalizeOptionalText(input.reason) ?? null,
      expiresAt ?? null,
      input.actorSystemAccountId,
      now,
      now
    )
    commitDatabaseTransaction(database, transactionStarted)
  } catch (error) {
    rollbackDatabaseTransaction(database, transactionStarted)
    throw error
  }
  return mapClientIpPolicyRow(database.prepare('SELECT * FROM client_ip_policies WHERE id = ?').get(id) as unknown as ClientIpPolicyRow)
}

export async function createClientIpPolicyAsync(input: ClientIpPolicyMutationInput): Promise<ClientIpPolicySummary> {
  if (runtimeConfig.databaseDriver !== 'postgres') {
    return createClientIpPolicy(input)
  }
  const ipHash = normalizeIpHash(input.ipHash)
  if (!ipHash) {
    throw new Error('IP 标识无效')
  }
  const policyType = normalizeClientIpPolicyType(input.policyType)
  const client = createPostgresDatabaseClient(await getPostgresPool())
  const registry = await client.one<{ ip_hash?: string }>(`
    SELECT ip_hash
    FROM ${statsTable(client, 'client_ip_registry')}
    WHERE ip_hash = ?
    LIMIT 1
  `, [ipHash])
  if (!registry) {
    throw new Error('IP 不存在')
  }
  const id = newId('ip_policy')
  const now = nowIso()
  const expiresAt = optionalRfc3339Instant(input.expiresAt, 'Client-IP 策略 expiresAt')
  await client.transaction(async (tx) => {
    await tx.execute(`
      UPDATE ${statsTable(tx, 'client_ip_policies')}
      SET status = 'disabled',
        disabled_at = ?,
        disabled_by_system_account_id = ?,
        disabled_reason = ?,
        updated_at = ?
      WHERE ip_hash = ?
        AND status = 'active'
    `, [now, input.actorSystemAccountId, activePolicyReplacementReason(policyType), now, ipHash])
    await tx.execute(`
      INSERT INTO ${statsTable(tx, 'client_ip_policies')} (
        id, ip_hash, policy_type, status, reason, expires_at,
        created_by_system_account_id, created_at, updated_at
      ) VALUES (?, ?, ?, 'active', ?, ?, ?, ?, ?)
    `, [
      id,
      ipHash,
      policyType,
      normalizeOptionalText(input.reason) ?? null,
      expiresAt ?? null,
      input.actorSystemAccountId,
      now,
      now
    ])
  })
  const row = await client.one<ClientIpPolicyRow>(`SELECT * FROM ${statsTable(client, 'client_ip_policies')} WHERE id = ?`, [id])
  if (!row) {
    throw new Error('IP 策略保存失败')
  }
  return mapClientIpPolicyRow(row)
}

export function disableClientIpPolicies(input: ClientIpPolicyDisableInput): { disabledCount: number } {
  const ipHash = normalizeIpHash(input.ipHash)
  if (!ipHash) {
    throw new Error('IP 标识无效')
  }
  const policyType = input.policyType ? normalizeClientIpPolicyType(input.policyType) : undefined
  const now = nowIso()
  const params: SQLInputValue[] = [
    now,
    input.actorSystemAccountId,
    normalizeOptionalText(input.reason) ?? '管理员解除策略',
    now,
    ipHash,
    ...(policyType ? [policyType] : [])
  ]
  const result = getStatsDatabase().prepare(`
    UPDATE client_ip_policies
    SET status = 'disabled',
      disabled_at = ?,
      disabled_by_system_account_id = ?,
      disabled_reason = ?,
      updated_at = ?
    WHERE ip_hash = ?
      AND status = 'active'
      ${policyType ? 'AND policy_type = ?' : ''}
  `).run(...params)
  return { disabledCount: Number(result.changes ?? 0) }
}

export async function disableClientIpPoliciesAsync(input: ClientIpPolicyDisableInput): Promise<{ disabledCount: number }> {
  if (runtimeConfig.databaseDriver !== 'postgres') {
    return disableClientIpPolicies(input)
  }
  const ipHash = normalizeIpHash(input.ipHash)
  if (!ipHash) {
    throw new Error('IP 标识无效')
  }
  const policyType = input.policyType ? normalizeClientIpPolicyType(input.policyType) : undefined
  const now = nowIso()
  const client = createPostgresDatabaseClient(await getPostgresPool())
  const result = await client.execute(`
    UPDATE ${statsTable(client, 'client_ip_policies')}
    SET status = 'disabled',
      disabled_at = ?,
      disabled_by_system_account_id = ?,
      disabled_reason = ?,
      updated_at = ?
    WHERE ip_hash = ?
      AND status = 'active'
      ${policyType ? 'AND policy_type = ?' : ''}
  `, [
    now,
    input.actorSystemAccountId,
    normalizeOptionalText(input.reason) ?? '管理员解除策略',
    now,
    ipHash,
    ...(policyType ? [policyType] : [])
  ])
  return { disabledCount: Number(result.changes ?? 0) }
}

export function listActiveClientIpPolicies(): ActiveClientIpPolicy[] {
  const rows = getStatsDatabase().prepare(`
    SELECT policies.id, policies.ip_hash, policies.policy_type, policies.reason, policies.expires_at,
      registry.aggregate_ip_key, registry.client_ip
    FROM client_ip_policies policies
    INNER JOIN client_ip_registry registry ON registry.ip_hash = policies.ip_hash
    WHERE policies.status = 'active'
  `).all() as unknown as Array<{
    id: string
    ip_hash: string
    policy_type: string
    reason: string | null
    expires_at: string | null
    aggregate_ip_key: string
    client_ip: string
  }>
  const nowMs = Date.now()
  return rows
    .map(mapActiveClientIpPolicyRow)
    .filter((policy) => isActiveClientIpPolicyAt(policy, nowMs))
}

export async function listActiveClientIpPoliciesAsync(): Promise<ActiveClientIpPolicy[]> {
  if (sqliteReadWorkerPoolEnabled()) {
    return requestSqliteReadWorker({
      type: 'list_active_client_ip_policies_read_only'
    })
  }
  if (runtimeConfig.databaseDriver !== 'postgres') {
    return listActiveClientIpPolicies()
  }
  const client = createPostgresDatabaseClient(await getPostgresPool())
  const rows = await client.query<ActiveClientIpPolicyRow>(`
    SELECT policies.id, policies.ip_hash, policies.policy_type, policies.reason, policies.expires_at,
      registry.aggregate_ip_key, registry.client_ip
    FROM ${statsTable(client, 'client_ip_policies')} policies
    INNER JOIN ${statsTable(client, 'client_ip_registry')} registry ON registry.ip_hash = policies.ip_hash
    WHERE policies.status = 'active'
  `)
  const nowMs = Date.now()
  return rows
    .map(mapActiveClientIpPolicyRow)
    .filter((policy) => isActiveClientIpPolicyAt(policy, nowMs))
}

export function findActiveClientIpPolicyByHash(inputIpHash: string): ActiveClientIpPolicy | undefined {
  const ipHash = normalizeIpHash(inputIpHash)
  if (!ipHash) {
    return undefined
  }
  const row = getStatsDatabase().prepare(`
    SELECT policies.id, policies.ip_hash, policies.policy_type, policies.reason, policies.expires_at,
      registry.aggregate_ip_key, registry.client_ip
    FROM client_ip_policies policies
    INNER JOIN client_ip_registry registry ON registry.ip_hash = policies.ip_hash
    WHERE policies.ip_hash = ?
      AND policies.status = 'active'
    LIMIT 1
  `).get(ipHash) as unknown as {
    id: string
    ip_hash: string
    policy_type: string
    reason: string | null
    expires_at: string | null
    aggregate_ip_key: string
    client_ip: string
  } | undefined
  if (!row) return undefined
  const policy = mapActiveClientIpPolicyRow(row)
  return isActiveClientIpPolicyAt(policy, Date.now()) ? policy : undefined
}

export async function findActiveClientIpPolicyByHashAsync(inputIpHash: string): Promise<ActiveClientIpPolicy | undefined> {
  if (sqliteReadWorkerPoolEnabled()) {
    return requestSqliteReadWorker({
      type: 'find_active_client_ip_policy_by_hash_read_only',
      ipHash: inputIpHash
    })
  }
  if (runtimeConfig.databaseDriver !== 'postgres') {
    return findActiveClientIpPolicyByHash(inputIpHash)
  }
  const ipHash = normalizeIpHash(inputIpHash)
  if (!ipHash) {
    return undefined
  }
  const client = createPostgresDatabaseClient(await getPostgresPool())
  const row = await client.one<ActiveClientIpPolicyRow>(`
    SELECT policies.id, policies.ip_hash, policies.policy_type, policies.reason, policies.expires_at,
      registry.aggregate_ip_key, registry.client_ip
    FROM ${statsTable(client, 'client_ip_policies')} policies
    INNER JOIN ${statsTable(client, 'client_ip_registry')} registry ON registry.ip_hash = policies.ip_hash
    WHERE policies.ip_hash = ?
      AND policies.status = 'active'
    LIMIT 1
  `, [ipHash])
  if (!row) return undefined
  const policy = mapActiveClientIpPolicyRow(row)
  return isActiveClientIpPolicyAt(policy, Date.now()) ? policy : undefined
}

export function recordClientIpPolicyHits(hits: ClientIpPolicyHitInput[]): { recorded: number } {
  if (!hits.length) return { recorded: 0 }
  const database = getStatsDatabase()
  const updatedAt = nowIso()
  const insert = database.prepare(`
    INSERT INTO client_ip_policy_hits (
      ip_hash, stat_date, policy_id, hit_count, last_hit_at, updated_at
    ) VALUES (?, ?, ?, ?, ?, ?)
    ON CONFLICT(ip_hash, stat_date, policy_id) DO UPDATE SET
      hit_count = hit_count + excluded.hit_count,
      last_hit_at = CASE
        WHEN client_ip_policy_hits.last_hit_at IS NULL OR excluded.last_hit_at > client_ip_policy_hits.last_hit_at THEN excluded.last_hit_at
        ELSE client_ip_policy_hits.last_hit_at
      END,
      updated_at = excluded.updated_at
  `)
  const transactionStarted = beginDatabaseTransaction(database)
  let recorded = 0
  try {
    for (const hit of hits) {
      const ipHash = normalizeIpHash(hit.ipHash)
      const policyId = normalizeOptionalText(hit.policyId)
      if (!ipHash || !policyId) continue
      const hitAt = optionalRfc3339Instant(hit.hitAt, 'Client-IP 策略 hitAt') ?? updatedAt
      insert.run(
        ipHash,
        dateKey(new Date(hitAt), usageStatsTimezone()),
        policyId,
        Math.max(1, Math.trunc(Number(hit.hitCount ?? 1))),
        hitAt,
        updatedAt
      )
      recorded += 1
    }
    commitDatabaseTransaction(database, transactionStarted)
  } catch (error) {
    rollbackDatabaseTransaction(database, transactionStarted)
    throw error
  }
  return { recorded }
}

export async function recordClientIpPolicyHitsAsync(hits: ClientIpPolicyHitInput[]): Promise<{ recorded: number }> {
  if (runtimeConfig.databaseDriver !== 'postgres') {
    return recordClientIpPolicyHits(hits)
  }
  if (!hits.length) return { recorded: 0 }
  const updatedAt = nowIso()
  const timezone = await usageStatsTimezoneAsync()
  const entries = normalizeClientIpPolicyHitEntries(hits, updatedAt, timezone)
  if (!entries.length) return { recorded: 0 }
  const client = createPostgresDatabaseClient(await getPostgresPool())
  for (const chunk of chunkValues(entries, 500)) {
    await client.execute(`
      INSERT INTO ${statsTable(client, 'client_ip_policy_hits')} (
        ip_hash, stat_date, policy_id, hit_count, last_hit_at, updated_at
      ) VALUES ${multiRowPlaceholders(chunk.length, 6)}
      ON CONFLICT(ip_hash, stat_date, policy_id) DO UPDATE SET
        hit_count = client_ip_policy_hits.hit_count + EXCLUDED.hit_count,
        last_hit_at = CASE
          WHEN client_ip_policy_hits.last_hit_at IS NULL OR EXCLUDED.last_hit_at > client_ip_policy_hits.last_hit_at THEN EXCLUDED.last_hit_at
          ELSE client_ip_policy_hits.last_hit_at
        END,
        updated_at = EXCLUDED.updated_at
    `, chunk.flatMap((entry) => [
      entry.ipHash,
      entry.statDate,
      entry.policyId,
      entry.hitCount,
      entry.hitAt,
      updatedAt
    ]))
  }
  return { recorded: entries.length }
}

function normalizeClientIpPolicyHitEntries(
  hits: ClientIpPolicyHitInput[],
  updatedAt: string,
  timezone: string
): Array<{ ipHash: string; statDate: string; policyId: string; hitCount: number; hitAt: string }> {
  const entries: Array<{ ipHash: string; statDate: string; policyId: string; hitCount: number; hitAt: string }> = []
  for (const hit of hits) {
    const ipHash = normalizeIpHash(hit.ipHash)
    const policyId = normalizeOptionalText(hit.policyId)
    if (!ipHash || !policyId) continue
    const hitAt = optionalRfc3339Instant(hit.hitAt, 'Client-IP 策略 hitAt') ?? updatedAt
    entries.push({
      ipHash,
      statDate: dateKey(new Date(hitAt), timezone),
      policyId,
      hitCount: Math.max(1, Math.trunc(Number(hit.hitCount ?? 1))),
      hitAt
    })
  }
  return entries
}

function mapActiveClientIpPolicyRow(row: {
  id: string
  ip_hash: string
  policy_type: string
  reason: string | null
  expires_at: string | null
  aggregate_ip_key: string
  client_ip: string
}): ActiveClientIpPolicy {
  return {
    id: row.id,
    ipHash: row.ip_hash,
    policyType: normalizeClientIpPolicyType(row.policy_type),
    aggregateIpKey: row.aggregate_ip_key,
    clientIp: row.client_ip,
    reason: row.reason ?? undefined,
    expiresAt: optionalRfc3339Instant(row.expires_at, 'Client-IP 策略 expiresAt')
  }
}

function statsTable(client: DatabaseClient, tableName: string): string {
  return client.dialect.qualifyTable(statsSchemaName, tableName)
}

function multiRowPlaceholders(rowCount: number, columnCount: number): string {
  const row = `(${Array.from({ length: columnCount }, () => '?').join(', ')})`
  return Array.from({ length: rowCount }, () => row).join(', ')
}

interface ActiveClientIpPolicyRow {
  id: string
  ip_hash: string
  policy_type: string
  reason: string | null
  expires_at: string | null
  aggregate_ip_key: string
  client_ip: string
}

function mapClientIpPolicyRow(row: ClientIpPolicyRow): ClientIpPolicySummary {
  return {
    id: row.id,
    ipHash: row.ip_hash,
    policyType: normalizeClientIpPolicyType(row.policy_type),
    status: row.status === 'disabled' ? 'disabled' : 'active',
    reason: row.reason ?? undefined,
    expiresAt: optionalRfc3339Instant(row.expires_at, 'Client-IP 策略 expiresAt'),
    createdBySystemAccountId: row.created_by_system_account_id,
    createdAt: requiredRfc3339Instant(row.created_at, 'Client-IP 策略 createdAt'),
    updatedAt: requiredRfc3339Instant(row.updated_at, 'Client-IP 策略 updatedAt'),
    disabledAt: optionalRfc3339Instant(row.disabled_at, 'Client-IP 策略 disabledAt'),
    disabledBySystemAccountId: row.disabled_by_system_account_id ?? undefined,
    disabledReason: row.disabled_reason ?? undefined
  }
}

function normalizeOptionalText(value?: string | null): string | undefined {
  const text = value?.trim()
  return text || undefined
}

function normalizeClientIpPolicyType(value?: string | null): ClientIpPolicyType {
  return value === 'allowlist' ? 'allowlist' : 'blacklist'
}

function activePolicyReplacementReason(nextPolicyType: ClientIpPolicyType): string {
  return nextPolicyType === 'allowlist' ? '被新的白名单策略替换' : '被新的封禁策略替换'
}

function optionalRfc3339Instant(value: unknown, label: string): string | undefined {
  if (value === undefined || value === null) return undefined
  return requiredRfc3339Instant(value, label)
}

function isActiveClientIpPolicyAt(policy: ActiveClientIpPolicy, nowMs: number): boolean {
  if (policy.expiresAt === undefined) return true
  const expiresAtMs = rfc3339InstantMilliseconds(policy.expiresAt)
  if (expiresAtMs === undefined) {
    throw new Error('Client-IP 策略 expiresAt必须是带 Z 或数值 offset 的 RFC3339 时间')
  }
  return expiresAtMs > nowMs
}

interface ClientIpPolicyRow {
  id: string
  ip_hash: string
  policy_type: string
  status: string
  reason: string | null
  expires_at: string | null
  created_by_system_account_id: string
  created_at: string
  updated_at: string
  disabled_at: string | null
  disabled_by_system_account_id: string | null
  disabled_reason: string | null
}
