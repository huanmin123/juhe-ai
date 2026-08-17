import assert from 'node:assert/strict'

import type { AccountSummary } from '../../domain/types.js'
import { currentAccountHealthJobsProbeInput } from '../../modules/internal-api/account-health-jobs-dispatch-boundary.js'

const eligibleAccount = {} as AccountSummary

assert.equal(currentAccountHealthJobsProbeInput(undefined, 1), undefined)
assert.equal(currentAccountHealthJobsProbeInput(eligibleAccount, undefined), undefined)
assert.equal(currentAccountHealthJobsProbeInput(eligibleAccount, 0), undefined)
assert.deepEqual(currentAccountHealthJobsProbeInput(eligibleAccount, 1), { account: eligibleAccount, inputVersion: 1 })

console.log('account-health-jobs-dispatch-boundary-regression passed')
