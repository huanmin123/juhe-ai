import type { AccountOAuthAuthorizeForm } from './accountFormTypes'
import type { AccountOAuthCreateCommonPayload } from './accountSavePayload'

export type OAuthCodePayload = AccountOAuthCreateCommonPayload & {
  sessionId?: string
  callbackUrl: string
}

export type OAuthRefreshTokenPayload = AccountOAuthCreateCommonPayload & {
  refreshToken: string
}

export type ReauthorizeCodePayload = {
  sessionId?: string
  callbackUrl: string
}

export type ReauthorizeRefreshTokenPayload = {
  refreshToken: string
}

export function buildOAuthCreatePayload(input: {
  commonPayload: AccountOAuthCreateCommonPayload
  form: AccountOAuthAuthorizeForm
  sessionId?: string
}): OAuthCodePayload | OAuthRefreshTokenPayload {
  if (input.form.oauthMode === 'manual') {
    return {
      ...input.commonPayload,
      sessionId: input.sessionId,
      callbackUrl: input.form.callbackUrl
    }
  }
  return {
    ...input.commonPayload,
    refreshToken: input.form.refreshToken
  }
}

export function validateReauthorizeForm(form: AccountOAuthAuthorizeForm, hasAuthSession: boolean): string | undefined {
  if (form.oauthMode === 'manual' && !hasAuthSession) return '请先生成授权链接'
  if (form.oauthMode === 'manual' && !form.callbackUrl.trim()) return '请粘贴回调 URL'
  if (form.oauthMode === 'refresh_token' && !form.refreshToken.trim()) return '请填写 Refresh Token'
  return undefined
}

export function buildReauthorizePayload(input: {
  form: AccountOAuthAuthorizeForm
  sessionId?: string
}): ReauthorizeCodePayload | ReauthorizeRefreshTokenPayload {
  if (input.form.oauthMode === 'manual') {
    return {
      sessionId: input.sessionId,
      callbackUrl: input.form.callbackUrl
    }
  }
  return { refreshToken: input.form.refreshToken }
}

export function authUrl(value?: string): string | undefined {
  return value || undefined
}
