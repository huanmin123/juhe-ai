import type {
  ApiKeyHybridRoutingConfig,
  RouteStrategyListResult,
  RouteStrategyMode,
  RouteStrategyOptionSummary,
  RouteStrategyGroupBindingStatus,
  RouteStrategyStatus,
  RouteStrategySummary
} from '@/types/domain'
import type { ListParams } from '../contracts'
import { http, unwrap } from '../http'
import { stripSystemAccountParam } from '../params'

export interface RouteStrategyListParams extends ListParams {
  page?: number
  pageSize?: number
  keyword?: string
  status?: RouteStrategyStatus | 'all'
  mode?: RouteStrategyMode | 'all'
}

export interface RouteStrategyOptionsParams extends ListParams {
  keyword?: string
  ids?: string[]
  limit?: number
  activeOnly?: boolean
}

export interface RouteStrategyMutationPayload {
  name?: string
  description?: string | null
  mode?: RouteStrategyMode
  status?: RouteStrategyStatus
  groupBindings?: Array<{
    groupId: string
    priority?: number
    weight?: number
    status?: RouteStrategyGroupBindingStatus
  }>
  hybridRoutingConfig?: ApiKeyHybridRoutingConfig | null
}

export const routeStrategiesApi = {
  list: (params?: RouteStrategyListParams) => unwrap<RouteStrategyListResult>(http.get('/route-strategies', { params })),
  options: (params?: RouteStrategyOptionsParams) => unwrap<RouteStrategyOptionSummary[]>(http.get('/route-strategies/options', { params })),
  detail: (id: string, params?: ListParams) => unwrap<RouteStrategySummary>(http.get(`/route-strategies/${id}`, { params })),
  create: (payload: RouteStrategyMutationPayload, params?: ListParams) => unwrap<RouteStrategySummary>(http.post('/route-strategies', payload, { params })),
  update: (id: string, payload: RouteStrategyMutationPayload, params?: ListParams) => unwrap<RouteStrategySummary>(http.patch(`/route-strategies/${id}`, payload, { params })),
  delete: (id: string, params?: ListParams) => http.delete(`/route-strategies/${id}`, { params })
}

export const myRouteStrategiesApi = {
  list: (params?: RouteStrategyListParams) => unwrap<RouteStrategyListResult>(http.get('/my-route-strategies', { params: stripSystemAccountParam(params) })),
  options: (params?: RouteStrategyOptionsParams) => unwrap<RouteStrategyOptionSummary[]>(http.get('/my-route-strategies/options', { params: stripSystemAccountParam(params) })),
  detail: (id: string) => unwrap<RouteStrategySummary>(http.get(`/my-route-strategies/${id}`)),
  create: (payload: RouteStrategyMutationPayload) => unwrap<RouteStrategySummary>(http.post('/my-route-strategies', payload)),
  update: (id: string, payload: RouteStrategyMutationPayload) => unwrap<RouteStrategySummary>(http.patch(`/my-route-strategies/${id}`, payload)),
  delete: (id: string) => http.delete(`/my-route-strategies/${id}`)
}
