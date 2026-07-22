import { runtimeConfig } from '../config/runtime.js'
import {
  beginImmediateDatabaseTransaction,
  commitDatabaseTransaction,
  getStatsDatabase,
  newId,
  nowIso,
  rollbackDatabaseTransaction
} from './database.js'
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
  ownerId: string
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

export interface BackgroundTaskRunReconcileInput {
  queuedBefore: string
  runningHeartbeatBefore: string
  now?: string
  limit?: number
}

export interface BackgroundTaskRunReconcileResult {
  failedQueuedCount: number
  failedRunningCount: number
  deletedExpiredLeaseCount: number
}

type BackgroundTaskRunRow = Record<string, unknown>
type BackgroundTaskLeaseRow = {
  run_id: unknown
  job_name: unknown
  lease_key: unknown
}

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
  const database = getStatsDatabase()
  const transactionStarted = beginImmediateDatabaseTransaction(database)
  try {
    const row = database.prepare(`
      SELECT run_id, job_name, lease_key
      FROM background_task_runs
      WHERE run_id = ?
        AND status = 'queued'
    `).get(input.runId) as BackgroundTaskLeaseRow | undefined
    if (!row) {
      commitDatabaseTransaction(database, transactionStarted)
      return false
    }
    const acquired = acquireBackgroundJobLease({
      leaseKey: String(row.lease_key),
      jobName: String(row.job_name),
      shardKey: String(row.run_id),
      ownerId: input.ownerId,
      runId: input.runId,
      leaseUntil: input.leaseUntil,
      now
    })
    if (!acquired) {
      commitDatabaseTransaction(database, transactionStarted)
      return false
    }
    const changed = database.prepare(`
      UPDATE background_task_runs
      SET status = 'running',
        owner_id = ?,
        started_at = COALESCE(started_at, ?),
        heartbeat_at = ?,
        updated_at = ?
      WHERE run_id = ?
        AND status = 'queued'
    `).run(input.ownerId, now, now, now, input.runId).changes
    if (changed <= 0) throw new Error('后台任务运行权状态切换失败')
    commitDatabaseTransaction(database, transactionStarted)
    return true
  } catch (error) {
    rollbackDatabaseTransaction(database, transactionStarted)
    throw error
  }
}

export function heartbeatBackgroundTaskRun(runId: string, ownerId: string, leaseUntil: string, now = nowIso()): boolean {
  const database = getStatsDatabase()
  const transactionStarted = beginImmediateDatabaseTransaction(database)
  try {
    const row = database.prepare(`
      SELECT lease_key
      FROM background_task_runs
      WHERE run_id = ?
        AND owner_id = ?
        AND status = 'running'
    `).get(runId, ownerId) as Pick<BackgroundTaskLeaseRow, 'lease_key'> | undefined
    if (!row || !renewBackgroundTaskJobLease(String(row.lease_key), ownerId, runId, leaseUntil, now)) {
      commitDatabaseTransaction(database, transactionStarted)
      return false
    }
    const changed = database.prepare(`
      UPDATE background_task_runs
      SET heartbeat_at = ?, updated_at = ?
      WHERE run_id = ?
        AND owner_id = ?
        AND status = 'running'
    `).run(now, now, runId, ownerId).changes
    if (changed <= 0) throw new Error('后台任务心跳状态更新失败')
    commitDatabaseTransaction(database, transactionStarted)
    return true
  } catch (error) {
    rollbackDatabaseTransaction(database, transactionStarted)
    throw error
  }
}

