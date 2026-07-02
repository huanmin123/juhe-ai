import { getStatsDatabase, newId, nowIso } from './database.js'
import { createPostgresDatabaseClient, type DatabaseClient } from './database-client.js'
import { getPostgresPool } from './postgres-client.js'

export type BackgroundTaskRunStatus = 'queued' | 'running' | 'completed' | 'failed' | 'skipped'

export interface BackgroundTaskRunCreateInput {
  jobName: string
  jobType: string
  workerRole: string
  leaseKey: string
  params?: Record<string, unknown>
  submittedAt?: string
}

export interface BackgroundTaskRunStartInput {
  runId: string
  ownerId: string
  leaseUntil: string
  now?: string
}

export interface BackgroundTaskRunFinishInput {
  runId: string
  status: Extract<BackgroundTaskRunStatus, 'completed' | 'failed' | 'skipped'>
  result?: Record<string, unknown>
  errorMessage?: string
  exitCode?: number
  finishedAt?: string
}

export interface BackgroundTaskRunSummary {
  runId: string
  jobName: string
  jobType: string
  workerRole: string
  status: BackgroundTaskRunStatus
  leaseKey: string
  ownerId?: string
  params: Record<string, unknown>
  result: Record<string, unknown>
  errorMessage?: string
  submittedAt: string
  startedAt?: string
  heartbeatAt?: string
  finishedAt?: string
  durationMs?: number
  exitCode?: number
  createdAt: string
  updatedAt: string
}

type BackgroundTaskRunRow = Record<string, unknown>

export function createBackgroundTaskRun(input: BackgroundTaskRunCreateInput): BackgroundTaskRunSummary {
  const now = nowIso()
  const runId = newId('bgtask')
  const submittedAt = input.submittedAt ?? now
  getStatsDatabase().prepare(`
    INSERT INTO background_task_runs (
      run_id, job_name, job_type, worker_role, status, lease_key, params_json, result_json,
      submitted_at, created_at, updated_at
    ) VALUES (?, ?, ?, ?, 'queued', ?, ?, '{}', ?, ?, ?)
  `).run(
    runId,
    input.jobName,
    input.jobType,
    input.workerRole,
    input.leaseKey,
    safeJson(input.params ?? {}),
    submittedAt,
    now,
    now
  )
  return getBackgroundTaskRun(runId) as BackgroundTaskRunSummary
}

export function tryStartBackgroundTaskRun(input: BackgroundTaskRunStartInput): boolean {
  const now = input.now ?? nowIso()
  const changed = getStatsDatabase().prepare(`
    UPDATE background_task_runs
    SET status = 'running',
      owner_id = ?,
      started_at = COALESCE(started_at, ?),
      heartbeat_at = ?,
      updated_at = ?
    WHERE run_id = ?
      AND status = 'queued'
  `).run(input.ownerId, now, now, now, input.runId).changes
  if (changed <= 0) return false
  return acquireBackgroundJobLease({
    leaseKey: backgroundTaskLeaseKey(input.runId),
    jobName: 'temporary-maintenance-worker',
    shardKey: input.runId,
    ownerId: input.ownerId,
    runId: input.runId,
    leaseUntil: input.leaseUntil,
    now
  })
}

export function heartbeatBackgroundTaskRun(runId: string, ownerId: string, leaseUntil: string, now = nowIso()): boolean {
  const changed = getStatsDatabase().prepare(`
    UPDATE background_task_runs
    SET heartbeat_at = ?, updated_at = ?
    WHERE run_id = ?
      AND owner_id = ?
      AND status = 'running'
  `).run(now, now, runId, ownerId).changes
  if (changed <= 0) return false
  return renewBackgroundJobLease(backgroundTaskLeaseKey(runId), ownerId, leaseUntil, now)
}

