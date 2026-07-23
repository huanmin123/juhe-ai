import assert from 'node:assert/strict'

import { codexResponsesContractRevision } from '../../modules/gateway/codex-responses/contract-registry.js'
import {
  codexResponsesGuardDiagnosticLimit,
  createCodexResponsesGuardMarker,
  createCodexResponsesResponseGuard,
  isCodexResponsesGuardMarker
} from '../../modules/gateway/codex-responses/response-guard.js'
import { GatewayDownstreamCommitState } from '../../modules/gateway/response/downstream-commit-state.js'
import { OpenAIStreamInspector } from '../../modules/gateway/protocols/openai-v1/stream-inspection.js'
import { inspectCodexResponsesJsonAtMarkedBoundary } from '../../modules/gateway/response/non-stream-json-inspection.js'
import { pipeUpstreamStream } from '../../modules/gateway/response/stream.js'
import { transformCodexResponsesChatBridgeUpstreamResponse } from '../../modules/providers/drivers/_shared/codex-responses-chat-bridge.js'

type JsonRecord = Record<string, unknown>

const nativeCommitState = new GatewayDownstreamCommitState()
const nativeGuard = createCodexResponsesResponseGuard({
  marker: createCodexResponsesGuardMarker('raw_upstream'),
  downstreamCommitState: nativeCommitState
})
const nativeBadResponse: JsonRecord = {
  id: 'resp_native_bad',
  object: 'response',
  output: [{ id: 'ctc_missing_fields', type: 'custom_tool_call' }]
}
const nativeResult = nativeGuard.inspectJson(nativeBadResponse)
assert.equal(nativeResult.provenance, 'raw_upstream')
assert.equal(nativeResult.checkpoint, 'raw_upstream')
assert.equal(nativeResult.outcome, 'blocked')
assert.equal(nativeResult.retryable, true, 'semantic commit 前的确定性违规应可交给既有重试决策')
assert.equal(nativeResult.value, nativeBadResponse, 'shadow 默认不得复制或修改响应')
assert.deepEqual(nativeBadResponse.output, [{ id: 'ctc_missing_fields', type: 'custom_tool_call' }])
assert.equal(nativeCommitState.transportCommitted, false, 'guard 不得自行提交 transport')
assert.equal(nativeCommitState.semanticCommitted, false, 'guard 不得自行提交 semantic')

const bridgeCommitState = new GatewayDownstreamCommitState()
const bridgeGuard = createCodexResponsesResponseGuard({
  marker: createCodexResponsesGuardMarker('gateway_bridge'),
  downstreamCommitState: bridgeCommitState
})
const bridgeBadResult = bridgeGuard.inspectJson({
  id: 'resp_bridge_bad',
  object: 'response',
  output: [{ id: 'fc_wrong_custom', type: 'custom_tool_call', call_id: 'call_bridge', name: 'apply_patch', input: '' }]
})
assert.equal(bridgeBadResult.provenance, 'gateway_bridge')
assert.equal(bridgeBadResult.outcome, 'repairable')
assert.equal(bridgeBadResult.retryable, false, 'R0 候选不是 blocked retry 信号')

const marker = createCodexResponsesGuardMarker('gateway_bridge')
assert.equal(isCodexResponsesGuardMarker(marker), true)
assert.equal(isCodexResponsesGuardMarker({ ...marker, checkpoint: 'raw_upstream' }), true)
assert.equal(isCodexResponsesGuardMarker({ ...marker, checkpoint: 'request_history' }), false)
assert.equal(isCodexResponsesGuardMarker({ checkpoint: 'raw_upstream' }), false)

const unknownResult = nativeGuard.inspectJson({
  id: 'resp_future',
  object: 'response',
  output: [{ id: 'future_1', type: 'future_response_item', payload: 'opaque' }]
})
assert.equal(unknownResult.outcome, 'observed_unknown')
assert.equal(unknownResult.retryable, false, '未知新类型只能告警透传，不能触发换号')

