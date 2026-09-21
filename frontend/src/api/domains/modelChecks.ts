import type {
  ModelCheckOptions,
  ModelCheckAccountOption,
  ModelCheckQuestionBankListParams,
  ModelCheckQuestionBankListResult,
  ModelCheckQuestionBankItem,
  ModelCheckQuestionBankMutationInput,
  ModelCheckQuestionBankOptionsResult,
  ModelCheckQuestionBankReviewInput,
  ModelCheckRunDetail,
  ModelCheckRunListParams,
  ModelCheckRunListResult,
  ModelCheckRunPayload,
  ModelCheckStopResult,
  ActiveModelCheckRunSummary,
  ModelQualityPolicy,
  ModelQualityPolicyUpdateInput,
  ModelQualitySchedule,
  ModelQualityScheduleListResult,
  ModelQualityScheduleMutationInput,
  ModelQualitySchedulePatchInput
} from '@/types/domain'
import type { ModelCheckScopeParams, ModelCheckStreamOptions } from '../contracts'
import { http, noTimeout, unwrap } from '../http'
import { runModelCheckStream } from '../modelCheckStream'
import { modelCheckRunListParams } from '../params'

export const modelChecksApi = {
  options: (params?: ModelCheckScopeParams) => unwrap<ModelCheckOptions>(http.get('/model-checks/options', { params })),
  accountOptions: (params: ModelCheckScopeParams & { purpose: 'run' | 'history' | 'schedule'; accountId?: string; keyword?: string; limit: number; selectedIds?: string[] }, options?: { signal?: AbortSignal }) => unwrap<ModelCheckAccountOption[]>(http.get('/model-checks/account-options', { params, signal: options?.signal })),
  active: (params?: ModelCheckScopeParams) => unwrap<ActiveModelCheckRunSummary | null>(http.get('/model-checks/run/active', { params })),
  run: (payload: ModelCheckRunPayload, params?: ModelCheckScopeParams) => unwrap<ModelCheckRunDetail>(http.post('/model-checks/run', payload, { ...noTimeout, params })),
  runStream: (payload: ModelCheckRunPayload, options?: ModelCheckStreamOptions, params?: ModelCheckScopeParams) => runModelCheckStream('/model-checks/run/stream', payload, options, params),
  stop: (params?: ModelCheckScopeParams) => unwrap<ModelCheckStopResult>(http.post('/model-checks/run/stop', {}, { params })),
  list: (params?: ModelCheckRunListParams) => unwrap<ModelCheckRunListResult>(http.get('/model-checks/runs', { params: modelCheckRunListParams(params) })),
  detail: (id: string, params?: ModelCheckScopeParams) => unwrap<ModelCheckRunDetail>(http.get(`/model-checks/runs/${id}`, { params })),
  qualityPolicy: (params?: ModelCheckScopeParams) => unwrap<ModelQualityPolicy>(http.get('/model-checks/quality-policy', { params })),
  saveQualityPolicy: (payload: ModelQualityPolicyUpdateInput, params?: ModelCheckScopeParams) => unwrap<ModelQualityPolicy>(http.patch('/model-checks/quality-policy', payload, { params })),
  qualitySchedules: (params?: ModelCheckScopeParams & { page?: number; pageSize?: number }) => unwrap<ModelQualityScheduleListResult>(http.get('/model-checks/quality-schedules', { params })),
  saveQualitySchedule: (payload: ModelQualityScheduleMutationInput, params?: ModelCheckScopeParams) => unwrap<ModelQualitySchedule>(http.post('/model-checks/quality-schedules', payload, { params })),
  patchQualitySchedule: (id: string, payload: ModelQualitySchedulePatchInput, params?: ModelCheckScopeParams) => unwrap<ModelQualitySchedule>(http.patch(`/model-checks/quality-schedules/${id}`, payload, { params })),
  deleteQualitySchedule: (id: string, params?: ModelCheckScopeParams) => unwrap<{ deleted: boolean }>(http.delete(`/model-checks/quality-schedules/${id}`, { params })),
  questionBankList: (params?: ModelCheckScopeParams & ModelCheckQuestionBankListParams) => unwrap<ModelCheckQuestionBankListResult>(http.get('/model-checks/question-bank', { params })),
  questionBankCreate: (payload: ModelCheckQuestionBankMutationInput, params?: ModelCheckScopeParams) => unwrap<ModelCheckQuestionBankItem>(http.post('/model-checks/question-bank', payload, { params })),
  questionBankDetail: (id: string, params?: ModelCheckScopeParams) => unwrap<ModelCheckQuestionBankItem>(http.get(`/model-checks/question-bank/${id}`, { params })),
  questionBankUpdate: (id: string, payload: ModelCheckQuestionBankMutationInput, params?: ModelCheckScopeParams) => unwrap<ModelCheckQuestionBankItem>(http.patch(`/model-checks/question-bank/${id}`, payload, { params })),
  questionBankDelete: (id: string, params?: ModelCheckScopeParams) => unwrap<{ deleted: boolean }>(http.delete(`/model-checks/question-bank/${id}`, { params })),
  questionBankReview: (id: string, payload: ModelCheckQuestionBankReviewInput, params?: ModelCheckScopeParams) => unwrap<ModelCheckQuestionBankItem>(http.post(`/model-checks/question-bank/${id}/review`, payload, { params })),
  questionBankOptions: (params?: ModelCheckScopeParams & { keyword?: string; limit?: number }, options?: { signal?: AbortSignal }) => unwrap<ModelCheckQuestionBankOptionsResult>(http.get('/model-checks/question-bank/options', { params, signal: options?.signal })),
  questionBankByIds: (ids: string[], options?: { signal?: AbortSignal }) => unwrap<ModelCheckQuestionBankOptionsResult>(http.get('/model-checks/question-bank/by-ids', { params: { ids: ids.join(',') }, signal: options?.signal }))
}

