import assert from 'node:assert/strict'
import { randomBytes } from 'node:crypto'

import { Client } from 'pg'

import {
  closeAccountHealthJobsStoreSource,
  createPostgresAccountHealthJobsStoreSource,
  listAccountHealthJobsOutcomesForAccountsAsync
} from '../../storage/account-health-jobs-outcome.repository.js'
import { hourBucketsUntilNow } from '../../storage/usage-stats-window-helpers.js'

const adminUrl = requiredEnv('JUHE_AI_HEALTH_RANGE_SMOKE_ADMIN_POSTGRES_URL')
const appUrl = requiredEnv('JUHE_AI_HEALTH_RANGE_SMOKE_APP_POSTGRES_URL')
const databaseName = `juhe_ai_sub2api_dev_health_${randomBytes(6).toString('hex')}`
const appDatabaseUrl = databaseUrl(appUrl, databaseName)
const appRole = new URL(appUrl).username
const admin = new Client({ connectionString: databaseUrl(adminUrl, 'postgres') })

try {
  await admin.connect()
  await admin.query(`CREATE DATABASE ${databaseName} OWNER ${quoteIdentifier(appRole)}`)
  await setupMockJobsDatabase(appDatabaseUrl)

  const accountIds = Array.from({ length: 10 }, (_, index) => `health-mock-account-${index}`)
  const hourBuckets = hourBucketsUntilNow(7 * 24, Date.now(), 'Asia/Shanghai')
  const source = createPostgresAccountHealthJobsStoreSource(appDatabaseUrl)
  const durations: number[] = []
  let outcomes: Awaited<ReturnType<typeof listAccountHealthJobsOutcomesForAccountsAsync>> = []
  try {
    for (let index = 0; index < 5; index += 1) {
      const startedAt = performance.now()
      outcomes = await listAccountHealthJobsOutcomesForAccountsAsync(source, {
        accountIds,
        observedAfter: new Date(Date.now() - 7 * 24 * 60 * 60 * 1000).toISOString(),
        timezone: 'Asia/Shanghai',
        hourBuckets
      })
      durations.push(performance.now() - startedAt)
    }
  } finally {
    await closeAccountHealthJobsStoreSource(source)
  }

  assert.equal(outcomes.length, accountIds.length * hourBuckets.length, '高频 J1 mock 必须每账户每小时只返回最新的非 stale outcome')
  assert(outcomes.every((outcome) => outcome.outcome === 'complete_success'), 'stale outcome 不得遮蔽同小时的有效健康结果')
  const p95 = percentile(durations, 0.95)
  assert(p95 < 1_000, `高频 mock 下 AI 健康 J1 查询 p95 过高: ${p95.toFixed(1)}ms`)
  console.log(JSON.stringify({
    database: 'isolated',
    insertedOutcomes: accountIds.length * hourBuckets.length * 30,
    returnedOutcomes: outcomes.length,
    durationsMs: durations.map((duration) => Number(duration.toFixed(1))),
    p95Ms: Number(p95.toFixed(1))
  }))
} finally {
  await cleanupDatabase(admin, databaseName)
  await admin.end()
}

async function setupMockJobsDatabase(connectionString: string): Promise<void> {
  const client = new Client({ connectionString })
  try {
    await client.connect()
    await client.query('CREATE SCHEMA juhe_jobs')
    await client.query(`
      CREATE TABLE juhe_jobs.account_health_outcomes (
        outcome_id TEXT PRIMARY KEY,
        account_id TEXT NOT NULL,
        outcome TEXT NOT NULL,
        observed_at TIMESTAMPTZ NOT NULL,
        payload JSONB NOT NULL
      )
    `)
    await client.query(`
      CREATE INDEX idx_account_health_outcomes_account_observed_non_stale
      ON juhe_jobs.account_health_outcomes(account_id, observed_at DESC, outcome_id DESC)
      INCLUDE (payload)
      WHERE outcome <> 'stale'
    `)
    await client.query(`
      INSERT INTO juhe_jobs.account_health_outcomes (outcome_id, account_id, outcome, observed_at, payload)
      SELECT
        'mock-' || account_index || '-' || hour_index || '-' || sample_index,
        'health-mock-account-' || account_index,
        CASE WHEN sample_index = 29 THEN 'stale' ELSE 'complete_success' END,
        date_trunc('hour', now()) - (hour_index || ' hours')::interval + (sample_index || ' seconds')::interval,
        jsonb_build_object(
          'outcome_id', 'mock-' || account_index || '-' || hour_index || '-' || sample_index,
          'request_id', 'mock-request-' || account_index || '-' || hour_index || '-' || sample_index,
          'account_id', 'health-mock-account-' || account_index,
          'outcome', CASE WHEN sample_index = 29 THEN 'stale' ELSE 'complete_success' END,
          'observed_at', to_char((date_trunc('hour', now()) - (hour_index || ' hours')::interval + (sample_index || ' seconds')::interval) AT TIME ZONE 'UTC', 'YYYY-MM-DD"T"HH24:MI:SS.MS"Z"'),
          'input_version', 1,
          'config_revision', 1,
          'dispatch_revision', 1
        )
      FROM generate_series(0, 9) AS account_index
      CROSS JOIN generate_series(0, 167) AS hour_index
      CROSS JOIN generate_series(0, 29) AS sample_index
    `)
  } finally {
    await client.end()
  }
}

async function cleanupDatabase(adminClient: Client, name: string): Promise<void> {
  if (!/^juhe_ai_sub2api_dev_health_[a-f0-9]{12}$/.test(name)) throw new Error('隔离健康 smoke 数据库名无效')
  await adminClient.query('SELECT pg_terminate_backend(pid) FROM pg_stat_activity WHERE datname = $1 AND pid <> pg_backend_pid()', [name])
  await adminClient.query(`DROP DATABASE IF EXISTS ${name}`)
}

function databaseUrl(value: string, database: string): string {
  const url = new URL(value)
  url.pathname = `/${database}`
  return url.toString()
}

function quoteIdentifier(value: string): string {
  if (!/^[A-Za-z_][A-Za-z0-9_]*$/.test(value)) throw new Error('隔离健康 smoke 应用角色名无效')
  return `"${value}"`
}

function percentile(values: number[], fraction: number): number {
  const sorted = [...values].sort((left, right) => left - right)
  return sorted[Math.min(sorted.length - 1, Math.ceil(sorted.length * fraction) - 1)] ?? 0
}

function requiredEnv(name: string): string {
  const value = process.env[name]?.trim()
  if (!value) throw new Error(`${name} 必须设置`)
  return value
}
