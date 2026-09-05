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
  DB_SERVICE_SLOW_REQUEST_THRESHOLD_MS,
  GATEWAY_SLOW_STAGE_THRESHOLD_MS,
  dbServiceSuccessLogLevel,
  gatewayRequestStageLogLevel
} from '../../shared/logging/runtime-log-policy.js'
import {
  GATEWAY_REQUEST_STAGES,
  buildRequestStageLogFields,
  captureDownstreamResponseState,
  logRequestStage,
  normalizeHeaderId,
  parseTraceParent,
  recordRequestTimingLogDrops,
  resolveRequestSummaryOutcome,
  shouldWriteGatewayStageDetail,
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
assert.deepEqual(captureDownstreamResponseState({ headersSent: true, writableEnded: false, statusCode: 201 }, 'finish'), {
  downstreamEvent: 'finish',
  responseCommitted: true,
  statusCode: 201
})
assert.deepEqual(captureDownstreamResponseState({ headersSent: false, writableEnded: false, statusCode: 200 }, 'close'), {
  downstreamEvent: 'close',
  responseCommitted: false
})
assert.deepEqual(captureDownstreamResponseState({ headersSent: false, writableEnded: true, statusCode: 204 }, 'finish'), {
  downstreamEvent: 'finish',
  responseCommitted: true,
  statusCode: 204
})

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

assert.equal(dbServiceSuccessLogLevel(DB_SERVICE_SLOW_REQUEST_THRESHOLD_MS - 0.001), 'debug')
assert.equal(dbServiceSuccessLogLevel(DB_SERVICE_SLOW_REQUEST_THRESHOLD_MS), 'info')
assert.equal(gatewayRequestStageLogLevel('success', GATEWAY_SLOW_STAGE_THRESHOLD_MS - 0.001), 'debug')
assert.equal(gatewayRequestStageLogLevel('skipped', GATEWAY_SLOW_STAGE_THRESHOLD_MS), 'info')
assert.equal(gatewayRequestStageLogLevel('expected_failure', 0), 'warn')
assert.equal(gatewayRequestStageLogLevel('aborted', 0), 'warn')
assert.equal(gatewayRequestStageLogLevel('unexpected_failure', 0), 'error')
assert.equal(shouldWriteGatewayStageDetail(undefined, 'expected_failure', Number.MAX_SAFE_INTEGER), true)
assert.equal(shouldWriteGatewayStageDetail(undefined, 'unexpected_failure', Number.MAX_SAFE_INTEGER), true)

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

const debugStageEvents: Record<string, unknown>[] = []
const slowStageEvents: Record<string, unknown>[] = []
const warningStageEvents: Record<string, unknown>[] = []
const failureLaneEvents: Record<string, unknown>[] = []
const clockSafeLogger = {
  debug(fields: Record<string, unknown>) {
    debugStageEvents.push(fields)
  },
  info(fields: Record<string, unknown>) {
    slowStageEvents.push(fields)
  },
  warn(fields: Record<string, unknown>) {
    warningStageEvents.push(fields)
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
recordRequestTimingLogDrops(clockSafeContext, 10, 12)
recordRequestTimingLogDrops(clockSafeContext, 12, 15)
assert.equal(clockSafeContext.timingLogDroppedCount, 5, 'timing drop 必须按本请求每次写入增量累计')
withRequestContext(clockSafeContext, () => {
  // The guard must keep accidental Date.now() input from creating trillion-ms offsets.
  logRequestStage('request.accepted', {}, 'success', Date.now())
  logRequestStage('upstream.dispatch.failed', {
    error: new Error('unexpected dispatch failure'),
    queueSnapshot: { pending: 4 }
  }, 'unexpected_failure')
  logRequestStage(
    'upstream.fetch_headers',
    {},
    'success',
    performance.now() - GATEWAY_SLOW_STAGE_THRESHOLD_MS - 1
  )
  logRequestStage('route.group_access', { failureReason: 'no_route' }, 'expected_failure')
})
assert.equal(debugStageEvents.length, 1, '快速成功阶段只能写入 debug')
assert((debugStageEvents[0]?.startedOffsetMs as number) < 1_000)
assert((debugStageEvents[0]?.durationMs as number) < 1_000)
assert.equal(slowStageEvents.length, 1, '超过阈值的成功阶段必须写入 info')
assert.equal(slowStageEvents[0]?.stage, 'upstream.fetch_headers')
assert(Number(slowStageEvents[0]?.durationMs) >= GATEWAY_SLOW_STAGE_THRESHOLD_MS)
assert.equal(warningStageEvents.length, 1, '预期失败阶段必须写入 warn')
assert.equal(warningStageEvents[0]?.stage, 'route.group_access')
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
assert.equal(resolveRequestSummaryOutcome({
  ...clockSafeContext,
  terminalExpectedFailure: true,
  stageSummaries: [{ sequence: 1, stage: 'body.capture', outcome: 'expected_failure', durationMs: 1 }]
}, 503), 'expected_failure')
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
const stageInfoLines = requestContextRawLines.filter((line) => (
  line.includes('"event":"gateway.request.stage"') && line.includes('"probeIndex":')
))
assert.equal(stageInfoLines.length, 1, '70 个成功阶段在 info 级别下只能保留 1 个慢阶段')
const slowStageInfo = JSON.parse(stageInfoLines[0] ?? '{}') as Record<string, unknown>
assert.equal(slowStageInfo.probeIndex, 69)
assert(Number(slowStageInfo.durationMs) >= GATEWAY_SLOW_STAGE_THRESHOLD_MS)
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
const timingSummaryLine = requestContextRawLines.find((line) => line.includes('"event":"gateway.request.timing_summary"'))
const timingSummary = JSON.parse(timingSummaryLine ?? '{}') as Record<string, unknown>
for (const key of [
  'method',
  'path',
  'trafficSource',
  'requestLane',
  'model',
  'stream',
  'accountId',
  'groupId',
  'totalDurationMs',
  'preAuditDurationMs',
  'preUpstreamDurationMs',
  'upstreamHeadersDurationMs',
  'firstOutputDurationMs',
  'upstreamBodyDurationMs',
  'downstreamFinishDurationMs',
  'attemptCount',
  'stageCount',
  'timingLogDroppedCount',
  'timingLogQueuePeakCount',
  'timingLogQueuePeakBytes'
]) {
  assert.equal(key in timingSummary, true, `timing summary 缺少 ${key}: ${timingSummaryLine}`)
}
assert.equal(timingSummary.stageCount, 70, 'stageCount 必须保留超过摘要数组上限后的真实阶段总数')
assert.equal((timingSummary.stages as unknown[]).length, 64, 'summary 内嵌阶段数组必须保持有界')
assert.equal(timingSummary.droppedStageSummaries, 6, 'summary 必须明确内嵌阶段摘要丢弃数')
assert.equal(timingSummary.downstreamEvent, 'finish')
assert.equal(timingSummary.responseCommitted, true)
assert.equal(timingSummary.statusCode, 200)

const performanceRequestContextProbe = spawnSync(process.execPath, [
  '--import',
  'tsx',
  'src/scripts/regression/log-event-contract-raw-probe.ts'
], {
  cwd: process.cwd(),
  encoding: 'utf8',
  env: {
    ...process.env,
    NODE_ENV: 'test',
    JUHE_AI_RUNTIME_MODE: 'performance',
    JUHE_AI_PERFORMANCE_NODE_ROLE: 'gateway',
    JUHE_AI_ACCOUNT_HEALTH_CHECK_DISPATCH_URL: 'http://127.0.0.1:65535',
    JUHE_AI_DATABASE_DRIVER: 'postgres',
    JUHE_AI_CACHE_DRIVER: 'redis',
    JUHE_AI_RUNTIME_STATE_DRIVER: 'redis',
    JUHE_AI_QUEUE_DRIVER: 'redis_stream',
    JUHE_AI_POSTGRES_URL: 'postgres://test:test@127.0.0.1:5432/test',
    JUHE_AI_REDIS_CACHE_URL: 'redis://127.0.0.1:6379/0',
    JUHE_AI_REDIS_STATE_URL: 'redis://127.0.0.1:6380/0',
    JUHE_AI_REDIS_QUEUE_URL: 'redis://127.0.0.1:6381/0',
    JUHE_AI_GATEWAY_TIMING_DETAIL_SAMPLE_PERMILLE: '0',
    JUHE_AI_LOG_CONSOLE_ENABLED: 'true',
    JUHE_AI_LOG_FILE_ENABLED: 'false',
    JUHE_AI_LOG_LEVEL: 'info',
    JUHE_AI_PROCESS_ROLE: 'server'
  }
})
assert.equal(performanceRequestContextProbe.status, 0, performanceRequestContextProbe.stderr)
const performanceProbeLines = performanceRequestContextProbe.stdout
  .split(/\r?\n/)
  .filter((line) => line.startsWith('{'))
assert.equal(
  performanceProbeLines.some((line) => line.includes('"event":"gateway.request.stage"') && line.includes('"probeIndex":69')),
  false,
  '高性能模式未采样请求不得因事件循环延迟把正常阶段放大成 info 日志'
)
const performanceTimingLine = performanceProbeLines.find((line) => line.includes('"event":"gateway.request.timing_summary"'))
assert(performanceTimingLine, '高性能模式未采样请求仍必须保留 timing summary')
const performanceTiming = JSON.parse(performanceTimingLine) as Record<string, unknown>
assert.equal(performanceTiming.stageDetailsSampled, false)
assert.equal(performanceTiming.stageCount, 70)
assert.deepEqual(performanceTiming.stages, [])

const terminalCloseLines = requestContextRawLines
  .filter((line) => line.includes('"traceId":"trace-terminal-close-probe"'))
assert.equal(
  terminalCloseLines.some((line) => line.includes('"event":"http_request_closed"')),
  false,
  '协议成功终止后的 close 不得误报为请求中断'
)
const terminalTimingLine = terminalCloseLines.find((line) => line.includes('"event":"gateway.request.timing_summary"'))
assert(terminalTimingLine, '协议成功终止后的 close 仍必须产出耗时汇总')
const terminalTiming = JSON.parse(terminalTimingLine) as Record<string, unknown>
assert.equal(terminalTiming.outcome, 'success')
assert.equal(terminalTiming.downstreamEvent, 'close')
assert.equal(terminalTiming.responseCommitted, false)
assert.equal('statusCode' in terminalTiming, false, '未提交的协议终态 close 不得记录默认 HTTP 200')

const abortedCloseLines = requestContextRawLines
  .filter((line) => line.includes('"traceId":"trace-aborted-close-probe"'))
assert(
  abortedCloseLines.some((line) => line.includes('"event":"http_request_closed"')),
  '未收到协议终态的 close 必须保留请求中断告警'
)
const abortedTimingLine = abortedCloseLines.find((line) => line.includes('"event":"gateway.request.timing_summary"'))
assert(abortedTimingLine, '真正中断仍必须产出耗时汇总')
const abortedTiming = JSON.parse(abortedTimingLine) as Record<string, unknown>
assert.equal(abortedTiming.outcome, 'aborted')
assert.equal(abortedTiming.downstreamEvent, 'close')
assert.equal(abortedTiming.responseCommitted, false)
assert.equal('statusCode' in abortedTiming, false, '未提交的常规 close 不得记录默认 HTTP 200')
const abortedCloseLine = abortedCloseLines.find((line) => line.includes('"event":"http_request_closed"'))
assert(abortedCloseLine, '真正中断必须保留 downstream close 日志')
const abortedClose = JSON.parse(abortedCloseLine) as Record<string, unknown>
assert.equal(abortedClose.downstreamClose, true)
assert.equal('closeTrigger' in abortedClose, false)
assert.equal('clientActionConfirmed' in abortedClose, false)
assert.equal(abortedClose.downstreamEvent, 'close')
assert.equal(abortedClose.responseCommitted, false)
assert.equal('statusCode' in abortedClose, false, '未提交的 close 日志不得记录默认 HTTP 200')

const failedTerminalLines = requestContextRawLines
  .filter((line) => line.includes('"traceId":"trace-failed-terminal-close-probe"'))
assert.equal(
  failedTerminalLines.some((line) => line.includes('"event":"http_request_closed"')),
  false,
  '协议失败终态后的 close 也不得伪装为客户端中断'
)
const failedTerminalTimingLine = failedTerminalLines.find((line) => line.includes('"event":"gateway.request.timing_summary"'))
assert(failedTerminalTimingLine, '协议失败终态后的 close 必须产出失败耗时汇总')
const failedTerminalTiming = JSON.parse(failedTerminalTimingLine) as Record<string, unknown>
assert.equal(failedTerminalTiming.outcome, 'expected_failure')
assert.equal(failedTerminalTiming.downstreamEvent, 'close')
assert.equal(failedTerminalTiming.responseCommitted, false)
assert.equal('statusCode' in failedTerminalTiming, false, '未提交的失败协议终态不得记录默认 HTTP 200')

console.log('日志事件契约回归通过')
