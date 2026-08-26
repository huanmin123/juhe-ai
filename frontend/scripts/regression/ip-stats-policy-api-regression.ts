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

type IpStatsAction = 'detail' | 'blacklist' | 'allowlist' | 'unblock' | 'unallowlist'

const ipHashes = {
  detail: 'detail/client?scope=admin#01',
  blacklistMinutes: 'blacklist/minutes?duration=30#02',
  blacklistPermanent: 'blacklist/permanent?duration=none#03',
  blacklistDays: 'blacklist/days?duration=1#04',
  allowlistReason: 'allowlist/reason?source=regression#05',
  unblockReason: 'unblock/reason?source=regression#06',
  unallowlistReason: 'unallowlist/reason?source=regression#07',
  allowlistEmpty: 'allowlist/empty?source=ui#08',
  unblockEmpty: 'unblock/empty?source=ui#09',
  unallowlistEmpty: 'unallowlist/empty?source=ui#10'
} as const
const reason = '管理员确认该客户端 IP 可正常访问'
const listParams = {
  page: 2,
  pageSize: 20,
  keyword: '203.0.113.',
  status: 'blacklisted',
  startDate: '2026-07-08',
  endDate: '2026-07-14',
  sortField: 'requestCount',
  sortOrder: 'desc'
} as const
const detailParams = {
  page: 2,
  pageSize: 25,
  startDate: '2026-07-01',
  endDate: '2026-07-14',
  sortField: 'errorCount',
  sortOrder: 'desc'
} as const
const blacklistMinutesPayload = {
  reason: '该客户端 IP 触发异常访问策略',
  durationMinutes: 30
}
const blacklistPermanentPayload = {}
const blacklistDaysPayload = { durationDays: 1 }
const unblockPayload = { reason: '管理员确认解除该客户端 IP 封禁' }
const responseFixtures = {
  list: {
    items: [{
      ipHash: 'client_ip_list_regression',
      aggregateIpKey: '203.0.113.0/24',
      lastSeenAt: '2026-07-14T08:30:00.000Z',
      status: 'blacklisted',
      rangeUsage: {
        requestCount: 12,
        successCount: 9,
        errorCount: 3,
        errorRate: 0.25,
        inputTokens: 1200,
        outputTokens: 300,
        cacheReadTokens: 80,
        cacheReadCost: 0.001,
        cacheWriteTokens: 40,
        cacheWrite1hTokens: 20,
        cacheWriteCost: 0.002,
        thinkingTokens: 10,
        inputImageTokens: 2,
        outputImageTokens: 1,
        totalTokens: 1500,
        totalCost: 0.03,
        activeDays: 5,
        averageDurationMs: 420.5,
        averageFirstTokenMs: 85.25,
        maxDurationMs: 900,
        lastUsedAt: '2026-07-14T08:25:00.000Z',
        lastErrorAt: '2026-07-13T17:00:00.000Z'
      }
    }],
    pageUpperBound: 21,
    hasMore: false,
    page: 2,
    pageSize: 20,
    range: {
      startDate: listParams.startDate,
      endDate: listParams.endDate,
      days: 7,
      maxDays: 31
    },
    rangeReady: true
  },
  detail: {
    ipHash: ipHashes.detail,
    aggregateIpKey: 'client_ip_detail_regression',
    items: []
  },
  blacklistMinutes: {
    id: 'client_ip_policy_blacklist_minutes_regression',
    ipHash: ipHashes.blacklistMinutes,
    policyType: 'blacklist',
    status: 'active',
    reason: blacklistMinutesPayload.reason
  },
  blacklistPermanent: {
    id: 'client_ip_policy_blacklist_permanent_regression',
    ipHash: ipHashes.blacklistPermanent,
    policyType: 'blacklist',
    status: 'active'
  },
  blacklistDays: {
    id: 'client_ip_policy_blacklist_days_regression',
    ipHash: ipHashes.blacklistDays,
    policyType: 'blacklist',
    status: 'active',
    expiresAt: '2026-07-15T00:00:00.000Z'
  },
  allowlistReason: {
    id: 'client_ip_policy_allowlist_reason_regression',
    ipHash: ipHashes.allowlistReason,
    policyType: 'allowlist',
    status: 'active',
    reason
  },
  unblockReason: { disabledCount: 0 },
  unallowlistReason: { disabledCount: 0 },
  allowlistEmpty: {
    id: 'client_ip_policy_allowlist_empty_regression',
    ipHash: ipHashes.allowlistEmpty,
    policyType: 'allowlist',
    status: 'active'
  },
  unblockEmpty: { disabledCount: 1 },
  unallowlistEmpty: { disabledCount: 0 }
} as const
const mockResponsesByUrl = new Map<string, unknown>([
  ['/ip-stats', responseFixtures.list],
  [ipStatsPath(ipHashes.detail, 'detail'), responseFixtures.detail],
  [ipStatsPath(ipHashes.blacklistMinutes, 'blacklist'), responseFixtures.blacklistMinutes],
  [ipStatsPath(ipHashes.blacklistPermanent, 'blacklist'), responseFixtures.blacklistPermanent],
  [ipStatsPath(ipHashes.blacklistDays, 'blacklist'), responseFixtures.blacklistDays],
  [ipStatsPath(ipHashes.allowlistReason, 'allowlist'), responseFixtures.allowlistReason],
  [ipStatsPath(ipHashes.unblockReason, 'unblock'), responseFixtures.unblockReason],
  [ipStatsPath(ipHashes.unallowlistReason, 'unallowlist'), responseFixtures.unallowlistReason],
  [ipStatsPath(ipHashes.allowlistEmpty, 'allowlist'), responseFixtures.allowlistEmpty],
  [ipStatsPath(ipHashes.unblockEmpty, 'unblock'), responseFixtures.unblockEmpty],
  [ipStatsPath(ipHashes.unallowlistEmpty, 'unallowlist'), responseFixtures.unallowlistEmpty]
])
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
  const responseData = mockResponsesByUrl.get(url)
  assert.notEqual(responseData, undefined, `未配置请求路径 ${url} 的独立 mock 响应`)

  return {
    data: {
      data: responseData
    },
    status: 200,
    statusText: 'OK',
    headers: {},
    config
  }
}

