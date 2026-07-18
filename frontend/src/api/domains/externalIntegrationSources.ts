import type {
  CreatedExternalIntegrationSourceAuthorization,
  CreatedExternalIntegrationSourceToken,
  ExternalIntegrationScopeOption,
  ExternalIntegrationSourceListResult,
  ExternalIntegrationSourcePayload,
  ExternalIntegrationSourceSummary,
  ExternalIntegrationSourceTokenPayload,
  ExternalIntegrationSourceTokenSecretResult,
  ExternalIntegrationSourceTokenSummary,
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
  update: (id: string, payload: Partial<ExternalIntegrationSourcePayload>) => unwrap<ExternalIntegrationSourceSummary>(http.patch(`/external-integration-sources/${encodeURIComponent(id)}`, payload)),
  delete: (id: string) => http.delete(`/external-integration-sources/${encodeURIComponent(id)}`),
  resetBuiltInTestToken: () => unwrap<{ token: CreatedExternalIntegrationSourceToken; source?: ExternalIntegrationSourceSummary }>(http.post('/external-integration-sources/built-in-test-token/reset')),
  createToken: (id: string, payload: ExternalIntegrationSourceTokenPayload) => unwrap<{ token: CreatedExternalIntegrationSourceToken; source?: ExternalIntegrationSourceSummary }>(http.post(`/external-integration-sources/${id}/tokens`, payload)),
  tokenSecret: (id: string, tokenId: string) => unwrap<ExternalIntegrationSourceTokenSecretResult>(http.get(`/external-integration-sources/${encodeURIComponent(id)}/tokens/${encodeURIComponent(tokenId)}/secret`)),
  updateToken: (id: string, tokenId: string, payload: Partial<ExternalIntegrationSourceTokenPayload>) => unwrap<ExternalIntegrationSourceTokenSummary>(http.patch(`/external-integration-sources/${id}/tokens/${tokenId}`, payload))
}