const chatEnvelope = nativeGuard.inspectJson({
  id: 'chatcmpl_wrong_protocol',
  object: 'chat.completion',
  choices: [{ message: { role: 'assistant', content: 'wrong protocol' } }]
})
assert.equal(chatEnvelope.outcome, 'blocked', '声明 native Responses 却返回 Chat 包装必须在检查点 A 被识别')
assert.equal(chatEnvelope.issues[0]?.code, 'response_envelope_invalid')
assert.equal(chatEnvelope.retryable, true)

const compactGuard = createCodexResponsesResponseGuard({
  marker: createCodexResponsesGuardMarker('raw_upstream'),
  downstreamCommitState: new GatewayDownstreamCommitState(),
  envelopeKind: 'compact'
})
assert.equal(compactGuard.inspectJson({
  id: 'resp_compact',
  object: 'response.compaction',
  output: [{ id: 'cmp_compact', type: 'compaction', encrypted_content: 'opaque' }]
}).outcome, 'clean', '合法 /responses/compact envelope 不得被 Responses 主响应规则误拦')
assert.equal(compactGuard.inspectJson({
  output: [{ id: 'cmp_native', type: 'compaction', encrypted_content: 'opaque' }]
}).outcome, 'clean', 'Codex 原生 CompactHistoryResponse 允许只有 output')
compactGuard.dispose()

const transportOnlyCommit = new GatewayDownstreamCommitState()
transportOnlyCommit.markTransportCommitted(5)
const transportOnlyResult = createCodexResponsesResponseGuard({
  marker: createCodexResponsesGuardMarker('raw_upstream'),
  downstreamCommitState: transportOnlyCommit
}).inspectJson(nativeBadResponse)
assert.equal(transportOnlyResult.outcome, 'blocked')
assert.equal(transportOnlyResult.retryable, true, '仅写出 SSE comment/heartbeat 后仍允许透明重试')

nativeCommitState.markSemanticCommitted(12)
const postCommitBadResult = nativeGuard.inspectJson(nativeBadResponse)
assert.equal(postCommitBadResult.outcome, 'late_violation')
assert.equal(postCommitBadResult.retryable, false)
assert.equal(postCommitBadResult.commit.semanticCommitted, true)
assert.equal(nativeCommitState.downstreamBytesWritten, 12, 'guard 必须读取而不是复制或重算 commit truth')

const safeRepairInput: JsonRecord = {
  id: 'resp_safe_repair',
  object: 'response',
  output: [{ id: 'fc_wrong_custom', type: 'custom_tool_call', call_id: 'call_safe', name: 'apply_patch', input: '' }]
}
const safeRepairGuard = createCodexResponsesResponseGuard({
  marker: createCodexResponsesGuardMarker('raw_upstream'),
  downstreamCommitState: new GatewayDownstreamCommitState(),
  mode: 'safe_repair',
  createItemId: ({ prefix, sequence }) => `${prefix}_guard_${sequence}`
})
const safeRepairResult = safeRepairGuard.inspectJson(safeRepairInput)
assert.equal(safeRepairResult.outcome, 'repaired_safe')
assert.equal(((safeRepairResult.value.output as JsonRecord[])[0] as JsonRecord).id, 'ctc_guard_1')
assert.equal(((safeRepairInput.output as JsonRecord[])[0] as JsonRecord).id, 'fc_wrong_custom')
assert.deepEqual(safeRepairResult.repairRuleIds, ['codex.r0.response.replace_item_id'])

const strictGuard = createCodexResponsesResponseGuard({
  marker: createCodexResponsesGuardMarker('raw_upstream'),
  downstreamCommitState: new GatewayDownstreamCommitState(),
  mode: 'strict_intercept'
})
const strictResult = strictGuard.inspectJson(safeRepairInput)
assert.equal(strictResult.outcome, 'repairable', '严格模式仍要诊断 R0，但不得执行修复')
assert.equal(strictResult.value, safeRepairInput)
assert.deepEqual(strictResult.repairRuleIds, [])
strictGuard.dispose()

