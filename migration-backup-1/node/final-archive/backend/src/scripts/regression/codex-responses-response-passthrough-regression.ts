import assert from 'node:assert/strict'

import { GatewayDownstreamCommitState } from '../../modules/gateway/response/downstream-commit-state.js'
import { pipeUpstreamStream } from '../../modules/gateway/response/stream.js'

const timeoutProfile = {
  firstResponseTimeoutMs: 5_000,
  firstByteTimeoutMs: 5_000,
  idleTimeoutMs: 5_000,
  uncommittedAttemptMaxLifetimeMs: 30_000,
  noAvailableAccountWaitMs: 5_000
}

const originalSse = [
  'event: response.output_item.added',
  'data: {"type":"response.output_item.added","output_index":0,"item":{"id":"ctc_upstream_inconsistent","type":"custom_tool_call","call_id":"call_1"}}',
  '',
  'event: response.completed',
  'data: {"type":"response.completed","response":{"id":"resp_1","status":"completed","output":[]}}',
  '',
  ''
].join('\n')

const written: Buffer[] = []
const response = {
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
    written.push(Buffer.from(chunk))
    return true
  },
  end() {
    this.writableEnded = true
    return this
  }
}

let streamFailureCount = 0
const commitState = new GatewayDownstreamCommitState()
const result = await pipeUpstreamStream(
  (async function* (): AsyncIterable<Uint8Array> {
    yield Buffer.from(originalSse, 'utf8')
  })(),
  response as never,
  timeoutProfile,
  Date.now(),
  async () => { streamFailureCount += 1 },
  undefined,
  {
    responseProtocol: 'openai_v1',
    endpointFamily: 'responses',
    interpretProtocolFailures: false,
    downstreamCommitState: commitState
  }
)

assert.equal(result.completed, true, '上游 SSE 中的 Responses item 字段不一致不得被质量 guard 中止')
assert.equal(result.responseInspection, undefined, '未配置通用策略时不得把 SSE 格式差异转为拦截决策')
assert.equal(streamFailureCount, 0, '上游 SSE 格式差异不得触发账户失败或切号路径')
assert.equal(Buffer.concat(written).toString('utf8'), originalSse, '上游 SSE 必须按原字节透传，不得响应侧修复或改写')
assert.equal(commitState.semanticCommitted, true)

console.log('Codex Responses 响应透传回归通过：不一致 SSE 原样下发，不触发 guard、失败或切号')