let detailResult: Awaited<ReturnType<typeof ipStatsApi.detail>>
let blacklistMinutesResult: Awaited<ReturnType<typeof ipStatsApi.blacklist>>
let blacklistPermanentResult: Awaited<ReturnType<typeof ipStatsApi.blacklist>>
let blacklistDaysResult: Awaited<ReturnType<typeof ipStatsApi.blacklist>>
let allowlistReasonResult: Awaited<ReturnType<typeof ipStatsApi.allowlist>>
let unblockReasonResult: Awaited<ReturnType<typeof ipStatsApi.unblock>>
let unallowlistReasonResult: Awaited<ReturnType<typeof ipStatsApi.unallowlist>>
let emptyAllowlistResult: Awaited<ReturnType<typeof ipStatsApi.allowlist>>
let emptyUnblockResult: Awaited<ReturnType<typeof ipStatsApi.unblock>>
let emptyUnallowlistResult: Awaited<ReturnType<typeof ipStatsApi.unallowlist>>
let listResult: Awaited<ReturnType<typeof ipStatsApi.list>>

try {
  http.defaults.adapter = requestCaptureAdapter

  detailResult = await ipStatsApi.detail(ipHashes.detail, detailParams)
  blacklistMinutesResult = await ipStatsApi.blacklist(ipHashes.blacklistMinutes, blacklistMinutesPayload)
  blacklistPermanentResult = await ipStatsApi.blacklist(ipHashes.blacklistPermanent, blacklistPermanentPayload)
  blacklistDaysResult = await ipStatsApi.blacklist(ipHashes.blacklistDays, blacklistDaysPayload)
  allowlistReasonResult = await ipStatsApi.allowlist(ipHashes.allowlistReason, { reason })
  unblockReasonResult = await ipStatsApi.unblock(ipHashes.unblockReason, unblockPayload)
  unallowlistReasonResult = await ipStatsApi.unallowlist(ipHashes.unallowlistReason, { reason })
  emptyAllowlistResult = await ipStatsApi.allowlist(ipHashes.allowlistEmpty, {})
  emptyUnblockResult = await ipStatsApi.unblock(ipHashes.unblockEmpty, {})
  emptyUnallowlistResult = await ipStatsApi.unallowlist(ipHashes.unallowlistEmpty, {})
  listResult = await ipStatsApi.list(listParams)
} finally {
  http.defaults.adapter = originalAdapter
}