const streamCommitState = new GatewayDownstreamCommitState()
const streamGuard = createCodexResponsesResponseGuard({
  marker: createCodexResponsesGuardMarker('gateway_bridge'),
  downstreamCommitState: streamCommitState
})
const streamBlocked = streamGuard.inspectParsedSse({
  responseResourceId: 'resp_stream_guard',
  event: {
    type: 'response.output_item.added',
    output_index: 0,
    item: { id: 'ctc_stream_missing_fields', type: 'custom_tool_call' }
  }
})
assert.equal(streamBlocked.provenance, 'gateway_bridge')
assert.equal(streamBlocked.outcome, 'blocked')
assert.equal(streamBlocked.retryable, true)
streamCommitState.markSemanticCommitted(8)
const streamLate = streamGuard.inspectParsedSse({
  responseResourceId: 'resp_stream_guard',
  event: {
    type: 'response.output_item.done',
    output_index: 0,
    item: { id: 'ctc_stream_missing_fields', type: 'custom_tool_call' }
  }
})
assert.equal(streamLate.outcome, 'late_violation')
assert.equal(streamLate.retryable, false)

const boundedGuard = createCodexResponsesResponseGuard({
  marker: createCodexResponsesGuardMarker('raw_upstream'),
  downstreamCommitState: new GatewayDownstreamCommitState()
})
const bodyMarker = 'response-body-must-not-be-retained-'.repeat(512)
for (let index = 0; index < codexResponsesGuardDiagnosticLimit + 7; index += 1) {
  boundedGuard.inspectJson({
    id: `resp_bounded_${index}`,
    object: 'response',
    output: [{ id: `ctc_missing_${index}`, type: 'custom_tool_call', input: bodyMarker }]
  })
}
const boundedSnapshot = boundedGuard.snapshot()
assert.equal(boundedSnapshot.revision, codexResponsesContractRevision)
assert.equal(boundedSnapshot.diagnostics.length, codexResponsesGuardDiagnosticLimit)
assert.ok(boundedSnapshot.omittedDiagnosticCount > 0)
assert.equal(boundedSnapshot.outcome, 'blocked')
assert.equal(boundedSnapshot.retryable, true)
assert.equal(JSON.stringify(boundedSnapshot).includes(bodyMarker), false, 'guard snapshot 不得保留响应正文')
boundedGuard.dispose()
assert.equal(boundedGuard.snapshot().stream.identityCount, 0)
assert.equal(boundedGuard.snapshot().outcome, 'clean')

const auditMetadata: Array<{ label: string; metadata: Record<string, unknown> }> = []
const markedBoundaryResult = inspectCodexResponsesJsonAtMarkedBoundary({
  req: { originalUrl: '/v1/responses', path: '/v1/responses' } as never,
  upstreamResponse: {
    status: 200,
    ok: true,
    headers: new Headers({ 'content-type': 'application/json' }),
    body: null,
    codexResponsesGuardMarker: createCodexResponsesGuardMarker('raw_upstream')
  },
  auditCapture: {
    addGatewayMetadata: (entry: { label: string; metadata: Record<string, unknown> }) => auditMetadata.push(entry)
  } as never,
  clientStrategy: { clientProfile: 'codex' } as never,
  downstreamCommitState: new GatewayDownstreamCommitState()
}, nativeBadResponse)
assert.equal(markedBoundaryResult?.provenance, 'raw_upstream')
assert.equal(auditMetadata.length, 1)
assert.equal(auditMetadata[0]?.label, 'codex_responses_protocol_guard')
assert.equal(auditMetadata[0]?.metadata.codexResponsesGuardProvenance, 'raw_upstream')
assert.equal('message' in auditMetadata[0]!.metadata, false, '审计 metadata 不得写入协议消息或正文')

const genericBoundaryResult = inspectCodexResponsesJsonAtMarkedBoundary({
  req: { originalUrl: '/v1/responses', path: '/v1/responses' } as never,
  upstreamResponse: {
    status: 200,
    ok: true,
    headers: new Headers(),
    body: null,
    codexResponsesGuardMarker: createCodexResponsesGuardMarker('raw_upstream')
  },
  auditCapture: { addGatewayMetadata: () => assert.fail('普通 OpenAI 客户端不得启用 Codex guard') } as never,
  clientStrategy: { clientProfile: 'generic_openai' } as never,
  downstreamCommitState: new GatewayDownstreamCommitState()
}, nativeBadResponse)
assert.equal(genericBoundaryResult, undefined)

