import type { AuthSessionListResult, AuthSessionRevokeResult, CaptchaChallengeSummary, CurrentUserSummary } from '@/types/domain'
import type { AuthSessionListParams } from '../contracts'
import { http, unwrap } from '../http'
import { authSessionListParams } from '../params'

export const authApi = {
  captcha: () => unwrap<CaptchaChallengeSummary>(http.get('/auth/captcha')),
  login: (payload: { username: string; password: string; captchaId?: string; captchaCode?: string }) => unwrap<CurrentUserSummary>(http.post('/auth/login', payload)),
  logout: () => unwrap<{ loggedOut: boolean }>(http.post('/auth/logout')),
  me: () => unwrap<CurrentUserSummary>(http.get('/auth/me')),
  updateProfile: (payload: { displayName: string }) => unwrap<CurrentUserSummary>(http.patch('/auth/me', payload)),
  changePassword: (payload: { oldPassword?: string; newPassword: string }) => unwrap<CurrentUserSummary>(http.post('/auth/change-password', payload)),
  sessions: (params?: AuthSessionListParams) => unwrap<AuthSessionListResult>(http.get('/auth/sessions', { params: authSessionListParams(params) })),
  revokeSession: (sessionId: string) => unwrap<AuthSessionRevokeResult>(http.delete(`/auth/sessions/${encodeURIComponent(sessionId)}`))
}