export function finishBackgroundTaskRun(input: BackgroundTaskRunFinishInput): boolean {
  const finishedAt = input.finishedAt ?? nowIso()
  const row = getBackgroundTaskRun(input.runId)
  const startedAtMs = row?.startedAt ? Date.parse(row.startedAt) : NaN
  const durationMs = Number.isFinite(startedAtMs) ? Math.max(0, Date.parse(finishedAt) - startedAtMs) : undefined
  const changed = getStatsDatabase().prepare(`
    UPDATE background_task_runs
    SET status = ?,
      result_json = ?,
      error_message = ?,
      finished_at = ?,
      duration_ms = ?,
      exit_code = ?,
      updated_at = ?
    WHERE run_id = ?
  `).run(
    input.status,
    safeJson(input.result ?? {}),
    input.errorMessage ?? null,
    finishedAt,
    durationMs ?? null,
    typeof input.exitCode === 'number' ? Math.trunc(input.exitCode) : null,
    finishedAt,
    input.runId
  ).changes
  releaseBackgroundJobLease(backgroundTaskLeaseKey(input.runId), row?.ownerId)
  return changed > 0
}

export function getBackgroundTaskRun(runId: string): BackgroundTaskRunSummary | undefined {
  const row = getStatsDatabase().prepare(`
    SELECT *
    FROM background_task_runs
    WHERE run_id = ?
  `).get(runId) as BackgroundTaskRunRow | undefined
  return row ? backgroundTaskRunFromRow(row) : undefined
}

export async function createBackgroundTaskRunAsync(input: BackgroundTaskRunCreateInput): Promise<BackgroundTaskRunSummary> {
  const now = nowIso()
  const runId = newId('bgtask')
  const submittedAt = input.submittedAt ?? now
  const client = createPostgresDatabaseClient(await getPostgresPool())
  await client.execute(`
    INSERT INTO ${backgroundTaskRunTable(client, 'background_task_runs')} (
      run_id, job_name, job_type, worker_role, status, lease_key, params_json, result_json,
      submitted_at, created_at, updated_at
    ) VALUES (?, ?, ?, ?, 'queued', ?, ?, '{}', ?, ?, ?)
  `, [
    runId,
    input.jobName,
    input.jobType,
    input.workerRole,
    input.leaseKey,
    safeJson(input.params ?? {}),
    submittedAt,
    now,
    now
  ])
  return await getBackgroundTaskRunAsync(runId) as BackgroundTaskRunSummary
}

export async function tryStartBackgroundTaskRunAsync(input: BackgroundTaskRunStartInput): Promise<boolean> {
  const now = input.now ?? nowIso()
  const client = createPostgresDatabaseClient(await getPostgresPool())
  const changed = await client.execute(`
    UPDATE ${backgroundTaskRunTable(client, 'background_task_runs')}
    SET status = 'running',
      owner_id = ?,
      started_at = COALESCE(started_at, ?),
      heartbeat_at = ?,
      updated_at = ?
    WHERE run_id = ?
      AND status = 'queued'
  `, [input.ownerId, now, now, now, input.runId])
  if (changed.changes <= 0) return false
  return acquireBackgroundJobLeaseAsync({
    leaseKey: backgroundTaskLeaseKey(input.runId),
    jobName: 'temporary-maintenance-worker',
    shardKey: input.runId,
    ownerId: input.ownerId,
    runId: input.runId,
    leaseUntil: input.leaseUntil,
    now
  })
}

export async function heartbeatBackgroundTaskRunAsync(runId: string, ownerId: string, leaseUntil: string, now = nowIso()): Promise<boolean> {
  const client = createPostgresDatabaseClient(await getPostgresPool())
  const changed = await client.execute(`
    UPDATE ${backgroundTaskRunTable(client, 'background_task_runs')}
    SET heartbeat_at = ?, updated_at = ?
    WHERE run_id = ?
      AND owner_id = ?
      AND status = 'running'
  `, [now, now, runId, ownerId])
  if (changed.changes <= 0) return false
  return renewBackgroundJobLeaseAsync(backgroundTaskLeaseKey(runId), ownerId, leaseUntil, now, client)
}