const safeBoundaryResult = inspectCodexResponsesJsonAtMarkedBoundary({
  req: { method: 'POST', originalUrl: '/v1/responses', path: '/v1/responses' } as never,
  upstreamResponse: {
    status: 200,
    ok: true,
    headers: new Headers(),
    body: null,
    codexResponsesGuardMarker: createCodexResponsesGuardMarker('raw_upstream')
  },
  auditCapture: { addGatewayMetadata: () => {} } as never,
  clientStrategy: { clientProfile: 'codex' } as never,
  downstreamCommitState: new GatewayDownstreamCommitState(),
  guardMode: 'safe_repair'
}, safeRepairInput)
assert.equal(safeBoundaryResult?.outcome, 'repaired_safe')
assert.match(String(((safeBoundaryResult?.value.output as JsonRecord[])[0] as JsonRecord).id), /^ctc_/)

const offBoundaryResult = inspectCodexResponsesJsonAtMarkedBoundary({
  req: { method: 'POST', originalUrl: '/v1/responses', path: '/v1/responses' } as never,
  upstreamResponse: {
    status: 200,
    ok: true,
    headers: new Headers(),
    body: null,
    codexResponsesGuardMarker: createCodexResponsesGuardMarker('raw_upstream')
  },
  auditCapture: { addGatewayMetadata: () => assert.fail('off 模式不得构造或记录 guard') } as never,
  clientStrategy: { clientProfile: 'codex' } as never,
  downstreamCommitState: new GatewayDownstreamCommitState(),
  guardMode: 'off'
}, nativeBadResponse)
assert.equal(offBoundaryResult, undefined)

const compactBoundaryResult = inspectCodexResponsesJsonAtMarkedBoundary({
  req: { method: 'POST', originalUrl: '/v1/responses/compact', path: '/v1/responses/compact' } as never,
  upstreamResponse: {
    status: 200,
    ok: true,
    headers: new Headers(),
    body: null,
    codexResponsesGuardMarker: createCodexResponsesGuardMarker('raw_upstream')
  },
  auditCapture: { addGatewayMetadata: () => assert.fail('合法 compact 不应产生 guard 审计异常') } as never,
  clientStrategy: { clientProfile: 'codex' } as never,
  downstreamCommitState: new GatewayDownstreamCommitState()
}, {
  id: 'resp_compact_boundary',
  object: 'response.compaction',
  output: [{ id: 'cmp_boundary', type: 'compaction', encrypted_content: 'opaque' }]
})
assert.equal(compactBoundaryResult?.outcome, 'clean')

async function* emptyUpstreamBody(): AsyncIterable<Uint8Array> {}
const unmarkedUpstreamResponse = {
  status: 200,
  ok: true,
  headers: new Headers({ 'content-type': 'text/event-stream' }),
  body: emptyUpstreamBody()
}
const markedBridgeResponse = transformCodexResponsesChatBridgeUpstreamResponse(
  {
    method: 'POST',
    originalUrl: '/v1/responses',
    path: '/v1/responses',
    body: { model: 'gpt-test', stream: false }
  } as never,
  unmarkedUpstreamResponse,
  { enabled: true, explicitMappingBridge: true, defaultModel: 'gpt-test' }
)
assert.equal(markedBridgeResponse.codexResponsesGuardMarker?.checkpoint, 'gateway_bridge')
assert.equal('codexResponsesGuardMarker' in unmarkedUpstreamResponse, false, 'bridge marker 只能附在转换结果，不能污染 raw response')

