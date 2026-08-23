import assert from 'node:assert/strict'
import { createRequire } from 'node:module'
import { mkdtempSync, readFileSync, rmSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join, resolve } from 'node:path'
import { createSqliteDatabaseClient } from '../../storage/database-client.js'
import { decodeProxyLatencyJobsOutcome, listProxyLatencyJobsOutcomes } from '../../storage/proxy-latency-jobs-outcome.repository.js'
import { advanceProxyLatencyProjectionCursorAsync, currentProxyLatencyProjectionCursorAsync } from '../../storage/proxy-latency-projection-cursor.repository.js'
import { projectProxyLatencyJobsOutcomeAsync } from '../../modules/background/proxy-latency-jobs-projector.service.js'

const require = createRequire(import.meta.url)
const DatabaseSync = require('node:sqlite').DatabaseSync as new (path: string) => {
  exec(sql: string): void
  prepare(sql: string): { get(...values: unknown[]): unknown; all(...values: unknown[]): unknown[]; run(...values: unknown[]): unknown }
  close(): void
}

const tempDir = mkdtempSync(join(tmpdir(), 'juhe-ai-j3a-outcome-'))
const dbPath = join(tempDir, 'business.sqlite')
let businessDatabase: { close(): void } | undefined
let jobsDatabase: { close(): void } | undefined
try {
  const database = new DatabaseSync(dbPath)
  businessDatabase = database
  database.exec(`
    CREATE TABLE proxy_profiles (
      id TEXT PRIMARY KEY,
      updated_at TEXT NOT NULL,
      last_tested_at TEXT,
      test_status TEXT,
      latency_ms INTEGER,
      last_test_message TEXT
    );
    CREATE TABLE proxy_latency_projection_receipts (
      outcome_id TEXT PRIMARY KEY,
      proxy_id TEXT NOT NULL,
      input_version INTEGER NOT NULL,
      disposition TEXT NOT NULL,
      reason TEXT,
      applied_at TEXT NOT NULL
    );
    CREATE TABLE proxy_latency_projection_cursors (
      consumer_key TEXT PRIMARY KEY,
      stored_at TEXT,
      outcome_id TEXT,
      updated_at TEXT NOT NULL
    );
    INSERT INTO proxy_profiles(id, updated_at, test_status) VALUES ('proxy-j3a', '2026-08-23T00:00:00.123456Z', 'unknown');
  `)
  const client = createSqliteDatabaseClient(database as never)
  const appliedOutcome = decodeProxyLatencyJobsOutcome({
    outcome_id: 'j3a-outcome-applied',
    request_id: 'j3a-request-applied',
    proxy_id: 'proxy-j3a',
    observed_at: '2026-08-23T00:00:05.000000Z',
    input_version: 1,
    config_revision: '2026-08-23T00:00:00.123456Z',
    trigger: 'periodic',
    owner_fence_token: 1,
    proxy_fence_token: 1,
    overall_status: 'warning',
    items: [
      { provider: 'openai', profile_id: 'openai-default', status: 'passed', outcome: 'success', latency_ms: 40 },
      { provider: 'gemini', profile_id: 'gemini-default', status: 'unknown', outcome: 'probe_task_failure', error_code: 'deadline' }
    ]
  }, '2026-08-23T00:00:05.000001Z')
  const applied = await projectProxyLatencyJobsOutcomeAsync(client, appliedOutcome)
  assert.deepEqual(applied, {
    outcomeId: 'j3a-outcome-applied',
    proxyId: 'proxy-j3a',
    inputVersion: 1,
    disposition: 'applied',
    changed: true
  })
  const projected = database.prepare('SELECT test_status,latency_ms,last_test_message,last_tested_at FROM proxy_profiles WHERE id=?').get('proxy-j3a') as Record<string, unknown>
  assert.equal(projected.test_status, 'warning')
  assert.equal(projected.latency_ms, 40)
  assert.equal(projected.last_tested_at, '2026-08-23T00:00:05.000000Z')
  assert.equal(projected.last_test_message, '代理可用，存在 1 项告警')

  const replay = await projectProxyLatencyJobsOutcomeAsync(client, appliedOutcome)
  assert.equal(replay.disposition, 'applied')
  assert.equal(replay.changed, false)

  const stale = await projectProxyLatencyJobsOutcomeAsync(client, {
    ...appliedOutcome,
    outcomeId: 'j3a-outcome-stale-observed',
    requestId: 'j3a-request-stale-observed',
    observedAt: '2026-08-23T00:00:04.000000Z'
  })
  assert.equal(stale.disposition, 'stale')
  assert.equal(stale.reason, 'observed_at_stale')

  const configStale = await projectProxyLatencyJobsOutcomeAsync(client, {
    ...appliedOutcome,
    outcomeId: 'j3a-outcome-stale-config',
    requestId: 'j3a-request-stale-config',
    configRevision: '2026-08-23T00:00:00.000000Z'
  })
  assert.equal(configStale.disposition, 'stale')
  assert.equal(configStale.reason, 'config_revision_stale')

  const rejected = await projectProxyLatencyJobsOutcomeAsync(client, {
    ...appliedOutcome,
    outcomeId: 'j3a-outcome-rejected',
    requestId: 'j3a-request-rejected',
    overallStatus: 'passed'
  })
  assert.equal(rejected.disposition, 'rejected')
  assert.equal(rejected.reason, 'overall_status_mismatch')

  const missing = await projectProxyLatencyJobsOutcomeAsync(client, {
    ...appliedOutcome,
    outcomeId: 'j3a-outcome-missing',
    requestId: 'j3a-request-missing',
    proxyId: 'proxy-deleted'
  })
  assert.equal(missing.disposition, 'ignored')
  assert.equal(missing.reason, 'proxy_missing_or_deleted')

  assert.equal(await currentProxyLatencyProjectionCursorAsync(client, 'j3a-test'), undefined)
  assert.equal(await advanceProxyLatencyProjectionCursorAsync(client, 'j3a-test', { storedAt: '2026-08-23T00:00:05.000001Z', outcomeId: 'j3a-outcome-applied' }), true)
  assert.deepEqual(await currentProxyLatencyProjectionCursorAsync(client, 'j3a-test'), { storedAt: '2026-08-23T00:00:05.000001Z', outcomeId: 'j3a-outcome-applied' })
  assert.equal(await advanceProxyLatencyProjectionCursorAsync(client, 'j3a-test', { storedAt: '2026-08-23T00:00:05.000000Z', outcomeId: 'j3a-outcome-older' }), false)

  const jobsPath = join(tempDir, 'jobs.sqlite')
  const jobs = new DatabaseSync(jobsPath)
  jobsDatabase = jobs
  jobs.exec('CREATE TABLE proxy_latency_outcomes (outcome_id TEXT PRIMARY KEY, request_id TEXT, proxy_id TEXT, input_version INTEGER, config_revision TEXT, trigger TEXT, owner_fence_token INTEGER, proxy_fence_token INTEGER, overall_status TEXT, observed_at TEXT, payload TEXT, committed INTEGER, stored_at TEXT)')
  const goldenPath = resolve(import.meta.dirname, '../../../../backend-go/projects/jobs/internal/proxylatency/testdata/j3a-outcome-golden.json')
  const payload = readFileSync(goldenPath, 'utf8')
  const goldenOutcome = JSON.parse(payload) as Record<string, unknown>
  jobs.prepare('INSERT INTO proxy_latency_outcomes VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?)').run(
    goldenOutcome.outcome_id, goldenOutcome.request_id, goldenOutcome.proxy_id, goldenOutcome.input_version, goldenOutcome.config_revision, goldenOutcome.trigger, goldenOutcome.owner_fence_token, goldenOutcome.proxy_fence_token, goldenOutcome.overall_status, goldenOutcome.observed_at, payload, 1, '2026-08-23T00:00:05.123457Z'
  )
  const source = await listProxyLatencyJobsOutcomes({ mode: 'sqlite', databasePath: jobsPath }, { limit: 10 })
  assert.equal(source.length, 1)
  assert.equal(source[0]?.outcomeId, 'j3a-golden-outcome')
  assert.equal(source[0]?.storageObservedAt, '2026-08-23T00:00:05.123457Z')
  assert.throws(() => decodeProxyLatencyJobsOutcome({ ...JSON.parse(payload), unexpected: true }, source[0]?.storageObservedAt ?? ''), /未知字段/u)
} finally {
  try { jobsDatabase?.close() } catch { /* cleanup below reports the primary assertion */ }
  try { businessDatabase?.close() } catch { /* cleanup below reports the primary assertion */ }
  rmSync(tempDir, { recursive: true, force: true })
}

console.log('proxy latency jobs outcome regression passed')
