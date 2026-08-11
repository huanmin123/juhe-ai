import type {
  CreatedOAuthClient,
  OAuthClientCreatePayload,
  OAuthClientIntegrationPackage,
  OAuthClientSummary,
  OAuthClientStatus,
  OAuthIntegrationInfo
} from '@/types/domain'
import { http, unwrap } from '../http'

export const oauthApplicationsApi = {
  listClients: () => unwrap<OAuthClientSummary[]>(http.get('/oauth/clients')),
  createClient: (payload: OAuthClientCreatePayload) => unwrap<CreatedOAuthClient>(http.post('/oauth/clients', payload)),
  updateClientStatus: (clientId: string, status: OAuthClientStatus) => unwrap<OAuthClientSummary>(http.patch(`/oauth/clients/${encodeURIComponent(clientId)}`, { status })),
  reissueClientSecret: (clientId: string) => unwrap<CreatedOAuthClient>(http.post(`/oauth/clients/${encodeURIComponent(clientId)}/secret/reissue`)),
  integrationPackage: (clientId: string) => unwrap<OAuthClientIntegrationPackage>(http.get(`/oauth/clients/${encodeURIComponent(clientId)}/integration-package`)),
  integrationInfo: () => unwrap<OAuthIntegrationInfo>(http.get('/oauth/integration-info'))
}
