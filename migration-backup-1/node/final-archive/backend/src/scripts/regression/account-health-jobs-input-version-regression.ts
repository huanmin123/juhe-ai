import assert from 'node:assert/strict'
import { createRequire } from 'node:module'
import type { DatabaseSync } from 'node:sqlite'

import {
  currentAccountHealthJobsInputVersion,
  reserveAccountHealthJobsInputVersion
} from '../../storage/account-health-jobs-input-version.repository.js'

const require = createRequire(import.meta.url)
const Constructor = require('node:sqlite').DatabaseSync as new (path: string) => DatabaseSync

const database = new Constructor(':memory:')
try {
  database.exec(`
    CREATE TABLE account_health_jobs_input_versions (
      account_id TEXT PRIMARY KEY,
      current_version INTEGER NOT NULL,
      reserved_at TEXT NOT NULL
    )
  `)
  assert.equal(reserveAccountHealthJobsInputVersion('account-1', database), 1)
  assert.equal(reserveAccountHealthJobsInputVersion('account-1', database), 2)
  assert.equal(currentAccountHealthJobsInputVersion('account-1', database), 2)
  assert.equal(currentAccountHealthJobsInputVersion('unknown', database), undefined)
} finally {
  database.close()
}

console.log('account-health-jobs-input-version-regression passed')