export async function finishBackgroundTaskRunAsync(input: BackgroundTaskRunFinishInput): Promise<boolean> {
  const finishedAt = input.finishedAt ?? nowIso()
  const client = createPostgresDatabaseClient(await getPostgresPool())
  const row = await getBackgroundTaskRunAsync(input.runId, client)
  const startedAtMs = row?.startedAt ? Date.parse(row.startedAt) : NaN
  const durationMs = Number.isFinite(startedAtMs) ? Math.max(0, Date.parse(finishedAt) - startedAtMs) : undefined
  const changed = await client.execute(`
    UPDATE ${backgroundTaskRunTable(client, 'background_task_runs')}
    SET status = ?,
      result_json = ?,
      error_message = ?,
      finished_at = ?,
      duration_ms = ?,
      exit_code = ?,
      updated_at = ?
    WHERE run_id = ?
  `, [
    input.status,
    safeJson(input.result ?? {}),
    input.errorMessage ?? null,
    finishedAt,
    durationMs ?? null,
    typeof input.exitCode === 'number' ? Math.trunc(input.exitCode) : null,
    finishedAt,
    input.runId
  ])
  await releaseBackgroundJobLeaseAsync(backgroundTaskLeaseKey(input.runId), row?.ownerId, client)
  return changed.changes > 0
}

export async function getBackgroundTaskRunAsync(runId: string, client?: DatabaseClient): Promise<BackgroundTaskRunSummary | undefined> {
  const databaseClient = client ?? createPostgresDatabaseClient(await getPostgresPool())
  const row = await databaseClient.one<BackgroundTaskRunRow>(`
    SELECT *
    FROM ${backgroundTaskRunTable(databaseClient, 'background_task_runs')}
    WHERE run_id = ?
    LIMIT 1
  `, [runId])
  return row ? backgroundTaskRunFromRow(row) : undefined
}

export function acquireBackgroundJobLease(input: {
  leaseKey: string
  jobName: string
  shardKey?: string
  ownerId: string
  runId?: string
  leaseUntil: string
  now?: string
}): boolean {
  const now = input.now ?? nowIso()
  const database = getStatsDatabase()
  const existing = database.prepare(`
    SELECT lease_until AS leaseUntil
    FROM background_job_leases
    WHERE lease_key = ?
  `).get(input.leaseKey) as { leaseUntil?: string } | undefined
  if (existing && existing.leaseUntil && existing.leaseUntil > now) {
    return false
  }
  database.prepare(`
    INSERT INTO background_job_leases (
      lease_key, job_name, shard_key, owner_id, run_id, lease_until, heartbeat_at, started_at, updated_at
    ) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
    ON CONFLICT(lease_key) DO UPDATE SET
      job_name = excluded.job_name,
      shard_key = excluded.shard_key,
      owner_id = excluded.owner_id,
      run_id = excluded.run_id,
      lease_until = excluded.lease_until,
      heartbeat_at = excluded.heartbeat_at,
      started_at = excluded.started_at,
      updated_at = excluded.updated_at
  `).run(
    input.leaseKey,
    input.jobName,
    input.shardKey ?? '',
    input.ownerId,
    input.runId ?? null,
    input.leaseUntil,
    now,
    now,
    now
  )
  return true
}

function renewBackgroundJobLease(leaseKey: string, ownerId: string, leaseUntil: string, now: string): boolean {
  return getStatsDatabase().prepare(`
    UPDATE background_job_leases
    SET lease_until = ?, heartbeat_at = ?, updated_at = ?
    WHERE lease_key = ?
      AND owner_id = ?
  `).run(leaseUntil, now, now, leaseKey, ownerId).changes > 0
}

function releaseBackgroundJobLease(leaseKey: string, ownerId?: string): void {
  if (!ownerId) return
  getStatsDatabase().prepare(`
    DELETE FROM background_job_leases
    WHERE lease_key = ?
      AND owner_id = ?
  `).run(leaseKey, ownerId)
}

