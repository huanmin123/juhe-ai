import assert from 'node:assert/strict'
import { createRequire } from 'node:module'
import type { DatabaseSync } from 'node:sqlite'

import {
  advanceAccountHealthProjectionCursor,
  currentAccountHealthProjectionCursor
} from '../../storage/account-health-projection-cursor.repository.js'

const require = createRequire(import.meta.url)
const Constructor = require('node:sqlite').DatabaseSync as new (path: string) => DatabaseSync
const database = new Constructor(':memory:')
try {
  database.exec(`CREATE TABLE account_health_projection_cursors(consumer_key TEXT PRIMARY KEY, observed_at TEXT, outcome_id TEXT, updated_at TEXT NOT NULL)`)
  const first = { observedAt: '2026-08-16T00:00:00.000Z', outcomeId: 'outcome-1' }
  const second = { observedAt: '2026-08-16T00:00:00.000Z', outcomeId: 'outcome-2' }
  assert.equal(advanceAccountHealthProjectionCursor('business-projector', first, database), true)
  assert.equal(advanceAccountHealthProjectionCursor('business-projector', first, database), false)
  assert.equal(advanceAccountHealthProjectionCursor('business-projector', second, database), true)
  assert.equal(advanceAccountHealthProjectionCursor('business-projector', first, database), false)
  assert.deepEqual(currentAccountHealthProjectionCursor('business-projector', database), second)
} finally {
  database.close()
}

console.log('account-health-projection-cursor-regression passed')
