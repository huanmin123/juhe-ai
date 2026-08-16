import assert from 'node:assert/strict'
import { createRequire } from 'node:module'
import { mkdtempSync, rmSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join, resolve } from 'node:path'

import { decodeAccountHealthJobsOutcomePayload, listAccountHealthJobsOutcomes } from '../../storage/account-health-jobs-outcome.repository.js'

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
  database.exec('CREATE TABLE account_health_outcomes(outcome_id TEXT PRIMARY KEY, observed_at TEXT NOT NULL, payload TEXT NOT NULL)')
  database.prepare('INSERT INTO account_health_outcomes(outcome_id, observed_at, payload) VALUES (?, ?, ?)').run(
    'outcome-1',
    '2026-08-16T00:00:00.000Z',
    JSON.stringify({
      outcome_id: 'outcome-1', request_id: 'request-1', account_id: 'account-1', outcome: 'complete_success', observed_at: '2026-08-16T00:00:00.000Z', input_version: 1, config_revision: 2, dispatch_revision: 3,
      projection: { target_account_id: 'account-1', transition_kind: 'health_success', input_version: 1, config_revision: 2, dispatch_revision: 3, expected_account_status: 'active', values: { last_health_check_at: '2026-08-16T00:00:00.000Z' } }
    })
  )
  database.prepare('INSERT INTO account_health_outcomes(outcome_id, observed_at, payload) VALUES (?, ?, ?)').run(
    'outcome-2',
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
  const postgresJsonbPayload = decodeAccountHealthJobsOutcomePayload({
    outcome_id: 'outcome-3', request_id: 'request-3', account_id: 'account-1', outcome: 'complete_success', observed_at: '2026-08-16T00:00:00.000Z', input_version: 1, config_revision: 2, dispatch_revision: 3
  })
  assert.equal(postgresJsonbPayload.outcome_id, 'outcome-3')
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
