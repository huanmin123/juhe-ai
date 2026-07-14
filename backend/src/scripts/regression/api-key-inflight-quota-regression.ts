import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'

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

  const exactLimit = reserveApiKeyInflightCost({
    apiKeyId: 'key_exact_limit',
    limits,
    currentCosts: { ...zeroCosts, total: 0.4 },
    estimatedCostUsd: 0.6,
    releaseDelayMs: 0
  })
  assert.equal(exactLimit.allowed, true, '投影成本恰好等于额度时应允许当前请求用完余额')
  exactLimit.reservation?.complete()

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
  assert.equal(await estimateGatewayRequestCostUsd(unpricedRequest, 'gpt'), undefined, '无定价模型不能伪造在途成本')

  const serviceSource = readFileSync(new URL('../../modules/gateway/quota/api-key-inflight-quota.service.ts', import.meta.url), 'utf8')
  assert.match(
    serviceSource,
    /estimateGatewayRequestCostUsd\(input\.req, input\.providerCode, input\.apiKey\.system_account_id\)/,
    '在途额度估算必须携带 API Key 所属系统账户，以命中个人模型价格'
  )

  console.log('API Key 在途额度回归通过：并发原子预留、快照合并与延迟释放符合预期')
} finally {
  clearApiKeyInflightQuotaReservationsForTest()
}

async function waitMs(ms: number): Promise<void> {
  await new Promise<void>((resolve) => setTimeout(resolve, ms))
}
