import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'
import { resolve } from 'node:path'

import {
  diagnosticResponseContextFromGatewayNonStream,
  diagnosticResponseContextFromGatewayResponse
} from '../../modules/gateway/diagnostics/diagnostic-response-context.js'
import {
  gatewayNonStreamJsonBodyFromValue,
  publishGatewayNonStreamJsonBody
} from '../../modules/gateway/response/non-stream-json-body.js'
import { pipeUpstreamStream } from '../../modules/gateway/response/stream.js'
import { MemoryGatewayResponse } from '../../modules/gateway/testing/memory-gateway-http.js'

const parsedValue = {
  model: 'gpt-5.6-sol',
  output_text: 'OK',
  usage: { total_tokens: 2 }
}
const parsedBody = gatewayNonStreamJsonBodyFromValue(parsedValue)
const response = new MemoryGatewayResponse(Date.now())
publishGatewayNonStreamJsonBody(response, parsedBody)
assert.strictEqual(response.nonStreamJsonBody(), parsedBody, 'MemoryGatewayResponse 必须保留网关解析结果的同一引用')

let contextParseCount = 0
const context = diagnosticResponseContextFromGatewayNonStream(JSON.stringify(parsedValue), response.nonStreamJsonBody(), {
  onJsonParseAttempt: () => { contextParseCount += 1 }
})
assert.strictEqual(context.json, parsedValue, '诊断上下文必须直接复用网关 parsed value')
assert.equal(contextParseCount, 0, '已有网关 parsed body 时诊断上下文不得再次 JSON.parse')

let invalidParseCount = 0
const invalidContext = diagnosticResponseContextFromGatewayNonStream('{invalid', { status: 'invalid' }, {
  onJsonParseAttempt: () => { invalidParseCount += 1 }
})
assert.equal(invalidContext.payloads.length, 0)
assert.equal(invalidParseCount, 0, '网关已确认 invalid 的非流式正文不得再次尝试解析')

const streamResponse = new MemoryGatewayResponse(Date.now() - 5)
const streamBodyText = [
  'event: response.output_text.delta',
  `data: ${JSON.stringify({ type: 'response.output_text.delta', delta: 'OK' })}`,
  '',
  'event: response.completed',
  `data: ${JSON.stringify({ type: 'response.completed', response: { status: 'completed', output: [] } })}`,
  '',
  ''
].join('\n')
const streamResult = await pipeUpstreamStream(
  oneChunk(streamBodyText),
  streamResponse.asResponse(),
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
    interpretProtocolFailures: false
  }
)
assert.equal(streamResult.completed, true)
assert.equal(streamResponse.parsedStreamEvents().length, 2, '真实流式管线必须发布每个已解析 SSE event')
let streamParseCount = 0
const streamContext = diagnosticResponseContextFromGatewayResponse(
  streamBodyText,
  undefined,
  streamResponse.parsedStreamEvents(),
  { onJsonParseAttempt: () => { streamParseCount += 1 } }
)
assert.strictEqual(
  streamContext.events[0]?.json,
  streamResponse.parsedStreamEvents()[0]?.data,
  '诊断上下文必须复用网关 SSE event 对象'
)
assert.equal(streamParseCount, 0, '已有网关 SSE event 时诊断上下文不得重放 JSON.parse')
assert.notEqual(streamResponse.firstTokenMs(), undefined, '首 token 时间必须复用网关 inspection，不得自行重解析 SSE')

const finalizationSource = readFileSync(resolve('src/modules/gateway/response/finalization.ts'), 'utf8')
const streamSource = readFileSync(resolve('src/modules/gateway/response/stream.ts'), 'utf8')
const accountTestSource = readFileSync(resolve('src/modules/accounts/account-test.service.ts'), 'utf8')
assert.match(finalizationSource, /publishGatewayNonStreamJsonBody\(res, parsedJsonBody\)/, '非流式网关必须发布最终 parsed body')
assert.match(accountTestSource, /response\.nonStreamJsonBody\(\)/, '账户诊断必须消费 MemoryGatewayResponse 保存的 parsed body')
assert.match(streamSource, /publishGatewayStreamParsedEvent\(res, event\)/, '流式网关必须发布已经解析的 SSE event')
assert.match(accountTestSource, /response\.parsedStreamEvents\(\)/, '账户诊断必须消费 MemoryGatewayResponse 保存的 SSE event')

console.log('诊断响应上下文复用回归通过：MemoryGatewayResponse 保留网关 JSON/SSE 解析结果，账户诊断不再重复解析')

async function* oneChunk(text: string): AsyncIterable<Uint8Array> {
  yield Buffer.from(text, 'utf8')
}
