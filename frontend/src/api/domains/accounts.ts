import type {
  AccountBatchEditRequest,
  AccountBatchEditResult,
  AccountExportResult,
  AccountImportOptions,
  AccountImportResult,
  AccountApiKeyRuntimeResponse,
  AccountAdvancedDetail,
  AccountCreateResult,
  AccountEditBasicDetail,
  AccountListResult,
  AccountMutationResult,
  AuthorizedAccountDispatchMutationResult,
  AccountOptionSummary,
  AccountSummary,
  AccountTagSummary,
  AccountTestSession,
  AccountTestTask,
  AccountTrafficMigrationResult,
  AccountTrafficMigrationSourceStatus,
  AccountSupportedEndpointMode
} from '@/types/domain'
import type {
  AccountBalanceDraftTestPayload,
  AccountDraftTestPayload,
  AccountModelCatalogDiscoveryAccountPayload,
  AccountExportPayload,
  AccountListParams,
  AccountOptionParams,
  AccountTestPayload,
  AccountTestOptionsParams,
  ListParams,
  RequestControlOptions
} from '../contracts'
import { http, unwrap } from '../http'
import { accountListParams, accountOptionsParams } from '../params'

export interface AccountTestModelOption {
  label: string
  value: string
}

export interface AccountManualTestModelOption {
  id: string
  name: string
}

export interface AccountTestModelCapabilities {
  id: string
  testEndpointModes: AccountSupportedEndpointMode[]
}

export interface AccountModelCatalogRefreshResult {
  addedModels: string[]
  recommendedHealthCheckModel?: string
}

export type AccountTestOptions = AccountManualTestModelOption[]

export interface AccountUpdatePayload extends Record<string, unknown> {
  expectedConfigRevision: number
}

export interface AuthorizedAccountDispatchPayload {
  expectedConfigRevision: number
  status?: 'active' | 'disabled'
  priority?: number
  superPriorityEnabled?: boolean
  fallbackEnabled?: boolean
  clearFailureState?: boolean
}