assert.equal(capturedRequests.length, 11, '应捕获列表、详情、三种封禁 payload 及白名单/解封的 reason 与空 payload 请求')
assertDetailRequest(capturedRequests[0], ipHashes.detail)
assertPolicyRequest(capturedRequests[1], 'blacklist', ipHashes.blacklistMinutes, blacklistMinutesPayload, '分钟封禁')
assertPolicyRequest(capturedRequests[2], 'blacklist', ipHashes.blacklistPermanent, blacklistPermanentPayload, '永久封禁')
assertPolicyRequest(capturedRequests[3], 'blacklist', ipHashes.blacklistDays, blacklistDaysPayload, '按天封禁')
assertPolicyRequest(capturedRequests[4], 'allowlist', ipHashes.allowlistReason, { reason }, '带原因加入白名单')
assertPolicyRequest(capturedRequests[5], 'unblock', ipHashes.unblockReason, unblockPayload, '带原因解除封禁')
assertPolicyRequest(capturedRequests[6], 'unallowlist', ipHashes.unallowlistReason, { reason }, '带原因移出白名单')
assertPolicyRequest(capturedRequests[7], 'allowlist', ipHashes.allowlistEmpty, {}, '空 payload 加入白名单')
assertPolicyRequest(capturedRequests[8], 'unblock', ipHashes.unblockEmpty, {}, '空 payload 解除封禁')
assertPolicyRequest(capturedRequests[9], 'unallowlist', ipHashes.unallowlistEmpty, {}, '空 payload 移出白名单')
assertListRequest(capturedRequests[10])

assert.deepEqual(listResult, responseFixtures.list, '列表接口必须解包自己的独立 data 响应')
assert.deepEqual(detailResult, responseFixtures.detail, '详情接口必须解包自己的 data 响应')
assert.deepEqual(blacklistMinutesResult, responseFixtures.blacklistMinutes, '分钟封禁接口必须解包自己的 data 响应')
assert.deepEqual(blacklistPermanentResult, responseFixtures.blacklistPermanent, '永久封禁接口必须解包自己的 data 响应')
assert.deepEqual(blacklistDaysResult, responseFixtures.blacklistDays, '按天封禁接口必须解包自己的 data 响应')
assert.deepEqual(allowlistReasonResult, responseFixtures.allowlistReason, '带原因加入白名单必须解包自己的 data 响应')
assert.deepEqual(unblockReasonResult, responseFixtures.unblockReason, '带原因解除封禁的 disabledCount=0 必须正常解包')
assert.deepEqual(unallowlistReasonResult, responseFixtures.unallowlistReason, '带原因移出白名单的 disabledCount=0 必须正常解包')
assert.deepEqual(emptyAllowlistResult, responseFixtures.allowlistEmpty, '空 payload 加入白名单必须解包自己的 data 响应')
assert.deepEqual(emptyUnblockResult, responseFixtures.unblockEmpty, '空 payload 解除封禁必须解包自己的 data 响应')
assert.deepEqual(emptyUnallowlistResult, responseFixtures.unallowlistEmpty, '空 payload 移出白名单必须解包自己的 data 响应')

console.log('客户端 IP 列表/详情/策略 API request-capture 回归通过：完整列表 query、独立编码路径、UI 默认/边界 body 与响应解包契约正确')

function assertListRequest(request: CapturedRequest): void {
  assert.equal(request.method, 'GET', 'list 必须发送 GET')
  assert.equal(request.url, '/ip-stats', 'list 必须请求客户端 IP 统计列表路径')
  assert.deepEqual(request.params, listParams, 'list 必须原样发送页面使用的完整筛选、范围与排序 query')
  assert.equal(request.body, undefined, 'list 不得发送 body')
}

function assertDetailRequest(request: CapturedRequest, ipHash: string): void {
  assert.equal(request.method, 'GET', 'detail 必须发送 GET')
  assert.equal(request.url, ipStatsPath(ipHash, 'detail'), 'detail 必须使用 encodeURIComponent 编码 IP 哈希后请求详情路径')
  assert.deepEqual(request.params, detailParams, 'detail 必须原样发送分页、日期与排序 query')
  assert.equal(request.body, undefined, 'detail 不得发送 body')
}

function assertPolicyRequest(
  request: CapturedRequest,
  action: Exclude<IpStatsAction, 'detail'>,
  ipHash: string,
  expectedBody: Record<string, unknown>,
  scenario: string
): void {
  assert.equal(request.method, 'POST', `${scenario}必须发送 POST`)
  assert.equal(request.url, ipStatsPath(ipHash, action), `${scenario}必须使用 encodeURIComponent 编码 IP 哈希后请求策略路径`)
  assert.equal(request.params, undefined, `${scenario}不得发送 query`)
  assert.deepEqual(request.body, expectedBody, `${scenario}必须发送精确 payload body`)
}

function ipStatsPath(ipHash: string, action: IpStatsAction): string {
  return `/ip-stats/${encodeURIComponent(ipHash)}/${action}`
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
