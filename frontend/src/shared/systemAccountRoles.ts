import type { SystemAccountRole } from '@/types/domain'

export function isAdminRole(role: unknown): role is 'super_admin' | 'admin' {
  return role === 'super_admin' || role === 'admin'
}

export function isSuperAdminRole(role: unknown): role is 'super_admin' {
  return role === 'super_admin'
}

export function systemAccountRoleLabel(role: SystemAccountRole): string {
  if (role === 'super_admin') return '超级管理员'
  if (role === 'admin') return '管理员'
  return '用户'
}

export function systemAccountRoleColor(role: SystemAccountRole): string {
  if (role === 'super_admin') return 'gold'
  if (role === 'admin') return 'geekblue'
  return 'default'
}
