import { strict as assert } from 'node:assert'

import {
  gatewayTimeoutProfileForLane,
  type GatewayTimeoutSettings
} from '../../modules/gateway/policy/timeout-profile.js'
import {
  upstreamRequestTimeoutMs,
  upstreamSocketTimeoutMs
} from '../../modules/gateway/upstream/request.js'

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

console.log('gateway timeout profile regression passed')