export function finishBackgroundTaskRun(input: BackgroundTaskRunFinishInput): boolean {
  const finishedAt = input.finishedAt ?? nowIso()
  const row = getBackgroundTaskRun(input.runId)
  if (!row || row.ownerId !== input.ownerId || row.status !== 'running') return false
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
      AND owner_id = ?
      AND status = 'running'
  `).run(
    input.status,
    safeJson(input.result ?? {}),
    input.errorMessage ?? null,
    finishedAt,
    durationMs ?? null,
    typeof input.exitCode === 'number' ? Math.trunc(input.exitCode) : null,
    finishedAt,
    input.runId,
    input.ownerId
  ).changes
  if (changed > 0) releaseBackgroundTaskJobLease(row.leaseKey, input.ownerId, input.runId)
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
  return await client.transaction(async (tx) => {
    const row = await tx.one<BackgroundTaskLeaseRow>(`
      SELECT run_id, job_name, lease_key
      FROM ${backgroundTaskRunTable(tx, 'background_task_runs')}
      WHERE run_id = ?
        AND status = 'queued'
      FOR UPDATE
    `, [input.runId])
    if (!row) return false
    const acquired = await acquireBackgroundJobLeaseAsync({
      leaseKey: String(row.lease_key),
      jobName: String(row.job_name),
      shardKey: String(row.run_id),
      ownerId: input.ownerId,
      runId: input.runId,
      leaseUntil: input.leaseUntil,
      now
    }, tx)
    if (!acquired) return false
    const changed = await tx.execute(`
      UPDATE ${backgroundTaskRunTable(tx, 'background_task_runs')}
      SET status = 'running',
        owner_id = ?,
        started_at = COALESCE(started_at, ?),
        heartbeat_at = ?,
        updated_at = ?
      WHERE run_id = ?
        AND status = 'queued'
    `, [input.ownerId, now, now, now, input.runId])
    if (changed.changes <= 0) throw new Error('后台任务运行权状态切换失败')
    return true
  })
}

export async function heartbeatBackgroundTaskRunAsync(runId: string, ownerId: string, leaseUntil: string, now = nowIso()): Promise<boolean> {
  const client = createPostgresDatabaseClient(await getPostgresPool())
  return await client.transaction(async (tx) => {
    const row = await tx.one<Pick<BackgroundTaskLeaseRow, 'lease_key'>>(`
      SELECT lease_key
      FROM ${backgroundTaskRunTable(tx, 'background_task_runs')}
      WHERE run_id = ?
        AND owner_id = ?
        AND status = 'running'
      FOR UPDATE
    `, [runId, ownerId])
    if (!row || !await renewBackgroundTaskJobLeaseAsync(String(row.lease_key), ownerId, runId, leaseUntil, now, tx)) return false
    const changed = await tx.execute(`
      UPDATE ${backgroundTaskRunTable(tx, 'background_task_runs')}
      SET heartbeat_at = ?, updated_at = ?
      WHERE run_id = ?
        AND owner_id = ?
        AND status = 'running'
    `, [now, now, runId, ownerId])
    if (changed.changes <= 0) throw new Error('后台任务心跳状态更新失败')
    return true
  })
}

export async function finishBackgroundTaskRunAsync(input: BackgroundTaskRunFinishInput): Promise<boolean> {
  const finishedAt = input.finishedAt ?? nowIso()
  const client = createPostgresDatabaseClient(await getPostgresPool())
  const row = await getBackgroundTaskRunAsync(input.runId, client)
  if (!row || row.ownerId !== input.ownerId || row.status !== 'running') return false
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
      AND owner_id = ?
      AND status = 'running'
  `, [
    input.status,
    safeJson(input.result ?? {}),
    input.errorMessage ?? null,
    finishedAt,
    durationMs ?? null,
    typeof input.exitCode === 'number' ? Math.trunc(input.exitCode) : null,
    finishedAt,
    input.runId,
    input.ownerId
  ])
  if (changed.changes > 0) await releaseBackgroundTaskJobLeaseAsync(row.leaseKey, input.ownerId, input.runId, client)
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

