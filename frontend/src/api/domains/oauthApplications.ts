import type {
  CreatedOAuthClient,
  OAuthClientCreatePayload,
  OAuthClientSummary,
  OAuthClientStatus,
  OAuthConnectedApplicationSummary,
  OAuthConnectedApplicationListResult,
  OAuthSigningKeyRotationResult
} from '@/types/domain'
import { http, unwrap } from '../http'

export const oauthApplicationsApi = {
  listClients: () => unwrap<OAuthClientSummary[]>(http.get('/oauth/clients')),
  createClient: (payload: OAuthClientCreatePayload) => unwrap<CreatedOAuthClient>(http.post('/oauth/clients', payload)),
  updateClientStatus: (clientId: string, status: OAuthClientStatus) => unwrap<OAuthClientSummary>(http.patch(`/oauth/clients/${encodeURIComponent(clientId)}`, { status })),
  rotateSigningKey: () => unwrap<OAuthSigningKeyRotationResult>(http.post('/oauth/keys/rotate')),
  listConnectedApplications: () => unwrap<OAuthConnectedApplicationListResult | OAuthConnectedApplicationSummary[]>(http.get('/oauth/connected-applications')),
  revokeConnectedApplication: async (clientId: string): Promise<void> => {
    await http.delete(`/oauth/connected-applications/${encodeURIComponent(clientId)}`)
  }
}
