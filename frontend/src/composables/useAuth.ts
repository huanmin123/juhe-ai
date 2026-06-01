import { computed, ref } from 'vue'

import { api } from '@/api/client'
import { isAdminRole, isSuperAdminRole } from '@/shared/systemAccountRoles'
import type { CaptchaChallengeSummary, CurrentUserSummary } from '@/types/domain'

const currentUser = ref<CurrentUserSummary>()
const authChecked = ref(false)

export const authState = {
  currentUser,
  authChecked,
  isLoggedIn: computed(() => Boolean(currentUser.value)),
  isAdmin: computed(() => isAdminRole(currentUser.value?.role)),
  isSuperAdmin: computed(() => isSuperAdminRole(currentUser.value?.role))
}

export async function loadCurrentUser(force = false): Promise<CurrentUserSummary | undefined> {
  if (authChecked.value && !force) {
    return currentUser.value
  }
  try {
    currentUser.value = await api.auth.me()
    return currentUser.value
  } catch {
    currentUser.value = undefined
    return undefined
  } finally {
    authChecked.value = true
  }
}

export async function loadCaptcha(): Promise<CaptchaChallengeSummary> {
  return api.auth.captcha()
}

export async function login(payload: { username: string; password: string; captchaId: string; captchaCode: string }): Promise<CurrentUserSummary> {
  currentUser.value = await api.auth.login(payload)
  authChecked.value = true
  return currentUser.value
}

export async function logout(): Promise<void> {
  try {
    await api.auth.logout()
  } finally {
    clearAuthState()
  }
}

export async function changePassword(payload: { oldPassword?: string; newPassword: string }): Promise<CurrentUserSummary> {
  currentUser.value = await api.auth.changePassword(payload)
  return currentUser.value
}

export function clearAuthState(): void {
  currentUser.value = undefined
  authChecked.value = true
}
