import type {
  ModelCheckOptions,
  ModelCheckAccountOption,
  ModelCheckRunDetail,
  ModelCheckRunListParams,
  ModelCheckRunListResult,
  ModelCheckRunPayload,
  ModelCheckStopResult,
  ActiveModelCheckRunSummary
} from '@/types/domain'
import type { ModelCheckScopeParams, ModelCheckStreamOptions } from '../contracts'
import { http, noTimeout, unwrap } from '../http'
import { runModelCheckStream } from '../modelCheckStream'
import { modelCheckRunListParams } from '../params'

export const modelChecksApi = {
  options: (params?: ModelCheckScopeParams) => unwrap<ModelCheckOptions>(http.get('/model-checks/options', { params })),
  accountOptions: (params: ModelCheckScopeParams & { purpose: 'run' | 'history'; keyword?: string; limit: number; selectedIds?: string[] }) => unwrap<ModelCheckAccountOption[]>(http.get('/model-checks/account-options', { params })),
  active: (params?: ModelCheckScopeParams) => unwrap<ActiveModelCheckRunSummary | null>(http.get('/model-checks/run/active', { params })),
  run: (payload: ModelCheckRunPayload, params?: ModelCheckScopeParams) => unwrap<ModelCheckRunDetail>(http.post('/model-checks/run', payload, { ...noTimeout, params })),
  runStream: (payload: ModelCheckRunPayload, options?: ModelCheckStreamOptions, params?: ModelCheckScopeParams) => runModelCheckStream('/model-checks/run/stream', payload, options, params),
  stop: (params?: ModelCheckScopeParams) => unwrap<ModelCheckStopResult>(http.post('/model-checks/run/stop', {}, { params })),
  list: (params?: ModelCheckRunListParams) => unwrap<ModelCheckRunListResult>(http.get('/model-checks/runs', { params: modelCheckRunListParams(params) })),
  detail: (id: string, params?: ModelCheckScopeParams) => unwrap<ModelCheckRunDetail>(http.get(`/model-checks/runs/${id}`, { params }))
}

export const myModelChecksApi = {
  options: () => unwrap<ModelCheckOptions>(http.get('/my-model-checks/options')),
  accountOptions: (params: { purpose: 'run' | 'history'; keyword?: string; limit: number; selectedIds?: string[] }) => unwrap<ModelCheckAccountOption[]>(http.get('/my-model-checks/account-options', { params })),
  active: () => unwrap<ActiveModelCheckRunSummary | null>(http.get('/my-model-checks/run/active')),
  run: (payload: ModelCheckRunPayload) => unwrap<ModelCheckRunDetail>(http.post('/my-model-checks/run', payload, noTimeout)),
  runStream: (payload: ModelCheckRunPayload, options?: ModelCheckStreamOptions) => runModelCheckStream('/my-model-checks/run/stream', payload, options),
  stop: () => unwrap<ModelCheckStopResult>(http.post('/my-model-checks/run/stop', {})),
  list: (params?: ModelCheckRunListParams) => unwrap<ModelCheckRunListResult>(http.get('/my-model-checks/runs', { params: modelCheckRunListParams(params) })),
  detail: (id: string) => unwrap<ModelCheckRunDetail>(http.get(`/my-model-checks/runs/${id}`))
}
