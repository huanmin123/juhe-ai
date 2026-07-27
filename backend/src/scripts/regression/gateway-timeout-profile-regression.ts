import { strict as assert } from 'node:assert'
import { readFileSync } from 'node:fs'
import { createServer } from 'node:http'

import { runtimeConfig } from '../../config/runtime.js'
import {
  gatewayTimeoutProfileForLane,
  type GatewayTimeoutSettings
} from '../../modules/gateway/policy/timeout-profile.js'
import { GatewayRequestWallBudget } from '../../modules/gateway/routing/route-coordination.js'

const {
  upstreamRequestTimeoutMs,
  upstreamSocketTimeoutMs,
  requestUpstream
} = await import('../../modules/gateway/upstream/request.js')

const settings: GatewayTimeoutSettings = {
  textFirstResponseTimeoutSeconds: 120,
  textStreamIdleTimeoutSeconds: 30,
  textUncommittedAttemptMaxLifetimeSeconds: 1800,
  imageFirstResponseTimeoutSeconds: 600,
  imageStreamIdleTimeoutSeconds: 120,
  imageUncommittedAttemptMaxLifetimeSeconds: 3600,
  noAvailableAccountWaitTimeoutSeconds: 270
}

const textProfile = gatewayTimeoutProfileForLane(settings, 'text')
const imageProfile = gatewayTimeoutProfileForLane(settings, 'image')
const compactionProfile = gatewayTimeoutProfileForLane(settings, 'text', { disableTimeouts: true })

assert.deepEqual(textProfile, {
  firstResponseTimeoutMs: 120_000,
  firstByteTimeoutMs: 120_000,
  idleTimeoutMs: 30_000,
  uncommittedAttemptMaxLifetimeMs: 1_800_000,
  noAvailableAccountWaitMs: 270_000
})

assert.deepEqual(imageProfile, {
  firstResponseTimeoutMs: 600_000,
  firstByteTimeoutMs: 600_000,
  idleTimeoutMs: 120_000,
  uncommittedAttemptMaxLifetimeMs: 3_600_000,
  noAvailableAccountWaitMs: 270_000
})

const streamRequest = {
  body: { stream: true },
  path: '/v1/responses',
  originalUrl: '/v1/responses'
}
assert.equal(upstreamRequestTimeoutMs(textProfile), 120_000, '文本 lane 上游首响应应使用 120 秒')
assert.equal(upstreamRequestTimeoutMs(imageProfile), 600_000, '图像 lane 上游首响应应使用 600 秒')
assert.equal(upstreamSocketTimeoutMs(streamRequest as never, textProfile), 120_000, '文本流 transport timeout 不应短于首响应等待')
assert.equal(upstreamSocketTimeoutMs(streamRequest as never, imageProfile), 600_000, '图像流 transport timeout 不应短于首响应等待')

assert.equal(compactionProfile.timeoutsDisabled, true, 'Responses 压缩请求必须使用显式无时限 profile')
assert.equal(upstreamRequestTimeoutMs(compactionProfile), undefined, '压缩请求不得设置响应头等待时限')
assert.equal(upstreamSocketTimeoutMs(streamRequest as never, compactionProfile), undefined, '压缩请求不得设置 socket 时限')

let nowMs = 100_000
const boundedWallBudget = new GatewayRequestWallBudget({
  requestAcceptedAtMs: nowMs,
  budgetMs: 1_000,
  now: () => nowMs
})
const compactionWallBudget = boundedWallBudget.withoutLimit()
nowMs += 24 * 60 * 60 * 1000
assert.equal(compactionWallBudget.unbounded, true, '压缩请求必须显式关闭网关总墙钟预算')
assert.equal(compactionWallBudget.remainingMs(), Number.POSITIVE_INFINITY, '无时限墙钟不得随请求运行时间减少')
assert.equal(compactionWallBudget.precommitRemainingMs(), Number.POSITIVE_INFINITY, '无时限墙钟不得生成响应提交截止')
assert.equal(compactionWallBudget.handoffRequired(), false, '无时限墙钟不得触发客户端 handoff')

const upstreamRequestSource = readFileSync(new URL('../../modules/gateway/upstream/request.ts', import.meta.url), 'utf8')
const streamSource = readFileSync(new URL('../../modules/gateway/response/stream.ts', import.meta.url), 'utf8')
const finalizationSource = readFileSync(new URL('../../modules/gateway/response/finalization.ts', import.meta.url), 'utf8')
const compactPreflightSource = readFileSync(new URL('../../modules/gateway/codex-responses/compact-preflight.ts', import.meta.url), 'utf8')
assert.match(upstreamRequestSource, /!options\.disableTimeouts[\s\S]*options\.timeoutMs \?\? 120000/, '只有显式无时限策略可以绕过默认 socket 时限')
assert.match(streamSource, /buildGatewayStreamReadPlan\(timeoutProfile/, '压缩 profile 必须使用保留 raw idle 的统一流读取计划')
assert.match(finalizationSource, /timeoutsDisabled === true[\s\S]*firstByteTimeoutMs[\s\S]*timeoutsDisabled === true[\s\S]*maxLifetimeMs/, '非流式响应必须同时关闭首字和最大生命周期')
assert.match(compactPreflightSource, /syntheticReq[\s\S]*input\.requestCoordination/, '改写为 Chat Completions 的 compact 摘要请求必须继承显式无时限协调策略')

const delayedServer = createServer((_req, res) => {
  setTimeout(() => {
    res.writeHead(200, { 'content-type': 'text/plain' })
    res.end('delayed compact response')
  }, 40)
})
const previousAllowPrivateBaseUrls = runtimeConfig.upstreamUrlSecurity.allowPrivateBaseUrls
runtimeConfig.upstreamUrlSecurity.allowPrivateBaseUrls = true
await new Promise<void>((resolve) => delayedServer.listen(0, '127.0.0.1', resolve))
try {
  const address = delayedServer.address()
  assert(address && typeof address === 'object')
  const url = `http://127.0.0.1:${address.port}/delayed`
  await assert.rejects(
    requestUpstream(url, {
      method: 'GET',
      headers: new Headers(),
      timeoutMs: 5,
      requestTimeoutMs: 5
    }),
    '普通上游请求必须继续受 transport/响应头时限保护'
  )
  const delayedResponse = await requestUpstream(url, {
    method: 'GET',
    headers: new Headers(),
    timeoutMs: 5,
    requestTimeoutMs: 5,
    firstByteDeadlineMs: 5,
    disableTimeouts: true
  })
  assert.equal(delayedResponse.status, 200, '显式无时限请求不得被 transport、响应头或首字计时器中断')
  for await (const _chunk of delayedResponse.body ?? []) {
  }

  const abortController = new AbortController()
  const abortTimer = setTimeout(() => abortController.abort(), 5)
  try {
    await assert.rejects(
      requestUpstream(url, {
        method: 'GET',
        headers: new Headers(),
        disableTimeouts: true,
        signal: abortController.signal
      }),
      '无时限压缩请求仍必须响应客户端取消信号'
    )
  } finally {
    clearTimeout(abortTimer)
  }
} finally {
  runtimeConfig.upstreamUrlSecurity.allowPrivateBaseUrls = previousAllowPrivateBaseUrls
  await new Promise<void>((resolve, reject) => delayedServer.close((error) => error ? reject(error) : resolve()))
}

console.log('gateway timeout profile regression passed')
