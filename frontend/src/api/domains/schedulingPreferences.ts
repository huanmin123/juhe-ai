import type {
  SchedulingPreferenceChatMode,
  SchedulingPreferenceDetail,
  SchedulingPreferenceListResult,
  SchedulingPreferenceMutationResult,
  SchedulingPreferenceOptionSummary,
  SchedulingPreferenceSpeedFirstConfig,
  SchedulingPreferenceSummary
} from '@/types/domain'
import { http, unwrap } from '../http'
import { stripSystemAccountParam } from '../params'

/** 全局内置调度偏好：对话·成本优先。由 maintenance --seed 幂等建立，所有 owner 只读可绑定，任何写入口 403。 */
export const SCHEDULING_PREFERENCE_BUILTIN_CHAT_COST_FIRST_ID = 'schpref-builtin-chat-cost-first'
export const SCHEDULING_PREFERENCE_BUILTIN_CHAT_COST_FIRST_LABEL = '系统默认（对话·成本优先）'

export interface SchedulingPreferenceListParams {
  /** 管理面 owner 收窄；缺省或 all 为全局视图。个人面忽略该参数。 */
  systemAccountId?: string
  page?: number
  pageSize?: number
  keyword?: string
  chatMode?: SchedulingPreferenceChatMode | 'all'
  status?: 'enabled' | 'disabled' | 'all'
}

export interface SchedulingPreferenceOptionsParams {
  systemAccountId?: string
  ids?: string[]
  keyword?: string
  limit?: number
  activeOnly?: boolean
}

export interface SchedulingPreferenceScopeParams {
  systemAccountId?: string
}

export interface SchedulingPreferenceCreateScopeParams {
  /** 管理面创建必填目标 owner（不支持 all）；个人面 owner 固定为当前登录用户。 */
  systemAccountId: string
}

export interface SchedulingPreferenceCreatePayload {
  name: string
  chatMode: SchedulingPreferenceChatMode
  chatSpeedFirstConfig?: SchedulingPreferenceSpeedFirstConfig
}

export interface SchedulingPreferencePatchPayload {
  name?: string
  chatMode?: SchedulingPreferenceChatMode
  chatSpeedFirstConfig?: SchedulingPreferenceSpeedFirstConfig
  enabled?: boolean
  expectedUpdatedAt: string
}

export const schedulingPreferencesApi = {
  list: (params?: SchedulingPreferenceListParams) => unwrap<SchedulingPreferenceListResult>(http.get('/scheduling-preferences', { params })),
  options: (params?: SchedulingPreferenceOptionsParams) => unwrap<SchedulingPreferenceOptionSummary[]>(http.get('/scheduling-preferences/options', { params })),
  detail: (id: string, params?: SchedulingPreferenceScopeParams) => unwrap<SchedulingPreferenceDetail>(http.get(`/scheduling-preferences/${encodeURIComponent(id)}`, { params })),
  create: (payload: SchedulingPreferenceCreatePayload, params: SchedulingPreferenceCreateScopeParams) => unwrap<SchedulingPreferenceSummary>(http.post('/scheduling-preferences', payload, { params })),
  update: (id: string, payload: SchedulingPreferencePatchPayload, params?: SchedulingPreferenceScopeParams) => unwrap<SchedulingPreferenceMutationResult>(http.patch(`/scheduling-preferences/${encodeURIComponent(id)}`, payload, { params })),
  delete: (id: string, params?: SchedulingPreferenceScopeParams) => http.delete(`/scheduling-preferences/${encodeURIComponent(id)}`, { params })
}

export const mySchedulingPreferencesApi = {
  list: (params?: Omit<SchedulingPreferenceListParams, 'systemAccountId'>) => unwrap<SchedulingPreferenceListResult>(http.get('/my-scheduling-preferences', { params: stripSystemAccountParam(params) })),
  options: (params?: Omit<SchedulingPreferenceOptionsParams, 'systemAccountId'>) => unwrap<SchedulingPreferenceOptionSummary[]>(http.get('/my-scheduling-preferences/options', { params: stripSystemAccountParam(params) })),
  detail: (id: string) => unwrap<SchedulingPreferenceDetail>(http.get(`/my-scheduling-preferences/${encodeURIComponent(id)}`)),
  create: (payload: SchedulingPreferenceCreatePayload) => unwrap<SchedulingPreferenceSummary>(http.post('/my-scheduling-preferences', payload)),
  update: (id: string, payload: SchedulingPreferencePatchPayload) => unwrap<SchedulingPreferenceMutationResult>(http.patch(`/my-scheduling-preferences/${encodeURIComponent(id)}`, payload)),
  delete: (id: string) => http.delete(`/my-scheduling-preferences/${encodeURIComponent(id)}`)
}
