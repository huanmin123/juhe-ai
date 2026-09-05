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
import { requiredRfc3339Instant, rfc3339InstantMilliseconds } from '../shared/rfc3339.js'

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

export function createBackgroundTaskRun(input: BackgroundTaskRunCreateInput): BackgroundTaskRunSummary {
  const now = nowIso()
  const runId = newId('bgtask')
  const submittedAt = input.submittedAt === undefined
    ? now
    : requiredRfc3339Instant(input.submittedAt, '后台任务 submittedAt')
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
  const now = input.now === undefined ? nowIso() : requiredRfc3339Instant(input.now, '后台任务 now')
  const leaseUntil = requiredRfc3339Instant(input.leaseUntil, '后台任务 leaseUntil')
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
    leaseUntil,
    now
  })
}

export function heartbeatBackgroundTaskRun(runId: string, ownerId: string, leaseUntil: string, now?: string): boolean {
  const normalizedNow = now === undefined ? nowIso() : requiredRfc3339Instant(now, '后台任务 now')
  const normalizedLeaseUntil = requiredRfc3339Instant(leaseUntil, '后台任务 leaseUntil')
  const changed = getStatsDatabase().prepare(`
    UPDATE background_task_runs
    SET heartbeat_at = ?, updated_at = ?
    WHERE run_id = ?
      AND owner_id = ?
      AND status = 'running'
  `).run(normalizedNow, normalizedNow, runId, ownerId).changes
  if (changed <= 0) return false
  return renewBackgroundJobLease(backgroundTaskLeaseKey(runId), ownerId, normalizedLeaseUntil, normalizedNow)
}

export function finishBackgroundTaskRun(input: BackgroundTaskRunFinishInput): boolean {
  const finishedAt = input.finishedAt === undefined
    ? nowIso()
    : requiredRfc3339Instant(input.finishedAt, '后台任务 finishedAt')
  const row = getBackgroundTaskRun(input.runId)
  const startedAtMs = row?.startedAt === undefined ? undefined : rfc3339InstantMilliseconds(row.startedAt)
  if (row?.startedAt !== undefined && startedAtMs === undefined) {
    throw new Error('后台任务 startedAt 必须是带 Z 或数值 offset 的 RFC3339 时间')
  }
  const finishedAtMs = rfc3339InstantMilliseconds(finishedAt)
  if (finishedAtMs === undefined) throw new Error('后台任务 finishedAt 必须是带 Z 或数值 offset 的 RFC3339 时间')
  const durationMs = startedAtMs === undefined ? undefined : Math.max(0, finishedAtMs - startedAtMs)
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
  const submittedAt = input.submittedAt === undefined
    ? now
    : requiredRfc3339Instant(input.submittedAt, '后台任务 submittedAt')
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
  const now = input.now === undefined ? nowIso() : requiredRfc3339Instant(input.now, '后台任务 now')
  const leaseUntil = requiredRfc3339Instant(input.leaseUntil, '后台任务 leaseUntil')
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
    leaseUntil,
    now
  })
}

export async function heartbeatBackgroundTaskRunAsync(runId: string, ownerId: string, leaseUntil: string, now?: string): Promise<boolean> {
  const normalizedNow = now === undefined ? nowIso() : requiredRfc3339Instant(now, '后台任务 now')
  const normalizedLeaseUntil = requiredRfc3339Instant(leaseUntil, '后台任务 leaseUntil')
  const client = createPostgresDatabaseClient(await getPostgresPool())
  const changed = await client.execute(`
    UPDATE ${backgroundTaskRunTable(client, 'background_task_runs')}
    SET heartbeat_at = ?, updated_at = ?
    WHERE run_id = ?
      AND owner_id = ?
      AND status = 'running'
  `, [normalizedNow, normalizedNow, runId, ownerId])
  if (changed.changes <= 0) return false
  return renewBackgroundJobLeaseAsync(backgroundTaskLeaseKey(runId), ownerId, normalizedLeaseUntil, normalizedNow, client)
}