const parsedSseGuard = createCodexResponsesResponseGuard({
  marker: createCodexResponsesGuardMarker('gateway_bridge'),
  downstreamCommitState: new GatewayDownstreamCommitState()
})
const parsedEventTypes: string[] = []
const parsedInspector = new OpenAIStreamInspector()
parsedInspector.setParsedEventObserver((event) => {
  parsedEventTypes.push(event.eventType)
  parsedSseGuard.inspectOpenAiSseEvent(event)
})
parsedInspector.pushText([
  'event: response.created',
  'data: {"type":"response.created","response":{"id":"resp_parser","object":"response","output":[]}}',
  '',
  'event: response.output_item.added',
  'data: {"type":"response.output_item.added","output_index":0,"item":{"id":"ctc_parser","type":"custom_tool_call"}}',
  '',
  ''
].join('\n'))
assert.deepEqual(parsedEventTypes, ['response.created', 'response.output_item.added'])
assert.equal(parsedSseGuard.snapshot().outcome, 'blocked')
assert.equal(parsedSseGuard.snapshot().stream.identityCount, 0, 'R2 item 不得污染流 identity state')
parsedSseGuard.dispose()

const coverageGuard = createCodexResponsesResponseGuard({
  marker: createCodexResponsesGuardMarker('raw_upstream'),
  downstreamCommitState: new GatewayDownstreamCommitState()
})
const coverageInspector = new OpenAIStreamInspector()
coverageInspector.setParserCoverageObserver(() => coverageGuard.observeCoverageGap())
coverageInspector.pushText(`${'x'.repeat(2 * 1024 * 1024)}\n`)
assert.equal(coverageGuard.snapshot().outcome, 'observed_unknown', '解析覆盖降级不得假报 clean 或归因成确定性上游违规')
assert.equal(coverageGuard.snapshot().diagnostics[0]?.code, 'protocol_guard_coverage_degraded')
assert.equal(coverageGuard.snapshot().diagnostics[0]?.provenance, 'unknown')
coverageGuard.dispose()

const originalJsonParse = JSON.parse
let defaultFastPathParseCount = 0
JSON.parse = ((...args: Parameters<typeof JSON.parse>) => {
  defaultFastPathParseCount += 1
  return originalJsonParse(...args)
}) as typeof JSON.parse
try {
  new OpenAIStreamInspector().pushText([
    'event: response.output_text.delta',
    'data: {"type":"response.output_text.delta","delta":"x"}',
    '',
    ''
  ].join('\n'))
} finally {
  JSON.parse = originalJsonParse
}
assert.equal(defaultFastPathParseCount, 0, '未启用 guard 时必须保留常见 text delta 的零 JSON.parse 快路径')

const eofCommitState = new GatewayDownstreamCommitState()
const eofGuard = createCodexResponsesResponseGuard({
  marker: createCodexResponsesGuardMarker('raw_upstream'),
  downstreamCommitState: eofCommitState
})
const eofResponse = {
  headersSent: false,
  writableEnded: false,
  destroyed: false,
  writableLength: 0,
  writableHighWaterMark: 16 * 1024,
  once() { return this },
  off() { return this },
  hasHeader() { return false },
  setHeader() { return this },
  status() { return this },
  write() {
    this.headersSent = true
    return true
  },
  end() {
    this.writableEnded = true
    return this
  }
}
async function* eofUpstream(): AsyncIterable<Uint8Array> {
  yield Buffer.from([
    'event: response.created',
    'data: {"type":"response.created","response":{"id":"resp_eof","object":"response","output":[]}}',
    '',
    'event: response.output_item.added',
    'data: {"type":"response.output_item.added","output_index":0,"item":{"id":"ctc_eof","type":"custom_tool_call"}}',
    '',
    ''
  ].join('\n'), 'utf8')
}
const eofResult = await pipeUpstreamStream(
  eofUpstream(),
  eofResponse as never,
  {
    firstResponseTimeoutMs: 5_000,
    firstByteTimeoutMs: 5_000,
    idleTimeoutMs: 5_000,
    uncommittedAttemptMaxLifetimeMs: 30_000,
    noAvailableAccountWaitMs: 5_000
  },
  Date.now(),
  async () => {},
  undefined,
  {
    responseProtocol: 'openai_v1',
    endpointFamily: 'responses',
    interpretProtocolFailures: false,
    downstreamCommitState: eofCommitState,
    codexResponsesGuard: eofGuard
  }
)
assert.equal(eofResult.completed, true)
assert.equal(eofResult.codexResponsesGuard?.outcome, 'blocked', '正常 EOF 收口不得在 snapshot 前清空 guard 累计状态')
assert.ok((eofResult.codexResponsesGuard?.diagnostics.length ?? 0) > 0)
assert.equal(eofGuard.snapshot().outcome, 'clean', '结果快照后必须释放 guard 状态')