export const accountsApi = {
  list: (params?: AccountListParams) => unwrap<AccountListResult>(http.get('/accounts', { params: accountListParams(params) })),
  options: (params?: AccountOptionParams) => unwrap<AccountOptionSummary[]>(http.get('/accounts/options', { params: accountOptionsParams(params) })),
  tags: (params?: ListParams) => unwrap<AccountTagSummary[]>(http.get('/accounts/tags', { params })),
  deleteTag: (id: string, params?: ListParams) => http.delete(`/accounts/tags/${id}`, { params }),
  editBasicDetail: (id: string, params?: ListParams) => unwrap<AccountEditBasicDetail>(http.get(`/accounts/${id}/edit-basic`, { params })),
  advancedDetail: (id: string, params?: ListParams) => unwrap<AccountAdvancedDetail>(http.get(`/accounts/${id}/advanced`, { params })),
  apiKeyRuntime: (id: string, params?: ListParams) => unwrap<AccountApiKeyRuntimeResponse>(http.get(`/accounts/${id}/api-key-runtime`, { params })),
  export: (payload: AccountExportPayload, params?: ListParams) => unwrap<AccountExportResult>(http.post('/accounts/export', payload, { params })),
  importPreview: (payload: { data: unknown; options?: AccountImportOptions }, params?: ListParams) => unwrap<AccountImportResult>(http.post('/accounts/import/preview', payload, { params })),
  importConfirm: (payload: { data: unknown; options?: AccountImportOptions }, params?: ListParams) => unwrap<AccountImportResult>(http.post('/accounts/import/confirm', payload, { params })),
  create: (payload: Record<string, unknown>, params?: ListParams) => unwrap<AccountCreateResult>(http.post('/accounts', payload, { params })),
  update: (id: string, payload: AccountUpdatePayload, params?: ListParams) => unwrap<AccountMutationResult>(http.patch(`/accounts/${id}`, payload, { params })),
  forceActivate: (id: string, params?: ListParams) => unwrap<AccountSummary>(http.post(`/accounts/${id}/force-activate`, { acknowledgedAccountAvailable: true }, { params })),
  refreshBalance: (id: string, params?: ListParams) => unwrap<AccountSummary['balanceSnapshot']>(http.post(`/accounts/${id}/balance/refresh`, {}, { params })),
  testBalanceDraft: (payload: AccountBalanceDraftTestPayload, params?: ListParams) => unwrap<AccountSummary['balanceSnapshot']>(http.post('/accounts/balance/test-draft', payload, { params })),
  refreshModelCatalog: (payload: { account: AccountModelCatalogDiscoveryAccountPayload }, params?: ListParams, options?: RequestControlOptions) => unwrap<AccountModelCatalogRefreshResult>(http.post('/accounts/model-catalog/refresh', payload, { params, signal: options?.signal })),
  batchEditContext: (accountIds: string[], params?: ListParams) => unwrap<AccountSummary[]>(http.post('/accounts/batch-edit-context', { accountIds }, { params })),
  batchUpdate: (payload: AccountBatchEditRequest, params?: ListParams) => unwrap<AccountBatchEditResult>(http.post('/accounts/batch-update', payload, { params })),
  updateTags: (id: string, payload: { tags: string[]; expectedConfigRevision: number }, params?: ListParams) => unwrap<AccountMutationResult>(http.patch(`/accounts/${id}/tags`, payload, { params })),
  updateAuthorizedDispatch: (id: string, payload: AuthorizedAccountDispatchPayload, params?: ListParams) => unwrap<AuthorizedAccountDispatchMutationResult>(http.patch(`/accounts/${id}/authorized-dispatch`, payload, { params })),
  testOptions: (id: string, params?: AccountTestOptionsParams, options?: RequestControlOptions) => unwrap<AccountTestOptions>(http.get(`/accounts/${id}/test-options`, { params, signal: options?.signal })),
  testModelCapabilities: (id: string, modelId: string, params?: ListParams, options?: RequestControlOptions) => unwrap<AccountTestModelCapabilities>(http.get(`/accounts/${id}/test-options/models/${encodeURIComponent(modelId)}`, { params, signal: options?.signal })),
  bindGroup: (id: string, payload: { groupId: string; expectedConfigRevision: number }, params?: ListParams) => unwrap<AccountMutationResult>(http.post(`/accounts/${id}/group`, payload, { params })),
  migrateTraffic: (id: string, payload: { targetAccountId: string; sourceStatus?: AccountTrafficMigrationSourceStatus }, params?: ListParams) => unwrap<AccountTrafficMigrationResult>(http.post(`/accounts/${id}/traffic-migration`, payload, { params })),
  test: (id: string, payload?: AccountTestPayload, params?: ListParams, options?: RequestControlOptions) => unwrap<AccountTestTask>(http.post(`/accounts/${id}/test`, payload ?? {}, { params, signal: options?.signal })),
  testDraft: (payload: AccountDraftTestPayload, params?: ListParams, options?: RequestControlOptions) => unwrap<AccountTestTask>(http.post('/accounts/test-draft', payload, { params, signal: options?.signal })),
  createTestSession: (params?: ListParams) => unwrap<AccountTestSession>(http.post('/accounts/test-sessions', {}, { params })),
  heartbeatTestSession: (sessionId: string, params?: ListParams) => unwrap<AccountTestSession>(http.post(`/accounts/test-sessions/${sessionId}/heartbeat`, {}, { params })),
  completeTestSession: (sessionId: string, params?: ListParams) => unwrap<AccountTestSession>(http.post(`/accounts/test-sessions/${sessionId}/complete`, {}, { params })),
  cancelTestSession: (sessionId: string, params?: ListParams) => unwrap<AccountTestSession>(http.post(`/accounts/test-sessions/${sessionId}/cancel`, {}, { params })),
  testTask: (taskId: string, params?: ListParams, options?: RequestControlOptions) => unwrap<AccountTestTask>(http.get(`/accounts/test-tasks/${taskId}`, { params, signal: options?.signal })),
  cancelTestTask: (taskId: string, params?: ListParams) => unwrap<AccountTestTask>(http.post(`/accounts/test-tasks/${taskId}/cancel`, {}, { params })),
  returnAuthorization: (id: string, params?: ListParams) => http.post(`/accounts/${id}/return-authorization`, {}, { params }),
  delete: (id: string, params?: ListParams) => http.delete(`/accounts/${id}`, { params })
}

