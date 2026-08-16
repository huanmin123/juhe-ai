import assert from 'node:assert/strict'
import { createRequire } from 'node:module'
import type { DatabaseSync } from 'node:sqlite'

import {
  acknowledgeAccountHealthJobsInputOutboxEvent,
  claimNextAccountHealthJobsInputOutboxEvent,
  failAccountHealthJobsInputOutboxEvent,
  reserveAndEnqueueAccountHealthJobsInput,
  reserveAndEnqueueAccountHealthJobsInputInTransaction,
  supersedeAccountHealthJobsInputOutboxEvent
} from '../../storage/account-health-jobs-input-outbox.repository.js'
import { runInDatabaseTransaction } from '../../storage/database.js'

const require = createRequire(import.meta.url)
const Constructor = require('node:sqlite').DatabaseSync as new (path: string) => DatabaseSync

const database = new Constructor(':memory:')
try {
  database.exec(`
    CREATE TABLE account_health_jobs_input_versions (
      account_id TEXT PRIMARY KEY,
      current_version INTEGER NOT NULL,
      reserved_at TEXT NOT NULL
    );
    CREATE TABLE account_health_jobs_input_outbox (
      event_id TEXT PRIMARY KEY,
      account_id TEXT NOT NULL,
      input_version INTEGER NOT NULL,
      event_kind TEXT NOT NULL,
      reason TEXT NOT NULL,
      config_revision INTEGER NOT NULL,
      dispatch_revision INTEGER NOT NULL,
      status TEXT NOT NULL,
      claim_token TEXT,
      claimed_until TEXT,
      attempt_count INTEGER NOT NULL DEFAULT 0,
      available_at TEXT NOT NULL,
      last_error TEXT,
      created_at TEXT NOT NULL,
      updated_at TEXT NOT NULL,
      UNIQUE (account_id, input_version)
    );
  `)

  const first = reserveAndEnqueueAccountHealthJobsInput({
    accountId: 'account-1', configRevision: 4, dispatchRevision: 8, kind: 'snapshot', reason: 'credentials_changed'
  }, database)
  const second = reserveAndEnqueueAccountHealthJobsInput({
    accountId: 'account-1', configRevision: 4, dispatchRevision: 8, kind: 'tombstone', reason: 'proxy_disabled'
  }, database)
  assert.equal(first.inputVersion, 1)
  assert.equal(second.inputVersion, 2)
  const rows = (database.prepare('SELECT input_version, event_kind, status FROM account_health_jobs_input_outbox WHERE account_id = ? ORDER BY input_version').all('account-1') as Array<{ input_version: number, event_kind: string, status: string }>)
    .map((row) => ({ input_version: row.input_version, event_kind: row.event_kind, status: row.status }))
  assert.deepEqual(rows, [
    { input_version: 1, event_kind: 'snapshot', status: 'pending' },
    { input_version: 2, event_kind: 'tombstone', status: 'pending' }
  ])

  const claimed = claimNextAccountHealthJobsInputOutboxEvent(30_000, database, '2030-08-16T00:00:00.000Z')
  assert.ok(claimed)
  assert.equal(claimed?.attemptCount, 1)
  assert.equal(acknowledgeAccountHealthJobsInputOutboxEvent(claimed!.eventId, claimed!.claimToken, database, '2030-08-16T00:00:01.000Z'), true)
  assert.equal(acknowledgeAccountHealthJobsInputOutboxEvent(claimed!.eventId, claimed!.claimToken, database, '2030-08-16T00:00:02.000Z'), false)

  const retry = claimNextAccountHealthJobsInputOutboxEvent(30_000, database, '2030-08-16T00:00:03.000Z')
  assert.ok(retry)
  assert.notEqual(retry?.inputVersion, claimed.inputVersion)
  assert.equal(failAccountHealthJobsInputOutboxEvent(retry!.eventId, retry!.claimToken, 'rename failed', '2030-08-16T00:00:10.000Z', database, '2030-08-16T00:00:04.000Z'), true)
  assert.equal(claimNextAccountHealthJobsInputOutboxEvent(30_000, database, '2030-08-16T00:00:09.000Z'), undefined)
  const replay = claimNextAccountHealthJobsInputOutboxEvent(30_000, database, '2030-08-16T00:00:10.000Z')
  assert.equal(replay?.eventId, retry?.eventId)
  assert.equal(replay?.attemptCount, 2)
  assert.equal(supersedeAccountHealthJobsInputOutboxEvent(replay!.eventId, replay!.claimToken, database, '2030-08-16T00:00:11.000Z'), true)

  assert.throws(() => runInDatabaseTransaction(() => {
    reserveAndEnqueueAccountHealthJobsInputInTransaction({
      accountId: 'rolled-back', configRevision: 1, dispatchRevision: 1, kind: 'snapshot', reason: 'test'
    }, database)
    throw new Error('rollback')
  }, database), /rollback/)
  const rolledBackOutbox = database.prepare('SELECT count(*) AS count FROM account_health_jobs_input_outbox WHERE account_id = ?').get('rolled-back') as { count: number }
  const rolledBackVersion = database.prepare('SELECT count(*) AS count FROM account_health_jobs_input_versions WHERE account_id = ?').get('rolled-back') as { count: number }
  assert.equal(rolledBackOutbox.count, 0)
  assert.equal(rolledBackVersion.count, 0)
} finally {
  database.close()
}

console.log('account-health-jobs-input-outbox-regression passed')
