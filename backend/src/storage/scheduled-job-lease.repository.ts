import { createHash } from 'node:crypto'

import { createPostgresDatabaseClient, type DatabaseClient } from './database-client.js'
import { getPostgresPool } from './postgres-client.js'

const scheduledJobLeaseAdvisoryNamespace = 'juhe-ai:scheduled-job-lease:v1:'
const minimumScheduledJobLeaseTtlMs = 100
const maximumScheduledJobLeaseTtlMs = 24 * 60 * 60 * 1000
const postgresUtcNowTextSql = `to_char(clock_timestamp() AT TIME ZONE 'UTC', 'YYYY-MM-DD"T"HH24:MI:SS.MS"Z"')`
const postgresUtcLeaseUntilTextSql = `to_char((clock_timestamp() + (? * INTERVAL '1 millisecond')) AT TIME ZONE 'UTC', 'YYYY-MM-DD"T"HH24:MI:SS.MS"Z"')`

export interface ScheduledJobLeaseIdentity {
  leaseKey: string
  ownerId: string
  fencingToken: string
  leaseUntil: string
}

export type ScheduledJobLeaseFence = Pick<ScheduledJobLeaseIdentity, 'leaseKey' | 'ownerId' | 'fencingToken'>

export interface ScheduledJobLeaseAcquireInput {
  jobName: string
  shardKey?: string
  leaseKey?: string
  ownerId: string
  runId?: string
  ttlMs: number
}

export type ScheduledJobLeaseAcquireResult =
  | {
      acquired: true
      lease: ScheduledJobLeaseIdentity
    }
  | {
      acquired: false
      reason: 'advisory_busy' | 'lease_held'
      leaseKey: string
    }

interface ScheduledJobLeaseRow {
  lease_key: string
  owner_id: string
  fencing_token: string | number | bigint
  lease_until: string
}

export class ScheduledJobLeaseLostError extends Error {
  constructor(readonly lease: ScheduledJobLeaseFence) {
    super(`后台任务租约已失效：${lease.leaseKey}`)
    this.name = 'ScheduledJobLeaseLostError'
  }
}

export function scheduledJobLeaseKey(jobName: string, shardKey = 'global'): string {
  return `scheduled:${requiredLeaseText(jobName, 'jobName')}:${requiredLeaseText(shardKey, 'shardKey')}`
}

export function scheduledJobLeaseAdvisoryKey(leaseKey: string): string {
  const digest = createHash('sha256')
    .update(scheduledJobLeaseAdvisoryNamespace)
    .update(requiredLeaseText(leaseKey, 'leaseKey'))
    .digest()
  const unsigned = digest.readBigUInt64BE(0)
  const signed = unsigned > 0x7fff_ffff_ffff_ffffn
    ? unsigned - 0x1_0000_0000_0000_0000n
    : unsigned
  return signed.toString()
}

