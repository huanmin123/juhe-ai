import { strict as assert } from 'node:assert'

import type { AxiosAdapter, AxiosRequestConfig } from 'axios'
import { ref } from 'vue'

import {
  myRouteStrategiesApi,
  routeStrategiesApi,
  type RouteStrategyMutationPayload
} from '../../src/api/domains/routeStrategies'
import { http } from '../../src/api/http'
import { useScopedRouteStrategiesApi } from '../../src/composables/useScopedDomainApi'

interface CapturedRequest {
  method: string
  url: string
  params?: Record<string, unknown>
  body: unknown
}

const systemAccountId = 'system_account_route_strategy_regression'
const payload: RouteStrategyMutationPayload = {
  name: '策略路由创建 HTTP 契约回归',
  description: null,
  mode: 'hybrid_smart',
  status: 'active',
  groupBindings: [
    {
      groupId: 'group_primary',
      priority: 1,
      weight: 70,
      status: 'active'
    },
    {
      groupId: 'group_fallback',
      priority: 2,
      weight: 30,
      status: 'disabled'
    }
  ],
  normalRoutingConfig: null,
  hybridRoutingConfig: null
}
const capturedRequests: CapturedRequest[] = []
const originalAdapter = http.defaults.adapter

const requestCaptureAdapter: AxiosAdapter = async (config) => {
  capturedRequests.push({
    method: String(config.method ?? '').toUpperCase(),
    url: String(config.url ?? ''),
    params: copyParams(config.params),
    body: parseRequestBody(config.data)
  })

  return {
    data: {
      data: {
        id: `route_strategy_${capturedRequests.length}`
      }
    },
    status: 200,
    statusText: 'OK',
    headers: {},
    config
  }
}

try {
  http.defaults.adapter = requestCaptureAdapter

  await routeStrategiesApi.create(payload, { systemAccountId })
  await myRouteStrategiesApi.create(payload)

  const managementApi = useScopedRouteStrategiesApi(ref(true))
  await managementApi.create(payload, { systemAccountId })

  const personalApi = useScopedRouteStrategiesApi(ref(false))
  await personalApi.create(payload, { systemAccountId: 'must_not_leak' })
} finally {
  http.defaults.adapter = originalAdapter
}

assert.equal(capturedRequests.length, 4, '应捕获两个底层 API 和两个作用域委派请求')

assertManagementCreate(capturedRequests[0], 'routeStrategiesApi.create')
assertPersonalCreate(capturedRequests[1], 'myRouteStrategiesApi.create')
assertManagementCreate(capturedRequests[2], 'useScopedRouteStrategiesApi 管理作用域')
assertPersonalCreate(capturedRequests[3], 'useScopedRouteStrategiesApi 个人作用域')

console.log('策略路由创建 API request-capture 回归通过：管理/个人路径、作用域 query 和显式 null body 契约正确')

function assertManagementCreate(request: CapturedRequest, source: string): void {
  assert.equal(request.method, 'POST', `${source} 必须发送 POST`)
  assert.equal(request.url, '/route-strategies', `${source} 必须请求 /route-strategies`)
  assert.deepEqual(
    request.params,
    { systemAccountId },
    `${source} 必须保留 systemAccountId query`
  )
  assertCreateBody(request.body, source)
}

function assertPersonalCreate(request: CapturedRequest, source: string): void {
  assert.equal(request.method, 'POST', `${source} 必须发送 POST`)
  assert.equal(request.url, '/my-route-strategies', `${source} 必须请求 /my-route-strategies`)
  assert.equal(
    request.params?.systemAccountId,
    undefined,
    `${source} 不得发送 systemAccountId query`
  )
  assertCreateBody(request.body, source)
}

function assertCreateBody(body: unknown, source: string): void {
  assert.deepEqual(body, payload, `${source} 必须原样发送策略路由创建 body`)
  assert.ok(isRecord(body), `${source} body 必须是对象`)
  assert.ok(Object.hasOwn(body, 'normalRoutingConfig'), `${source} 必须保留 normalRoutingConfig 字段`)
  assert.equal(body.normalRoutingConfig, null, `${source} 必须保留 normalRoutingConfig: null`)
  assert.ok(Object.hasOwn(body, 'hybridRoutingConfig'), `${source} 必须保留 hybridRoutingConfig 字段`)
  assert.equal(body.hybridRoutingConfig, null, `${source} 必须保留 hybridRoutingConfig: null`)
  assert.deepEqual(body.groupBindings, payload.groupBindings, `${source} 必须原样保留 groupBindings`)
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
