import type { AccountCreateResult, AccountSummary, OAuthAuthURLResult } from '@/types/domain'
import type { ListParams } from '../contracts'
import { http, unwrap } from '../http'

export interface GrokSsoImportResult {
  created: Array<{ index: number; name: string; email?: string; account: AccountSummary }>
  failed: Array<{ index: number; name?: string; email?: string; error: string }>
}

export const grokOAuthApi = {
  authUrl: (payload: Record<string, unknown>) => unwrap<OAuthAuthURLResult>(http.post('/grok-oauth/auth-url', payload)),
  createFromCode: (payload: Record<string, unknown>, params?: ListParams) => unwrap<AccountCreateResult>(http.post('/grok-oauth/create-from-code', payload, { params })),
  createFromRefreshToken: (payload: Record<string, unknown>, params?: ListParams) => unwrap<AccountCreateResult>(http.post('/grok-oauth/create-from-refresh-token', payload, { params })),
  ssoToOAuth: (payload: Record<string, unknown>, params?: ListParams) => unwrap<GrokSsoImportResult>(http.post('/grok-oauth/sso-to-oauth', payload, { params, timeout: 600000 })),
  refreshToken: (id: string, params?: ListParams) => unwrap<AccountSummary>(http.post(`/grok-oauth/accounts/${id}/refresh-token`, {}, { params, timeout: 130000 })),
  reauthorizeFromCode: (id: string, payload: Record<string, unknown>, params?: ListParams) => unwrap<AccountSummary>(http.post(`/grok-oauth/accounts/${id}/reauthorize-from-code`, payload, { params })),
  reauthorizeFromRefreshToken: (id: string, payload: Record<string, unknown>, params?: ListParams) => unwrap<AccountSummary>(http.post(`/grok-oauth/accounts/${id}/reauthorize-from-refresh-token`, payload, { params }))
}

export const myGrokOAuthApi = {
  authUrl: (payload: Record<string, unknown>) => unwrap<OAuthAuthURLResult>(http.post('/my-grok-oauth/auth-url', payload)),
  createFromCode: (payload: Record<string, unknown>) => unwrap<AccountCreateResult>(http.post('/my-grok-oauth/create-from-code', payload)),
  createFromRefreshToken: (payload: Record<string, unknown>) => unwrap<AccountCreateResult>(http.post('/my-grok-oauth/create-from-refresh-token', payload)),
  ssoToOAuth: (payload: Record<string, unknown>) => unwrap<GrokSsoImportResult>(http.post('/my-grok-oauth/sso-to-oauth', payload, { timeout: 600000 })),
  refreshToken: (id: string) => unwrap<AccountSummary>(http.post(`/my-grok-oauth/accounts/${id}/refresh-token`, {}, { timeout: 130000 })),
  reauthorizeFromCode: (id: string, payload: Record<string, unknown>) => unwrap<AccountSummary>(http.post(`/my-grok-oauth/accounts/${id}/reauthorize-from-code`, payload)),
  reauthorizeFromRefreshToken: (id: string, payload: Record<string, unknown>) => unwrap<AccountSummary>(http.post(`/my-grok-oauth/accounts/${id}/reauthorize-from-refresh-token`, payload))
}
