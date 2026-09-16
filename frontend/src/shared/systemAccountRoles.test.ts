import { describe, expect, it } from 'vitest'

import { isAdminRole, isSuperAdminRole, systemAccountRoleColor, systemAccountRoleLabel } from './systemAccountRoles'

describe('isAdminRole', () => {
  it('super_admin 与 admin 返回 true', () => {
    expect(isAdminRole('super_admin')).toBe(true)
    expect(isAdminRole('admin')).toBe(true)
  })

  it('普通用户与非法值返回 false', () => {
    expect(isAdminRole('user')).toBe(false)
    expect(isAdminRole(undefined)).toBe(false)
    expect(isAdminRole('')).toBe(false)
    expect(isAdminRole(123)).toBe(false)
  })
})

describe('isSuperAdminRole', () => {
  it('仅 super_admin 返回 true', () => {
    expect(isSuperAdminRole('super_admin')).toBe(true)
    expect(isSuperAdminRole('admin')).toBe(false)
    expect(isSuperAdminRole('user')).toBe(false)
    expect(isSuperAdminRole(null)).toBe(false)
  })
})

describe('systemAccountRoleLabel', () => {
  it('映射角色中文文案', () => {
    expect(systemAccountRoleLabel('super_admin')).toBe('超级管理员')
    expect(systemAccountRoleLabel('admin')).toBe('管理员')
    expect(systemAccountRoleLabel('user')).toBe('用户')
  })
})

describe('systemAccountRoleColor', () => {
  it('映射角色标签颜色', () => {
    expect(systemAccountRoleColor('super_admin')).toBe('gold')
    expect(systemAccountRoleColor('admin')).toBe('geekblue')
    expect(systemAccountRoleColor('user')).toBe('default')
  })
})
