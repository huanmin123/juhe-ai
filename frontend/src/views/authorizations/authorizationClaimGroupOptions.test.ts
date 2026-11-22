import { describe, expect, it } from 'vitest'

import type { GroupOptionSummary } from '@/types/domain'
import { claimTargetGroupOptions, claimTargetGroupOptionsQuery } from './authorizationClaimGroupOptions'

function group(overrides: Partial<GroupOptionSummary> = {}): GroupOptionSummary {
  return { id: 'g-1', name: '分组', ...overrides }
}

describe('claimTargetGroupOptionsQuery', () => {
  it('必须带 purpose=account（缺省 select 只投影 {id,name}，候选恒为空）与 manageableOnly/groupKind=standard', () => {
    expect(claimTargetGroupOptionsQuery()).toEqual({
      purpose: 'account',
      manageableOnly: true,
      groupKind: 'standard',
      limit: 50
    })
  })
})

describe('claimTargetGroupOptions', () => {
  it('过滤授权绑入、停用与非普通分组，保留已启用普通分组', () => {
    const options = claimTargetGroupOptions([
      group({ id: 'g-ok', name: '可用分组', enabled: true, groupKind: 'standard', accessType: 'owner' }),
      group({ id: 'g-auth', name: '授权绑入分组', enabled: true, groupKind: 'standard', accessType: 'authorized' }),
      group({ id: 'g-disabled', name: '停用分组', enabled: false, groupKind: 'standard', accessType: 'owner' }),
      group({ id: 'g-super', name: '超级分组', enabled: true, groupKind: 'super', accessType: 'owner' })
    ])
    expect(options).toEqual([{ label: '可用分组', value: 'g-ok' }])
  })

  it('缺 groupKind 的行（select purpose 投影形态）一律丢弃', () => {
    const options = claimTargetGroupOptions([
      group({ id: 'g-bare', name: '仅 id/name 投影' })
    ])
    expect(options).toEqual([])
  })

  it('可选字段缺省时按可领取处理（accessType 非 authorized、enabled 未停用）', () => {
    const options = claimTargetGroupOptions([
      group({ id: 'g-bare', name: '未标访问类型', groupKind: 'standard' }),
      group({ id: 'g-enabled', name: '显式启用', enabled: true, groupKind: 'standard' })
    ])
    expect(options).toEqual([
      { label: '未标访问类型', value: 'g-bare' },
      { label: '显式启用', value: 'g-enabled' }
    ])
  })

  it('映射为 label/value 并保持输入顺序', () => {
    const options = claimTargetGroupOptions([
      group({ id: 'g-b', name: '乙分组', enabled: true, groupKind: 'standard', accessType: 'owner' }),
      group({ id: 'g-a', name: '甲分组', enabled: true, groupKind: 'standard', accessType: 'owner' })
    ])
    expect(options).toEqual([
      { label: '乙分组', value: 'g-b' },
      { label: '甲分组', value: 'g-a' }
    ])
  })

  it('全部被过滤时返回空数组', () => {
    const options = claimTargetGroupOptions([
      group({ id: 'g-auth', name: '授权绑入分组', groupKind: 'standard', accessType: 'authorized' }),
      group({ id: 'g-super', name: '超级分组', groupKind: 'super', accessType: 'owner' })
    ])
    expect(options).toEqual([])
  })

  it('空输入返回空数组', () => {
    expect(claimTargetGroupOptions([])).toEqual([])
  })
})
