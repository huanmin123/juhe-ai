import type { AccountCreateResult, AccountSummary, OAuthAuthURLResult } from '@/types/domain'
import type { ListParams } from '../contracts'
import { http, unwrap } from '../http'

export const openaiOAuthApi = {
  authUrl: (payload: Record<string, unknown>) => unwrap<OAuthAuthURLResult>(http.post('/openai-oauth/auth-url', payload)),
  createFromCode: (payload: Record<string, unknown>, params?: ListParams) => unwrap<AccountCreateResult>(http.post('/openai-oauth/create-from-code', payload, { params })),
  createFromRefreshToken: (payload: Record<string, unknown>, params?: ListParams) => unwrap<AccountCreateResult>(http.post('/openai-oauth/create-from-refresh-token', payload, { params })),
  refreshToken: (id: string, params?: ListParams) => unwrap<AccountSummary>(http.post(`/openai-oauth/accounts/${id}/refresh-token`, {}, { params, timeout: 130000 })),
  reauthorizeFromCode: (id: string, payload: Record<string, unknown>, params?: ListParams) => unwrap<AccountSummary>(http.post(`/openai-oauth/accounts/${id}/reauthorize-from-code`, payload, { params })),
  reauthorizeFromRefreshToken: (id: string, payload: Record<string, unknown>, params?: ListParams) => unwrap<AccountSummary>(http.post(`/openai-oauth/accounts/${id}/reauthorize-from-refresh-token`, payload, { params }))
}

export const myOpenaiOAuthApi = {
  authUrl: (payload: Record<string, unknown>) => unwrap<OAuthAuthURLResult>(http.post('/my-openai-oauth/auth-url', payload)),
  createFromCode: (payload: Record<string, unknown>) => unwrap<AccountCreateResult>(http.post('/my-openai-oauth/create-from-code', payload)),
  createFromRefreshToken: (payload: Record<string, unknown>) => unwrap<AccountCreateResult>(http.post('/my-openai-oauth/create-from-refresh-token', payload)),
  refreshToken: (id: string) => unwrap<AccountSummary>(http.post(`/my-openai-oauth/accounts/${id}/refresh-token`, {}, { timeout: 130000 })),
  reauthorizeFromCode: (id: string, payload: Record<string, unknown>) => unwrap<AccountSummary>(http.post(`/my-openai-oauth/accounts/${id}/reauthorize-from-code`, payload)),
  reauthorizeFromRefreshToken: (id: string, payload: Record<string, unknown>) => unwrap<AccountSummary>(http.post(`/my-openai-oauth/accounts/${id}/reauthorize-from-refresh-token`, payload))
}
