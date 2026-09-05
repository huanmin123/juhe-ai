import assert from 'node:assert/strict'

import { gatewayProbeOutcome } from '../../modules/gateway/runtime/account-health-jobs-source-fence.consumer.js'
import { sourceFenceOutcomeCursor } from '../../modules/gateway/runtime/account-health-jobs-source-fence-runtime.service.js'

assert.equal(gatewayProbeOutcome('complete_success'), 'success')
assert.equal(gatewayProbeOutcome('upstream_failure'), 'health_failure')
assert.equal(gatewayProbeOutcome('framing_complete_neutral'), 'unknown')
assert.equal(gatewayProbeOutcome('probe_task_failure'), 'probe_task_failure')
assert.equal(gatewayProbeOutcome('stale'), 'stale')

assert.deepEqual(
  sourceFenceOutcomeCursor({ outcome_id: 'microsecond-row', observed_at: '2026-08-19T04:07:26.724455789Z', storage_observed_at: '2026-08-19T04:07:26.724455Z' }),
  { outcomeId: 'microsecond-row', observedAt: '2026-08-19T04:07:26.724455Z' },
  'source-fence cursor must use the durable storage ordering timestamp'
)
assert.deepEqual(
  sourceFenceOutcomeCursor({ outcome_id: 'legacy-row', observed_at: '2026-08-19T04:07:26.724455789Z' }),
  { outcomeId: 'legacy-row', observedAt: '2026-08-19T04:07:26.724455789Z' },
  'legacy rows without storage timestamp must remain readable'
)

console.log('account-health-jobs-source-fence-consumer-regression passed')
