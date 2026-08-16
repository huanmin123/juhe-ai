import assert from 'node:assert/strict'
import { createRequire } from 'node:module'
import type { DatabaseSync } from 'node:sqlite'

import { projectAccountHealthJobsOutcome } from '../../storage/account-health-projection.repository.js'
import type { AccountHealthJobsOutcome } from '../../storage/account-health-jobs-outcome.repository.js'

const require = createRequire(import.meta.url)
const Constructor = require('node:sqlite').DatabaseSync as new (path: string) => DatabaseSync

const database = new Constructor(':memory:')
try {
  database.exec(`
    CREATE TABLE accounts (
      id TEXT PRIMARY KEY,
      status TEXT NOT NULL,
      schedulable INTEGER NOT NULL,
      config_revision INTEGER NOT NULL,
      dispatch_revision INTEGER NOT NULL,
      authorization_instance_source_account_id TEXT,
      cooldown_until TEXT,
      cooldown_retest_failure_count INTEGER NOT NULL DEFAULT 0,
      cooldown_retest_observation_started_at TEXT,
      cooldown_retest_generation TEXT,
      cooldown_retest_last_at TEXT,
      cooldown_retest_last_status_code INTEGER,
      last_error_code TEXT,
      last_error_message TEXT,
      last_error_trace_id TEXT,
      last_health_check_at TEXT,
      next_health_check_at TEXT,
      last_health_success_at TEXT,
      health_check_failure_count INTEGER NOT NULL DEFAULT 0,
      health_check_failure_started_at TEXT,
      last_health_check_status_code INTEGER,
      last_health_check_error_code TEXT,
      last_health_check_error_message TEXT,
      last_health_check_trace_id TEXT,
      deleted_at TEXT,
      updated_at TEXT NOT NULL
    );
    CREATE TABLE account_health_jobs_input_versions (
      account_id TEXT PRIMARY KEY,
      current_version INTEGER NOT NULL,
      reserved_at TEXT NOT NULL
    );
    CREATE TABLE account_health_projection_receipts (
      outcome_id TEXT PRIMARY KEY,
      account_id TEXT NOT NULL,
      input_version INTEGER NOT NULL,
      disposition TEXT NOT NULL,
      reason TEXT,
      applied_at TEXT NOT NULL
    );
  `)
  database.prepare(`INSERT INTO accounts(id, status, schedulable, config_revision, dispatch_revision, updated_at) VALUES (?, ?, ?, ?, ?, ?)`)
    .run('account-1', 'active', 1, 2, 3, '2026-08-16T00:00:00.000Z')
  database.prepare(`INSERT INTO account_health_jobs_input_versions(account_id, current_version, reserved_at) VALUES (?, ?, ?)`)
    .run('account-1', 1, '2026-08-16T00:00:00.000Z')

  const applied = projectAccountHealthJobsOutcome(healthSuccess('outcome-1'), database)
  assert.deepEqual(applied, { outcomeId: 'outcome-1', accountId: 'account-1', inputVersion: 1, disposition: 'applied', changed: true })
  const account = database.prepare(`SELECT last_health_check_at, last_health_success_at, next_health_check_at, health_check_failure_count FROM accounts WHERE id = ?`).get('account-1') as Record<string, unknown>
  assert.equal(account.last_health_check_at, '2026-08-16T00:00:00.000Z')
  assert.equal(account.last_health_success_at, '2026-08-16T00:00:00.000Z')
  assert.equal(account.next_health_check_at, '2026-08-16T01:00:00.000Z')
  assert.equal(account.health_check_failure_count, 0)

  const replay = projectAccountHealthJobsOutcome(healthSuccess('outcome-1'), database)
  assert.deepEqual(replay, { outcomeId: 'outcome-1', accountId: 'account-1', inputVersion: 1, disposition: 'applied', changed: false })

  database.prepare(`UPDATE account_health_jobs_input_versions SET current_version = 2 WHERE account_id = ?`).run('account-1')
  const stale = projectAccountHealthJobsOutcome(healthSuccess('outcome-2'), database)
  assert.equal(stale.disposition, 'stale')
  assert.equal(stale.reason, 'input_version_stale')
  const receipt = database.prepare(`SELECT disposition FROM account_health_projection_receipts WHERE outcome_id = ?`).get('outcome-2') as { disposition: string }
  assert.equal(receipt.disposition, 'stale')

  database.prepare(`INSERT INTO accounts(id, status, schedulable, config_revision, dispatch_revision, cooldown_until, cooldown_retest_observation_started_at, cooldown_retest_generation, updated_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`)
    .run('account-2', 'temporary_unavailable', 1, 4, 5, '2026-08-16T00:05:00.000Z', '2026-08-16T00:00:00.000Z', 'generation-1', '2026-08-16T00:00:00.000Z')
  database.prepare(`INSERT INTO account_health_jobs_input_versions(account_id, current_version, reserved_at) VALUES (?, ?, ?)`)
    .run('account-2', 1, '2026-08-16T00:00:00.000Z')
  const deferred = projectAccountHealthJobsOutcome(cooldownDefer('outcome-3'), database)
  assert.equal(deferred.disposition, 'applied')
  const cooling = database.prepare(`SELECT cooldown_until, cooldown_retest_generation FROM accounts WHERE id = ?`).get('account-2') as Record<string, unknown>
  assert.equal(cooling.cooldown_until, '2026-08-16T00:10:00.000Z')
  assert.equal(cooling.cooldown_retest_generation, 'generation-1')
  database.prepare(`UPDATE accounts SET cooldown_retest_generation = ? WHERE id = ?`).run('generation-2', 'account-2')
  const staleCooldown = projectAccountHealthJobsOutcome(cooldownDefer('outcome-4'), database)
  assert.equal(staleCooldown.disposition, 'stale')
  assert.equal(staleCooldown.reason, 'cooldown_generation_stale')
} finally {
  database.close()
}

console.log('account-health-projection-regression passed')

function healthSuccess(outcomeId: string): AccountHealthJobsOutcome {
  return {
    outcome_id: outcomeId,
    request_id: `request-${outcomeId}`,
    account_id: 'account-1',
    outcome: 'complete_success',
    observed_at: '2026-08-16T00:00:00.000Z',
    input_version: 1,
    config_revision: 2,
    dispatch_revision: 3,
    status_code: 200,
    next_due_at: '2026-08-16T01:00:00.000Z',
    projection: {
      target_account_id: 'account-1',
      transition_kind: 'health_success',
      input_version: 1,
      config_revision: 2,
      dispatch_revision: 3,
      expected_account_status: 'active'
    }
  }
}

function cooldownDefer(outcomeId: string): AccountHealthJobsOutcome {
  return {
    outcome_id: outcomeId,
    request_id: `request-${outcomeId}`,
    account_id: 'account-2',
    outcome: 'framing_complete_neutral',
    observed_at: '2026-08-16T00:05:00.000Z',
    input_version: 1,
    config_revision: 4,
    dispatch_revision: 5,
    next_due_at: '2026-08-16T00:10:00.000Z',
    failure_count: 2,
    projection: {
      target_account_id: 'account-2',
      transition_kind: 'cooldown_defer',
      input_version: 1,
      config_revision: 4,
      dispatch_revision: 5,
      expected_account_status: 'temporary_unavailable',
      expected_cooldown_fence: {
        observation_started_at: '2026-08-16T00:00:00.000Z',
        generation: 'generation-1'
      },
      cooldown_fence: {
        observation_started_at: '2026-08-16T00:00:00.000Z',
        generation: 'generation-1'
      }
    }
  }
}
