import { describe, expect, it } from 'vitest'

import {
  principalLabelForId,
  rememberPrincipalLabel,
  rememberPrincipalSelection,
  rememberPrincipalSelections,
  rememberSystemAccountPrincipals,
  rememberSystemTeamPrincipals,
  systemAccountPrincipalName
} from './principalLabelCache'
import type { SystemAccountPrincipalSummary, SystemTeamPrincipalSummary } from '@/types/domain'

function accountSummary(id: string, displayName: string): SystemAccountPrincipalSummary {
  return { id, username: `user-${id}`, displayName, status: 'active' } as SystemAccountPrincipalSummary
}

function teamSummary(id: string, name: string): SystemTeamPrincipalSummary {
  return { id, name, status: 'active' } as SystemTeamPrincipalSummary
}

describe('rememberPrincipalLabel / principalLabelForId', () => {
  it('记录后可按 kind 与 id 查询', () => {
    rememberPrincipalLabel('system_account', 'acc-1', '账户一')
    rememberPrincipalLabel('team', 'team-1', '团队一')
    expect(principalLabelForId('system_account', 'acc-1')).toBe('账户一')
    expect(principalLabelForId('team', 'team-1')).toBe('团队一')
  })

  it('空白 id 或名称不写入', () => {
    rememberPrincipalLabel('system_account', '  ', '名称')
    rememberPrincipalLabel('system_account', 'acc-2', '   ')
    rememberPrincipalLabel('system_account', undefined, undefined)
    expect(principalLabelForId('system_account', 'acc-2')).toBeUndefined()
  })

  it('kind 之间相互隔离', () => {
    rememberPrincipalLabel('system_account', 'shared-id', '账户')
    expect(principalLabelForId('team', 'shared-id')).toBeUndefined()
  })

  it('查询时空白 id 返回 undefined', () => {
    rememberPrincipalLabel('team', 'team-2', '团队二')
    expect(principalLabelForId('team', undefined)).toBeUndefined()
    expect(principalLabelForId('team', '   ')).toBeUndefined()
  })
})

describe('rememberPrincipalSelection / rememberPrincipalSelections', () => {
  it('记录选中主体（默认 kind 为 system_account）', () => {
    rememberPrincipalSelection({ id: 'acc-3', name: '账户三', kind: 'system_account' })
    rememberPrincipalSelection({ id: 'team-3', name: '团队三', kind: 'team' })
    rememberPrincipalSelection({ id: 'acc-4', name: '账户四', kind: 'system_account' })
    expect(principalLabelForId('system_account', 'acc-3')).toBe('账户三')
    expect(principalLabelForId('team', 'team-3')).toBe('团队三')
    expect(principalLabelForId('system_account', 'acc-4')).toBe('账户四')
  })

  it('undefined 选中项被跳过', () => {
    rememberPrincipalSelections([undefined, { id: 'acc-5', name: '账户五', kind: 'system_account' }])
    expect(principalLabelForId('system_account', 'acc-5')).toBe('账户五')
  })
})

describe('rememberSystemAccountPrincipals / rememberSystemTeamPrincipals', () => {
  it('账户主体使用 displayName，团队主体使用 name', () => {
    rememberSystemAccountPrincipals([accountSummary('acc-6', '显示名六')])
    rememberSystemTeamPrincipals([teamSummary('team-6', '团队六')])
    expect(principalLabelForId('system_account', 'acc-6')).toBe('显示名六')
    expect(principalLabelForId('team', 'team-6')).toBe('团队六')
  })
})

describe('systemAccountPrincipalName', () => {
  it('返回账户 displayName', () => {
    expect(systemAccountPrincipalName(accountSummary('acc-7', '显示名七'))).toBe('显示名七')
  })
})
