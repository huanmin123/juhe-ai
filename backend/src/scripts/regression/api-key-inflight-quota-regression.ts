import assert from 'node:assert/strict'

import {
  clearApiKeyInflightQuotaReservationsForTest,
  reserveApiKeyInflightCost,
  apiKeyInflightQuotaSnapshot,
  estimateGatewayRequestCostUsd
} from '../../modules/gateway/quota/api-key-inflight-quota.service.js'
import type { Request } from 'express'

const limits = { total: { enabled: true as const, limit: 1 } }
const zeroCosts = { hourly: 0, daily: 0, weekly: 0, monthly: 0, total: 0 }

try {
  const first = reserveApiKeyInflightCost({
    apiKeyId: 'key_inflight_quota',
    limits,
    currentCosts: zeroCosts,
    estimatedCostUsd: 0.6,
    releaseDelayMs: 10
  })
  assert.equal(first.allowed, true, '第一个预估请求应占用在途额度')
  assert(first.reservation)

  const second = reserveApiKeyInflightCost({
    apiKeyId: 'key_inflight_quota',
    limits,
    currentCosts: zeroCosts,
    estimatedCostUsd: 0.6,
    releaseDelayMs: 10
  })
  assert.equal(second.allowed, false, '并发请求不得共同穿透同一份剩余额度')
  assert.equal(apiKeyInflightQuotaSnapshot()[0]?.reservedCostUsd, 0.6)

  first.reservation.complete()
  await waitMs(20)
  const afterRelease = reserveApiKeyInflightCost({
    apiKeyId: 'key_inflight_quota',
    limits,
    currentCosts: zeroCosts,
    estimatedCostUsd: 0.6,
    releaseDelayMs: 0
  })
  assert.equal(afterRelease.allowed, true, '统计接管延迟结束后应释放在途 reservation')
  afterRelease.reservation?.complete()

  const nearLimit = reserveApiKeyInflightCost({
    apiKeyId: 'key_near_limit',
    limits,
    currentCosts: { ...zeroCosts, total: 0.5 },
    estimatedCostUsd: 0.6,
    releaseDelayMs: 0
  })
  assert.equal(nearLimit.allowed, false, '快照成本与当前请求预估合计达到额度时应拒绝')

  const unpricedRequest = {
    originalUrl: '/v1/responses',
    path: '/v1/responses',
    body: { model: 'model-without-price' },
    gatewayRequestBody: {
      rawBodyBytes: 1024,
      contentType: 'application/json',
      isJson: true,
      jsonParseStatus: 'parsed',
      jsonParseWarningBytes: 2048,
      model: 'model-without-price',
      maxOutputTokens: 8192
    }
  } as unknown as Request
  assert.equal(estimateGatewayRequestCostUsd(unpricedRequest, 'gpt'), undefined, '无定价模型不能伪造在途成本')

  console.log('API Key 在途额度回归通过：并发原子预留、快照合并与延迟释放符合预期')
} finally {
  clearApiKeyInflightQuotaReservationsForTest()
}

async function waitMs(ms: number): Promise<void> {
  await new Promise<void>((resolve) => setTimeout(resolve, ms))
}
