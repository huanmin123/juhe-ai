import type {
  AccountBatchEditContextField,
  AccountBatchEditContextItem,
  AccountBatchEditRequest,
  AccountBatchEditResult,
  AccountExportResult,
  AccountImportOptions,
  AccountImportResult,
  AccountImportSourceMode,
  AccountApiKeyRuntimeResponse,
  AccountAdvancedDetail,
  AccountCreateResult,
  AccountCloneContext,
  AccountEditBasicDetail,
  AccountListResult,
  AccountMutationResult,
  AccountOAuthReauthorizationContext,
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
import { http, noTimeout, unwrap } from '../http'
import { accountListParams, accountOptionsParams } from '../params'

export interface AccountTestModelOption {
  label: string
  testEndpointModes: AccountSupportedEndpointMode[]
  value: string
}

export interface AccountManualTestModelOption {
  id: string
  name: string
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

interface AccountImportRequestPayload {
  data: unknown
  sourceMode?: AccountImportSourceMode
  options?: AccountImportOptions
}

export interface AuthorizedAccountDispatchPayload {
  expectedConfigRevision: number
  status?: 'active' | 'disabled'
  priority?: number
  superPriorityEnabled?: boolean
  fallbackEnabled?: boolean
  clearFailureState?: boolean
}

export interface AccountLockMutationPayload {
  expectedConfigRevision?: number
  lockDeathTimeoutSeconds?: number
  lockRetryIntervalSeconds?: number
}

export const accountsApi = {
  list: (params?: AccountListParams, options?: RequestControlOptions) => unwrap<AccountListResult>(http.get('/accounts', { params: accountListParams(params), signal: options?.signal })),
  options: (params?: AccountOptionParams) => unwrap<AccountOptionSummary[]>(http.get('/accounts/options', { params: accountOptionsParams(params) })),
  tags: (params?: ListParams) => unwrap<AccountTagSummary[]>(http.get('/accounts/tags', { params })),
  deleteTag: (id: string, params?: ListParams) => http.delete(`/accounts/tags/${id}`, { params }),
  editBasicDetail: (id: string, params?: ListParams) => unwrap<AccountEditBasicDetail>(http.get(`/accounts/${id}/edit-basic`, { params })),
  advancedDetail: (id: string, params?: ListParams) => unwrap<AccountAdvancedDetail>(http.get(`/accounts/${id}/advanced`, { params })),
  cloneContext: (id: string, params?: ListParams) => unwrap<AccountCloneContext>(http.get(`/accounts/${id}/clone-context`, { params })),
  oauthReauthorizationContext: (id: string, params?: ListParams) => unwrap<AccountOAuthReauthorizationContext>(http.get(`/accounts/${id}/oauth-reauthorization-context`, { params })),
  apiKeyRuntime: (id: string, params?: ListParams) => unwrap<AccountApiKeyRuntimeResponse>(http.get(`/accounts/${id}/api-key-runtime`, { params })),
  revalidateApiKeyRuntime: (id: string, payload: { expectedConfigRevision: number }, params?: ListParams) => unwrap<{ id: string; configRevision: number; changed: number }>(http.post(`/accounts/${id}/api-key-runtime/revalidate`, payload, { params })),
  export: (payload: AccountExportPayload, params?: ListParams) => unwrap<AccountExportResult>(http.post('/accounts/export', payload, { params })),
  importPreview: (payload: AccountImportRequestPayload, params?: ListParams) => unwrap<AccountImportResult>(http.post('/accounts/import/preview', payload, { params })),
  importConfirm: (payload: AccountImportRequestPayload, params?: ListParams) => unwrap<AccountImportResult>(http.post('/accounts/import/confirm', payload, { params })),
  create: (payload: Record<string, unknown>, params?: ListParams) => unwrap<AccountCreateResult>(http.post('/accounts', payload, { params })),
  update: (id: string, payload: AccountUpdatePayload, params?: ListParams) => unwrap<AccountMutationResult>(http.patch(`/accounts/${id}`, payload, { params })),
  lock: (id: string, payload?: AccountLockMutationPayload, params?: ListParams) => unwrap<Record<string, unknown>>(http.post(`/accounts/${id}/lock`, payload ?? {}, { params })),
  unlock: (id: string, payload?: AccountLockMutationPayload, params?: ListParams) => unwrap<Record<string, unknown>>(http.post(`/accounts/${id}/unlock`, payload ?? {}, { params })),
  updateLockConfig: (id: string, payload: AccountLockMutationPayload, params?: ListParams) => unwrap<Record<string, unknown>>(http.post(`/accounts/${id}/lock-config`, payload, { params })),
  forceActivate: (id: string, params?: ListParams) => unwrap<AccountSummary>(http.post(`/accounts/${id}/force-activate`, { acknowledgedAccountAvailable: true }, { params })),
  refreshBalance: (id: string, params?: ListParams) => unwrap<AccountSummary['balanceSnapshot']>(http.post(`/accounts/${id}/balance/refresh`, {}, { params })),
  testBalanceDraft: (payload: AccountBalanceDraftTestPayload, params?: ListParams) => unwrap<AccountSummary['balanceSnapshot']>(http.post('/accounts/balance/test-draft', payload, { params })),
  refreshModelCatalog: (payload: { account: AccountModelCatalogDiscoveryAccountPayload }, params?: ListParams, options?: RequestControlOptions) => unwrap<AccountModelCatalogRefreshResult>(http.post('/accounts/model-catalog/refresh', payload, { params, signal: options?.signal })),
  batchEditContext: (accountIds: string[], fields: AccountBatchEditContextField[], params?: ListParams) => unwrap<AccountBatchEditContextItem[]>(http.post('/accounts/batch-edit-context', { accountIds, fields }, { params })),
  batchUpdate: (payload: AccountBatchEditRequest, params?: ListParams) => unwrap<AccountBatchEditResult>(http.post('/accounts/batch-update', payload, { params })),
  updateTags: (id: string, payload: { tags: string[]; expectedConfigRevision: number }, params?: ListParams) => unwrap<AccountMutationResult>(http.patch(`/accounts/${id}/tags`, payload, { params })),
  updateAuthorizedDispatch: (id: string, payload: AuthorizedAccountDispatchPayload, params?: ListParams) => unwrap<AuthorizedAccountDispatchMutationResult>(http.patch(`/accounts/${id}/authorized-dispatch`, payload, { params })),
  testOptions: (id: string, params?: AccountTestOptionsParams, options?: RequestControlOptions) => unwrap<AccountTestOptions>(http.get(`/accounts/${id}/test-options`, { ...noTimeout, params, signal: options?.signal })),
  bindGroup: (id: string, payload: { groupId: string; expectedConfigRevision: number }, params?: ListParams) => unwrap<AccountMutationResult>(http.post(`/accounts/${id}/group`, payload, { params })),
  migrateTraffic: (id: string, payload: { targetAccountId: string; sourceStatus?: AccountTrafficMigrationSourceStatus }, params?: ListParams) => unwrap<AccountTrafficMigrationResult>(http.post(`/accounts/${id}/traffic-migration`, payload, { params })),
  test: (id: string, payload?: AccountTestPayload, params?: ListParams, options?: RequestControlOptions) => unwrap<AccountTestTask>(http.post(`/accounts/${id}/test`, payload ?? {}, { ...noTimeout, params, signal: options?.signal })),
  testDraft: (payload: AccountDraftTestPayload, params?: ListParams, options?: RequestControlOptions) => unwrap<AccountTestTask>(http.post('/accounts/test-draft', payload, { ...noTimeout, params, signal: options?.signal })),
  createTestSession: (params?: ListParams) => unwrap<AccountTestSession>(http.post('/accounts/test-sessions', {}, { ...noTimeout, params })),
  heartbeatTestSession: (sessionId: string, params?: ListParams) => unwrap<AccountTestSession>(http.post(`/accounts/test-sessions/${sessionId}/heartbeat`, {}, { ...noTimeout, params })),
  completeTestSession: (sessionId: string, params?: ListParams) => unwrap<AccountTestSession>(http.post(`/accounts/test-sessions/${sessionId}/complete`, {}, { ...noTimeout, params })),
  cancelTestSession: (sessionId: string, params?: ListParams) => unwrap<AccountTestSession>(http.post(`/accounts/test-sessions/${sessionId}/cancel`, {}, { ...noTimeout, params })),
  testTask: (taskId: string, params?: ListParams, options?: RequestControlOptions) => unwrap<AccountTestTask>(http.get(`/accounts/test-tasks/${taskId}`, { ...noTimeout, params, signal: options?.signal })),
  cancelTestTask: (taskId: string, params?: ListParams) => unwrap<AccountTestTask>(http.post(`/accounts/test-tasks/${taskId}/cancel`, {}, { ...noTimeout, params })),
  returnAuthorization: (id: string, params?: ListParams) => http.post(`/accounts/${id}/return-authorization`, {}, { params }),
  delete: (id: string, params?: ListParams) => http.delete(`/accounts/${id}`, { params })
}

export const myAccountsApi = {
  list: (params?: AccountListParams, options?: RequestControlOptions) => unwrap<AccountListResult>(http.get('/my-accounts', { params: accountListParams(params, false), signal: options?.signal })),
  options: (params?: AccountOptionParams) => unwrap<AccountOptionSummary[]>(http.get('/my-accounts/options', { params: accountOptionsParams(params, false) })),
  tags: () => unwrap<AccountTagSummary[]>(http.get('/my-accounts/tags')),
  deleteTag: (id: string) => http.delete(`/my-accounts/tags/${id}`),
  editBasicDetail: (id: string) => unwrap<AccountEditBasicDetail>(http.get(`/my-accounts/${id}/edit-basic`)),
  advancedDetail: (id: string) => unwrap<AccountAdvancedDetail>(http.get(`/my-accounts/${id}/advanced`)),
  cloneContext: (id: string) => unwrap<AccountCloneContext>(http.get(`/my-accounts/${id}/clone-context`)),
  oauthReauthorizationContext: (id: string) => unwrap<AccountOAuthReauthorizationContext>(http.get(`/my-accounts/${id}/oauth-reauthorization-context`)),
  apiKeyRuntime: (id: string) => unwrap<AccountApiKeyRuntimeResponse>(http.get(`/my-accounts/${id}/api-key-runtime`)),
  revalidateApiKeyRuntime: (id: string, payload: { expectedConfigRevision: number }) => unwrap<{ id: string; configRevision: number; changed: number }>(http.post(`/my-accounts/${id}/api-key-runtime/revalidate`, payload)),
  export: (payload: AccountExportPayload) => unwrap<AccountExportResult>(http.post('/my-accounts/export', payload)),
  importPreview: (payload: AccountImportRequestPayload) => unwrap<AccountImportResult>(http.post('/my-accounts/import/preview', payload)),
  importConfirm: (payload: AccountImportRequestPayload) => unwrap<AccountImportResult>(http.post('/my-accounts/import/confirm', payload)),
  create: (payload: Record<string, unknown>) => unwrap<AccountCreateResult>(http.post('/my-accounts', payload)),
  update: (id: string, payload: AccountUpdatePayload) => unwrap<AccountMutationResult>(http.patch(`/my-accounts/${id}`, payload)),
  lock: (id: string, payload?: AccountLockMutationPayload) => unwrap<Record<string, unknown>>(http.post(`/my-accounts/${id}/lock`, payload ?? {})),
  unlock: (id: string, payload?: AccountLockMutationPayload) => unwrap<Record<string, unknown>>(http.post(`/my-accounts/${id}/unlock`, payload ?? {})),
  updateLockConfig: (id: string, payload: AccountLockMutationPayload) => unwrap<Record<string, unknown>>(http.post(`/my-accounts/${id}/lock-config`, payload)),
  forceActivate: (id: string) => unwrap<AccountSummary>(http.post(`/my-accounts/${id}/force-activate`, { acknowledgedAccountAvailable: true })),
  refreshBalance: (id: string) => unwrap<AccountSummary['balanceSnapshot']>(http.post(`/my-accounts/${id}/balance/refresh`, {})),
  testBalanceDraft: (payload: AccountBalanceDraftTestPayload) => unwrap<AccountSummary['balanceSnapshot']>(http.post('/my-accounts/balance/test-draft', payload)),
  refreshModelCatalog: (payload: { account: AccountModelCatalogDiscoveryAccountPayload }, options?: RequestControlOptions) => unwrap<AccountModelCatalogRefreshResult>(http.post('/my-accounts/model-catalog/refresh', payload, { signal: options?.signal })),
  batchEditContext: (accountIds: string[], fields: AccountBatchEditContextField[]) => unwrap<AccountBatchEditContextItem[]>(http.post('/my-accounts/batch-edit-context', { accountIds, fields })),
  batchUpdate: (payload: AccountBatchEditRequest) => unwrap<AccountBatchEditResult>(http.post('/my-accounts/batch-update', payload)),
  updateTags: (id: string, payload: { tags: string[]; expectedConfigRevision: number }) => unwrap<AccountMutationResult>(http.patch(`/my-accounts/${id}/tags`, payload)),
  updateAuthorizedDispatch: (id: string, payload: AuthorizedAccountDispatchPayload) => unwrap<AuthorizedAccountDispatchMutationResult>(http.patch(`/my-accounts/${id}/authorized-dispatch`, payload)),
  testOptions: (id: string, params?: AccountTestOptionsParams, options?: RequestControlOptions) => unwrap<AccountTestOptions>(http.get(`/my-accounts/${id}/test-options`, { ...noTimeout, params, signal: options?.signal })),
  bindGroup: (id: string, payload: { groupId: string; expectedConfigRevision: number }) => unwrap<AccountMutationResult>(http.post(`/my-accounts/${id}/group`, payload)),
  migrateTraffic: (id: string, payload: { targetAccountId: string; sourceStatus?: AccountTrafficMigrationSourceStatus }) => unwrap<AccountTrafficMigrationResult>(http.post(`/my-accounts/${id}/traffic-migration`, payload)),
  test: (id: string, payload?: AccountTestPayload, options?: RequestControlOptions) => unwrap<AccountTestTask>(http.post(`/my-accounts/${id}/test`, payload ?? {}, { ...noTimeout, signal: options?.signal })),
  testDraft: (payload: AccountDraftTestPayload, options?: RequestControlOptions) => unwrap<AccountTestTask>(http.post('/my-accounts/test-draft', payload, { ...noTimeout, signal: options?.signal })),
  createTestSession: () => unwrap<AccountTestSession>(http.post('/my-accounts/test-sessions', {}, noTimeout)),
  heartbeatTestSession: (sessionId: string) => unwrap<AccountTestSession>(http.post(`/my-accounts/test-sessions/${sessionId}/heartbeat`, {}, noTimeout)),
  completeTestSession: (sessionId: string) => unwrap<AccountTestSession>(http.post(`/my-accounts/test-sessions/${sessionId}/complete`, {}, noTimeout)),
  cancelTestSession: (sessionId: string) => unwrap<AccountTestSession>(http.post(`/my-accounts/test-sessions/${sessionId}/cancel`, {}, noTimeout)),
  testTask: (taskId: string, options?: RequestControlOptions) => unwrap<AccountTestTask>(http.get(`/my-accounts/test-tasks/${taskId}`, { ...noTimeout, signal: options?.signal })),
  cancelTestTask: (taskId: string) => unwrap<AccountTestTask>(http.post(`/my-accounts/test-tasks/${taskId}/cancel`, {}, noTimeout)),
  returnAuthorization: (id: string) => http.post(`/my-accounts/${id}/return-authorization`, {}),
  delete: (id: string) => http.delete(`/my-accounts/${id}`)
}
