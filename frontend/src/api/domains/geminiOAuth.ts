import type { AccountCreateResult, AccountSupportedEndpointMode, OAuthAuthURLResult, OAuthCredentialRotationPayload, OAuthCredentialRotationResult } from '@/types/domain'
import type { ListParams } from '../contracts'
import { http, unwrap } from '../http'

export interface GeminiOAuthCapabilities {
  defaultOAuthType: 'code_assist' | 'google_one' | 'ai_studio'
  oauthTypes: Array<{
    oauthType: 'code_assist' | 'google_one' | 'ai_studio'
    label: string
    usesBuiltInClient: boolean
    requiresClientCredentials: boolean
    redirectUri: string
    scope: string
    supportsProjectId: boolean
    supportsTierId: boolean
    supportedEndpointModes: AccountSupportedEndpointMode[]
  }>
}

export const geminiOAuthApi = {
  capabilities: () => unwrap<GeminiOAuthCapabilities>(http.get('/gemini-oauth/capabilities')),
  authUrl: (payload: Record<string, unknown>) => unwrap<OAuthAuthURLResult>(http.post('/gemini-oauth/auth-url', payload)),
  createFromCode: (payload: Record<string, unknown>, params?: ListParams) => unwrap<AccountCreateResult>(http.post('/gemini-oauth/create-from-code', payload, { params })),
  createFromRefreshToken: (payload: Record<string, unknown>, params?: ListParams) => unwrap<AccountCreateResult>(http.post('/gemini-oauth/create-from-refresh-token', payload, { params })),
  refreshToken: (id: string, payload: OAuthCredentialRotationPayload, params?: ListParams) => unwrap<OAuthCredentialRotationResult>(http.post(`/gemini-oauth/accounts/${id}/refresh-token`, payload, { params, timeout: 130000 })),
  reauthorizeFromCode: (id: string, payload: OAuthCredentialRotationPayload, params?: ListParams) => unwrap<OAuthCredentialRotationResult>(http.post(`/gemini-oauth/accounts/${id}/reauthorize-from-code`, payload, { params })),
  reauthorizeFromRefreshToken: (id: string, payload: OAuthCredentialRotationPayload, params?: ListParams) => unwrap<OAuthCredentialRotationResult>(http.post(`/gemini-oauth/accounts/${id}/reauthorize-from-refresh-token`, payload, { params }))
}

export const myGeminiOAuthApi = {
  capabilities: () => unwrap<GeminiOAuthCapabilities>(http.get('/my-gemini-oauth/capabilities')),
  authUrl: (payload: Record<string, unknown>) => unwrap<OAuthAuthURLResult>(http.post('/my-gemini-oauth/auth-url', payload)),
  createFromCode: (payload: Record<string, unknown>) => unwrap<AccountCreateResult>(http.post('/my-gemini-oauth/create-from-code', payload)),
  createFromRefreshToken: (payload: Record<string, unknown>) => unwrap<AccountCreateResult>(http.post('/my-gemini-oauth/create-from-refresh-token', payload)),
  refreshToken: (id: string, payload: OAuthCredentialRotationPayload) => unwrap<OAuthCredentialRotationResult>(http.post(`/my-gemini-oauth/accounts/${id}/refresh-token`, payload, { timeout: 130000 })),
  reauthorizeFromCode: (id: string, payload: OAuthCredentialRotationPayload) => unwrap<OAuthCredentialRotationResult>(http.post(`/my-gemini-oauth/accounts/${id}/reauthorize-from-code`, payload)),
  reauthorizeFromRefreshToken: (id: string, payload: OAuthCredentialRotationPayload) => unwrap<OAuthCredentialRotationResult>(http.post(`/my-gemini-oauth/accounts/${id}/reauthorize-from-refresh-token`, payload))
}
