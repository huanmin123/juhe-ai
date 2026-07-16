import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'
import { fileURLToPath } from 'node:url'
import { automaticAccountProbeOutcome } from '../../modules/accounts/automatic-account-probe-outcome.js'

const taskFailure = automaticAccountProbeOutcome({ success: false, accountFailureEligible: true }, false)
assert.equal(taskFailure, 'probe_task_failure')

const upstreamFailure = automaticAccountProbeOutcome({ success: false, accountFailureEligible: true }, true)
assert.equal(upstreamFailure, 'upstream_failure')

assert.equal(automaticAccountProbeOutcome({ success: true, accountFailureEligible: false }, true), 'complete_success')
assert.equal(automaticAccountProbeOutcome({ success: false, accountFailureEligible: false }, false), 'probe_task_failure')

const sideEffectsSource = readFileSync(fileURLToPath(new URL('../../modules/gateway/runtime/account-side-effects.service.ts', import.meta.url)), 'utf8')
assert.doesNotMatch(sideEffectsSource, /result\.success\s*\|\|\s*result\.accountFailureEligible\s*===\s*false/)
assert.match(sideEffectsSource, /onUpstreamAttempt:/)
assert.match(sideEffectsSource, /automaticAccountProbeOutcome\(result, upstreamAttemptObserved\)/)

console.log('AUTOMATIC_ACCOUNT_PROBE_OUTCOME_OK')
