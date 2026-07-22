import { computed, ref } from 'vue'
import axios from 'axios'

import { api } from '@/api/client'
import { isAdminRole, isSuperAdminRole } from '@/shared/systemAccountRoles'
import type { CaptchaChallengeSummary, CurrentUserSummary } from '@/types/domain'
import { chatGenerationRuntime } from '@/views/chat/chatGenerationRuntime'
import { getDefaultChatLocalCache } from '@/views/chat/chatLocalCache'
import { clearChatPendingSubmission } from '@/views/chat/chatPendingSubmissionStorage'
import { drainChatConversationSyncAccount, invalidateChatConversationSyncAccount } from '@/views/chat/chatConversationSync'

const currentUser = ref<CurrentUserSummary>()
const authChecked = ref(false)
let authStateVersion = 0
let authLoadGeneration = 0

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
  const requestVersion = authStateVersion
  const loadGeneration = ++authLoadGeneration
  try {
    const user = await api.auth.me()
    if (requestVersion !== authStateVersion || loadGeneration !== authLoadGeneration) return currentUser.value
    applyCurrentUser(user)
    authChecked.value = true
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
  const operationVersion = ++authStateVersion
  const user = await api.auth.login(payload)
  if (operationVersion === authStateVersion) {
    applyCurrentUser(user)
    authChecked.value = true
  }
  return user
}

export async function logout(): Promise<void> {
  const systemAccountId = currentUser.value?.id
  const operationVersion = ++authStateVersion
  await api.auth.logout()
  if (operationVersion !== authStateVersion) return
  let chatCleanupFailed = false
  let chatCleanupError: unknown
  try {
    await clearCurrentAccountChatState(systemAccountId)
  } catch (error) {
    chatCleanupFailed = true
    chatCleanupError = error
  }
  if (operationVersion !== authStateVersion) return
  clearAuthState()
  if (chatCleanupFailed) throw chatCleanupError
}

function isExplicitUnauthorized(error: unknown): boolean {
  return axios.isAxiosError(error) && error.response?.status === 401
}

export async function clearCurrentAccountChatState(systemAccountId = currentUser.value?.id): Promise<void> {
  if (!systemAccountId) return
  invalidateChatConversationSyncAccount(systemAccountId)
  chatGenerationRuntime.close(systemAccountId)
  if (typeof window !== 'undefined') clearChatPendingSubmission(window.sessionStorage, systemAccountId)
  await drainChatConversationSyncAccount(systemAccountId)
  await getDefaultChatLocalCache().clearAccount(systemAccountId)
}

export async function changePassword(payload: { oldPassword?: string; newPassword: string }): Promise<CurrentUserSummary> {
  const operationVersion = ++authStateVersion
  const user = await api.auth.changePassword(payload)
  if (operationVersion === authStateVersion) applyCurrentUser(user)
  return user
}

export async function updateProfile(payload: { displayName: string }): Promise<CurrentUserSummary> {
  const operationVersion = ++authStateVersion
  const user = await api.auth.updateProfile(payload)
  if (operationVersion === authStateVersion) applyCurrentUser(user)
  return user
}

export function clearAuthState(): void {
  authStateVersion += 1
  currentUser.value = undefined
  authChecked.value = true
}

function applyCurrentUser(user: CurrentUserSummary): void {
  currentUser.value = user
}
