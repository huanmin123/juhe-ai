import type {
  SecurityPolicyDetail,
  SecurityPolicyListItem,
  SecurityPolicyListResult,
  SecurityPolicyMutationResult,
  SecurityPolicyOptionSummary,
  SecurityPolicyRule
} from '@/types/domain'
import { http, unwrap } from '../http'
import { stripSystemAccountParam } from '../params'

/** 全局内置安全策略：规则集为空 = 不处理 = 请求原样转发。由 maintenance --seed 幂等建立，所有 owner 只读可绑定，任何写入口 403。此常量仅作 options 未加载时的回显兜底。 */
export const BUILTIN_SECURITY_POLICY_ID = 'secpol-builtin-default'
export const BUILTIN_SECURITY_POLICY_NAME = '默认安全策略'

export interface SecurityPolicyListParams {
  /** 管理面 owner 收窄；缺省或 all 为全局视图。个人面忽略该参数。 */
  systemAccountId?: string
  page?: number
  pageSize?: number
  keyword?: string
}

export interface SecurityPolicyOptionsParams {
  systemAccountId?: string
  ids?: string[]
  keyword?: string
  limit?: number
}

export interface SecurityPolicyScopeParams {
  systemAccountId?: string
}

export interface SecurityPolicyCreateScopeParams {
  /** 管理面创建必填目标 owner（不支持 all）；个人面 owner 固定为当前登录用户。 */
  systemAccountId: string
}

export interface SecurityPolicyCreatePayload {
  name: string
  description?: string
  rules: SecurityPolicyRule[]
}

export interface SecurityPolicyPatchPayload {
  name?: string
  description?: string
  rules?: SecurityPolicyRule[]
  expectedUpdatedAt: string
}

export const securityPoliciesApi = {
  list: (params?: SecurityPolicyListParams) => unwrap<SecurityPolicyListResult>(http.get('/security-policies', { params })),
  options: (params?: SecurityPolicyOptionsParams) => unwrap<SecurityPolicyOptionSummary[]>(http.get('/security-policies/options', { params })),
  detail: (id: string, params?: SecurityPolicyScopeParams) => unwrap<SecurityPolicyDetail>(http.get(`/security-policies/${encodeURIComponent(id)}`, { params })),
  create: (payload: SecurityPolicyCreatePayload, params: SecurityPolicyCreateScopeParams) => unwrap<SecurityPolicyListItem>(http.post('/security-policies', payload, { params })),
  update: (id: string, payload: SecurityPolicyPatchPayload, params?: SecurityPolicyScopeParams) => unwrap<SecurityPolicyMutationResult>(http.patch(`/security-policies/${encodeURIComponent(id)}`, payload, { params })),
  /** 删除总是成功（同调度偏好契约）：响应 data 含 resetBindings（事务内重置回内置默认策略的绑定 Key 数）。 */
  delete: (id: string, params?: SecurityPolicyScopeParams) => unwrap<{ resetBindings: number }>(http.delete(`/security-policies/${encodeURIComponent(id)}`, { params }))
}

export const mySecurityPoliciesApi = {
  list: (params?: Omit<SecurityPolicyListParams, 'systemAccountId'>) => unwrap<SecurityPolicyListResult>(http.get('/my-security-policies', { params: stripSystemAccountParam(params) })),
  options: (params?: Omit<SecurityPolicyOptionsParams, 'systemAccountId'>) => unwrap<SecurityPolicyOptionSummary[]>(http.get('/my-security-policies/options', { params: stripSystemAccountParam(params) })),
  detail: (id: string) => unwrap<SecurityPolicyDetail>(http.get(`/my-security-policies/${encodeURIComponent(id)}`)),
  create: (payload: SecurityPolicyCreatePayload) => unwrap<SecurityPolicyListItem>(http.post('/my-security-policies', payload)),
  update: (id: string, payload: SecurityPolicyPatchPayload) => unwrap<SecurityPolicyMutationResult>(http.patch(`/my-security-policies/${encodeURIComponent(id)}`, payload)),
  delete: (id: string) => unwrap<{ resetBindings: number }>(http.delete(`/my-security-policies/${encodeURIComponent(id)}`))
}
