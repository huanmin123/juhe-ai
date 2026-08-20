import assert from 'node:assert/strict'
import { createRequire } from 'node:module'
import { mkdtempSync, readFileSync, rmSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join, resolve } from 'node:path'

import {
  decodeAccountHealthJobsOutcomePayload,
  listAccountHealthJobsOutcomes,
  listAccountHealthJobsOutcomesForAccounts
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
} finally {
  rmSync(root, { recursive: true, force: true })
}

console.log('account-health-jobs-outcome-read-only-regression passed')
