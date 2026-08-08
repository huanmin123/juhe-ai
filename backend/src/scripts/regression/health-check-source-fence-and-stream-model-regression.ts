import assert from 'node:assert/strict'
import { EventEmitter } from 'node:events'
import { readFileSync } from 'node:fs'
import { resolve } from 'node:path'

import { runtimeConfig } from '../../config/runtime.js'
import {
  clearUsageRecordQueueForTest,
  peekPendingUsageRecordForTest,
  setDbServiceUsageRecordLocalWriteAllowedForTest
} from '../../modules/gateway/usage/record-queue.service.js'
import {
  createUpstreamResponseModelObservation,
  observeUpstreamResponseModelBody
} from '../../modules/gateway/observability/upstream-response-model.js'
import { handleStreamUpstreamResponse } from '../../modules/gateway/response/finalization.js'
import { GatewayDownstreamCommitState } from '../../modules/gateway/response/downstream-commit-state.js'

const healthCheckServiceSource = readFileSync(resolve('src/modules/background/account-health-check.service.ts'), 'utf8')
const finalizationSource = readFileSync(resolve('src/modules/gateway/response/finalization.ts'), 'utf8')

assert.match(
  healthCheckServiceSource,
  /const execution = accountHealthCheckExecutions\.get\(executionKey\)[\s\S]{0,300}try \{[\s\S]{0,1000}const account = await accountForHealthCheckQueueItem\(item\)/,
  '账户查询必须在执行记录保护的 try/finally 内，零重试失败时才能清理 source execution'
)
assert.match(
  healthCheckServiceSource,
  /let coordination: Awaited<ReturnType<typeof acquireAvailabilityProbe>> \| undefined[\s\S]{0,2600}const acquiredCoordination = await acquireAvailabilityProbe/,
  'coordinator 获取必须处于相同的执行记录保护范围'
)
assert.match(
  healthCheckServiceSource,
  /finally \{[\s\S]{0,240}if \(accountHealthCheckExecutions\.get\(executionKey\) === execution\) \{[\s\S]{0,120}accountHealthCheckExecutions\.delete\(executionKey\)/,
  '完成旧执行不得无条件删除 take 之后新注册的 source-only execution'
)
assert.doesNotMatch(
  healthCheckServiceSource,
  /finally \{[\s\S]{0,180}accountHealthCheckExecutions\.delete\(executionKey\)(?![\s\S]{0,20}\})/,
  'finally 不得按 key 无条件清理执行记录'
)
assert.match(
  healthCheckServiceSource,
  /completedExecution = takeAccountHealthCheckExecution\(executionKey\)/,
  '完成边界必须保留已摘取的本地 execution，以便后续异步步骤失败时结算'
)
assert.match(
  healthCheckServiceSource,
  /if \(completedExecution && !completedExecutionSourceFencesSettled\) \{\s+settleCompletedSourceFences\(\[\.\.\.completedExecution\.sourceFences\.values\(\)\], 'probe_task_failure'\)/,
  'take() 后读取共享 source fence 失败时，已摘取的本地 fence 仍必须结算'
)
assert.match(
  healthCheckServiceSource,
  /if \(coordination\?\.disposition === 'owner'\) \{[\s\S]{0,700}catch \(settlementError\) \{[\s\S]{0,1000}settleSourceFencesForExecution\(executionKey, execution, 'probe_task_failure'\)/,
  'coordinator 结算失败不得跳过当前 execution 的本地 source fence 清理'
)

const observation = createUpstreamResponseModelObservation({ protocol: 'openai', sse: true })
const interruptedBody = {
  async *[Symbol.asyncIterator](): AsyncGenerator<Uint8Array> {
    yield Buffer.from('event: response.created\ndata: {"type":"response.created","response":{"model":"gpt-5.6-interrupted"}}\n\n')
    throw new Error('upstream stream interrupted after response.model')
  }
}
await assert.rejects(async () => {
  for await (const _chunk of observeUpstreamResponseModelBody(interruptedBody, observation)) {
    // The observation must survive the interrupted source iterator.
  }
})
assert.equal(observation.model, 'gpt-5.6-interrupted', '中断前已经观察到的 SSE response.model 必须保留')
const originalProcessRole = runtimeConfig.processRole
runtimeConfig.processRole = 'db-service'
setDbServiceUsageRecordLocalWriteAllowedForTest(true)
try {
  await assertIncompleteStreamUsageRetainsObservedModel()
  assert.match(
    finalizationSource,
    /if \(!streamResult\.completed\) \{[\s\S]{0,1800}usage: usageWithObservedUpstreamResponseModel\(streamUsageFallback\.usage, upstreamResponse\)/,
    '不完整 SSE 的使用记录必须合并已观察到的上游响应模型'
  )
  assert.match(
    finalizationSource,
    /const usage = input\.parsedJsonBody[\s\S]{0,2400}usage: usageWithObservedUpstreamResponseModel\(usage, input\.upstreamResponse\)/,
    'non-stream hybrid quality failure 的使用记录必须合并已观察到的上游响应模型'
  )
} finally {
  clearUsageRecordQueueForTest()
  setDbServiceUsageRecordLocalWriteAllowedForTest(false)
  runtimeConfig.processRole = originalProcessRole
}

console.log('健康检查 source fence 生命周期与不完整 SSE 上游模型回归通过')

async function assertIncompleteStreamUsageRetainsObservedModel(): Promise<void> {
  clearUsageRecordQueueForTest()
  setDbServiceUsageRecordLocalWriteAllowedForTest(true)
  const observation = createUpstreamResponseModelObservation({ protocol: 'openai', sse: true })
  const response = createMockResponse()
  const req = {
    body: { model: 'gpt-5.6-interrupted', stream: true },
    headers: {},
    method: 'POST',
    path: '/responses',
    originalUrl: '/v1/responses',
    header: () => undefined
  } as never
  const account = {
    id: 'acct-interrupted-sse-model',
    name: '不完整 SSE 模型审计账户',
    providerCode: 'gpt',
    providerProtocolProfileId: 'gpt-openai-v1',
    protocolCode: 'openai',
    protocolVersion: 'v1',
    type: 'api_key',
    status: 'active',
    credentials: {}
  } as never
  const result = await handleStreamUpstreamResponse({
    req,
    res: response as never,
    account,
    upstreamResponse: {
      status: 200,
      ok: true,
      headers: new Headers({ 'content-type': 'text/event-stream' }),
      body: observeUpstreamResponseModelBody({
        async *[Symbol.asyncIterator](): AsyncGenerator<Uint8Array> {
          yield Buffer.from('event: response.created\ndata: {"type":"response.created","response":{"model":"gpt-5.6-interrupted"}}\n\n')
          yield Buffer.from('event: response.output_text.delta\ndata: {"type":"response.output_text.delta","delta":"partial"}\n\n')
          throw new Error('upstream stream interrupted after response.model')
        }
      }, observation),
      upstreamResponseModelObservation: observation
    },
    upstreamUrl: 'https://example.test/v1/responses',
    auditAttemptId: 'audit-interrupted-sse-model',
    auditCapture: {
      shouldCaptureSuccessPayloads: () => false,
      completeAttempt: () => undefined,
      addGatewayMetadata: () => undefined,
      omitPayloadBodies: () => undefined,
      finalize: () => undefined
    } as never,
    settings: {} as never,
    timeoutProfile: { timeoutsDisabled: true } as never,
    usageContext: {
      traceId: 'trace-interrupted-sse-model',
      trafficSource: 'gateway',
      systemAccountId: 'sys-interrupted-sse-model',
      groupId: 'group-interrupted-sse-model',
      endpoint: '/v1/responses',
      requestSnapshot: {
        method: 'POST',
        path: '/v1/responses',
        originalUrl: '/v1/responses',
        traceId: 'trace-interrupted-sse-model',
        headers: {}
      }
    },
    startedAt: Date.now() - 10,
    signal: new AbortController().signal,
    accountStateMutationEnabled: false,
    automaticAccountStateMutationEnabled: false,
    downstreamCommitState: new GatewayDownstreamCommitState()
  })
  assert.equal(result.alreadyFinalized, true, '不完整 SSE 必须在流式失败路径自行结算')
  assert.equal(observation.model, 'gpt-5.6-interrupted', '实际流式失败前必须已观察到 response.model')
  const recordedUsage = await waitForPendingUsageRecord()
  assert.equal(
    recordedUsage.upstreamResponseModel,
    'gpt-5.6-interrupted',
    '不完整 SSE 的实际 usage 记录必须保留已观测上游模型'
  )
  clearUsageRecordQueueForTest()
}

async function waitForPendingUsageRecord() {
  const deadline = Date.now() + 2_000
  while (true) {
    const record = peekPendingUsageRecordForTest()
    if (record) return record
    assert(Date.now() < deadline, '不完整 SSE 必须在受控本地写入模式下产生 usage 记录')
    await new Promise<void>((resolvePromise) => setTimeout(resolvePromise, 10))
  }
}

function createMockResponse(): EventEmitter & Record<string, unknown> {
  const response = new EventEmitter() as EventEmitter & Record<string, unknown>
  const headers = new Map<string, unknown>()
  response.headersSent = false
  response.writableEnded = false
  response.destroyed = false
  response.statusCode = 200
  response.status = function status(this: Record<string, unknown>, statusCode: number) {
    this.statusCode = statusCode
    return this
  }
  response.setHeader = (name: string, value: unknown) => headers.set(name.toLowerCase(), value)
  response.hasHeader = (name: string) => headers.has(name.toLowerCase())
  response.getHeader = (name: string) => headers.get(name.toLowerCase())
  response.getHeaders = () => Object.fromEntries(headers)
  response.write = function write(this: Record<string, unknown>) {
    this.headersSent = true
    return true
  }
  response.end = function end(this: Record<string, unknown>) {
    this.writableEnded = true
  }
  response.destroy = function destroy(this: Record<string, unknown>) {
    this.destroyed = true
  }
  return response
}
