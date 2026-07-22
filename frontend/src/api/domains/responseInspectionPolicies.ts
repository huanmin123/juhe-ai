import type {
  ResponseInspectionPolicyDetail,
  ResponseInspectionPolicyListResult,
  ResponseInspectionPolicyProviderOption
} from '@/types/domain'
import type { RequestControlOptions, ResponseInspectionPolicyPayload } from '../contracts'
import { http, unwrap } from '../http'

export const responseInspectionPoliciesApi = {
  list: (options?: RequestControlOptions) => unwrap<ResponseInspectionPolicyListResult>(http.get('/response-inspection-policies', options)),
  detail: (id: string, options?: RequestControlOptions) => unwrap<ResponseInspectionPolicyDetail>(http.get(`/response-inspection-policies/${encodeURIComponent(id)}`, options)),
  providerOptions: (options?: RequestControlOptions) => unwrap<ResponseInspectionPolicyProviderOption[]>(http.get('/response-inspection-policies/provider-options', options)),
  create: (payload: ResponseInspectionPolicyPayload) => unwrap<ResponseInspectionPolicyDetail>(http.post('/response-inspection-policies', payload)),
  update: (id: string, payload: ResponseInspectionPolicyPayload) => unwrap<ResponseInspectionPolicyDetail>(http.put(`/response-inspection-policies/${encodeURIComponent(id)}`, payload)),
  delete: (id: string) => http.delete(`/response-inspection-policies/${encodeURIComponent(id)}`)
}
