import { strict as assert } from 'node:assert'
import { spawnSync } from 'node:child_process'
import type { Logger } from 'pino'

import {
  LOG_EVENT_VERSION,
  createLogEventEnvelope,
  type LogEventEnvelope
} from '../../shared/logging/log-event-contract.js'
import {
  captureExpectedFailureContext,
  captureUnexpectedFailureContext
} from '../../shared/logging/log-failure-context.js'
import {
  GATEWAY_REQUEST_STAGES,
  buildRequestStageLogFields,
  logRequestStage,
  normalizeHeaderId,
  parseTraceParent,
  resolveRequestSummaryOutcome,
  withRequestContext,
  type RequestContext
} from '../../shared/request-context.js'

assert.equal(normalizeHeaderId('trace-safe_1:worker.2'), 'trace-safe_1:worker.2')
assert.equal(normalizeHeaderId('trace with spaces'), undefined)
assert.equal(normalizeHeaderId('x'.repeat(129)), undefined)
assert.equal(
  parseTraceParent('00-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-01'),
  '4bf92f3577b34da6a3ce929d0e0e4736'
)
assert.equal(parseTraceParent('00-00000000000000000000000000000000-00f067aa0ba902b7-01'), undefined)
assert.equal(parseTraceParent('ff-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-01'), undefined)

assert.equal(new Set(GATEWAY_REQUEST_STAGES).size, GATEWAY_REQUEST_STAGES.length)
for (const stage of [
  'body.receive',
  'body.capture',
  'model.capability_filter',
  'capacity.client_ip_concurrency',
  'account.concurrency_acquire',
  'upstream.request_prepare',
  'upstream.fetch_headers',
  'upstream.first_output',
  'audit.finalize'
] as const) {
  assert(GATEWAY_REQUEST_STAGES.includes(stage), `阶段契约缺少 ${stage}`)
}

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
const rawFieldEvent = createLogEventEnvelope({
  level: 'info',
  service: 'gateway',
  role: 'node',
  event: 'gateway.request.stage',
  fields: { authorization: 'Bearer secret' }
})
assert.equal(rawFieldEvent.fields?.authorization, 'Bearer secret')

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
assert.deepEqual(failure.redactedFields, [])
assert.equal(failure.decisionInputs?.authorization, 'Bearer secret')
assert(JSON.stringify(failure).includes('Bearer secret'))

const wideFailure = captureUnexpectedFailureContext(new Error('wide'), {
  decisionInputs: { candidates: Array.from({ length: 101 }, (_, index) => index) }
})
assert.equal(wideFailure.truncationReason, 'field_or_event_limit')

let hostileGetterReads = 0
const hostileDecisionInputs: Record<string, unknown> = {}
Object.defineProperty(hostileDecisionInputs, 'mustNotRead', {
  enumerable: true,
  get() {
    hostileGetterReads += 1
    throw new Error('failure context must not invoke getters')
  }
})
hostileDecisionInputs.payload = 'x'.repeat(1024 * 1024)
hostileDecisionInputs.self = hostileDecisionInputs
for (let index = 0; index < 200; index += 1) {
  Object.defineProperty(hostileDecisionInputs, `getter${index}`, {
    enumerable: true,
    get() {
      hostileGetterReads += 1
      return index
    }
  })
}
const hostileFailure = captureUnexpectedFailureContext(new Error('hostile failure scene'), {
  decisionInputs: hostileDecisionInputs
})
assert.equal(hostileGetterReads, 0)
assert.equal(hostileFailure.decisionInputs?.mustNotRead, '[unreadable: accessor]')
assert.equal(hostileFailure.decisionInputs?.self, '[truncated: circular reference]')
assert.equal(hostileFailure.truncationReason, 'field_or_event_limit')
assert((hostileFailure.decisionInputs?.payload as string).length < 16 * 1024)

const expected = captureExpectedFailureContext('quota_exceeded', {
  threshold: 100,
  current: 101,
  decision: 'reject',
  reason: 'daily_quota'
})
assert.equal(expected.failureClass, 'expected')
assert.equal(expected.decisionInputs.current, 101)

const reserved = buildRequestStageLogFields(undefined, 'model.capability_filter', {
  traceId: 'trace-stage-1',
  event: 'overridden',
  stage: 'overridden',
  outcome: 'accounts'
}, 'success', 100, 110)
assert.equal(reserved.event, 'gateway.request.stage')
assert.equal(reserved.stage, 'model.capability_filter')
assert.equal(reserved.outcome, 'success')
assert.equal(reserved.traceId, 'trace-stage-1')
assert.equal(reserved.durationMs, 10)

const unexpectedStage = buildRequestStageLogFields(undefined, 'upstream.dispatch.failed', {
  traceId: 'trace-stage-2',
  error: new Error('upstream failed'),
  candidateAccountCount: 3,
  retryState: { attempt: 2 }
}, 'unexpected_failure', 200, 215)
assert.equal(unexpectedStage.failureClass, 'unexpected')
assert.equal((unexpectedStage.error as { message?: string }).message, 'upstream failed')
assert.equal((unexpectedStage.retryState as { attempt?: number }).attempt, 2)
assert.equal('error' in unexpectedStage, true)