const safeStreamCommitState = new GatewayDownstreamCommitState()
const safeStreamGuard = createCodexResponsesResponseGuard({
  marker: createCodexResponsesGuardMarker('raw_upstream'),
  downstreamCommitState: safeStreamCommitState,
  mode: 'safe_repair',
  createItemId: ({ prefix }) => `${prefix}_stream_repaired`
})
const safeStreamChunks: Buffer[] = []
const safeStreamResponse = {
  headersSent: false,
  writableEnded: false,
  destroyed: false,
  writableLength: 0,
  writableHighWaterMark: 16 * 1024,
  once() { return this },
  off() { return this },
  hasHeader() { return false },
  setHeader() { return this },
  status() { return this },
  write(chunk: Buffer) {
    this.headersSent = true
    safeStreamChunks.push(Buffer.from(chunk))
    return true
  },
  end() {
    this.writableEnded = true
    return this
  }
}
async function* safeStreamUpstream(): AsyncIterable<Uint8Array> {
  const badItem = { id: 'fc_wrong_stream', type: 'custom_tool_call', call_id: 'call_safe', name: 'apply_patch', input: '' }
  const events = [
    { type: 'response.created', response: { id: 'resp_safe_stream', object: 'response', output: [] } },
    { type: 'response.output_item.added', output_index: 0, item: badItem },
    { type: 'response.output_item.done', output_index: 0, item: { ...badItem, input: '{}' } },
    { type: 'response.completed', response: { id: 'resp_safe_stream', object: 'response', output: [{ ...badItem, input: '{}' }] } }
  ]
  yield Buffer.from(events.map((event) => `event: ${event.type}\ndata: ${JSON.stringify(event)}\n\n`).join(''), 'utf8')
}
const safeStreamResult = await pipeUpstreamStream(
  safeStreamUpstream(),
  safeStreamResponse as never,
  {
    firstResponseTimeoutMs: 5_000,
    firstByteTimeoutMs: 5_000,
    idleTimeoutMs: 5_000,
    uncommittedAttemptMaxLifetimeMs: 30_000,
    noAvailableAccountWaitMs: 5_000
  },
  Date.now(),
  async () => {},
  undefined,
  {
    responseProtocol: 'openai_v1',
    endpointFamily: 'responses',
    downstreamCommitState: safeStreamCommitState,
    codexResponsesGuard: safeStreamGuard
  }
)
const safeStreamText = Buffer.concat(safeStreamChunks).toString('utf8')
assert.equal(safeStreamResult.completed, true)
assert.equal(safeStreamResult.codexResponsesGuard?.outcome, 'repaired_safe')
assert.match(safeStreamText, /ctc_stream_repaired/)
assert.equal(safeStreamText.includes('fc_wrong_stream'), false, 'safe_repair 必须把 added/done/completed 的错误 ID 一致改写到 wire')
assert.deepEqual(safeStreamResult.codexResponsesGuard?.repairRuleIds, ['codex.r0.response.replace_stream_item_id'])

