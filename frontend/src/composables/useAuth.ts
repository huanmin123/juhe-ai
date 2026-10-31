import { computed, ref } from 'vue'
import axios from 'axios'

import { api } from '@/api/client'
import { isAdminRole, isSuperAdminRole } from '@/shared/systemAccountRoles'
import type { CaptchaChallengeSummary, CurrentUserSummary } from '@/types/domain'

const currentUser = ref<CurrentUserSummary>()
const authChecked = ref(false)
let authStateVersion = 0
let authLoadGeneration = 0
const authRevision = ref(0)

export const authState = {
  currentUser,
  authChecked,
  revision: authRevision,
  isLoggedIn: computed(() => Boolean(currentUser.value)),
  isAdmin: computed(() => isAdminRole(currentUser.value?.role)),
  isSuperAdmin: computed(() => isSuperAdminRole(currentUser.value?.role))
}

export async function loadCurrentUser(force = false): Promise<CurrentUserSummary | undefined> {
  if (authChecked.value && !force) {
    return currentUser.value
  }
  const requestVersion = authStateVersion
  const loadGeneration = ++authLoadGeneration
  try {
    const user = await api.auth.me()
    if (requestVersion !== authStateVersion || loadGeneration !== authLoadGeneration) return currentUser.value
    applyCurrentUser(user)
    return currentUser.value
  } catch (error: unknown) {
    if (requestVersion !== authStateVersion || loadGeneration !== authLoadGeneration) return currentUser.value
    if (isExplicitUnauthorized(error)) {
      clearAuthState()
      return undefined
    }
    if (currentUser.value) {
      return currentUser.value
    }
    throw error
  }
}

export async function loadCaptcha(): Promise<CaptchaChallengeSummary> {
  return api.auth.captcha()
}

export async function login(payload: { username: string; password: string; captchaId?: string; captchaCode?: string }): Promise<CurrentUserSummary> {
  const operationVersion = advanceAuthStateVersion()
  const user = await api.auth.login(payload)
  if (operationVersion === authStateVersion) {
    applyCurrentUser(user)
  }
  return user
}

export async function logout(): Promise<void> {
  const operationVersion = advanceAuthStateVersion()
  await api.auth.logout()
  // 版本守卫保护清理期间新登录的身份，不被本次旧注销清空。
  if (operationVersion !== authStateVersion) return
  clearAuthState()
}

function isExplicitUnauthorized(error: unknown): boolean {
  return axios.isAxiosError(error) && error.response?.status === 401
}

export async function changePassword(payload: { oldPassword?: string; newPassword: string }): Promise<CurrentUserSummary> {
  const operationVersion = advanceAuthStateVersion()
  const user = await api.auth.changePassword(payload)
  if (operationVersion === authStateVersion) applyCurrentUser(user)
  return user
}

export async function updateProfile(payload: { displayName: string }): Promise<CurrentUserSummary> {
  const operationVersion = advanceAuthStateVersion()
  const user = await api.auth.updateProfile(payload)
  if (operationVersion === authStateVersion) applyCurrentUser(user)
  return user
}

export function clearAuthState(): void {
  advanceAuthStateVersion()
  if (currentUser.value) authRevision.value += 1
  currentUser.value = undefined
  authChecked.value = true
}

function applyCurrentUser(user: CurrentUserSummary): void {
  const previous = currentUser.value
  if (
    !previous
    || previous.id !== user.id
    || previous.role !== user.role
    || previous.mustChangePassword !== user.mustChangePassword
  ) {
    authRevision.value += 1
  }
  currentUser.value = user
  authChecked.value = true
}

function advanceAuthStateVersion(): number {
  authStateVersion += 1
  return authStateVersion
}
