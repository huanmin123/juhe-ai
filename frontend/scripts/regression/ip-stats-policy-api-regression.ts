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

const ipHash = 'client/ip?scope=admin#detail'
const encodedIpHash = 'client%2Fip%3Fscope%3Dadmin%23detail'
const reason = '管理员确认该客户端 IP 可正常访问'
const detailParams = {
  page: 2,
  pageSize: 25,
  startDate: '2026-07-01',
  endDate: '2026-07-14',
  sortField: 'errorCount',
  sortOrder: 'desc'
} as const
const blacklistPayload = {
  reason: '该客户端 IP 触发异常访问策略',
  durationMinutes: 30
}
const unblockPayload = { reason: '管理员确认解除该客户端 IP 封禁' }
const capturedRequests: CapturedRequest[] = []
const originalAdapter = http.defaults.adapter

const requestCaptureAdapter: AxiosAdapter = async (config) => {
  const url = String(config.url ?? '')
  capturedRequests.push({
    method: String(config.method ?? '').toUpperCase(),
    url,
    params: copyParams(config.params),
    body: parseRequestBody(config.data)
  })

  return {
    data: {
      data: responseDataFor(url)
    },
    status: 200,
    statusText: 'OK',
    headers: {},
    config
  }
}

let detailResult: Awaited<ReturnType<typeof ipStatsApi.detail>>
let blacklistResult: Awaited<ReturnType<typeof ipStatsApi.blacklist>>
let allowlistResult: Awaited<ReturnType<typeof ipStatsApi.allowlist>>
let unblockResult: Awaited<ReturnType<typeof ipStatsApi.unblock>>
let unallowlistResult: Awaited<ReturnType<typeof ipStatsApi.unallowlist>>
let emptyAllowlistResult: Awaited<ReturnType<typeof ipStatsApi.allowlist>>
let emptyUnallowlistResult: Awaited<ReturnType<typeof ipStatsApi.unallowlist>>

try {
  http.defaults.adapter = requestCaptureAdapter

  detailResult = await ipStatsApi.detail(ipHash, detailParams)
  blacklistResult = await ipStatsApi.blacklist(ipHash, blacklistPayload)
  allowlistResult = await ipStatsApi.allowlist(ipHash, { reason })
  unblockResult = await ipStatsApi.unblock(ipHash, unblockPayload)
  unallowlistResult = await ipStatsApi.unallowlist(ipHash, { reason })
  emptyAllowlistResult = await ipStatsApi.allowlist(ipHash, {})
  emptyUnallowlistResult = await ipStatsApi.unallowlist(ipHash, {})
} finally {
  http.defaults.adapter = originalAdapter
}

assert.equal(capturedRequests.length, 7, '应捕获详情及四种策略接口，并保留白名单操作空 payload 回归')
assertDetailRequest(capturedRequests[0])
assertPolicyRequest(capturedRequests[1], 'blacklist', blacklistPayload)
assertPolicyRequest(capturedRequests[2], 'allowlist', { reason })
assertPolicyRequest(capturedRequests[3], 'unblock', unblockPayload)
assertPolicyRequest(capturedRequests[4], 'unallowlist', { reason })
assertPolicyRequest(capturedRequests[5], 'allowlist', {})
assertPolicyRequest(capturedRequests[6], 'unallowlist', {})

assert.equal(detailResult.ipHash, ipHash, '详情接口必须解包 data 并保留响应中的 IP 哈希')
assert.equal(blacklistResult.id, 'client_ip_policy_blacklist_regression', '黑名单接口必须解包 data')
assert.equal(allowlistResult.id, 'client_ip_policy_allowlist_regression', '白名单接口必须解包 data')
assert.equal(allowlistResult.ipHash, ipHash, '白名单接口必须保留响应中的 IP 哈希')
assert.deepEqual(unblockResult, { disabledCount: 0 }, '解除封禁 disabledCount=0 也必须作为成功响应解包')
assert.deepEqual(unallowlistResult, { disabledCount: 0 }, '解除白名单 disabledCount=0 也必须作为成功响应解包')
assert.equal(emptyAllowlistResult.id, 'client_ip_policy_allowlist_regression', '空 payload 白名单请求也必须正常解包')
assert.deepEqual(emptyUnallowlistResult, { disabledCount: 0 }, '空 payload 解除白名单请求也必须正常解包')

console.log('客户端 IP 详情/策略 API request-capture 回归通过：方法、编码路径、query、body 与响应解包契约正确')

function assertDetailRequest(request: CapturedRequest): void {
  assert.equal(request.method, 'GET', 'detail 必须发送 GET')
  assert.equal(request.url, `/ip-stats/${encodedIpHash}/detail`, 'detail 必须编码 IP 哈希后请求详情路径')
  assert.deepEqual(request.params, detailParams, 'detail 必须原样发送分页、日期与排序 query')
  assert.equal(request.body, undefined, 'detail 不得发送 body')
}

function assertPolicyRequest(
  request: CapturedRequest,
  action: 'blacklist' | 'allowlist' | 'unblock' | 'unallowlist',
  expectedBody: Record<string, unknown>
): void {
  assert.equal(request.method, 'POST', `${action} 必须发送 POST`)
  assert.equal(request.url, `/ip-stats/${encodedIpHash}/${action}`, `${action} 必须编码 IP 哈希后请求策略路径`)
  assert.equal(request.params, undefined, `${action} 不得发送 query`)
  assert.deepEqual(request.body, expectedBody, `${action} 必须原样发送 payload body`)
}

function responseDataFor(url: string): unknown {
  if (url.endsWith('/detail')) {
    return {
      ipHash,
      aggregateIpKey: 'client_ip_detail_regression',
      items: []
    }
  }
  if (url.endsWith('/unblock') || url.endsWith('/unallowlist')) {
    return { disabledCount: 0 }
  }
  if (url.endsWith('/blacklist')) {
    return {
      id: 'client_ip_policy_blacklist_regression',
      ipHash,
      policyType: 'blacklist',
      status: 'active',
      reason: blacklistPayload.reason
    }
  }
  return {
    id: 'client_ip_policy_allowlist_regression',
    ipHash,
    policyType: 'allowlist',
    status: 'active',
    reason
  }
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
