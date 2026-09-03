import assert from 'node:assert/strict'
import { createRequire } from 'node:module'
import type { DatabaseSync } from 'node:sqlite'

import { projectAccountHealthJobsOutcome } from '../../storage/account-health-projection.repository.js'
import type { AccountHealthJobsOutcome } from '../../storage/account-health-jobs-outcome.repository.js'
import { encryptJson } from '../../storage/crypto.js'

const require = createRequire(import.meta.url)
const Constructor = require('node:sqlite').DatabaseSync as new (path: string) => DatabaseSync

type CooldownFence = {
  observation_started_at: string
  generation: string
  source_config_revision?: number
}

const defaultCooldownFence: CooldownFence = {
  observation_started_at: '2026-08-16T00:00:00.000Z',
  generation: 'generation-1'
}

const database = new Constructor(':memory:')
try {
  database.exec(`
    CREATE TABLE accounts (
      id TEXT PRIMARY KEY,
      status TEXT NOT NULL,
      schedulable INTEGER NOT NULL,
      config_revision INTEGER NOT NULL,
      dispatch_revision INTEGER NOT NULL,
      circuit_projection_revision INTEGER NOT NULL DEFAULT 0,
      authorization_instance_source_account_id TEXT,
      cooldown_until TEXT,
      cooldown_retest_failure_count INTEGER NOT NULL DEFAULT 0,
      cooldown_retest_observation_started_at TEXT,
      cooldown_retest_generation TEXT,
      type TEXT NOT NULL DEFAULT 'api_key',
      credentials_encrypted TEXT NOT NULL DEFAULT '',
      balance_query_enabled INTEGER NOT NULL DEFAULT 0,
      balance_query_config_json TEXT NOT NULL DEFAULT '{}',
      balance_query_next_refresh_at TEXT,
      availability_schedule_json TEXT,
      availability_schedule_next_check_at TEXT,
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
    CREATE TABLE account_circuit_outbox (
      event_id TEXT PRIMARY KEY,
      projection_key TEXT NOT NULL,
      dedupe_key TEXT NOT NULL UNIQUE,
      event_type TEXT NOT NULL,
      account_id TEXT NOT NULL,
      account_runtime_key TEXT NOT NULL,
      circuit_scope_key TEXT,
      incident_id TEXT,
      transition_id TEXT NOT NULL,
      dispatch_revision INTEGER NOT NULL,
      generation INTEGER,
      ledger_revision INTEGER,
      status TEXT NOT NULL,
      available_at_ms INTEGER NOT NULL,
      claim_token TEXT,
      claimed_by TEXT,
      claim_until_ms INTEGER,
      attempt_count INTEGER NOT NULL DEFAULT 0,
      last_error_class TEXT,
      acknowledged_at_ms INTEGER,
      created_at_ms INTEGER NOT NULL,
      updated_at_ms INTEGER NOT NULL
    );
  `)
  database.prepare(`INSERT INTO accounts(id, status, schedulable, config_revision, dispatch_revision, credentials_encrypted, updated_at) VALUES (?, ?, ?, ?, ?, ?, ?)`)
    .run('account-1', 'active', 1, 2, 3, encryptJson({ api_key: 'sk-account-1' }), '2026-08-16T00:00:00.000Z')
  database.prepare(`INSERT INTO account_health_jobs_input_versions(account_id, current_version, reserved_at) VALUES (?, ?, ?)`)
    .run('account-1', 1, '2026-08-16T00:00:00.000Z')

  const applied = projectAccountHealthJobsOutcome(healthSuccess('outcome-1'), database)
  assert.deepEqual(applied, { outcomeId: 'outcome-1', accountId: 'account-1', inputVersion: 1, disposition: 'applied', changed: true })
  const account = database.prepare(`SELECT last_health_check_at, last_health_success_at, next_health_check_at, health_check_failure_count FROM accounts WHERE id = ?`).get('account-1') as Record<string, unknown>
  assert.equal(account.last_health_check_at, '2026-08-16T00:00:00.000Z')
  assert.equal(account.last_health_success_at, '2026-08-16T00:00:00.000Z')
  assert.equal(account.next_health_check_at, '2026-08-16T01:00:00.000Z')
  assert.equal(account.health_check_failure_count, 0)

  database.prepare(`INSERT INTO accounts(id, status, schedulable, config_revision, dispatch_revision, credentials_encrypted, updated_at) VALUES (?, ?, ?, ?, ?, ?, ?)`)
    .run('account-4', 'active', 1, 8, 9, encryptJson({ api_key: 'sk-account-4' }), '2026-08-16T00:00:00.000Z')
  database.prepare(`INSERT INTO account_health_jobs_input_versions(account_id, current_version, reserved_at) VALUES (?, ?, ?)`)
    .run('account-4', 1, '2026-08-16T00:00:00.000Z')
  const thresholdTransition = projectAccountHealthJobsOutcome(temporaryUnavailableAfterThreshold('outcome-threshold'), database)
  assert.equal(thresholdTransition.disposition, 'applied')
  const thresholdAccount = database.prepare(`SELECT status, cooldown_retest_failure_count, health_check_failure_count FROM accounts WHERE id = ?`).get('account-4') as Record<string, unknown>
  assert.equal(thresholdAccount.status, 'temporary_unavailable')
  assert.equal(thresholdAccount.cooldown_retest_failure_count, 0)
  assert.equal(thresholdAccount.health_check_failure_count, 3)

  database.prepare(`INSERT INTO accounts(id, status, schedulable, config_revision, dispatch_revision, credentials_encrypted, updated_at, cooldown_until, cooldown_retest_observation_started_at, cooldown_retest_generation, health_check_failure_count, health_check_failure_started_at, last_health_check_at, next_health_check_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`)
    .run('account-5', 'temporary_unavailable', 1, 4, 5, encryptJson({ api_key: 'sk-account-5' }), '2026-08-16T00:00:00.000Z', '2026-08-16T00:10:00.000Z', '2026-08-16T00:00:00.000Z', 'generation-1', 3, '2026-08-15T23:50:00.000Z', '2026-08-16T00:00:00.000Z', '2026-08-16T00:05:00.000Z')
  database.prepare(`INSERT INTO account_health_jobs_input_versions(account_id, current_version, reserved_at) VALUES (?, ?, ?)`)
    .run('account-5', 1, '2026-08-16T00:00:00.000Z')
  const cooldownFailurePreservesHealth = cooldownFailure('outcome-cooldown-preserves-health')
  cooldownFailurePreservesHealth.account_id = 'account-5'
  cooldownFailurePreservesHealth.projection!.target_account_id = 'account-5'
  const cooldownFailureResult = projectAccountHealthJobsOutcome(cooldownFailurePreservesHealth, database)
  assert.equal(cooldownFailureResult.disposition, 'applied')
  const cooldownPreserved = database.prepare(`SELECT health_check_failure_count, health_check_failure_started_at, last_health_check_at, next_health_check_at, cooldown_retest_failure_count, cooldown_retest_last_at FROM accounts WHERE id = ?`).get('account-5') as Record<string, unknown>
  assert.equal(cooldownPreserved.health_check_failure_count, 3)
  assert.equal(cooldownPreserved.health_check_failure_started_at, '2026-08-15T23:50:00.000Z')
  assert.equal(cooldownPreserved.last_health_check_at, '2026-08-16T00:00:00.000Z')
  assert.equal(cooldownPreserved.next_health_check_at, '2026-08-16T00:05:00.000Z')
  assert.equal(cooldownPreserved.cooldown_retest_failure_count, 2)
  assert.equal(cooldownPreserved.cooldown_retest_last_at, '2026-08-16T00:05:00.000Z')

  const malformedHistorical = projectAccountHealthJobsOutcome(healthFailureExpectedError('outcome-malformed-historical'), database)
  assert.deepEqual(malformedHistorical, {
    outcomeId: 'outcome-malformed-historical',
    accountId: 'account-1',
    inputVersion: 1,
    disposition: 'rejected',
    changed: false,
    reason: 'projection_health_failure_expected_status_invalid'
  })

  const replay = projectAccountHealthJobsOutcome(healthSuccess('outcome-1'), database)
  assert.deepEqual(replay, { outcomeId: 'outcome-1', accountId: 'account-1', inputVersion: 1, disposition: 'applied', changed: false })

  database.prepare(`UPDATE account_health_jobs_input_versions SET current_version = 2 WHERE account_id = ?`).run('account-1')
  const stale = projectAccountHealthJobsOutcome(healthSuccess('outcome-2'), database)
  assert.equal(stale.disposition, 'stale')
  assert.equal(stale.reason, 'input_version_stale')
  const receipt = database.prepare(`SELECT disposition FROM account_health_projection_receipts WHERE outcome_id = ?`).get('outcome-2') as { disposition: string }
  assert.equal(receipt.disposition, 'stale')

  database.prepare(`INSERT INTO accounts(id, status, schedulable, config_revision, dispatch_revision, credentials_encrypted, cooldown_until, cooldown_retest_observation_started_at, cooldown_retest_generation, updated_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`)
    .run('account-2', 'temporary_unavailable', 1, 4, 5, encryptJson({ api_key: 'sk-account-2' }), '2026-08-16T00:05:00.000Z', '2026-08-16T00:00:00.000Z', 'generation-1', '2026-08-16T00:00:00.000Z')
  database.prepare(`INSERT INTO account_health_jobs_input_versions(account_id, current_version, reserved_at) VALUES (?, ?, ?)`)
    .run('account-2', 1, '2026-08-16T00:00:00.000Z')

  const observationFenceMismatch = projectAccountHealthJobsOutcome(cooldownDefer('outcome-3-observation-mismatch', {
    observation_started_at: '2026-08-16T00:00:01.000Z',
    generation: 'generation-1'
  }), database)
  assert.deepEqual(observationFenceMismatch, {
    outcomeId: 'outcome-3-observation-mismatch',
    accountId: 'account-2',
    inputVersion: 1,
    disposition: 'rejected',
    changed: false,
    reason: 'projection_cooldown_fence_mismatch'
  })

  const sourceFenceMismatch = projectAccountHealthJobsOutcome(cooldownDefer('outcome-3-source-mismatch', {
    observation_started_at: '2026-08-16T00:00:00.000Z',
    generation: 'generation-1',
    source_config_revision: 8
  }), database)
  assert.equal(sourceFenceMismatch.disposition, 'rejected')
  assert.equal(sourceFenceMismatch.reason, 'projection_cooldown_fence_mismatch')

  const failureFenceMismatch = projectAccountHealthJobsOutcome(cooldownFailure('outcome-3-failure-mismatch', {
    observation_started_at: '2026-08-16T00:00:00.000Z',
    generation: 'generation-2'
  }), database)
  assert.equal(failureFenceMismatch.disposition, 'rejected')
  assert.equal(failureFenceMismatch.reason, 'projection_cooldown_fence_mismatch')

  const deferred = projectAccountHealthJobsOutcome(cooldownDefer('outcome-3'), database)
  assert.equal(deferred.disposition, 'applied')
  const cooling = database.prepare(`SELECT cooldown_until, cooldown_retest_generation FROM accounts WHERE id = ?`).get('account-2') as Record<string, unknown>
  assert.equal(cooling.cooldown_until, '2026-08-16T00:10:00.000Z')
  assert.equal(cooling.cooldown_retest_generation, 'generation-1')
  database.prepare(`UPDATE accounts SET cooldown_retest_generation = ? WHERE id = ?`).run('generation-2', 'account-2')
  const staleCooldown = projectAccountHealthJobsOutcome(cooldownDefer('outcome-4'), database)
  assert.equal(staleCooldown.disposition, 'stale')
  assert.equal(staleCooldown.reason, 'cooldown_generation_stale')

  const recovered = projectAccountHealthJobsOutcome(cooldownSuccess('outcome-5', {
    observation_started_at: '2026-08-16T00:00:00.000Z',
    generation: 'generation-2'
  }), database)
  assert.equal(recovered.disposition, 'applied')
  const recoveredAccount = database.prepare(`SELECT status, cooldown_until, cooldown_retest_generation, dispatch_revision FROM accounts WHERE id = ?`).get('account-2') as Record<string, unknown>
  assert.equal(recoveredAccount.status, 'active')
  assert.equal(recoveredAccount.cooldown_until, null)
  assert.equal(recoveredAccount.cooldown_retest_generation, null)
  assert.equal(recoveredAccount.dispatch_revision, 6, 'cooldown 成功恢复必须推进 dispatch revision')

  database.prepare(`UPDATE accounts SET status = ?, schedulable = ?, cooldown_until = ?, cooldown_retest_failure_count = ?, cooldown_retest_observation_started_at = ?, cooldown_retest_generation = ? WHERE id = ?`)
    .run('temporary_unavailable', 1, '2026-08-23T00:00:00.000Z', 1, '2026-08-16T00:00:00.000Z', 'terminal-generation', 'account-2')
  const terminalOutcome = cooldownError('outcome-5-terminal', {
    observation_started_at: '2026-08-16T00:00:00.000Z',
    generation: 'terminal-generation'
  })
  terminalOutcome.dispatch_revision = 6
  terminalOutcome.projection!.dispatch_revision = 6
  const terminal = projectAccountHealthJobsOutcome(terminalOutcome, database)
  assert.equal(terminal.disposition, 'applied')
  const terminalAccount = database.prepare(`SELECT status, schedulable, cooldown_until, cooldown_retest_failure_count, cooldown_retest_observation_started_at, cooldown_retest_generation, cooldown_retest_last_at, cooldown_retest_last_status_code, last_error_code FROM accounts WHERE id = ?`).get('account-2') as Record<string, unknown>
  assert.equal(terminalAccount.status, 'error')
  assert.equal(terminalAccount.schedulable, 0)
  assert.equal(terminalAccount.cooldown_until, null)
  assert.equal(terminalAccount.cooldown_retest_failure_count, 2)
  assert.equal(terminalAccount.cooldown_retest_observation_started_at, '2026-08-16T00:00:00.000Z')
  assert.equal(terminalAccount.cooldown_retest_generation, 'terminal-generation')
  assert.equal(terminalAccount.cooldown_retest_last_at, '2026-08-16T00:05:00.000Z')
  assert.equal(terminalAccount.cooldown_retest_last_status_code, 503)
  assert.equal(terminalAccount.last_error_code, 'cooldown_retest_observation_timeout')

  database.prepare(`INSERT INTO accounts(id, status, schedulable, config_revision, dispatch_revision, credentials_encrypted, updated_at) VALUES (?, ?, ?, ?, ?, ?, ?)`)
    .run('account-3', 'pending_test', 0, 6, 7, encryptJson({ api_key: 'sk-account-3' }), '2026-08-16T00:00:00.000Z')
  database.prepare(`INSERT INTO account_health_jobs_input_versions(account_id, current_version, reserved_at) VALUES (?, ?, ?)`)
    .run('account-3', 1, '2026-08-16T00:00:00.000Z')
  const activation = projectAccountHealthJobsOutcome(activationSuccess('outcome-6'), database)
  assert.equal(activation.disposition, 'applied')
  const activated = database.prepare(`SELECT status, schedulable, balance_query_next_refresh_at, dispatch_revision FROM accounts WHERE id = ?`).get('account-3') as Record<string, unknown>
  assert.equal(activated.status, 'active')
  assert.equal(activated.schedulable, 1)
  assert.equal(activated.balance_query_next_refresh_at, '2026-08-16T00:15:00.000Z')
  assert.equal(activated.dispatch_revision, 8, 'activation 成功恢复必须推进 dispatch revision')

  database.prepare(`INSERT INTO accounts(id, status, schedulable, config_revision, dispatch_revision, credentials_encrypted, availability_schedule_json, updated_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?)`)
    .run('account-6', 'pending_test', 0, 10, 11, encryptJson({ api_key: 'sk-account-6' }), '{invalid', '2026-08-16T00:00:00.000Z')
  database.prepare(`INSERT INTO account_health_jobs_input_versions(account_id, current_version, reserved_at) VALUES (?, ?, ?)`)
    .run('account-6', 1, '2026-08-16T00:00:00.000Z')
  const malformedScheduleOutcome = activationSuccess('outcome-7')
  malformedScheduleOutcome.account_id = 'account-6'
  malformedScheduleOutcome.config_revision = 10
  malformedScheduleOutcome.dispatch_revision = 11
  malformedScheduleOutcome.projection = {
    ...malformedScheduleOutcome.projection!,
    target_account_id: 'account-6',
    config_revision: 10,
    dispatch_revision: 11
  }
  const malformedSchedule = projectAccountHealthJobsOutcome(malformedScheduleOutcome, database)
  assert.equal(malformedSchedule.disposition, 'applied')
  const malformedScheduleAccount = database.prepare(`SELECT status, dispatch_revision, availability_schedule_next_check_at FROM accounts WHERE id = ?`).get('account-6') as Record<string, unknown>
  assert.equal(malformedScheduleAccount.status, 'disabled', '损坏时间计划不能在健康成功后绕过停用状态')
  assert.equal(malformedScheduleAccount.dispatch_revision, 11, '未恢复为 active 时不能发布 dispatch revision')
  assert.equal(malformedScheduleAccount.availability_schedule_next_check_at, null)
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

function temporaryUnavailableAfterThreshold(outcomeId: string): AccountHealthJobsOutcome {
  const fence: CooldownFence = { observation_started_at: '2026-08-16T00:00:00.000Z', generation: 'threshold-generation' }
  return {
    outcome_id: outcomeId,
    request_id: `request-${outcomeId}`,
    account_id: 'account-4',
    outcome: 'upstream_failure',
    observed_at: '2026-08-16T00:00:00.000Z',
    input_version: 1,
    config_revision: 8,
    dispatch_revision: 9,
    next_due_at: '2026-08-16T00:00:03.000Z',
    failure_count: 0,
    failure_started_at: '2026-08-15T23:50:00.000Z',
    projection: {
      target_account_id: 'account-4',
      transition_kind: 'temporary_unavailable',
      input_version: 1,
      config_revision: 8,
      dispatch_revision: 9,
      expected_account_status: 'active',
      cooldown_fence: fence,
      values: { health_check_failure_count: 3 }
    }
  }
}

function cooldownDefer(outcomeId: string, outputFence: CooldownFence = defaultCooldownFence): AccountHealthJobsOutcome {
  return cooldownOutcome(outcomeId, 'cooldown_defer', 'framing_complete_neutral', outputFence)
}

function cooldownFailure(outcomeId: string, outputFence: CooldownFence = defaultCooldownFence): AccountHealthJobsOutcome {
  return cooldownOutcome(outcomeId, 'cooldown_failure', 'upstream_failure', outputFence)
}

function cooldownSuccess(outcomeId: string, expectedFence: CooldownFence = defaultCooldownFence): AccountHealthJobsOutcome {
  return cooldownOutcome(outcomeId, 'cooldown_success', 'complete_success', undefined, expectedFence)
}

function cooldownError(outcomeId: string, expectedFence: CooldownFence): AccountHealthJobsOutcome {
  const outcome = cooldownOutcome(outcomeId, 'cooldown_failure', 'upstream_failure', expectedFence, expectedFence)
  outcome.next_due_at = undefined
  outcome.account_status = 'error'
  outcome.error_code = 'cooldown_retest_observation_timeout'
  outcome.error_message = '冷却复测观察期已超过 7 天'
  outcome.status_code = 503
  outcome.projection = {
    ...outcome.projection!,
    transition_kind: 'cooldown_error',
    expected_cooldown_fence: expectedFence
  }
  return outcome
}

function cooldownOutcome(
  outcomeId: string,
  transition: 'cooldown_defer' | 'cooldown_failure' | 'cooldown_success',
  outcome: 'complete_success' | 'framing_complete_neutral' | 'upstream_failure',
  outputFence?: CooldownFence,
  expectedFence: CooldownFence = defaultCooldownFence
): AccountHealthJobsOutcome {
  const projection: NonNullable<AccountHealthJobsOutcome['projection']> = {
    target_account_id: 'account-2',
    transition_kind: transition,
    input_version: 1,
    config_revision: 4,
    dispatch_revision: 5,
    expected_account_status: 'temporary_unavailable',
    expected_cooldown_fence: expectedFence
  }
  if (outputFence) projection.cooldown_fence = outputFence
  return {
    outcome_id: outcomeId,
    request_id: `request-${outcomeId}`,
    account_id: 'account-2',
    outcome,
    observed_at: '2026-08-16T00:05:00.000Z',
    input_version: 1,
    config_revision: 4,
    dispatch_revision: 5,
    next_due_at: '2026-08-16T00:10:00.000Z',
    failure_count: 2,
    projection
  }
}

function activationSuccess(outcomeId: string): AccountHealthJobsOutcome {
  return {
    outcome_id: outcomeId,
    request_id: `request-${outcomeId}`,
    account_id: 'account-3',
    outcome: 'complete_success',
    observed_at: '2026-08-16T00:15:00.000Z',
    input_version: 1,
    config_revision: 6,
    dispatch_revision: 7,
    status_code: 200,
    next_due_at: '2026-08-16T01:15:00.000Z',
    projection: {
      target_account_id: 'account-3',
      transition_kind: 'activation_success',
      input_version: 1,
      config_revision: 6,
      dispatch_revision: 7,
      expected_account_status: 'pending_test'
    }
  }
}

function healthFailureExpectedError(outcomeId: string): AccountHealthJobsOutcome {
  return {
    outcome_id: outcomeId,
    request_id: `request-${outcomeId}`,
    account_id: 'account-1',
    outcome: 'upstream_failure',
    observed_at: '2026-08-16T00:20:00.000Z',
    input_version: 1,
    config_revision: 2,
    dispatch_revision: 3,
    error_code: 'upstream_connection_closed',
    projection: {
      target_account_id: 'account-1',
      transition_kind: 'health_failure',
      input_version: 1,
      config_revision: 2,
      dispatch_revision: 3,
      expected_account_status: 'error'
    }
  }
}