export async function finishBackgroundTaskRunAsync(input: BackgroundTaskRunFinishInput): Promise<boolean> {
  const finishedAt = input.finishedAt === undefined
    ? nowIso()
    : requiredRfc3339Instant(input.finishedAt, '后台任务 finishedAt')
  const client = createPostgresDatabaseClient(await getPostgresPool())
  const row = await getBackgroundTaskRunAsync(input.runId, client)
  const startedAtMs = row?.startedAt === undefined ? undefined : rfc3339InstantMilliseconds(row.startedAt)
  if (row?.startedAt !== undefined && startedAtMs === undefined) {
    throw new Error('后台任务 startedAt 必须是带 Z 或数值 offset 的 RFC3339 时间')
  }
  const finishedAtMs = rfc3339InstantMilliseconds(finishedAt)
  if (finishedAtMs === undefined) throw new Error('后台任务 finishedAt 必须是带 Z 或数值 offset 的 RFC3339 时间')
  const durationMs = startedAtMs === undefined ? undefined : Math.max(0, finishedAtMs - startedAtMs)
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

export function reconcileStaleBackgroundTaskRuns(input: BackgroundTaskRunReconcileInput): BackgroundTaskRunReconcileResult {
  const now = input.now === undefined ? nowIso() : requiredRfc3339Instant(input.now, '后台任务 now')
  const queuedBefore = requiredRfc3339Instant(input.queuedBefore, '后台任务 queuedBefore')
  const runningHeartbeatBefore = requiredRfc3339Instant(input.runningHeartbeatBefore, '后台任务 runningHeartbeatBefore')
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
      queuedBefore,
      now,
      queuedBefore,
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
      runningHeartbeatBefore,
      now,
      runningHeartbeatBefore,
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
  const now = input.now === undefined ? nowIso() : requiredRfc3339Instant(input.now, '后台任务 now')
  const queuedBefore = requiredRfc3339Instant(input.queuedBefore, '后台任务 queuedBefore')
  const runningHeartbeatBefore = requiredRfc3339Instant(input.runningHeartbeatBefore, '后台任务 runningHeartbeatBefore')
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
      queuedBefore,
      now,
      queuedBefore,
      now,
      limit
    ])).changes
    const failedRunningCount = (await tx.execute(reconcileRunningTaskRunsSql(runsTable, leasesTable), [
      safeJson({ reconciled: true, reconciledReason: 'lease_expired_after_worker_exit' }),
      '临时维护 worker 心跳中断且无有效租约，后台任务已自动收口为失败',
      now,
      now,
      runningHeartbeatBefore,
      now,
      runningHeartbeatBefore,
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
  const now = input.now === undefined ? nowIso() : requiredRfc3339Instant(input.now, '后台任务 now')
  const leaseUntil = requiredRfc3339Instant(input.leaseUntil, '后台任务 leaseUntil')
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
    leaseUntil,
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

export function releaseBackgroundJobLease(leaseKey: string, ownerId?: string): void {
  if (!ownerId) return
  getStatsDatabase().prepare(`
    DELETE FROM background_job_leases
    WHERE lease_key = ?
      AND owner_id = ?
  `).run(leaseKey, ownerId)
}

export async function acquireBackgroundJobLeaseAsync(input: {
  leaseKey: string
  jobName: string
  shardKey?: string
  ownerId: string
  runId?: string
  leaseUntil: string
  now?: string
}): Promise<boolean> {
  if (runtimeConfig.databaseDriver !== 'postgres') return acquireBackgroundJobLease(input)
  const now = input.now === undefined ? nowIso() : requiredRfc3339Instant(input.now, '后台任务 now')
  const leaseUntil = requiredRfc3339Instant(input.leaseUntil, '后台任务 leaseUntil')
  const client = createPostgresDatabaseClient(await getPostgresPool())
  const result = await client.execute(`
      INSERT INTO ${backgroundTaskRunTable(client, 'background_job_leases')} (
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
      leaseUntil,
      now,
      now,
      now,
      now
    ])
  return result.changes > 0
}

export async function renewBackgroundJobLeaseAsync(leaseKey: string, ownerId: string, leaseUntil: string, now?: string, client?: DatabaseClient): Promise<boolean> {
  const normalizedNow = now === undefined ? nowIso() : requiredRfc3339Instant(now, '后台任务 now')
  const normalizedLeaseUntil = requiredRfc3339Instant(leaseUntil, '后台任务 leaseUntil')
  if (runtimeConfig.databaseDriver !== 'postgres') return renewBackgroundJobLease(leaseKey, ownerId, normalizedLeaseUntil, normalizedNow)
  const databaseClient = client ?? createPostgresDatabaseClient(await getPostgresPool())
  const result = await databaseClient.execute(`
    UPDATE ${backgroundTaskRunTable(databaseClient, 'background_job_leases')}
    SET lease_until = ?, heartbeat_at = ?, updated_at = ?
    WHERE lease_key = ?
      AND owner_id = ?
  `, [normalizedLeaseUntil, normalizedNow, normalizedNow, leaseKey, ownerId])
  return result.changes > 0
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

function backgroundTaskLeaseKey(runId: string): string {
  return `temporary-maintenance-worker:${runId}`
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
          AND current_lease.job_name = 'temporary-maintenance-worker'
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
            AND leases.job_name = 'temporary-maintenance-worker'
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
          AND current_lease.job_name = 'temporary-maintenance-worker'
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
            AND leases.job_name = 'temporary-maintenance-worker'
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
      WHERE leases.job_name = 'temporary-maintenance-worker'
        AND leases.lease_until <= ?
        AND (runs.run_id IS NULL OR runs.status NOT IN ('queued', 'running'))
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
    submittedAt: requiredRfc3339Instant(row.submitted_at, 'background_task_runs.submitted_at'),
    startedAt: optionalTimestamp(row.started_at, 'background_task_runs.started_at'),
    heartbeatAt: optionalTimestamp(row.heartbeat_at, 'background_task_runs.heartbeat_at'),
    finishedAt: optionalTimestamp(row.finished_at, 'background_task_runs.finished_at'),
    durationMs: optionalNumber(row.duration_ms),
    exitCode: optionalNumber(row.exit_code),
    createdAt: requiredRfc3339Instant(row.created_at, 'background_task_runs.created_at'),
    updatedAt: requiredRfc3339Instant(row.updated_at, 'background_task_runs.updated_at')
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

function optionalTimestamp(value: unknown, field: string): string | undefined {
  if (value === null || value === undefined) return undefined
  return requiredRfc3339Instant(value, field)
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
