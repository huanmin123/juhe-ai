import assert from 'node:assert/strict'

import { Pool } from 'pg'

import { createPostgresDatabaseClient } from '../../storage/database-client.js'
import { projectAccountHealthJobsOutcomeAsync } from '../../storage/account-health-projection.repository.js'
import type { AccountHealthJobsOutcome } from '../../storage/account-health-jobs-outcome.repository.js'
import { closeRedisClients } from '../../shared/redis-client.js'

const postgresUrl = process.env.JUHE_AI_J1_PROJECTION_POSTGRES_SMOKE_URL?.trim()
const accountId = process.env.JUHE_AI_J1_PROJECTION_POSTGRES_SMOKE_ACCOUNT_ID?.trim()
if (!postgresUrl || !accountId) {
  throw new Error('J1 PG projector smoke 需要 JUHE_AI_J1_PROJECTION_POSTGRES_SMOKE_URL 和 JUHE_AI_J1_PROJECTION_POSTGRES_SMOKE_ACCOUNT_ID')
}

const pool = new Pool({ connectionString: postgresUrl })
try {
  const client = createPostgresDatabaseClient(pool)
  const row = await client.one<{
    config_revision: number | string | bigint
    dispatch_revision: number | string | bigint
    current_version: number | string | bigint
  }>(`
    SELECT a.config_revision, a.dispatch_revision, v.current_version
    FROM juhe_business.accounts a
    JOIN juhe_business.account_health_jobs_input_versions v ON v.account_id = a.id
    WHERE a.id = ? AND a.deleted_at IS NULL
  `, [accountId])
  assert(row, 'J1 PG projector smoke fixture account 不存在或缺少 input epoch')
  const configRevision = Number(row.config_revision)
  const dispatchRevision = Number(row.dispatch_revision)
  const inputVersion = Number(row.current_version)
  assert(Number.isInteger(configRevision) && configRevision > 0)
  assert(Number.isInteger(dispatchRevision) && dispatchRevision > 0)
  assert(Number.isInteger(inputVersion) && inputVersion > 0)

  const observedAt = '2026-08-17T00:00:00.000Z'
  const originalGeneration = 'j1-pg-projection-generation-1'
  await client.execute(`
    UPDATE juhe_business.accounts
    SET status = 'temporary_unavailable', schedulable = 1,
        cooldown_until = ?, cooldown_retest_observation_started_at = ?,
        cooldown_retest_generation = ?, updated_at = ?
    WHERE id = ?
  `, ['2026-08-17T00:05:00.000Z', observedAt, originalGeneration, observedAt, accountId])

  const applied = await projectAccountHealthJobsOutcomeAsync(client, cooldownDefer('j1-pg-projection-applied', accountId, inputVersion, configRevision, dispatchRevision, observedAt, originalGeneration))
  assert.deepEqual(applied, {
    outcomeId: 'j1-pg-projection-applied',
    accountId,
    inputVersion,
    disposition: 'applied',
    changed: true
  })
  const replay = await projectAccountHealthJobsOutcomeAsync(client, cooldownDefer('j1-pg-projection-applied', accountId, inputVersion, configRevision, dispatchRevision, observedAt, originalGeneration))
  assert.equal(replay.disposition, 'applied')
  assert.equal(replay.changed, false)

  await client.execute('UPDATE juhe_business.accounts SET cooldown_retest_generation = ? WHERE id = ?', ['j1-pg-projection-generation-2', accountId])
  const stale = await projectAccountHealthJobsOutcomeAsync(client, cooldownDefer('j1-pg-projection-stale', accountId, inputVersion, configRevision, dispatchRevision, observedAt, originalGeneration))
  assert.equal(stale.disposition, 'stale')
  assert.equal(stale.reason, 'cooldown_generation_stale')
  const receipt = await client.one<{ disposition: string; reason: string | null }>(
    'SELECT disposition, reason FROM juhe_business.account_health_projection_receipts WHERE outcome_id = ?',
    ['j1-pg-projection-stale']
  )
  assert.deepEqual(receipt, { disposition: 'stale', reason: 'cooldown_generation_stale' })
  console.log('account-health-projection-postgres-smoke passed')
} finally {
  await closeRedisClients()
  await pool.end()
}

// The projector's post-commit cache invalidation is intentionally fire-and-
// forget in production. This one-shot smoke has completed its explicit
// cleanup above, so do not let a late cache retry keep a regression process
// alive after its result has been emitted.
process.exit(0)

function cooldownDefer(
  outcomeId: string,
  accountId: string,
  inputVersion: number,
  configRevision: number,
  dispatchRevision: number,
  observedAt: string,
  generation: string
): AccountHealthJobsOutcome {
  return {
    outcome_id: outcomeId,
    request_id: `request-${outcomeId}`,
    account_id: accountId,
    outcome: 'framing_complete_neutral',
    observed_at: observedAt,
    input_version: inputVersion,
    config_revision: configRevision,
    dispatch_revision: dispatchRevision,
    next_due_at: '2026-08-17T00:10:00.000Z',
    failure_count: 2,
    projection: {
      target_account_id: accountId,
      transition_kind: 'cooldown_defer',
      input_version: inputVersion,
      config_revision: configRevision,
      dispatch_revision: dispatchRevision,
      expected_account_status: 'temporary_unavailable',
      expected_cooldown_fence: {
        observation_started_at: observedAt,
        generation
      },
      cooldown_fence: {
        observation_started_at: observedAt,
        generation
      }
    }
  }
}