export function reconcileStaleBackgroundTaskRuns(input: BackgroundTaskRunReconcileInput): BackgroundTaskRunReconcileResult {
  const now = input.now ?? nowIso()
  const limit = normalizeReconcileLimit(input.limit)
  const database = getStatsDatabase()
  const transactionStarted = beginImmediateDatabaseTransaction(database)
  try {
    const failedQueuedCount = Number(database.prepare(reconcileQueuedTaskRunsSql(
      'background_task_runs',
      'background_job_leases'
    )).run(
      safeJson({ reconciled: true, reconciledReason: 'worker_never_started' }),
      '临时维护 worker 未在期限内启动，后台任务已自动收口为失败',
      now,
      now,
      input.queuedBefore,
      now,
      input.queuedBefore,
      now,
      limit
    ).changes ?? 0)
    const failedRunningCount = Number(database.prepare(reconcileRunningTaskRunsSql(
      'background_task_runs',
      'background_job_leases'
    )).run(
      safeJson({ reconciled: true, reconciledReason: 'lease_expired_after_worker_exit' }),
      '临时维护 worker 心跳中断且无有效租约，后台任务已自动收口为失败',
      now,
      now,
      input.runningHeartbeatBefore,
      now,
      input.runningHeartbeatBefore,
      now,
      limit
    ).changes ?? 0)
    const deletedExpiredLeaseCount = Number(database.prepare(deleteExpiredTemporaryLeasesSql(
      'background_task_runs',
      'background_job_leases'
    )).run(now, limit).changes ?? 0)
    commitDatabaseTransaction(database, transactionStarted)
    return { failedQueuedCount, failedRunningCount, deletedExpiredLeaseCount }
  } catch (error) {
    rollbackDatabaseTransaction(database, transactionStarted)
    throw error
  }
}

export async function reconcileStaleBackgroundTaskRunsAsync(input: BackgroundTaskRunReconcileInput): Promise<BackgroundTaskRunReconcileResult> {
  if (runtimeConfig.databaseDriver !== 'postgres') return reconcileStaleBackgroundTaskRuns(input)
  const now = input.now ?? nowIso()
  const limit = normalizeReconcileLimit(input.limit)
  const client = createPostgresDatabaseClient(await getPostgresPool())
  return await client.transaction(async (tx) => {
    const runsTable = backgroundTaskRunTable(tx, 'background_task_runs')
    const leasesTable = backgroundTaskRunTable(tx, 'background_job_leases')
    const failedQueuedCount = (await tx.execute(reconcileQueuedTaskRunsSql(runsTable, leasesTable), [
      safeJson({ reconciled: true, reconciledReason: 'worker_never_started' }),
      '临时维护 worker 未在期限内启动，后台任务已自动收口为失败',
      now,
      now,
      input.queuedBefore,
      now,
      input.queuedBefore,
      now,
      limit
    ])).changes
    const failedRunningCount = (await tx.execute(reconcileRunningTaskRunsSql(runsTable, leasesTable), [
      safeJson({ reconciled: true, reconciledReason: 'lease_expired_after_worker_exit' }),
      '临时维护 worker 心跳中断且无有效租约，后台任务已自动收口为失败',
      now,
      now,
      input.runningHeartbeatBefore,
      now,
      input.runningHeartbeatBefore,
      now,
      limit
    ])).changes
    const deletedExpiredLeaseCount = (await tx.execute(deleteExpiredTemporaryLeasesSql(runsTable, leasesTable), [now, limit])).changes
    return { failedQueuedCount, failedRunningCount, deletedExpiredLeaseCount }
  })
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
  const result = database.prepare(`
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
    WHERE background_job_leases.lease_until <= ?
  `).run(
    input.leaseKey,
    input.jobName,
    input.shardKey ?? '',
    input.ownerId,
    input.runId ?? null,
    input.leaseUntil,
    now,
    now,
    now,
    now
  )
  return result.changes > 0
}

function renewBackgroundJobLease(leaseKey: string, ownerId: string, leaseUntil: string, now: string): boolean {
  return getStatsDatabase().prepare(`
    UPDATE background_job_leases
    SET lease_until = ?, heartbeat_at = ?, updated_at = ?
    WHERE lease_key = ?
      AND owner_id = ?
  `).run(leaseUntil, now, now, leaseKey, ownerId).changes > 0
}