export const myAccountsApi = {
  list: (params?: AccountListParams) => unwrap<AccountListResult>(http.get('/my-accounts', { params: accountListParams(params, false) })),
  options: (params?: AccountOptionParams) => unwrap<AccountOptionSummary[]>(http.get('/my-accounts/options', { params: accountOptionsParams(params, false) })),
  tags: () => unwrap<AccountTagSummary[]>(http.get('/my-accounts/tags')),
  deleteTag: (id: string) => http.delete(`/my-accounts/tags/${id}`),
  editBasicDetail: (id: string) => unwrap<AccountEditBasicDetail>(http.get(`/my-accounts/${id}/edit-basic`)),
  advancedDetail: (id: string) => unwrap<AccountAdvancedDetail>(http.get(`/my-accounts/${id}/advanced`)),
  apiKeyRuntime: (id: string) => unwrap<AccountApiKeyRuntimeResponse>(http.get(`/my-accounts/${id}/api-key-runtime`)),
  export: (payload: AccountExportPayload) => unwrap<AccountExportResult>(http.post('/my-accounts/export', payload)),
  importPreview: (payload: { data: unknown; options?: AccountImportOptions }) => unwrap<AccountImportResult>(http.post('/my-accounts/import/preview', payload)),
  importConfirm: (payload: { data: unknown; options?: AccountImportOptions }) => unwrap<AccountImportResult>(http.post('/my-accounts/import/confirm', payload)),
  create: (payload: Record<string, unknown>) => unwrap<AccountCreateResult>(http.post('/my-accounts', payload)),
  update: (id: string, payload: AccountUpdatePayload) => unwrap<AccountMutationResult>(http.patch(`/my-accounts/${id}`, payload)),
  forceActivate: (id: string) => unwrap<AccountSummary>(http.post(`/my-accounts/${id}/force-activate`, { acknowledgedAccountAvailable: true })),
  refreshBalance: (id: string) => unwrap<AccountSummary['balanceSnapshot']>(http.post(`/my-accounts/${id}/balance/refresh`, {})),
  testBalanceDraft: (payload: AccountBalanceDraftTestPayload) => unwrap<AccountSummary['balanceSnapshot']>(http.post('/my-accounts/balance/test-draft', payload)),
  refreshModelCatalog: (payload: { account: AccountModelCatalogDiscoveryAccountPayload }, options?: RequestControlOptions) => unwrap<AccountModelCatalogRefreshResult>(http.post('/my-accounts/model-catalog/refresh', payload, { signal: options?.signal })),
  batchEditContext: (accountIds: string[]) => unwrap<AccountSummary[]>(http.post('/my-accounts/batch-edit-context', { accountIds })),
  batchUpdate: (payload: AccountBatchEditRequest) => unwrap<AccountBatchEditResult>(http.post('/my-accounts/batch-update', payload)),
  updateTags: (id: string, payload: { tags: string[]; expectedConfigRevision: number }) => unwrap<AccountMutationResult>(http.patch(`/my-accounts/${id}/tags`, payload)),
  updateAuthorizedDispatch: (id: string, payload: AuthorizedAccountDispatchPayload) => unwrap<AuthorizedAccountDispatchMutationResult>(http.patch(`/my-accounts/${id}/authorized-dispatch`, payload)),
  testOptions: (id: string, params?: AccountTestOptionsParams, options?: RequestControlOptions) => unwrap<AccountTestOptions>(http.get(`/my-accounts/${id}/test-options`, { params, signal: options?.signal })),
  testModelCapabilities: (id: string, modelId: string, options?: RequestControlOptions) => unwrap<AccountTestModelCapabilities>(http.get(`/my-accounts/${id}/test-options/models/${encodeURIComponent(modelId)}`, { signal: options?.signal })),
  bindGroup: (id: string, payload: { groupId: string; expectedConfigRevision: number }) => unwrap<AccountMutationResult>(http.post(`/my-accounts/${id}/group`, payload)),
  migrateTraffic: (id: string, payload: { targetAccountId: string; sourceStatus?: AccountTrafficMigrationSourceStatus }) => unwrap<AccountTrafficMigrationResult>(http.post(`/my-accounts/${id}/traffic-migration`, payload)),
  test: (id: string, payload?: AccountTestPayload, options?: RequestControlOptions) => unwrap<AccountTestTask>(http.post(`/my-accounts/${id}/test`, payload ?? {}, { signal: options?.signal })),
  testDraft: (payload: AccountDraftTestPayload, options?: RequestControlOptions) => unwrap<AccountTestTask>(http.post('/my-accounts/test-draft', payload, { signal: options?.signal })),
  createTestSession: () => unwrap<AccountTestSession>(http.post('/my-accounts/test-sessions', {})),
  heartbeatTestSession: (sessionId: string) => unwrap<AccountTestSession>(http.post(`/my-accounts/test-sessions/${sessionId}/heartbeat`, {})),
  completeTestSession: (sessionId: string) => unwrap<AccountTestSession>(http.post(`/my-accounts/test-sessions/${sessionId}/complete`, {})),
  cancelTestSession: (sessionId: string) => unwrap<AccountTestSession>(http.post(`/my-accounts/test-sessions/${sessionId}/cancel`, {})),
  testTask: (taskId: string, options?: RequestControlOptions) => unwrap<AccountTestTask>(http.get(`/my-accounts/test-tasks/${taskId}`, { signal: options?.signal })),
  cancelTestTask: (taskId: string) => unwrap<AccountTestTask>(http.post(`/my-accounts/test-tasks/${taskId}/cancel`, {})),
  returnAuthorization: (id: string) => http.post(`/my-accounts/${id}/return-authorization`, {}),
  delete: (id: string) => http.delete(`/my-accounts/${id}`)
}
