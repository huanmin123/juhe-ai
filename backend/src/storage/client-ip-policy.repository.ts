import type { SQLInputValue } from 'node:sqlite'

import {
  beginDatabaseTransaction,
  commitDatabaseTransaction,
  getStatsDatabase,
  newId,
  nowIso,
  rollbackDatabaseTransaction
} from './database.js'
import { dateKey, usageStatsTimezone } from './usage-stats-helpers.js'
import { normalizeIpHash } from './client-ip-normalization.js'

export type ClientIpPolicyStatus = 'active' | 'disabled'

export interface ClientIpPolicySummary {
  id: string
  ipHash: string
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
  aggregateIpKey: string
  clientIp: string
  reason?: string
  expiresAt?: string
}

export interface ClientIpPolicyMutationInput {
  ipHash: string
  reason?: string
  expiresAt?: string
  actorSystemAccountId: string
}

export interface ClientIpPolicyDisableInput {
  ipHash: string
  reason?: string
  actorSystemAccountId: string
}

export interface ClientIpPolicyHitInput {
  ipHash: string
  policyId: string
  hitCount?: number
  hitAt?: string
}

export function createClientIpPolicy(input: ClientIpPolicyMutationInput): ClientIpPolicySummary {
  const ipHash = normalizeIpHash(input.ipHash)
  if (!ipHash) {
    throw new Error('IP 标识无效')
  }
  const database = getStatsDatabase()
  const registry = database.prepare('SELECT ip_hash FROM client_ip_registry WHERE ip_hash = ?').get(ipHash) as { ip_hash?: string } | undefined
  if (!registry) {
    throw new Error('IP 不存在')
  }
  const id = newId('ip_policy')
  const now = nowIso()
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
    `).run(now, input.actorSystemAccountId, '被新的封禁策略替换', now, ipHash)
    database.prepare(`
      INSERT INTO client_ip_policies (
        id, ip_hash, status, reason, expires_at,
        created_by_system_account_id, created_at, updated_at
      ) VALUES (?, ?, 'active', ?, ?, ?, ?, ?)
    `).run(
      id,
      ipHash,
      normalizeOptionalText(input.reason) ?? null,
      normalizeOptionalIso(input.expiresAt) ?? null,
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

export function disableClientIpPolicies(input: ClientIpPolicyDisableInput): { disabledCount: number } {
  const ipHash = normalizeIpHash(input.ipHash)
  if (!ipHash) {
    throw new Error('IP 标识无效')
  }
  const now = nowIso()
  const params: SQLInputValue[] = [
    now,
    input.actorSystemAccountId,
    normalizeOptionalText(input.reason) ?? '管理员解除策略',
    now,
    ipHash
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
  `).run(...params)
  return { disabledCount: Number(result.changes ?? 0) }
}

export function listActiveClientIpPolicies(): ActiveClientIpPolicy[] {
  const now = nowIso()
  const params: SQLInputValue[] = [now]
  const rows = getStatsDatabase().prepare(`
    SELECT policies.id, policies.ip_hash, policies.reason, policies.expires_at,
      registry.aggregate_ip_key, registry.client_ip
    FROM client_ip_policies policies
    INNER JOIN client_ip_registry registry ON registry.ip_hash = policies.ip_hash
    WHERE policies.status = 'active'
      AND (policies.expires_at IS NULL OR policies.expires_at > ?)
    ORDER BY policies.created_at DESC, policies.id DESC
  `).all(...params) as unknown as Array<{
    id: string
    ip_hash: string
    reason: string | null
    expires_at: string | null
    aggregate_ip_key: string
    client_ip: string
  }>
  return rows.map(mapActiveClientIpPolicyRow)
}

export function findActiveClientIpPolicyByHash(inputIpHash: string): ActiveClientIpPolicy | undefined {
  const ipHash = normalizeIpHash(inputIpHash)
  if (!ipHash) {
    return undefined
  }
  const now = nowIso()
  const row = getStatsDatabase().prepare(`
    SELECT policies.id, policies.ip_hash, policies.reason, policies.expires_at,
      registry.aggregate_ip_key, registry.client_ip
    FROM client_ip_policies policies
    INNER JOIN client_ip_registry registry ON registry.ip_hash = policies.ip_hash
    WHERE policies.ip_hash = ?
      AND policies.status = 'active'
      AND (policies.expires_at IS NULL OR policies.expires_at > ?)
    ORDER BY policies.created_at DESC, policies.id DESC
    LIMIT 1
  `).get(ipHash, now) as unknown as {
    id: string
    ip_hash: string
    reason: string | null
    expires_at: string | null
    aggregate_ip_key: string
    client_ip: string
  } | undefined
  return row ? mapActiveClientIpPolicyRow(row) : undefined
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
      const hitAt = normalizeOptionalIso(hit.hitAt) ?? updatedAt
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

function mapActiveClientIpPolicyRow(row: {
  id: string
  ip_hash: string
  reason: string | null
  expires_at: string | null
  aggregate_ip_key: string
  client_ip: string
}): ActiveClientIpPolicy {
  return {
    id: row.id,
    ipHash: row.ip_hash,
    aggregateIpKey: row.aggregate_ip_key,
    clientIp: row.client_ip,
    reason: row.reason ?? undefined,
    expiresAt: row.expires_at ?? undefined
  }
}

function mapClientIpPolicyRow(row: ClientIpPolicyRow): ClientIpPolicySummary {
  return {
    id: row.id,
    ipHash: row.ip_hash,
    status: row.status === 'disabled' ? 'disabled' : 'active',
    reason: row.reason ?? undefined,
    expiresAt: row.expires_at ?? undefined,
    createdBySystemAccountId: row.created_by_system_account_id,
    createdAt: row.created_at,
    updatedAt: row.updated_at,
    disabledAt: row.disabled_at ?? undefined,
    disabledBySystemAccountId: row.disabled_by_system_account_id ?? undefined,
    disabledReason: row.disabled_reason ?? undefined
  }
}

function normalizeOptionalText(value?: string | null): string | undefined {
  const text = value?.trim()
  return text || undefined
}

function normalizeOptionalIso(value?: string | null): string | undefined {
  const text = normalizeOptionalText(value)
  if (!text) return undefined
  const time = Date.parse(text)
  return Number.isFinite(time) ? new Date(time).toISOString() : undefined
}

interface ClientIpPolicyRow {
  id: string
  ip_hash: string
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