function renewBackgroundTaskJobLease(leaseKey: string, ownerId: string, runId: string, leaseUntil: string, now: string): boolean {
  return getStatsDatabase().prepare(`
    UPDATE background_job_leases
    SET lease_until = ?, heartbeat_at = ?, updated_at = ?
    WHERE lease_key = ?
      AND owner_id = ?
      AND run_id = ?
  `).run(leaseUntil, now, now, leaseKey, ownerId, runId).changes > 0
}

export function releaseBackgroundJobLease(leaseKey: string, ownerId?: string): void {
  if (!ownerId) return
  getStatsDatabase().prepare(`
    DELETE FROM background_job_leases
    WHERE lease_key = ?
      AND owner_id = ?
  `).run(leaseKey, ownerId)
}

function releaseBackgroundTaskJobLease(leaseKey: string, ownerId: string, runId: string): void {
  getStatsDatabase().prepare(`
    DELETE FROM background_job_leases
    WHERE lease_key = ?
      AND owner_id = ?
      AND run_id = ?
  `).run(leaseKey, ownerId, runId)
}

export async function acquireBackgroundJobLeaseAsync(input: {
  leaseKey: string
  jobName: string
  shardKey?: string
  ownerId: string
  runId?: string
  leaseUntil: string
  now?: string
}, client?: DatabaseClient): Promise<boolean> {
  if (runtimeConfig.databaseDriver !== 'postgres') return acquireBackgroundJobLease(input)
  const now = input.now ?? nowIso()
  const databaseClient = client ?? createPostgresDatabaseClient(await getPostgresPool())
  const result = await databaseClient.execute(`
      INSERT INTO ${backgroundTaskRunTable(databaseClient, 'background_job_leases')} (
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
      WHERE background_job_leases.lease_until <= ?
    `, [
      input.leaseKey,
      input.jobName,
      input.shardKey ?? '',
      input.ownerId,
      input.runId ?? null,
      input.leaseUntil,
      now,
      now,
      now,
      now
    ])
  return result.changes > 0
}

export async function renewBackgroundJobLeaseAsync(leaseKey: string, ownerId: string, leaseUntil: string, now = nowIso(), client?: DatabaseClient): Promise<boolean> {
  if (runtimeConfig.databaseDriver !== 'postgres') return renewBackgroundJobLease(leaseKey, ownerId, leaseUntil, now)
  const databaseClient = client ?? createPostgresDatabaseClient(await getPostgresPool())
  const result = await databaseClient.execute(`
    UPDATE ${backgroundTaskRunTable(databaseClient, 'background_job_leases')}
    SET lease_until = ?, heartbeat_at = ?, updated_at = ?
    WHERE lease_key = ?
      AND owner_id = ?
  `, [leaseUntil, now, now, leaseKey, ownerId])
  return result.changes > 0
}

async function renewBackgroundTaskJobLeaseAsync(
  leaseKey: string,
  ownerId: string,
  runId: string,
  leaseUntil: string,
  now: string,
  client: DatabaseClient
): Promise<boolean> {
  if (runtimeConfig.databaseDriver !== 'postgres') return renewBackgroundTaskJobLease(leaseKey, ownerId, runId, leaseUntil, now)
  const result = await client.execute(`
    UPDATE ${backgroundTaskRunTable(client, 'background_job_leases')}
    SET lease_until = ?, heartbeat_at = ?, updated_at = ?
    WHERE lease_key = ?
      AND owner_id = ?
      AND run_id = ?
  `, [leaseUntil, now, now, leaseKey, ownerId, runId])
  return result.changes > 0
}

async function releaseBackgroundTaskJobLeaseAsync(leaseKey: string, ownerId: string, runId: string, client: DatabaseClient): Promise<void> {
  if (runtimeConfig.databaseDriver !== 'postgres') {
    releaseBackgroundTaskJobLease(leaseKey, ownerId, runId)
    return
  }
  await client.execute(`
    DELETE FROM ${backgroundTaskRunTable(client, 'background_job_leases')}
    WHERE lease_key = ?
      AND owner_id = ?
      AND run_id = ?
  `, [leaseKey, ownerId, runId])
}

