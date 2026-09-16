import { describe, expect, it } from 'vitest'

import { allSystemAccountsValue, matchesSystemAccountFilter, selectedSystemAccountId, systemAccountDisplayText } from './systemAccountFilter'

describe('selectedSystemAccountId', () => {
  it('非管理员视角始终返回 undefined', () => {
    expect(selectedSystemAccountId('sa-1', false)).toBeUndefined()
    expect(selectedSystemAccountId(allSystemAccountsValue, false)).toBeUndefined()
  })

  it('管理员视角下空值或 all 值返回 undefined', () => {
    expect(selectedSystemAccountId('', true)).toBeUndefined()
    expect(selectedSystemAccountId('   ', true)).toBeUndefined()
    expect(selectedSystemAccountId(allSystemAccountsValue, true)).toBeUndefined()
  })

  it('管理员视角下具体 ID 会被 trim 后返回', () => {
    expect(selectedSystemAccountId(' sa-1 ', true)).toBe('sa-1')
  })
})

describe('matchesSystemAccountFilter', () => {
  it('未选中具体账号时所有条目都匹配', () => {
    expect(matchesSystemAccountFilter({ systemAccountId: 'sa-1' }, allSystemAccountsValue, true)).toBe(true)
    expect(matchesSystemAccountFilter({ systemAccountId: 'sa-2' }, '', true)).toBe(true)
    expect(matchesSystemAccountFilter({}, 'anything', false)).toBe(true)
  })

  it('选中具体账号时仅匹配同账号条目', () => {
    expect(matchesSystemAccountFilter({ systemAccountId: 'sa-1' }, 'sa-1', true)).toBe(true)
    expect(matchesSystemAccountFilter({ systemAccountId: 'sa-2' }, 'sa-1', true)).toBe(false)
  })

  it('无账号归属的条目仅在不筛选时匹配', () => {
    expect(matchesSystemAccountFilter({}, 'sa-1', true)).toBe(false)
    expect(matchesSystemAccountFilter({}, allSystemAccountsValue, true)).toBe(true)
  })
})

describe('systemAccountDisplayText', () => {
  it('有名称时显示名称', () => {
    expect(systemAccountDisplayText({ systemAccountName: '管理员' })).toBe('管理员')
  })

  it('无名称时显示占位符', () => {
    expect(systemAccountDisplayText({})).toBe('-')
    expect(systemAccountDisplayText({ systemAccountName: '' })).toBe('-')
  })
})
