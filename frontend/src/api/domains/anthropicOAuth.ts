import type { AccountSummary, OAuthAuthURLResult } from '@/types/domain'
import type { ListParams } from '../contracts'
import { http, unwrap } from '../http'

export const anthropicOAuthApi = {
  authUrl: (payload: Record<string, unknown>) => unwrap<OAuthAuthURLResult>(http.post('/anthropic-oauth/auth-url', payload)),
  createFromCode: (payload: Record<string, unknown>, params?: ListParams) => unwrap<AccountSummary>(http.post('/anthropic-oauth/create-from-code', payload, { params })),
  createFromRefreshToken: (payload: Record<string, unknown>, params?: ListParams) => unwrap<AccountSummary>(http.post('/anthropic-oauth/create-from-refresh-token', payload, { params })),
  refreshToken: (id: string, params?: ListParams) => unwrap<AccountSummary>(http.post(`/anthropic-oauth/accounts/${id}/refresh-token`, {}, { params, timeout: 130000 })),
  reauthorizeFromCode: (id: string, payload: Record<string, unknown>, params?: ListParams) => unwrap<AccountSummary>(http.post(`/anthropic-oauth/accounts/${id}/reauthorize-from-code`, payload, { params })),
  reauthorizeFromRefreshToken: (id: string, payload: Record<string, unknown>, params?: ListParams) => unwrap<AccountSummary>(http.post(`/anthropic-oauth/accounts/${id}/reauthorize-from-refresh-token`, payload, { params }))
}

export const myAnthropicOAuthApi = {
  authUrl: (payload: Record<string, unknown>) => unwrap<OAuthAuthURLResult>(http.post('/my-anthropic-oauth/auth-url', payload)),
  createFromCode: (payload: Record<string, unknown>) => unwrap<AccountSummary>(http.post('/my-anthropic-oauth/create-from-code', payload)),
  createFromRefreshToken: (payload: Record<string, unknown>) => unwrap<AccountSummary>(http.post('/my-anthropic-oauth/create-from-refresh-token', payload)),
  refreshToken: (id: string) => unwrap<AccountSummary>(http.post(`/my-anthropic-oauth/accounts/${id}/refresh-token`, {}, { timeout: 130000 })),
  reauthorizeFromCode: (id: string, payload: Record<string, unknown>) => unwrap<AccountSummary>(http.post(`/my-anthropic-oauth/accounts/${id}/reauthorize-from-code`, payload)),
  reauthorizeFromRefreshToken: (id: string, payload: Record<string, unknown>) => unwrap<AccountSummary>(http.post(`/my-anthropic-oauth/accounts/${id}/reauthorize-from-refresh-token`, payload))
}
