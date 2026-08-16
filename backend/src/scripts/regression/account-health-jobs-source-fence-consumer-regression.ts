import assert from 'node:assert/strict'

import { gatewayProbeOutcome } from '../../modules/gateway/runtime/account-health-jobs-source-fence.consumer.js'

assert.equal(gatewayProbeOutcome('complete_success'), 'success')
assert.equal(gatewayProbeOutcome('upstream_failure'), 'health_failure')
assert.equal(gatewayProbeOutcome('framing_complete_neutral'), 'unknown')
assert.equal(gatewayProbeOutcome('probe_task_failure'), 'probe_task_failure')
assert.equal(gatewayProbeOutcome('stale'), 'stale')

console.log('account-health-jobs-source-fence-consumer-regression passed')