export async function tryAcquireScheduledJobLease(
  input: ScheduledJobLeaseAcquireInput,
  client?: DatabaseClient
): Promise<ScheduledJobLeaseAcquireResult> {
  const databaseClient = await scheduledJobLeaseClient(client)
  const jobName = requiredLeaseText(input.jobName, 'jobName')
  const shardKey = requiredLeaseText(input.shardKey ?? 'global', 'shardKey')
  const leaseKey = input.leaseKey
    ? requiredLeaseText(input.leaseKey, 'leaseKey')
    : scheduledJobLeaseKey(jobName, shardKey)
  const ownerId = requiredLeaseText(input.ownerId, 'ownerId')
  const ttlMs = normalizedScheduledJobLeaseTtlMs(input.ttlMs)
  const runId = optionalLeaseText(input.runId, 'runId')

  return runScheduledJobLeaseTransaction(databaseClient, async (tx) => {
    const advisory = await tx.one<{ acquired?: boolean | number | string }>(
      'SELECT pg_try_advisory_xact_lock(?::bigint) AS acquired',
      [scheduledJobLeaseAdvisoryKey(leaseKey)]
    )
    if (!postgresBoolean(advisory?.acquired)) {
      return { acquired: false, reason: 'advisory_busy', leaseKey }
    }

    const table = scheduledJobLeaseTable(tx)
    const row = await tx.one<ScheduledJobLeaseRow>(`
      INSERT INTO ${table} AS current (
        lease_key, job_name, shard_key, owner_id, run_id, lease_until,
        heartbeat_at, started_at, updated_at, fencing_token
      ) VALUES (?, ?, ?, ?, ?, ${postgresUtcLeaseUntilTextSql}, ${postgresUtcNowTextSql}, ${postgresUtcNowTextSql}, ${postgresUtcNowTextSql}, 1)
      ON CONFLICT(lease_key) DO UPDATE SET
        job_name = excluded.job_name,
        shard_key = excluded.shard_key,
        owner_id = excluded.owner_id,
        run_id = excluded.run_id,
        lease_until = excluded.lease_until,
        heartbeat_at = excluded.heartbeat_at,
        started_at = excluded.started_at,
        updated_at = excluded.updated_at,
        fencing_token = current.fencing_token + 1
      WHERE current.lease_until <= ${postgresUtcNowTextSql}
      RETURNING lease_key, owner_id, fencing_token, lease_until
    `, [leaseKey, jobName, shardKey, ownerId, runId, ttlMs])
    if (!row) {
      return { acquired: false, reason: 'lease_held', leaseKey }
    }
    return { acquired: true, lease: scheduledJobLeaseIdentityFromRow(row) }
  })
}

export async function renewScheduledJobLease(
  lease: ScheduledJobLeaseIdentity,
  ttlMs: number,
  client?: DatabaseClient
): Promise<ScheduledJobLeaseIdentity | undefined> {
  const databaseClient = await scheduledJobLeaseClient(client)
  return runScheduledJobLeaseTransaction(databaseClient, async (tx) => {
    const row = await tx.one<ScheduledJobLeaseRow>(`
      UPDATE ${scheduledJobLeaseTable(tx)}
      SET lease_until = ${postgresUtcLeaseUntilTextSql},
        heartbeat_at = ${postgresUtcNowTextSql},
        updated_at = ${postgresUtcNowTextSql}
      WHERE lease_key = ?
        AND owner_id = ?
        AND fencing_token = ?
        AND lease_until > ${postgresUtcNowTextSql}
      RETURNING lease_key, owner_id, fencing_token, lease_until
    `, [
      normalizedScheduledJobLeaseTtlMs(ttlMs),
      requiredLeaseText(lease.leaseKey, 'leaseKey'),
      requiredLeaseText(lease.ownerId, 'ownerId'),
      normalizedFencingToken(lease.fencingToken)
    ])
    return row ? scheduledJobLeaseIdentityFromRow(row) : undefined
  })
}

export async function releaseScheduledJobLease(
  lease: ScheduledJobLeaseIdentity,
  client?: DatabaseClient
): Promise<boolean> {
  const databaseClient = await scheduledJobLeaseClient(client)
  return runScheduledJobLeaseTransaction(databaseClient, async (tx) => {
    const result = await tx.execute(`
      UPDATE ${scheduledJobLeaseTable(tx)}
      SET lease_until = ${postgresUtcNowTextSql},
        heartbeat_at = ${postgresUtcNowTextSql},
        updated_at = ${postgresUtcNowTextSql}
      WHERE lease_key = ?
        AND owner_id = ?
        AND fencing_token = ?
        AND lease_until > ${postgresUtcNowTextSql}
    `, [
      requiredLeaseText(lease.leaseKey, 'leaseKey'),
      requiredLeaseText(lease.ownerId, 'ownerId'),
      normalizedFencingToken(lease.fencingToken)
    ])
    return result.changes > 0
  })
}

