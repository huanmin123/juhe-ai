import type {
  ResponseInspectionPolicyDetail,
  ResponseInspectionPolicyListResult,
  ResponseInspectionPolicyOverview,
  ResponseInspectionPolicyProviderOption
} from '@/types/domain'
import type {
  RequestControlOptions,
  ResponseInspectionPolicyCreatePayload,
  ResponseInspectionPolicyPatchPayload,
  ResponseInspectionPolicyProviderOptionsParams
} from '../contracts'
import { http, unwrap } from '../http'

export const responseInspectionPoliciesApi = {
  list: (options?: RequestControlOptions) => unwrap<ResponseInspectionPolicyListResult>(http.get('/response-inspection-policies', options)),
  detail: (id: string, options?: RequestControlOptions) => unwrap<ResponseInspectionPolicyDetail>(http.get(`/response-inspection-policies/${encodeURIComponent(id)}`, options)),
  providerOptions: (params: ResponseInspectionPolicyProviderOptionsParams, options?: RequestControlOptions) => unwrap<ResponseInspectionPolicyProviderOption[]>(http.get('/response-inspection-policies/provider-options', { params, signal: options?.signal })),
  create: (payload: ResponseInspectionPolicyCreatePayload) => unwrap<ResponseInspectionPolicyOverview>(http.post('/response-inspection-policies', payload)),
  update: (id: string, payload: ResponseInspectionPolicyPatchPayload) => unwrap<ResponseInspectionPolicyOverview>(http.patch(`/response-inspection-policies/${encodeURIComponent(id)}`, payload)),
  delete: (id: string) => http.delete(`/response-inspection-policies/${encodeURIComponent(id)}`)
}