const clockSafeStages: Record<string, unknown>[] = []
const failureLaneEvents: Record<string, unknown>[] = []
const clockSafeLogger = {
  info(fields: Record<string, unknown>) {
    clockSafeStages.push(fields)
  },
  error(fields: Record<string, unknown>) {
    failureLaneEvents.push(fields)
  }
} as unknown as Logger
const clockSafeContext: RequestContext = {
  traceId: 'trace-clock-1',
  startedAt: Date.now(),
  monotonicStartedAt: performance.now(),
  method: 'POST',
  path: '/v1/chat/completions',
  originalUrl: '/v1/chat/completions',
  logger: clockSafeLogger
}
withRequestContext(clockSafeContext, () => {
  // The guard must keep accidental Date.now() input from creating trillion-ms offsets.
  logRequestStage('request.accepted', {}, 'success', Date.now())
  logRequestStage('upstream.dispatch.failed', {
    error: new Error('unexpected dispatch failure'),
    queueSnapshot: { pending: 4 }
  }, 'unexpected_failure')
})
assert((clockSafeStages[0]?.startedOffsetMs as number) < 1_000)
assert((clockSafeStages[0]?.durationMs as number) < 1_000)
assert.equal(failureLaneEvents.length, 1)
assert.equal(failureLaneEvents[0]?.event, 'gateway.request.failure')
assert.equal(failureLaneEvents[0]?.failureClass, 'unexpected')
const failureStageSnapshot = failureLaneEvents[0]?.stageSnapshot as {
  currentStage?: unknown
  completedStages?: unknown[]
} | undefined
assert.equal(failureStageSnapshot?.currentStage, 'upstream.dispatch.failed')
assert.equal(failureStageSnapshot?.completedStages?.length, 1)
assert.equal((failureStageSnapshot?.completedStages?.[0] as { stage?: unknown })?.stage, 'request.accepted')
assert.equal(resolveRequestSummaryOutcome(clockSafeContext, 200), 'unexpected_failure')
assert.equal(resolveRequestSummaryOutcome({ ...clockSafeContext, stageSummaries: [] }, 503), 'unexpected_failure')
assert.equal(resolveRequestSummaryOutcome({
  ...clockSafeContext,
  stageSummaries: [{ sequence: 1, stage: 'upstream.dispatch.failed', outcome: 'expected_failure', durationMs: 1 }]
}, 503), 'unexpected_failure')
assert.equal(resolveRequestSummaryOutcome({ ...clockSafeContext, stageSummaries: [] }, 200), 'success')

const rawLoggerProbe = spawnSync(process.execPath, [
  '--import',
  'tsx',
  '--input-type=module',
  '-e',
  [
    "const { logger, closeLogger } = await import('./src/shared/logger.ts')",
    "logger.child({ traceId: 'trace-raw-json-1', requestId: 'request-raw-json-1' }).info({ version: 1, service: 'juhe-ai', role: 'server', event: 'logger_raw_json_key_probe' }, 'probe')",
    'await closeLogger()'
  ].join(';')
], {
  cwd: process.cwd(),
  encoding: 'utf8',
  env: {
    ...process.env,
    NODE_ENV: 'test',
    JUHE_AI_LOG_CONSOLE_ENABLED: 'true',
    JUHE_AI_LOG_FILE_ENABLED: 'false',
    JUHE_AI_LOG_LEVEL: 'info',
    JUHE_AI_PROCESS_ROLE: 'server'
  }
})
assert.equal(rawLoggerProbe.status, 0, rawLoggerProbe.stderr)
const rawProbeLine = rawLoggerProbe.stdout
  .split(/\r?\n/)
  .find((line) => line.includes('"event":"logger_raw_json_key_probe"'))
assert(rawProbeLine, `未找到真实 logger JSON 行：${rawLoggerProbe.stdout}`)
for (const key of ['version', 'service', 'role', 'traceId', 'requestId']) {
  const occurrenceCount: number = rawProbeLine.match(new RegExp(`"${key}":`, 'g'))?.length ?? 0
  assert.equal(occurrenceCount, 1, `真实 logger JSON 行中的 ${key} 出现 ${occurrenceCount} 次：${rawProbeLine}`)
}

const requestContextRawProbe = spawnSync(process.execPath, [
  '--import',
  'tsx',
  'src/scripts/regression/log-event-contract-raw-probe.ts'
], {
  cwd: process.cwd(),
  encoding: 'utf8',
  env: {
    ...process.env,
    NODE_ENV: 'test',
    JUHE_AI_LOG_CONSOLE_ENABLED: 'true',
    JUHE_AI_LOG_FILE_ENABLED: 'false',
    JUHE_AI_LOG_LEVEL: 'info',
    JUHE_AI_PROCESS_ROLE: 'server'
  }
})
assert.equal(requestContextRawProbe.status, 0, requestContextRawProbe.stderr)
const requestContextRawLines = requestContextRawProbe.stdout
  .split(/\r?\n/)
  .filter((line) => line.startsWith('{'))
const expectedContextEvents = [
  'gateway.request.stage',
  'gateway.request.timing_summary',
  'http_request_completed',
  'request_logger_context_probe'
]
for (const eventName of expectedContextEvents) {
  const rawLine = requestContextRawLines.find((line) => line.includes(`"event":"${eventName}"`))
  assert(rawLine, `未找到请求上下文真实 JSON 行 ${eventName}：${requestContextRawProbe.stdout}`)
  for (const key of ['role', 'traceId', 'requestId', 'systemAccountId', 'systemAccountRole', 'apiKeyId', 'groupId', 'trafficSource']) {
    const occurrenceCount: number = rawLine.match(new RegExp(`"${key}":`, 'g'))?.length ?? 0
    assert.equal(occurrenceCount, 1, `${eventName} 的 ${key} 出现 ${occurrenceCount} 次：${rawLine}`)
  }
}

console.log('日志事件契约回归通过')