export async function releaseBackgroundJobLeaseAsync(leaseKey: string, ownerId?: string, client?: DatabaseClient): Promise<void> {
  if (!ownerId) return
  if (runtimeConfig.databaseDriver !== 'postgres') {
    releaseBackgroundJobLease(leaseKey, ownerId)
    return
  }
  const databaseClient = client ?? createPostgresDatabaseClient(await getPostgresPool())
  await databaseClient.execute(`
    DELETE FROM ${backgroundTaskRunTable(databaseClient, 'background_job_leases')}
    WHERE lease_key = ?
      AND owner_id = ?
  `, [leaseKey, ownerId])
}

function reconcileQueuedTaskRunsSql(runsTable: string, leasesTable: string): string {
  return `
    UPDATE ${runsTable} AS target
    SET status = 'failed',
      result_json = ?,
      error_message = ?,
      finished_at = ?,
      updated_at = ?
    WHERE target.worker_role = 'temporary-maintenance-worker'
      AND target.status = 'queued'
      AND target.submitted_at <= ?
      AND NOT EXISTS (
        SELECT 1
        FROM ${leasesTable} current_lease
        WHERE current_lease.run_id = target.run_id
          AND current_lease.lease_key = target.lease_key
          AND current_lease.lease_until > ?
      )
      AND target.run_id IN (
      SELECT runs.run_id
      FROM ${runsTable} runs
      WHERE runs.worker_role = 'temporary-maintenance-worker'
        AND runs.status = 'queued'
        AND runs.submitted_at <= ?
        AND NOT EXISTS (
          SELECT 1
          FROM ${leasesTable} leases
          WHERE leases.run_id = runs.run_id
            AND leases.lease_key = runs.lease_key
            AND leases.lease_until > ?
        )
      ORDER BY runs.updated_at ASC, runs.run_id ASC
      LIMIT ?
    )
  `
}

function reconcileRunningTaskRunsSql(runsTable: string, leasesTable: string): string {
  return `
    UPDATE ${runsTable} AS target
    SET status = 'failed',
      result_json = ?,
      error_message = ?,
      finished_at = ?,
      updated_at = ?
    WHERE target.worker_role = 'temporary-maintenance-worker'
      AND target.status = 'running'
      AND COALESCE(target.heartbeat_at, target.started_at, target.updated_at) <= ?
      AND NOT EXISTS (
        SELECT 1
        FROM ${leasesTable} current_lease
        WHERE current_lease.run_id = target.run_id
          AND current_lease.lease_key = target.lease_key
          AND current_lease.lease_until > ?
      )
      AND target.run_id IN (
      SELECT runs.run_id
      FROM ${runsTable} runs
      WHERE runs.worker_role = 'temporary-maintenance-worker'
        AND runs.status = 'running'
        AND COALESCE(runs.heartbeat_at, runs.started_at, runs.updated_at) <= ?
        AND NOT EXISTS (
          SELECT 1
          FROM ${leasesTable} leases
          WHERE leases.run_id = runs.run_id
            AND leases.lease_key = runs.lease_key
            AND leases.lease_until > ?
        )
      ORDER BY runs.updated_at ASC, runs.run_id ASC
      LIMIT ?
    )
  `
}

function deleteExpiredTemporaryLeasesSql(runsTable: string, leasesTable: string): string {
  return `
    DELETE FROM ${leasesTable}
    WHERE lease_key IN (
      SELECT leases.lease_key
      FROM ${leasesTable} leases
      LEFT JOIN ${runsTable} runs ON runs.run_id = leases.run_id
      WHERE leases.run_id IS NOT NULL
        AND leases.lease_until <= ?
        AND (
          runs.run_id IS NULL
          OR (runs.worker_role = 'temporary-maintenance-worker' AND runs.status NOT IN ('queued', 'running'))
        )
      ORDER BY leases.lease_until ASC, leases.lease_key ASC
      LIMIT ?
    )
  `
}

function normalizeReconcileLimit(value: number | undefined): number {
  return typeof value === 'number' && Number.isFinite(value)
    ? Math.min(1000, Math.max(1, Math.trunc(value)))
    : 500
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