export async function assertScheduledJobLease(
  client: DatabaseClient,
  lease: ScheduledJobLeaseFence
): Promise<void> {
  await runScheduledJobLeaseTransaction(client, async (tx) => {
    await pinScheduledJobLeaseInTransaction(tx, lease)
  })
}

export async function pinScheduledJobLeaseInTransaction(
  client: DatabaseClient,
  lease: ScheduledJobLeaseFence
): Promise<void> {
  assertPostgresLeaseClient(client)
  const row = await client.one<{ lease_key: string }>(`
    SELECT lease_key
    FROM ${scheduledJobLeaseTable(client)}
    WHERE lease_key = ?
      AND owner_id = ?
      AND fencing_token = ?
      AND lease_until > ${postgresUtcNowTextSql}
    LIMIT 1
    FOR UPDATE
  `, [
    requiredLeaseText(lease.leaseKey, 'leaseKey'),
    requiredLeaseText(lease.ownerId, 'ownerId'),
    normalizedFencingToken(lease.fencingToken)
  ])
  if (!row) throw new ScheduledJobLeaseLostError(lease)
}

function runScheduledJobLeaseTransaction<T>(
  client: DatabaseClient,
  operation: (tx: DatabaseClient) => Promise<T>
): Promise<T> {
  // The PostgreSQL adapter applies transaction-local statement, lock and
  // idle-in-transaction timeouts before invoking operation(). Keeping every
  // lease query in this short transaction also remains compatible with
  // transaction-pooling proxies; the externally scheduled task never shares it.
  return client.transaction(operation)
}

async function scheduledJobLeaseClient(client?: DatabaseClient): Promise<DatabaseClient> {
  const databaseClient = client ?? createPostgresDatabaseClient(await getPostgresPool())
  assertPostgresLeaseClient(databaseClient)
  return databaseClient
}

function assertPostgresLeaseClient(client: DatabaseClient): void {
  if (client.driver !== 'postgres') {
    throw new Error('后台周期任务共享租约只支持 PostgreSQL')
  }
}

function scheduledJobLeaseTable(client: DatabaseClient): string {
  return client.dialect.qualifyTable('juhe_stats', 'background_job_leases')
}

function scheduledJobLeaseIdentityFromRow(row: ScheduledJobLeaseRow): ScheduledJobLeaseIdentity {
  return {
    leaseKey: requiredLeaseText(row.lease_key, 'lease_key'),
    ownerId: requiredLeaseText(row.owner_id, 'owner_id'),
    fencingToken: normalizedFencingToken(row.fencing_token),
    leaseUntil: requiredLeaseText(row.lease_until, 'lease_until')
  }
}

function normalizedScheduledJobLeaseTtlMs(value: number): number {
  const number = Number(value)
  if (!Number.isFinite(number)) throw new Error('ttlMs 必须是有限数字')
  const normalized = Math.trunc(number)
  if (normalized < minimumScheduledJobLeaseTtlMs || normalized > maximumScheduledJobLeaseTtlMs) {
    throw new Error(`ttlMs 必须介于 ${minimumScheduledJobLeaseTtlMs} 和 ${maximumScheduledJobLeaseTtlMs} 之间`)
  }
  return normalized
}

function requiredLeaseText(value: unknown, fieldName: string): string {
  const normalized = typeof value === 'string' ? value.trim() : String(value ?? '').trim()
  if (!normalized) throw new Error(`${fieldName} 不能为空`)
  if (normalized.length > 512) throw new Error(`${fieldName} 长度不能超过 512`)
  return normalized
}

function optionalLeaseText(value: unknown, fieldName: string): string | null {
  if (value === undefined || value === null) return null
  return requiredLeaseText(value, fieldName)
}

function normalizedFencingToken(value: string | number | bigint): string {
  const normalized = String(value).trim()
  if (!/^[1-9]\d*$/.test(normalized)) throw new Error('fencingToken 必须是正整数')
  return normalized
}

function postgresBoolean(value: unknown): boolean {
  return value === true || value === 1 || value === '1' || value === 't' || value === 'true'
}