export const myModelChecksApi = {
  options: () => unwrap<ModelCheckOptions>(http.get('/my-model-checks/options')),
  accountOptions: (params: { purpose: 'run' | 'history' | 'schedule'; accountId?: string; keyword?: string; limit: number; selectedIds?: string[] }, options?: { signal?: AbortSignal }) => unwrap<ModelCheckAccountOption[]>(http.get('/my-model-checks/account-options', { params, signal: options?.signal })),
  active: () => unwrap<ActiveModelCheckRunSummary | null>(http.get('/my-model-checks/run/active')),
  run: (payload: ModelCheckRunPayload) => unwrap<ModelCheckRunDetail>(http.post('/my-model-checks/run', payload, noTimeout)),
  runStream: (payload: ModelCheckRunPayload, options?: ModelCheckStreamOptions) => runModelCheckStream('/my-model-checks/run/stream', payload, options),
  stop: () => unwrap<ModelCheckStopResult>(http.post('/my-model-checks/run/stop', {})),
  list: (params?: ModelCheckRunListParams) => unwrap<ModelCheckRunListResult>(http.get('/my-model-checks/runs', { params: modelCheckRunListParams(params) })),
  detail: (id: string) => unwrap<ModelCheckRunDetail>(http.get(`/my-model-checks/runs/${id}`)),
  qualityPolicy: () => unwrap<ModelQualityPolicy>(http.get('/my-model-checks/quality-policy')),
  saveQualityPolicy: (payload: ModelQualityPolicyUpdateInput) => unwrap<ModelQualityPolicy>(http.patch('/my-model-checks/quality-policy', payload)),
  qualitySchedules: (params?: { page?: number; pageSize?: number }) => unwrap<ModelQualityScheduleListResult>(http.get('/my-model-checks/quality-schedules', { params })),
  saveQualitySchedule: (payload: ModelQualityScheduleMutationInput) => unwrap<ModelQualitySchedule>(http.post('/my-model-checks/quality-schedules', payload)),
  patchQualitySchedule: (id: string, payload: ModelQualitySchedulePatchInput) => unwrap<ModelQualitySchedule>(http.patch(`/my-model-checks/quality-schedules/${id}`, payload)),
  deleteQualitySchedule: (id: string) => unwrap<{ deleted: boolean }>(http.delete(`/my-model-checks/quality-schedules/${id}`)),
  questionBankList: (params?: ModelCheckQuestionBankListParams) => unwrap<ModelCheckQuestionBankListResult>(http.get('/my-model-checks/question-bank', { params })),
  questionBankCreate: (payload: ModelCheckQuestionBankMutationInput) => unwrap<ModelCheckQuestionBankItem>(http.post('/my-model-checks/question-bank', payload)),
  questionBankDetail: (id: string) => unwrap<ModelCheckQuestionBankItem>(http.get(`/my-model-checks/question-bank/${id}`)),
  questionBankUpdate: (id: string, payload: ModelCheckQuestionBankMutationInput) => unwrap<ModelCheckQuestionBankItem>(http.patch(`/my-model-checks/question-bank/${id}`, payload)),
  questionBankDelete: (id: string) => unwrap<{ deleted: boolean }>(http.delete(`/my-model-checks/question-bank/${id}`)),
  questionBankReview: (id: string, payload: ModelCheckQuestionBankReviewInput) => unwrap<ModelCheckQuestionBankItem>(http.post(`/my-model-checks/question-bank/${id}/review`, payload)),
  questionBankOptions: (params?: { keyword?: string; limit?: number }, options?: { signal?: AbortSignal }) => unwrap<ModelCheckQuestionBankOptionsResult>(http.get('/my-model-checks/question-bank/options', { params, signal: options?.signal })),
  questionBankByIds: (ids: string[], options?: { signal?: AbortSignal }) => unwrap<ModelCheckQuestionBankOptionsResult>(http.get('/my-model-checks/question-bank/by-ids', { params: { ids: ids.join(',') }, signal: options?.signal }))
}
