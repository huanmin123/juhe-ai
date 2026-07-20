import { strict as assert } from 'node:assert'

import {
  LOG_EVENT_VERSION,
  createLogEventEnvelope,
  type LogEventEnvelope
} from '../../shared/logging/log-event-contract.js'
import {
  captureExpectedFailureContext,
  captureUnexpectedFailureContext
} from '../../shared/logging/log-failure-context.js'

const event: LogEventEnvelope = createLogEventEnvelope({
  level: 'info',
  service: 'gateway',
  role: 'node',
  event: 'gateway.request.stage',
  traceId: 'trace-contract-1',
  requestId: 'request-contract-1',
  stage: 'route.resolve',
  outcome: 'success',
  durationMs: 4,
  startedOffsetMs: 2,
  endedOffsetMs: 6,
  fields: { routeMode: 'weighted', groupCount: 2 }
})

assert.equal(event.version, LOG_EVENT_VERSION)
assert.equal(event.endedOffsetMs! - event.startedOffsetMs!, event.durationMs)
assert.equal(event.fields?.routeMode, 'weighted')
assert.throws(
  () => createLogEventEnvelope({
    level: 'info',
    service: 'gateway',
    role: 'node',
    event: 'gateway.request.stage',
    fields: { authorization: 'Bearer secret' }
  }),
  /敏感字段/
)

const rootCause = new Error('postgres connection reset')
const unexpected = new Error('route resolution failed', { cause: rootCause })
const failure = captureUnexpectedFailureContext(unexpected, {
  stageSnapshot: { current: 'route.resolve', completed: ['request.accept'] },
  queueSnapshot: { pending: 12, capacity: 100 },
  retryState: { attempt: 2, maxAttempts: 3 },
  decisionInputs: { routeMode: 'weighted', authorization: 'Bearer secret' }
})
assert.equal(failure.failureClass, 'unexpected')
assert.equal(failure.error?.cause?.message, rootCause.message)
assert.equal(failure.queueSnapshot?.pending, 12)
assert(failure.redactedFields.includes('decisionInputs.authorization'))
assert(!JSON.stringify(failure).includes('Bearer secret'))

const expected = captureExpectedFailureContext('quota_exceeded', {
  threshold: 100,
  current: 101,
  decision: 'reject',
  reason: 'daily_quota'
})
assert.equal(expected.failureClass, 'expected')
assert.equal(expected.decisionInputs.current, 101)

console.log('日志事件契约回归通过')
