import type { AccountCreateResult, OAuthAuthURLResult, OAuthCredentialRotationPayload, OAuthCredentialRotationResult } from '@/types/domain'
import type { ListParams } from '../contracts'
import { http, unwrap } from '../http'

export interface GrokSsoImportResult {
  createdCount: number
  failed: Array<{ index: number; error: string }>
}

export const grokOAuthApi = {
  authUrl: (payload: Record<string, unknown>) => unwrap<OAuthAuthURLResult>(http.post('/grok-oauth/auth-url', payload)),
  createFromCode: (payload: Record<string, unknown>, params?: ListParams) => unwrap<AccountCreateResult>(http.post('/grok-oauth/create-from-code', payload, { params })),
  createFromRefreshToken: (payload: Record<string, unknown>, params?: ListParams) => unwrap<AccountCreateResult>(http.post('/grok-oauth/create-from-refresh-token', payload, { params })),
  ssoToOAuth: (payload: Record<string, unknown>, params?: ListParams) => unwrap<GrokSsoImportResult>(http.post('/grok-oauth/sso-to-oauth', payload, { params, timeout: 600000 })),
  refreshToken: (id: string, payload: OAuthCredentialRotationPayload, params?: ListParams) => unwrap<OAuthCredentialRotationResult>(http.post(`/grok-oauth/accounts/${id}/refresh-token`, payload, { params, timeout: 130000 })),
  reauthorizeFromCode: (id: string, payload: OAuthCredentialRotationPayload, params?: ListParams) => unwrap<OAuthCredentialRotationResult>(http.post(`/grok-oauth/accounts/${id}/reauthorize-from-code`, payload, { params })),
  reauthorizeFromRefreshToken: (id: string, payload: OAuthCredentialRotationPayload, params?: ListParams) => unwrap<OAuthCredentialRotationResult>(http.post(`/grok-oauth/accounts/${id}/reauthorize-from-refresh-token`, payload, { params }))
}

export const myGrokOAuthApi = {
  authUrl: (payload: Record<string, unknown>) => unwrap<OAuthAuthURLResult>(http.post('/my-grok-oauth/auth-url', payload)),
  createFromCode: (payload: Record<string, unknown>) => unwrap<AccountCreateResult>(http.post('/my-grok-oauth/create-from-code', payload)),
  createFromRefreshToken: (payload: Record<string, unknown>) => unwrap<AccountCreateResult>(http.post('/my-grok-oauth/create-from-refresh-token', payload)),
  ssoToOAuth: (payload: Record<string, unknown>) => unwrap<GrokSsoImportResult>(http.post('/my-grok-oauth/sso-to-oauth', payload, { timeout: 600000 })),
  refreshToken: (id: string, payload: OAuthCredentialRotationPayload) => unwrap<OAuthCredentialRotationResult>(http.post(`/my-grok-oauth/accounts/${id}/refresh-token`, payload, { timeout: 130000 })),
  reauthorizeFromCode: (id: string, payload: OAuthCredentialRotationPayload) => unwrap<OAuthCredentialRotationResult>(http.post(`/my-grok-oauth/accounts/${id}/reauthorize-from-code`, payload)),
  reauthorizeFromRefreshToken: (id: string, payload: OAuthCredentialRotationPayload) => unwrap<OAuthCredentialRotationResult>(http.post(`/my-grok-oauth/accounts/${id}/reauthorize-from-refresh-token`, payload))
}
