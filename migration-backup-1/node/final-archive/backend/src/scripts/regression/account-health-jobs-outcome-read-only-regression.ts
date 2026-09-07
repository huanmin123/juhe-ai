import assert from 'node:assert/strict'
import { createRequire } from 'node:module'
import { mkdtempSync, readFileSync, rmSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join, resolve } from 'node:path'

import {
  closeAccountHealthJobsStoreSource,
  createPostgresAccountHealthJobsStoreSource,
  decodeAccountHealthJobsOutcomePayload,
  listAccountHealthJobsOutcomes,
  listAccountHealthJobsOutcomesForAccounts,
  listAccountHealthJobsOutcomesForAccountsAsync,
  type AccountHealthJobsPostgresStoreSource
} from '../../storage/account-health-jobs-outcome.repository.js'

const require = createRequire(import.meta.url)
const Constructor = require('node:sqlite').DatabaseSync as new (path: string) => {
  exec(sql: string): void
  prepare(sql: string): { run(...values: unknown[]): void }
  close(): void
}
const testRoot = resolve(process.env.JUHE_AI_TEST_TEMP_ROOT?.trim() || tmpdir())
const root = mkdtempSync(join(testRoot, 'juhe-ai-account-health-outcome-'))
const path = join(root, 'jobs.sqlite3')
try {
  const database = new Constructor(path)
  database.exec('CREATE TABLE account_health_outcomes(outcome_id TEXT PRIMARY KEY, account_id TEXT NOT NULL, observed_at TEXT NOT NULL, payload TEXT NOT NULL)')
  database.prepare('INSERT INTO account_health_outcomes(outcome_id, account_id, observed_at, payload) VALUES (?, ?, ?, ?)').run(
    'outcome-1',
    'account-1',
    '2026-08-16T00:00:00.000Z',
    JSON.stringify({
      outcome_id: 'outcome-1', request_id: 'request-1', account_id: 'account-1', outcome: 'complete_success', observed_at: '2026-08-16T00:00:00.000Z', input_version: 1, config_revision: 2, dispatch_revision: 3,
      projection: { target_account_id: 'account-1', transition_kind: 'health_success', input_version: 1, config_revision: 2, dispatch_revision: 3, expected_account_status: 'active', values: { last_health_check_at: '2026-08-16T00:00:00.000Z' } }
    })
  )
  database.prepare('INSERT INTO account_health_outcomes(outcome_id, account_id, observed_at, payload) VALUES (?, ?, ?, ?)').run(
    'outcome-2',
    'account-1',
    '2026-08-16T00:00:00.000Z',
    JSON.stringify({
      outcome_id: 'outcome-2', request_id: 'request-2', account_id: 'account-1', outcome: 'complete_success', observed_at: '2026-08-16T00:00:00.000Z', input_version: 1, config_revision: 2, dispatch_revision: 3,
      projection: { target_account_id: 'account-1', transition_kind: 'health_success', input_version: 1, config_revision: 2, dispatch_revision: 3, expected_account_status: 'active' }
    })
  )
  database.close()
  const outcomes = await listAccountHealthJobsOutcomes({ mode: 'sqlite', databasePath: path }, { limit: 10 })
  assert.equal(outcomes.length, 2)
  assert.equal(outcomes[0]?.projection?.transition_kind, 'health_success')
  const firstPage = await listAccountHealthJobsOutcomes({ mode: 'sqlite', databasePath: path }, { limit: 1 })
  assert.equal(firstPage[0]?.outcome_id, 'outcome-1')
  const secondPage = await listAccountHealthJobsOutcomes({ mode: 'sqlite', databasePath: path }, {
    after: { observedAt: firstPage[0]!.observed_at, outcomeId: firstPage[0]!.outcome_id },
    limit: 1
  })
  assert.equal(secondPage[0]?.outcome_id, 'outcome-2')
  const accountOutcomes = listAccountHealthJobsOutcomesForAccounts(
    { mode: 'sqlite', databasePath: path },
    { accountIds: ['account-1'], observedAfter: '2026-08-15T00:00:00.000Z' }
  )
  assert.deepEqual(accountOutcomes.map((outcome) => outcome.outcome_id), ['outcome-1', 'outcome-2'], '按账户读取必须只读返回时间窗内的 J1 outcome')
  assert.throws(
    () => listAccountHealthJobsOutcomesForAccounts({ mode: 'postgres', postgresUrl: 'postgres://unused' }, { accountIds: ['account-1'], observedAfter: '2026-08-15T00:00:00.000Z' }),
    /异步 reader/
  )
  const source = readFileSync(resolve('src/storage/account-health-jobs-outcome.repository.ts'), 'utf8')
  assert.match(source, /account_id = ANY\(\$1::text\[\]\)[\s\S]*observed_at >= \$2::timestamptz/, 'PostgreSQL 账户范围读取必须按账户与时间窗在 jobs store 内过滤')
  assert.match(source, /CROSS JOIN LATERAL/, 'AI 健康列表的 PostgreSQL J1 读取必须按账户和统计小时做索引范围查询')
  assert.match(source, /outcome <> 'stale'/, 'AI 健康列表的 PostgreSQL J1 读取必须先过滤 stale outcome')
  assert.match(source, /BEGIN READ ONLY/, 'J1 账户范围读取必须显式只读')
  const postgresJsonbPayload = decodeAccountHealthJobsOutcomePayload({
    outcome_id: 'outcome-3', request_id: 'request-3', account_id: 'account-1', outcome: 'complete_success', observed_at: '2026-08-16T00:00:00.000Z', input_version: 1, config_revision: 2, dispatch_revision: 3
  })
  assert.equal(postgresJsonbPayload.outcome_id, 'outcome-3')
  const legacyErrorProjection = decodeAccountHealthJobsOutcomePayload({
    outcome_id: 'outcome-legacy-error', request_id: 'request-legacy-error', account_id: 'account-1', outcome: 'upstream_failure', observed_at: '2026-08-16T00:00:00.000Z', input_version: 1, config_revision: 2, dispatch_revision: 3,
    projection: { target_account_id: 'account-1', transition_kind: 'health_failure', input_version: 1, config_revision: 2, dispatch_revision: 3, expected_account_status: 'error' }
  })
  assert.equal(legacyErrorProjection.projection?.expected_account_status, 'error')
  const historicalMalformedProjection = decodeAccountHealthJobsOutcomePayload({
    outcome_id: 'outcome-historical-error', request_id: 'request-historical-error', account_id: 'account-1', outcome: 'upstream_failure', observed_at: '2026-08-16T00:00:00.000Z', input_version: 1, config_revision: 2, dispatch_revision: 3,
    projection: { target_account_id: 'account-1', transition_kind: 'health_failure', input_version: 1, config_revision: 2, dispatch_revision: 3, expected_account_status: 'error' }
  })
  assert.equal(historicalMalformedProjection.projection?.expected_account_status, 'error', '历史 malformed projection 必须可被读取后由 projector 记录 rejection receipt')
  assert.throws(() => decodeAccountHealthJobsOutcomePayload({
    outcome_id: 'outcome-4', request_id: 'request-4', account_id: 'account-1', outcome: 'complete_success', observed_at: '2026-08-16T00:00:00.000Z', input_version: 1, config_revision: 2, dispatch_revision: 3,
    projection: { target_account_id: 'account-1', transition_kind: 'health_success', input_version: 1, config_revision: 2, dispatch_revision: 3 }
  }), /expected_account_status/)
  assert.throws(() => decodeAccountHealthJobsOutcomePayload({
    outcome_id: 'outcome-5', request_id: 'request-5', account_id: 'account-1', outcome: 'complete_success', observed_at: '2026-08-16T00:00:00.000Z', input_version: 1, config_revision: 2, dispatch_revision: 3,
    projection: { target_account_id: 'other-account', transition_kind: 'health_success', input_version: 1, config_revision: 2, dispatch_revision: 3, expected_account_status: 'active' }
  }), /account\/revision fence/)

  const postgresPayload = {
    outcome_id: 'postgres-outcome-1', request_id: 'request-postgres-1', account_id: 'account-1', outcome: 'complete_success', observed_at: '2026-08-16T00:00:00.000Z', input_version: 1, config_revision: 2, dispatch_revision: 3,
    projection: { target_account_id: 'account-1', transition_kind: 'health_success', input_version: 1, config_revision: 2, dispatch_revision: 3, expected_account_status: 'active' }
  }
  let connects = 0
  let releases = 0
  let ends = 0
  const queryTexts: string[] = []
  const pooledSource = createPostgresAccountHealthJobsStoreSource('postgres://unused')
  pooledSource.pool = {
    async connect() {
      connects += 1
      return {
        async query(text: string) {
          queryTexts.push(text)
          if (text.includes('payload, to_char')) {
            return { rows: [{ payload: postgresPayload, storage_observed_at: '2026-08-16T00:00:00.000000Z' }] }
          }
          return { rows: [] }
        },
        release() { releases += 1 }
      } as never
    },
    async end() { ends += 1 }
  }
  await listAccountHealthJobsOutcomes(pooledSource, { limit: 1 })
  await listAccountHealthJobsOutcomes(pooledSource, { limit: 1 })
  const accountPage = await listAccountHealthJobsOutcomesForAccountsAsync(pooledSource, {
    accountIds: ['account-1'],
    observedAfter: '2026-08-15T00:00:00.000Z',
    timezone: 'Asia/Shanghai',
    hourBuckets: ['2026-08-16T08']
  })
  assert.equal(accountPage.length, 1, 'PostgreSQL 健康列表账户范围读取应返回去重后的小时结果')
  assert.match(queryTexts.find((text) => text.includes('CROSS JOIN LATERAL')) ?? '', /observed_at < \(\(hours\.stat_hour \|\| ':00:00'\)::timestamp \+ INTERVAL '1 hour'\) AT TIME ZONE \$3/, 'PostgreSQL 健康列表必须按统计时区小时做上界范围读取')
  assert.equal(connects, 3, '常驻 J1 reader 每轮借用同一个 pool 的连接')
  assert.equal(releases, 3, '常驻 J1 reader 每轮必须归还连接')
  assert.equal(ends, 0, '常驻 J1 reader 在 runtime 退出前不能每轮销毁 pool')
  assert.equal(queryTexts.filter((text) => text === 'SET LOCAL statement_timeout = 5000').length, 3, 'J1 PostgreSQL 读取必须有语句超时')
  await closeAccountHealthJobsStoreSource(pooledSource)
  assert.equal(ends, 1, '常驻 J1 reader runtime 退出时必须关闭 pool')

  let transientEnds = 0
  const transientSource: AccountHealthJobsPostgresStoreSource = { mode: 'postgres', postgresUrl: 'postgres://unused' }
  transientSource.pool = {
    async connect() {
      return {
        async query(text: string) {
          if (text.includes('payload, to_char')) {
            return { rows: [{ payload: postgresPayload, storage_observed_at: '2026-08-16T00:00:00.000000Z' }] }
          }
          return { rows: [] }
        },
        release() {}
      } as never
    },
    async end() { transientEnds += 1 }
  }
  await listAccountHealthJobsOutcomes(transientSource, { limit: 1 })
  assert.equal(transientEnds, 1, '一次性 J1 reader 查询完成后不能保留闲置 pool')
} finally {
  rmSync(root, { recursive: true, force: true })
}

console.log('account-health-jobs-outcome-read-only-regression passed')
