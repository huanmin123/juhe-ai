import assert from 'node:assert/strict'

import type { AccountSummary } from '../../domain/types.js'
import { currentAccountHealthJobsProbeInput } from '../../modules/internal-api/account-health-jobs-dispatch-boundary.js'

const eligibleAccount = {} as AccountSummary
const revisions = { configRevision: 7, dispatchRevision: 11 }
const matchingAccount = { ...eligibleAccount, configRevision: 7 } as AccountSummary

assert.equal(currentAccountHealthJobsProbeInput(undefined, 1, revisions), undefined)
assert.equal(currentAccountHealthJobsProbeInput(matchingAccount, undefined, revisions), undefined)
assert.equal(currentAccountHealthJobsProbeInput(matchingAccount, 0, revisions), undefined)
assert.equal(currentAccountHealthJobsProbeInput(matchingAccount, 1, undefined), undefined)
assert.equal(currentAccountHealthJobsProbeInput(matchingAccount, 1, { ...revisions, configRevision: 8 }), undefined)
assert.deepEqual(currentAccountHealthJobsProbeInput(matchingAccount, 1, revisions), {
  account: { ...matchingAccount, dispatchRevision: 11 },
  inputVersion: 1
})

console.log('account-health-jobs-dispatch-boundary-regression passed')
