import { getRequestAuthContext } from '../modules/auth/request-context.js'

export interface AccessScope {
  systemAccountId: string
  role: 'admin' | 'user'
  systemAccountFilterId?: string
}

export function resolveAccessScope(access?: AccessScope): AccessScope | undefined {
  if (access) return access
  const context = getRequestAuthContext()
  return context ? { systemAccountId: context.systemAccountId, role: context.role } : undefined
}

export function currentSystemAccountId(access?: AccessScope): string {
  const systemAccountId = resolveAccessScope(access)?.systemAccountId?.trim()
  if (!systemAccountId) {
    throw new Error('缺少系统账户上下文')
  }
  return systemAccountId
}

export function canAccessAll(access?: AccessScope): boolean {
  const scope = resolveAccessScope(access)
  return scope?.role === 'admin'
}

export function scopedSystemAccountId(access?: AccessScope): string | undefined {
  const scope = resolveAccessScope(access)
  if (!scope) return undefined
  if (scope.role === 'admin') {
    const filterId = scope.systemAccountFilterId?.trim()
    return filterId || undefined
  }
  return scope.systemAccountId
}

export function buildSystemAccountScopeClause(access?: AccessScope, column = 'system_account_id'): { clause: string; params: Array<string> } {
  const systemAccountId = scopedSystemAccountId(access)
  if (!systemAccountId) {
    return { clause: '', params: [] }
  }
  return { clause: ` AND ${column} = ?`, params: [systemAccountId] }
}

export function buildSystemAccountWhereClause(access?: AccessScope, column = 'system_account_id'): { clause: string; params: Array<string> } {
  const systemAccountId = scopedSystemAccountId(access)
  if (!systemAccountId) {
    return { clause: '', params: [] }
  }
  return { clause: ` WHERE ${column} = ?`, params: [systemAccountId] }
}

export function includeSystemAccountFields(access?: AccessScope): boolean {
  return canAccessAll(access)
}

export function manageableSystemAccountId(access?: AccessScope): string | undefined {
  return scopedSystemAccountId(access) ?? (canAccessAll(access) ? undefined : resolveAccessScope(access)?.systemAccountId)
}

export function userVisibleSystemAccountId(access?: AccessScope): string | undefined {
  return scopedSystemAccountId(access) ?? resolveAccessScope(access)?.systemAccountId
}
