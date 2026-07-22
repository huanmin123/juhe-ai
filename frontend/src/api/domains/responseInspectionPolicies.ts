import type { ResponseInspectionPolicyListResult, ResponseInspectionPolicySummary } from '@/types/domain'
import type { ResponseInspectionPolicyPayload } from '../contracts'
import { http, unwrap } from '../http'

export const responseInspectionPoliciesApi = {
  list: () => unwrap<ResponseInspectionPolicyListResult>(http.get('/response-inspection-policies')),
  create: (payload: ResponseInspectionPolicyPayload) => unwrap<ResponseInspectionPolicySummary>(http.post('/response-inspection-policies', payload)),
  update: (id: string, payload: ResponseInspectionPolicyPayload) => unwrap<ResponseInspectionPolicySummary>(http.put(`/response-inspection-policies/${id}`, payload)),
  delete: (id: string) => http.delete(`/response-inspection-policies/${id}`)
}
