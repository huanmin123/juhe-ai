import assert from 'node:assert/strict'
import { createRequire } from 'node:module'
import { mkdtempSync, rmSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join } from 'node:path'
import { decodeAccountBalanceJobsOutcome, listAccountBalanceJobsOutcomes } from '../../storage/account-balance-jobs-outcome.repository.js'
import { createSqliteDatabaseClient } from '../../storage/database-client.js'
import { advanceAccountBalanceProjectionCursorAsync, currentAccountBalanceProjectionCursorAsync } from '../../storage/account-balance-projection-cursor.repository.js'

const fixture = {
  outcome_id: 'account-balance-outcome-1', request_id: 'account-balance-request-1', account_id: 'acct-j2', system_account_id: 'sys-j2',
  input_version: 3, config_revision: 7, trigger: 'periodic', observed_at: '2026-08-19T00:00:00Z',
  expected_next_refresh_at: '2026-08-18T23:55:00Z', next_refresh_at: '2026-08-19T00:00:00Z',
  snapshot: { status: 'fresh', remainingUsd: '1.250000', rawUnit: 'usd', basis: 'wallet' }
}
const outcome = decodeAccountBalanceJobsOutcome(fixture, '2026-08-19T00:00:00.000Z')
assert.equal(outcome.accountId, 'acct-j2')
assert.equal(outcome.expectedNextRefreshAt, '2026-08-18T23:55:00.000Z')
assert.equal(decodeAccountBalanceJobsOutcome({ ...fixture, adapter: 'custom' }, outcome.storageObservedAt).adapter, 'custom')
assert.throws(() => decodeAccountBalanceJobsOutcome({ ...fixture, trigger: 'bad' }, outcome.storageObservedAt), /trigger 无效/u)
assert.throws(() => decodeAccountBalanceJobsOutcome({ ...fixture, system_account_id: '' }, outcome.storageObservedAt), /system_account_id 无效/u)
assert.throws(() => decodeAccountBalanceJobsOutcome({ ...fixture, secret: 'nope' }, outcome.storageObservedAt), /未知字段/u)

const require = createRequire(import.meta.url)
const DatabaseSync = require('node:sqlite').DatabaseSync as new (path: string) => { exec(sql: string): void; close(): void }
const tempDir = mkdtempSync(join(tmpdir(), 'juhe-ai-j2-outcome-'))
const sqlitePath = join(tempDir, 'jobs.sqlite')
try {
  const database = new DatabaseSync(sqlitePath)
  database.exec('CREATE TABLE account_balance_outcomes (outcome_id TEXT PRIMARY KEY, request_id TEXT NOT NULL, account_id TEXT NOT NULL, input_version INTEGER NOT NULL, config_revision INTEGER NOT NULL, trigger TEXT NOT NULL, observed_at TEXT NOT NULL, payload TEXT NOT NULL, committed INTEGER NOT NULL)')
  const payload = JSON.stringify(fixture)
  const escaped = payload.replaceAll("'", "''")
  database.exec(`INSERT INTO account_balance_outcomes(outcome_id, request_id, account_id, input_version, config_revision, trigger, observed_at, payload, committed) VALUES ('account-balance-outcome-1', 'account-balance-request-1', 'acct-j2', 3, 7, 'periodic', '2026-08-19T00:00:00.000Z', '${escaped}', 1)`)
  database.exec(`INSERT INTO account_balance_outcomes(outcome_id, request_id, account_id, input_version, config_revision, trigger, observed_at, payload, committed) VALUES ('account-balance-outcome-stale', 'account-balance-request-stale', 'acct-j2', 3, 7, 'periodic', '2026-08-19T00:00:01.000Z', '${escaped}', 0)`)
  database.close()
  const sqliteOutcomes = await listAccountBalanceJobsOutcomes({ mode: 'sqlite', databasePath: sqlitePath }, { limit: 10 })
  assert.equal(sqliteOutcomes.length, 1)
  assert.equal(sqliteOutcomes[0]?.outcomeId, 'account-balance-outcome-1')
  assert.deepEqual((await listAccountBalanceJobsOutcomes({ mode: 'sqlite', databasePath: sqlitePath }, { after: { observedAt: '2026-08-19T00:00:00.000Z', outcomeId: 'account-balance-outcome-1' }, limit: 10 })), [])
  const cursorDatabase = new DatabaseSync(sqlitePath)
  cursorDatabase.exec('CREATE TABLE account_balance_projection_cursors (consumer_key TEXT PRIMARY KEY, observed_at TEXT, outcome_id TEXT, updated_at TEXT NOT NULL)')
  const cursorClient = createSqliteDatabaseClient(cursorDatabase as never)
  assert.equal(await currentAccountBalanceProjectionCursorAsync(cursorClient, 'j2-test'), undefined)
  assert.equal(await advanceAccountBalanceProjectionCursorAsync(cursorClient, 'j2-test', { observedAt: '2026-08-19T00:00:00.000Z', outcomeId: 'account-balance-outcome-1' }), true)
  assert.deepEqual(await currentAccountBalanceProjectionCursorAsync(cursorClient, 'j2-test'), { observedAt: '2026-08-19T00:00:00.000Z', outcomeId: 'account-balance-outcome-1' })
  cursorDatabase.close()
} finally {
  rmSync(tempDir, { recursive: true, force: true })
}
console.log('account balance jobs outcome regression passed')
