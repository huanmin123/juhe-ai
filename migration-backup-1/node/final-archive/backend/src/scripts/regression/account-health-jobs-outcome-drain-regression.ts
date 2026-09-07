import assert from 'node:assert/strict'
import { createRequire } from 'node:module'
import { mkdtempSync, rmSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join, resolve } from 'node:path'

import { drainAccountHealthJobsOutcomes } from '../../modules/background/account-health-jobs-outcome-drain.service.js'
import type { AccountHealthJobsOutcomeCursor } from '../../storage/account-health-jobs-outcome.repository.js'

const require = createRequire(import.meta.url)
const Constructor = require('node:sqlite').DatabaseSync as new (path: string) => {
  exec(sql: string): void
  prepare(sql: string): { run(...values: unknown[]): void }
  close(): void
}
const testRoot = resolve(process.env.JUHE_AI_TEST_TEMP_ROOT?.trim() || tmpdir())
const root = mkdtempSync(join(testRoot, 'juhe-ai-account-health-drain-'))
const path = join(root, 'jobs.sqlite3')
try {
  const database = new Constructor(path)
  database.exec('CREATE TABLE account_health_outcomes(outcome_id TEXT PRIMARY KEY, observed_at TEXT NOT NULL, payload TEXT NOT NULL)')
  for (const outcomeId of ['outcome-1', 'outcome-2']) {
    database.prepare('INSERT INTO account_health_outcomes(outcome_id, observed_at, payload) VALUES (?, ?, ?)').run(
      outcomeId,
      '2026-08-16T00:00:00.000Z',
      JSON.stringify({
        outcome_id: outcomeId,
        request_id: `request-${outcomeId}`,
        account_id: 'account-1',
        outcome: 'probe_task_failure',
        observed_at: '2026-08-16T00:00:00.000Z',
        input_version: 1,
        config_revision: 2,
        dispatch_revision: 3
      })
    )
  }
  database.close()
  let cursor: AccountHealthJobsOutcomeCursor | undefined
  const applied: string[] = []
  const first = await drainAccountHealthJobsOutcomes({
    source: { mode: 'sqlite', databasePath: path },
    limit: 10,
    dependencies: {
      async loadCursor() { return cursor },
      async projectAndAdvance(outcome, next) {
        applied.push(outcome.outcome_id)
        cursor = next
        return { cursorAdvanced: true }
      }
    }
  })
  assert.equal(first.processed, 2)
  assert.deepEqual(applied, ['outcome-1', 'outcome-2'])
  assert.deepEqual(first.lastCursor, { observedAt: '2026-08-16T00:00:00.000Z', outcomeId: 'outcome-2' })
  const replay = await drainAccountHealthJobsOutcomes({
    source: { mode: 'sqlite', databasePath: path },
    limit: 10,
    dependencies: {
      async loadCursor() { return cursor },
      async projectAndAdvance() { throw new Error('cursor 已经推进后不得重放') }
    }
  })
  assert.equal(replay.processed, 0)
} finally {
  rmSync(root, { recursive: true, force: true })
}

console.log('account-health-jobs-outcome-drain-regression passed')
