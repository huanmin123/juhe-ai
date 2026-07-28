import type {
  CreatedExternalIntegrationSourceAuthorization,
  CreatedExternalIntegrationSourceToken,
  ExternalIntegrationScopeOption,
  ExternalIntegrationSourceListResult,
  ExternalIntegrationSourceMutationResult,
  ExternalIntegrationSourcePatchPayload,
  ExternalIntegrationSourcePayload,
  ExternalIntegrationSourceSummary,
  ExternalIntegrationSourceTokenMutationResult,
  ExternalIntegrationSourceTokenPatchPayload,
  ExternalIntegrationSourceTokenPayload,
  ExternalIntegrationSourceTokenSecretResult,
  ExternalPublicApiCatalog
} from '@/types/domain'
import type { ExternalIntegrationSourceListParams } from '../contracts'
import { http, unwrap } from '../http'

export const externalIntegrationSourcesApi = {
  scopes: () => unwrap<ExternalIntegrationScopeOption[]>(http.get('/external-integration-sources/scopes')),
  apiDocs: () => unwrap<ExternalPublicApiCatalog>(http.get('/external-integration-sources/api-docs')),
  list: (params?: ExternalIntegrationSourceListParams) => unwrap<ExternalIntegrationSourceListResult>(http.get('/external-integration-sources', { params })),
  detail: (id: string) => unwrap<ExternalIntegrationSourceSummary>(http.get(`/external-integration-sources/${encodeURIComponent(id)}`)),
  create: (payload: ExternalIntegrationSourcePayload) => unwrap<CreatedExternalIntegrationSourceAuthorization>(http.post('/external-integration-sources', payload)),
  update: (id: string, payload: ExternalIntegrationSourcePatchPayload) => unwrap<ExternalIntegrationSourceMutationResult>(http.patch(`/external-integration-sources/${encodeURIComponent(id)}`, payload)),
  delete: (id: string) => http.delete(`/external-integration-sources/${encodeURIComponent(id)}`),
  resetBuiltInTestToken: () => unwrap<{ token: CreatedExternalIntegrationSourceToken }>(http.post('/external-integration-sources/built-in-test-token/reset')),
  createToken: (id: string, payload: ExternalIntegrationSourceTokenPayload) => unwrap<{ token: CreatedExternalIntegrationSourceToken }>(http.post(`/external-integration-sources/${id}/tokens`, payload)),
  tokenSecret: (id: string, tokenId: string) => unwrap<ExternalIntegrationSourceTokenSecretResult>(http.get(`/external-integration-sources/${encodeURIComponent(id)}/tokens/${encodeURIComponent(tokenId)}/secret`)),
  updateToken: (id: string, tokenId: string, payload: ExternalIntegrationSourceTokenPatchPayload) => unwrap<ExternalIntegrationSourceTokenMutationResult>(http.patch(`/external-integration-sources/${encodeURIComponent(id)}/tokens/${encodeURIComponent(tokenId)}`, payload))
}
