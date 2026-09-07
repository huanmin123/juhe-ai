import { AsyncLocalStorage } from 'node:async_hooks'

import { isAdminRole, type SystemAccountRole } from '../../domain/types.js'

export interface RequestAuthContext {
  systemAccountId: string
  role: SystemAccountRole
  username: string
  displayName: string
  mustChangePassword: boolean
  sessionId: string
}

export interface RequestAccessScope {
  systemAccountId: string
  role: SystemAccountRole
  systemAccountFilterId?: string
}

const requestAuthContext = new AsyncLocalStorage<RequestAuthContext | undefined>()

export function withRequestAuthContext<T>(context: RequestAuthContext | undefined, handler: () => T): T {
  return requestAuthContext.run(context, handler)
}

export function getRequestAuthContext(): RequestAuthContext | undefined {
  return requestAuthContext.getStore()
}

export function getRequestAccessScope(systemAccountIdFilter?: unknown): RequestAccessScope | undefined {
  const context = getRequestAuthContext()
  if (!context) return undefined
  const filterId = isAdminRole(context.role) ? normalizeSystemAccountIdFilter(systemAccountIdFilter) : undefined
  return {
    systemAccountId: context.systemAccountId,
    role: context.role,
    systemAccountFilterId: filterId
  }
}

function normalizeSystemAccountIdFilter(value: unknown): string | undefined {
  const rawValue = Array.isArray(value) ? value[0] : value
  if (typeof rawValue !== 'string') return undefined
  const text = rawValue.trim()
  return text && text !== 'all' ? text : undefined
}
