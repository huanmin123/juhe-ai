import type { CaptchaChallengeSummary, CurrentUserSummary } from '@/types/domain'
import { http, unwrap } from '../http'

export const authApi = {
  captcha: () => unwrap<CaptchaChallengeSummary>(http.get('/auth/captcha')),
  login: (payload: { username: string; password: string; captchaId: string; captchaCode: string }) => unwrap<CurrentUserSummary>(http.post('/auth/login', payload)),
  logout: () => unwrap<{ loggedOut: boolean }>(http.post('/auth/logout')),
  me: () => unwrap<CurrentUserSummary>(http.get('/auth/me')),
  updateProfile: (payload: { displayName: string }) => unwrap<CurrentUserSummary>(http.patch('/auth/me', payload)),
  changePassword: (payload: { oldPassword?: string; newPassword: string }) => unwrap<CurrentUserSummary>(http.post('/auth/change-password', payload))
}
