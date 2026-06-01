import { ref } from 'vue'

import { isAdminRole } from '@/shared/systemAccountRoles'
import type { CurrentUserSummary } from '@/types/domain'

export type AppMenuMode = 'self' | 'admin'

const defaultMenuMode: AppMenuMode = 'self'
const menuModeStoragePrefix = 'juhe-ai:menu-mode:'

export const appMenuMode = ref<AppMenuMode>(defaultMenuMode)

function normalizeMenuMode(value: unknown): AppMenuMode {
  return value === 'admin' ? 'admin' : 'self'
}

function canUseAdminMode(user?: CurrentUserSummary): boolean {
  return isAdminRole(user?.role)
}

function getStorage(): Storage | undefined {
  if (typeof window === 'undefined') {
    return undefined
  }
  try {
    return window.localStorage
  } catch {
    return undefined
  }
}

function getStorageKey(user: CurrentUserSummary): string {
  return `${menuModeStoragePrefix}${user.id || user.username}`
}

export function readMenuModePreference(user?: CurrentUserSummary): AppMenuMode {
  if (!canUseAdminMode(user)) {
    return defaultMenuMode
  }
  const storage = getStorage()
  if (!storage || !user) {
    return defaultMenuMode
  }
  try {
    return normalizeMenuMode(storage.getItem(getStorageKey(user)))
  } catch {
    return defaultMenuMode
  }
}

export function syncMenuModeWithUser(user?: CurrentUserSummary): void {
  appMenuMode.value = readMenuModePreference(user)
}

export function setMenuModeFromRoute(user: CurrentUserSummary | undefined, mode: AppMenuMode): void {
  appMenuMode.value = canUseAdminMode(user) ? mode : defaultMenuMode
}

export function saveMenuModePreference(user: CurrentUserSummary | undefined, mode: AppMenuMode): AppMenuMode {
  const nextMode = canUseAdminMode(user) ? normalizeMenuMode(mode) : defaultMenuMode
  appMenuMode.value = nextMode
  const storage = getStorage()
  if (storage && user && canUseAdminMode(user)) {
    try {
      storage.setItem(getStorageKey(user), nextMode)
    } catch {
      // 浏览器禁用本地存储时仍允许本次切换，只是不保留下次偏好。
    }
  }
  return nextMode
}

export function getDefaultPathForMenuMode(mode: AppMenuMode): string {
  return mode === 'admin' ? '/stats' : '/my-accounts'
}

export function getPreferredEntryPath(user?: CurrentUserSummary): string {
  return getDefaultPathForMenuMode(readMenuModePreference(user))
}
