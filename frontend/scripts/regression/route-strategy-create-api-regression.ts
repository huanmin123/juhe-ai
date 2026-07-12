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
const routeStrategyId = 'route_strategy_update_regression'
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

  await routeStrategiesApi.update(routeStrategyId, payload, { systemAccountId })
  await myRouteStrategiesApi.update(routeStrategyId, payload)
  await managementApi.update(routeStrategyId, payload, { systemAccountId })
  await personalApi.update(routeStrategyId, payload, { systemAccountId: 'must_not_leak' })

  await routeStrategiesApi.delete(routeStrategyId)
  await routeStrategiesApi.delete(routeStrategyId, { systemAccountId })
  await myRouteStrategiesApi.delete(routeStrategyId)
  await managementApi.delete(routeStrategyId, { systemAccountId })
  await personalApi.delete(routeStrategyId, { systemAccountId: 'must_not_leak' })
} finally {
  http.defaults.adapter = originalAdapter
}

assert.equal(capturedRequests.length, 13, '应捕获创建/更新各四个及删除五个底层 API / 作用域委派请求')

assertManagementCreate(capturedRequests[0], 'routeStrategiesApi.create')
assertPersonalCreate(capturedRequests[1], 'myRouteStrategiesApi.create')
assertManagementCreate(capturedRequests[2], 'useScopedRouteStrategiesApi 管理作用域')
assertPersonalCreate(capturedRequests[3], 'useScopedRouteStrategiesApi 个人作用域')
assertManagementUpdate(capturedRequests[4], 'routeStrategiesApi.update')
assertPersonalUpdate(capturedRequests[5], 'myRouteStrategiesApi.update')
assertManagementUpdate(capturedRequests[6], 'useScopedRouteStrategiesApi 管理作用域 update')
assertPersonalUpdate(capturedRequests[7], 'useScopedRouteStrategiesApi 个人作用域 update')
assertManagementDelete(capturedRequests[8], undefined, 'routeStrategiesApi.delete 全局管理')
assertManagementDelete(
  capturedRequests[9],
  { systemAccountId },
  'routeStrategiesApi.delete 显式 owner'
)
assertPersonalDelete(capturedRequests[10], 'myRouteStrategiesApi.delete')
assertManagementDelete(
  capturedRequests[11],
  { systemAccountId },
  'useScopedRouteStrategiesApi 管理作用域 delete'
)
assertPersonalDelete(capturedRequests[12], 'useScopedRouteStrategiesApi 个人作用域 delete')

console.log('策略路由创建/更新/删除 API request-capture 回归通过：管理/个人路径、作用域 query 和请求 body 契约正确')

function assertManagementCreate(request: CapturedRequest, source: string): void {
  assert.equal(request.method, 'POST', `${source} 必须发送 POST`)
  assert.equal(request.url, '/route-strategies', `${source} 必须请求 /route-strategies`)
  assert.deepEqual(
    request.params,
    { systemAccountId },
    `${source} 必须保留 systemAccountId query`
  )
  assertMutationBody(request.body, source, '创建')
}

function assertPersonalCreate(request: CapturedRequest, source: string): void {
  assert.equal(request.method, 'POST', `${source} 必须发送 POST`)
  assert.equal(request.url, '/my-route-strategies', `${source} 必须请求 /my-route-strategies`)
  assert.equal(request.params, undefined, `${source} 不得发送任何 query`)
  assertMutationBody(request.body, source, '创建')
}

function assertManagementUpdate(request: CapturedRequest, source: string): void {
  assert.equal(request.method, 'PATCH', `${source} 必须发送 PATCH`)
  assert.equal(
    request.url,
    `/route-strategies/${routeStrategyId}`,
    `${source} 必须请求固定管理端策略路由路径`
  )
  assert.deepEqual(
    request.params,
    { systemAccountId },
    `${source} 必须仅发送 systemAccountId query`
  )
  assertMutationBody(request.body, source, '更新')
}

function assertPersonalUpdate(request: CapturedRequest, source: string): void {
  assert.equal(request.method, 'PATCH', `${source} 必须发送 PATCH`)
  assert.equal(
    request.url,
    `/my-route-strategies/${routeStrategyId}`,
    `${source} 必须请求固定个人策略路由路径`
  )
  assert.equal(request.params, undefined, `${source} 不得发送任何 query`)
  assertMutationBody(request.body, source, '更新')
}

function assertManagementDelete(
  request: CapturedRequest,
  params: Record<string, unknown> | undefined,
  source: string
): void {
  assert.equal(request.method, 'DELETE', `${source} 必须发送 DELETE`)
  assert.equal(
    request.url,
    `/route-strategies/${routeStrategyId}`,
    `${source} 必须请求固定管理端策略路由路径`
  )
  assert.deepEqual(request.params, params, `${source} 必须发送正确管理作用域 query`)
  assert.equal(request.body, undefined, `${source} 不得发送 body`)
}

function assertPersonalDelete(request: CapturedRequest, source: string): void {
  assert.equal(request.method, 'DELETE', `${source} 必须发送 DELETE`)
  assert.equal(
    request.url,
    `/my-route-strategies/${routeStrategyId}`,
    `${source} 必须请求固定个人策略路由路径`
  )
  assert.equal(request.params, undefined, `${source} 必须忽略 owner 且不得发送任何 query`)
  assert.equal(request.body, undefined, `${source} 不得发送 body`)
}

function assertMutationBody(body: unknown, source: string, action: '创建' | '更新'): void {
  assert.deepEqual(body, payload, `${source} 必须原样发送策略路由${action} body`)
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
