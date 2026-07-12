import { strict as assert } from 'node:assert'

import type { AxiosAdapter, AxiosRequestConfig } from 'axios'

import { ipStatsApi } from '../../src/api/domains/ipStats'
import { http } from '../../src/api/http'

interface CapturedRequest {
  method: string
  url: string
  params?: Record<string, unknown>
  body: unknown
}

const ipHash = 'a'.repeat(64)
const reason = '管理员确认该客户端 IP 可正常访问'
const capturedRequests: CapturedRequest[] = []
const originalAdapter = http.defaults.adapter

const requestCaptureAdapter: AxiosAdapter = async (config) => {
  capturedRequests.push({
    method: String(config.method ?? '').toUpperCase(),
    url: String(config.url ?? ''),
    params: copyParams(config.params),
    body: parseRequestBody(config.data)
  })

  const isUnallowlist = String(config.url ?? '').endsWith('/unallowlist')
  return {
    data: {
      data: isUnallowlist
        ? { disabledCount: 0 }
        : {
            id: 'client_ip_policy_allowlist_regression',
            ipHash,
            policyType: 'allowlist',
            status: 'active',
            reason
          }
    },
    status: 200,
    statusText: 'OK',
    headers: {},
    config
  }
}

let allowlistResult: Awaited<ReturnType<typeof ipStatsApi.allowlist>>
let unallowlistResult: Awaited<ReturnType<typeof ipStatsApi.unallowlist>>
let emptyAllowlistResult: Awaited<ReturnType<typeof ipStatsApi.allowlist>>
let emptyUnallowlistResult: Awaited<ReturnType<typeof ipStatsApi.unallowlist>>

try {
  http.defaults.adapter = requestCaptureAdapter

  allowlistResult = await ipStatsApi.allowlist(ipHash, { reason })
  unallowlistResult = await ipStatsApi.unallowlist(ipHash, { reason })
  emptyAllowlistResult = await ipStatsApi.allowlist(ipHash, {})
  emptyUnallowlistResult = await ipStatsApi.unallowlist(ipHash, {})
} finally {
  http.defaults.adapter = originalAdapter
}

assert.equal(capturedRequests.length, 4, '应捕获白名单、解除白名单及各自空 reason 请求')
assertPolicyRequest(capturedRequests[0], 'allowlist', { reason })
assertPolicyRequest(capturedRequests[1], 'unallowlist', { reason })
assertPolicyRequest(capturedRequests[2], 'allowlist', {})
assertPolicyRequest(capturedRequests[3], 'unallowlist', {})

assert.equal(allowlistResult.id, 'client_ip_policy_allowlist_regression', '白名单接口必须解包 data')
assert.equal(allowlistResult.ipHash, ipHash, '白名单接口必须保留响应中的 IP 哈希')
assert.deepEqual(unallowlistResult, { disabledCount: 0 }, '解除白名单 disabledCount=0 也必须作为成功响应解包')
assert.equal(emptyAllowlistResult.id, 'client_ip_policy_allowlist_regression', '空 payload 白名单请求也必须正常解包')
assert.deepEqual(emptyUnallowlistResult, { disabledCount: 0 }, '空 payload 解除白名单请求也必须正常解包')

console.log('客户端 IP 白名单 API request-capture 回归通过：路径、body、无 query 与响应解包契约正确')

function assertPolicyRequest(
  request: CapturedRequest,
  action: 'allowlist' | 'unallowlist',
  expectedBody: Record<string, unknown>
): void {
  assert.equal(request.method, 'POST', `${action} 必须发送 POST`)
  assert.equal(request.url, `/ip-stats/${ipHash}/${action}`, `${action} 必须请求固定客户端 IP 策略路径`)
  assert.equal(request.params, undefined, `${action} 不得发送 query`)
  assert.deepEqual(request.body, expectedBody, `${action} 必须原样发送 reason body`)
}

function parseRequestBody(data: AxiosRequestConfig['data']): unknown {
  if (typeof data === 'string') {
    return JSON.parse(data) as unknown
  }
  return data
}

function copyParams(params: unknown): Record<string, unknown> | undefined {
  return isRecord(params) ? { ...params } : undefined
}

function isRecord(value: unknown): value is Record<string, unknown> {
  return typeof value === 'object' && value !== null && !Array.isArray(value)
}