async function acquireBackgroundJobLeaseAsync(input: {
  leaseKey: string
  jobName: string
  shardKey?: string
  ownerId: string
  runId?: string
  leaseUntil: string
  now?: string
}): Promise<boolean> {
  const now = input.now ?? nowIso()
  const client = createPostgresDatabaseClient(await getPostgresPool())
  return await client.transaction(async (tx) => {
    const existing = await tx.one<{ lease_until?: string | null }>(`
      SELECT lease_until
      FROM ${backgroundTaskRunTable(tx, 'background_job_leases')}
      WHERE lease_key = ?
      FOR UPDATE
    `, [input.leaseKey])
    if (existing?.lease_until && existing.lease_until > now) {
      return false
    }
    await tx.execute(`
      INSERT INTO ${backgroundTaskRunTable(tx, 'background_job_leases')} (
        lease_key, job_name, shard_key, owner_id, run_id, lease_until, heartbeat_at, started_at, updated_at
      ) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
      ON CONFLICT(lease_key) DO UPDATE SET
        job_name = excluded.job_name,
        shard_key = excluded.shard_key,
        owner_id = excluded.owner_id,
        run_id = excluded.run_id,
        lease_until = excluded.lease_until,
        heartbeat_at = excluded.heartbeat_at,
        started_at = excluded.started_at,
        updated_at = excluded.updated_at
    `, [
      input.leaseKey,
      input.jobName,
      input.shardKey ?? '',
      input.ownerId,
      input.runId ?? null,
      input.leaseUntil,
      now,
      now,
      now
    ])
    return true
  })
}

async function renewBackgroundJobLeaseAsync(leaseKey: string, ownerId: string, leaseUntil: string, now: string, client?: DatabaseClient): Promise<boolean> {
  const databaseClient = client ?? createPostgresDatabaseClient(await getPostgresPool())
  const result = await databaseClient.execute(`
    UPDATE ${backgroundTaskRunTable(databaseClient, 'background_job_leases')}
    SET lease_until = ?, heartbeat_at = ?, updated_at = ?
    WHERE lease_key = ?
      AND owner_id = ?
  `, [leaseUntil, now, now, leaseKey, ownerId])
  return result.changes > 0
}

async function releaseBackgroundJobLeaseAsync(leaseKey: string, ownerId?: string, client?: DatabaseClient): Promise<void> {
  if (!ownerId) return
  const databaseClient = client ?? createPostgresDatabaseClient(await getPostgresPool())
  await databaseClient.execute(`
    DELETE FROM ${backgroundTaskRunTable(databaseClient, 'background_job_leases')}
    WHERE lease_key = ?
      AND owner_id = ?
  `, [leaseKey, ownerId])
}

function backgroundTaskLeaseKey(runId: string): string {
  return `temporary-maintenance-worker:${runId}`
}

function backgroundTaskRunTable(client: DatabaseClient, tableName: string): string {
  return client.driver === 'postgres'
    ? client.dialect.qualifyTable('juhe_stats', tableName)
    : client.dialect.quoteIdentifier(tableName)
}

function backgroundTaskRunFromRow(row: BackgroundTaskRunRow): BackgroundTaskRunSummary {
  return {
    runId: String(row.run_id),
    jobName: String(row.job_name),
    jobType: String(row.job_type),
    workerRole: String(row.worker_role),
    status: normalizeStatus(row.status),
    leaseKey: String(row.lease_key),
    ownerId: optionalString(row.owner_id),
    params: parseJsonObject(row.params_json),
    result: parseJsonObject(row.result_json),
    errorMessage: optionalString(row.error_message),
    submittedAt: String(row.submitted_at),
    startedAt: optionalString(row.started_at),
    heartbeatAt: optionalString(row.heartbeat_at),
    finishedAt: optionalString(row.finished_at),
    durationMs: optionalNumber(row.duration_ms),
    exitCode: optionalNumber(row.exit_code),
    createdAt: String(row.created_at),
    updatedAt: String(row.updated_at)
  }
}

function normalizeStatus(value: unknown): BackgroundTaskRunStatus {
  return value === 'queued' || value === 'running' || value === 'completed' || value === 'failed' || value === 'skipped'
    ? value
    : 'failed'
}

function optionalString(value: unknown): string | undefined {
  return typeof value === 'string' && value ? value : undefined
}

function optionalNumber(value: unknown): number | undefined {
  return typeof value === 'number' && Number.isFinite(value) ? value : undefined
}

function parseJsonObject(value: unknown): Record<string, unknown> {
  if (typeof value !== 'string' || !value) return {}
  try {
    const parsed = JSON.parse(value) as unknown
    return parsed && typeof parsed === 'object' && !Array.isArray(parsed) ? parsed as Record<string, unknown> : {}
  } catch {
    return {}
  }
}

function safeJson(value: Record<string, unknown>): string {
  try {
    return JSON.stringify(value)
  } catch {
    return '{}'
  }
}