const strictStreamCommitState = new GatewayDownstreamCommitState()
const strictStreamGuard = createCodexResponsesResponseGuard({
  marker: createCodexResponsesGuardMarker('raw_upstream'),
  downstreamCommitState: strictStreamCommitState,
  mode: 'strict_intercept'
})
const strictStreamChunks: Buffer[] = []
const strictStreamResponse = {
  headersSent: false,
  writableEnded: false,
  destroyed: false,
  writableLength: 0,
  writableHighWaterMark: 16 * 1024,
  once() { return this },
  off() { return this },
  hasHeader() { return false },
  setHeader() { return this },
  status() { return this },
  write(chunk: Buffer) {
    this.headersSent = true
    strictStreamChunks.push(Buffer.from(chunk))
    return true
  },
  end() {
    this.writableEnded = true
    return this
  }
}
async function* strictStreamUpstream(): AsyncIterable<Uint8Array> {
  const event = {
    type: 'response.output_item.added',
    output_index: 0,
    item: { id: 'fc_strict_wrong', type: 'custom_tool_call', call_id: 'call_strict', name: 'apply_patch', input: '' }
  }
  yield Buffer.from(`event: ${event.type}\ndata: ${JSON.stringify(event)}\n\n`, 'utf8')
}
const strictStreamResult = await pipeUpstreamStream(
  strictStreamUpstream(),
  strictStreamResponse as never,
  {
    firstResponseTimeoutMs: 5_000,
    firstByteTimeoutMs: 5_000,
    idleTimeoutMs: 5_000,
    uncommittedAttemptMaxLifetimeMs: 30_000,
    noAvailableAccountWaitMs: 5_000
  },
  Date.now(),
  async () => {},
  undefined,
  {
    responseProtocol: 'openai_v1',
    endpointFamily: 'responses',
    interpretProtocolFailures: false,
    downstreamCommitState: strictStreamCommitState,
    codexResponsesGuard: strictStreamGuard
  }
)
assert.equal(strictStreamResult.completed, false)
assert.equal(strictStreamResult.responseInspection?.upstreamErrorCode, 'codex_responses_protocol_intercepted')
assert.equal(strictStreamResult.codexResponsesGuard?.mode, 'strict_intercept')
assert.equal(strictStreamChunks.length, 0, '严格拦截必须在下游写入前阻止污染事件')

const safeBlockedCommitState = new GatewayDownstreamCommitState()
const safeBlockedGuard = createCodexResponsesResponseGuard({
  marker: createCodexResponsesGuardMarker('raw_upstream'),
  downstreamCommitState: safeBlockedCommitState,
  mode: 'safe_repair'
})
const safeBlockedResponse = {
  headersSent: false,
  writableEnded: false,
  destroyed: false,
  writableLength: 0,
  writableHighWaterMark: 16 * 1024,
  once() { return this },
  off() { return this },
  hasHeader() { return false },
  setHeader() { return this },
  status() { return this },
  write() {
    this.headersSent = true
    return true
  },
  end() {
    this.writableEnded = true
    return this
  }
}
const safeBlockedResult = await pipeUpstreamStream(
  (async function* safeBlockedUpstream(): AsyncIterable<Uint8Array> {
    const event = {
      type: 'response.output_item.added',
      output_index: 0,
      item: { id: 'ctc_blocked', type: 'custom_tool_call' }
    }
    yield Buffer.from(`event: ${event.type}\ndata: ${JSON.stringify(event)}\n\n`, 'utf8')
  })(),
  safeBlockedResponse as never,
  {
    firstResponseTimeoutMs: 5_000,
    firstByteTimeoutMs: 5_000,
    idleTimeoutMs: 5_000,
    uncommittedAttemptMaxLifetimeMs: 30_000,
    noAvailableAccountWaitMs: 5_000
  },
  Date.now(),
  async () => {},
  undefined,
  {
    responseProtocol: 'openai_v1',
    endpointFamily: 'responses',
    interpretProtocolFailures: false,
    downstreamCommitState: safeBlockedCommitState,
    codexResponsesGuard: safeBlockedGuard
  }
)
assert.equal(safeBlockedResult.completed, false)
assert.equal(safeBlockedResult.responseInspection?.upstreamErrorCode, 'codex_responses_protocol_blocked')
assert.equal(safeBlockedResult.codexResponsesGuard?.mode, 'safe_repair')

console.log('Codex Responses 双检查点回归通过：显式 provenance、shadow、R0、副作用边界与 late violation 已固定')
