import { strict as assert } from 'node:assert'
import { EventEmitter } from 'node:events'
import type { NextFunction, Response } from 'express'

import { captureGatewayRawBody } from '../../modules/gateway/request/body-middleware.js'
import {
  clearGatewayRequestBodyInFlightForTest,
  getGatewayRequestBodyInFlightState,
  type GatewayRawBodyRequest
} from '../../modules/gateway/request/body.js'
import { attachAccountSlotRelease } from '../../modules/gateway/routes.js'
import { observeGatewayHttpCompletion } from '../../modules/gateway/audit/capture.service.js'

class MockResponse extends EventEmitter {
  destroyed = false
  writableEnded = false
  writableFinished = false
}

clearGatewayRequestBodyInFlightForTest()

const rawBody = Buffer.from(JSON.stringify({ model: 'gpt-5.6-sol', input: 'lease lifecycle' }), 'utf8')
const request = Object.assign(new EventEmitter(), {
  body: rawBody,
  headers: { 'content-type': 'application/json' },
  method: 'POST',
  path: '/responses',
  originalUrl: '/v1/responses',
  aborted: false
}) as unknown as GatewayRawBodyRequest
const response = new MockResponse() as unknown as Response
let nextCalled = false
let bytesObservedInsideHandler = 0

await captureGatewayRawBody(request, response, (() => {
  nextCalled = true
  bytesObservedInsideHandler = getGatewayRequestBodyInFlightState().currentBytes
}) as NextFunction)

assert.equal(nextCalled, true, '合法请求体应进入网关业务处理')
assert.equal(bytesObservedInsideHandler, rawBody.byteLength, '进入业务处理时请求体 lease 必须仍然有效')
assert.equal(getGatewayRequestBodyInFlightState().currentBytes, rawBody.byteLength, '业务处理中不得提前释放请求体 lease')

response.emit('finish')
assert.equal(getGatewayRequestBodyInFlightState().currentBytes, 0, 'HTTP finish 后应释放请求体 lease')
assert.equal(getGatewayRequestBodyInFlightState().requestCount, 0, 'HTTP finish 后请求体 lease 计数应归零')

const accountResponse = new MockResponse() as unknown as Response
let accountReleaseCount = 0
const releaseAccountSlot = attachAccountSlotRelease(accountResponse, () => {
  accountReleaseCount += 1
})
accountResponse.emit('finish')
assert.equal(accountReleaseCount, 1, 'HTTP finish 应立即释放账户并发槽')
releaseAccountSlot()
accountResponse.emit('close')
assert.equal(accountReleaseCount, 1, '账户并发槽释放必须幂等')

const timingResponse = new MockResponse() as unknown as Response
const httpCompletion = observeGatewayHttpCompletion(timingResponse)
timingResponse.emit('finish')
const observedCompletedAtMs = httpCompletion.completedAtMs()
await new Promise<void>((resolvePromise) => setTimeout(resolvePromise, 10))
assert.equal(await httpCompletion.wait(), observedCompletedAtMs, '监听后再等待必须复用真实 HTTP finish 时间，不能混入后置副作用耗时')

clearGatewayRequestBodyInFlightForTest()
console.log('网关响应生命周期回归通过：请求体 lease、账户并发槽和完成时间均以同一次 HTTP finish/close 为边界')
