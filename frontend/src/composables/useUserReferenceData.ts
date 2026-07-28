import { api } from '@/api/client'
import { authState } from '@/composables/useAuth'
import { isAdminRole } from '@/shared/systemAccountRoles'
import type { UserReferenceData } from '@/types/domain'

export type UserReferenceDataViewScope = 'self' | 'admin'

export interface UserReferenceDataScopeParams {
  viewScope?: UserReferenceDataViewScope
  systemAccountId?: string
}

export interface UserReferenceDataRequestScope {
  viewerSystemAccountId: string
  authRevision: number
  viewScope: UserReferenceDataViewScope
  ownerSystemAccountId: string
}

export interface UserReferenceDataResource {
  syncAuthSession: (viewerSystemAccountId: string | undefined, authRevision: number) => void
  get: (scope: UserReferenceDataRequestScope) => UserReferenceData | undefined
  load: (scope: UserReferenceDataRequestScope) => Promise<UserReferenceData | undefined>
  invalidate: (scope: UserReferenceDataRequestScope) => void
  clear: () => void
}

interface UserReferenceDataCacheEntry {
  value?: UserReferenceData
  inFlight?: Promise<UserReferenceData | undefined>
  generation: number
}

export function createUserReferenceDataResource(options: {
  fetch: (scope: UserReferenceDataRequestScope) => Promise<UserReferenceData>
}): UserReferenceDataResource {
  const entries = new Map<string, UserReferenceDataCacheEntry>()
  let authSessionKey: string | undefined
  let generation = 0

  return {
    syncAuthSession(viewerSystemAccountId, authRevision) {
      const nextSessionKey = userReferenceDataAuthSessionKey(viewerSystemAccountId, authRevision)
      if (authSessionKey === nextSessionKey) return
      authSessionKey = nextSessionKey
      generation += 1
      entries.clear()
    },
    get(scope) {
      if (!isCurrentAuthSession(scope, authSessionKey)) return undefined
      return entries.get(userReferenceDataScopeKey(scope))?.value
    },
    async load(scope) {
      if (!isCurrentAuthSession(scope, authSessionKey)) return undefined
      const key = userReferenceDataScopeKey(scope)
      let entry = entries.get(key)
      if (entry?.value) return entry.value
      if (entry?.inFlight) return entry.inFlight
      entry = { generation: 0 }
      entries.set(key, entry)

      const requestGeneration = generation
      const entryGeneration = entry.generation
      const request = options.fetch(scope).then((value) => {
        if (
          generation !== requestGeneration
          || entries.get(key) !== entry
          || entry.generation !== entryGeneration
          || !isCurrentAuthSession(scope, authSessionKey)
        ) {
          return undefined
        }
        if (value.systemAccountId !== scope.ownerSystemAccountId) {
          throw new Error('用户默认资源响应与请求作用域不一致')
        }
        entry.value = value
        return value
      }).finally(() => {
        if (entries.get(key) === entry && entry.inFlight === request) {
          entry.inFlight = undefined
        }
      })
      entry.inFlight = request
      return request
    },
    invalidate(scope) {
      if (!isCurrentAuthSession(scope, authSessionKey)) return
      const key = userReferenceDataScopeKey(scope)
      const entry = entries.get(key)
      if (entry) entry.generation += 1
      entries.delete(key)
    },
    clear() {
      generation += 1
      entries.clear()
    }
  }
}

export function userReferenceDataScopeKey(scope: UserReferenceDataRequestScope): string {
  return [
    scope.viewerSystemAccountId,
    scope.authRevision,
    scope.viewScope,
    scope.ownerSystemAccountId
  ].join(':')
}

const userReferenceDataResource = createUserReferenceDataResource({
  fetch: (scope) => scope.viewScope === 'admin'
    ? api.uiBootstrap.options({ systemAccountId: scope.ownerSystemAccountId })
    : api.myUiBootstrap.options()
})

export function syncUserReferenceDataAuthState(
  viewerSystemAccountId = authState.currentUser.value?.id,
  authRevision = authState.revision.value
): void {
  userReferenceDataResource.syncAuthSession(viewerSystemAccountId, authRevision)
}

export function getCachedUserReferenceData(params: UserReferenceDataScopeParams = {}): UserReferenceData | undefined {
  const scope = currentUserReferenceDataScope(params)
  if (!scope) return undefined
  syncUserReferenceDataAuthState(scope.viewerSystemAccountId, scope.authRevision)
  return userReferenceDataResource.get(scope)
}

export function loadUserReferenceData(params: UserReferenceDataScopeParams = {}): Promise<UserReferenceData | undefined> {
  const scope = currentUserReferenceDataScope(params)
  if (!scope) return Promise.resolve(undefined)
  syncUserReferenceDataAuthState(scope.viewerSystemAccountId, scope.authRevision)
  return userReferenceDataResource.load(scope)
}

export async function prewarmSelfUserReferenceData(): Promise<UserReferenceData | undefined> {
  try {
    return await loadUserReferenceData({ viewScope: 'self' })
  } catch {
    return undefined
  }
}

export function invalidateUserReferenceData(params: UserReferenceDataScopeParams = {}): void {
  const scope = currentUserReferenceDataScope(params)
  if (!scope) return
  syncUserReferenceDataAuthState(scope.viewerSystemAccountId, scope.authRevision)
  userReferenceDataResource.invalidate(scope)
}

export function clearUserReferenceDataCache(): void {
  userReferenceDataResource.clear()
}

function currentUserReferenceDataScope(params: UserReferenceDataScopeParams): UserReferenceDataRequestScope | undefined {
  const viewer = authState.currentUser.value
  if (!viewer) return undefined
  const viewScope = params.viewScope === 'admin' ? 'admin' : 'self'
  if (viewScope === 'admin' && !isAdminRole(viewer.role)) return undefined
  const ownerSystemAccountId = viewScope === 'admin'
    ? params.systemAccountId?.trim()
    : viewer.id
  if (!ownerSystemAccountId) return undefined
  return {
    viewerSystemAccountId: viewer.id,
    authRevision: authState.revision.value,
    viewScope,
    ownerSystemAccountId
  }
}

function userReferenceDataAuthSessionKey(viewerSystemAccountId: string | undefined, authRevision: number): string {
  return `${viewerSystemAccountId?.trim() || 'anonymous'}:${authRevision}`
}

function isCurrentAuthSession(scope: UserReferenceDataRequestScope, authSessionKey: string | undefined): boolean {
  return authSessionKey === userReferenceDataAuthSessionKey(scope.viewerSystemAccountId, scope.authRevision)
}
