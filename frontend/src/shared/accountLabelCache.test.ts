import { describe, expect, it } from 'vitest'

import {
  accountLabelForId,
  accountSelectOptionLabel,
  accountSelectionForId,
  mergeSelectedAccountOptions,
  rememberAccountLabel,
  rememberAccountLabels,
  rememberAccountSelection,
  rememberAccountSelections,
  type AccountOptionLike,
  type AccountSelection
} from './accountLabelCache'
import type { SelectOption } from './selectLabelCache'

function account(partial: Partial<AccountOptionLike> & { id: string; name: string }): AccountOptionLike {
  return { ...partial }
}

describe('accountSelectOptionLabel', () => {
  it('owner 账户直接返回名称', () => {
    expect(accountSelectOptionLabel(account({ id: 'a1', name: '主账户' }))).toBe('主账户')
    expect(accountSelectOptionLabel(account({ id: 'a2', name: '授权账户', accessType: 'owner' }))).toBe('授权账户')
  })

  it('authorized 账户追加来源信息', () => {
    expect(accountSelectOptionLabel(account({ id: 'a3', name: '子账户', accessType: 'authorized', ownerSystemAccountName: '张三' })))
      .toBe('子账户（来自：张三）')
    expect(accountSelectOptionLabel(account({ id: 'a4', name: '子账户', accessType: 'authorized', ownerSystemAccountName: '   ' })))
      .toBe('子账户（来自授权）')
    expect(accountSelectOptionLabel(account({ id: 'a5', name: '子账户', accessType: 'authorized' })))
      .toBe('子账户（来自授权）')
  })

  it('authorized 账户名称中的历史授权后缀会被清理', () => {
    expect(accountSelectOptionLabel(account({ id: 'a6', name: '子账户（授权）', accessType: 'authorized', ownerSystemAccountName: '李四' })))
      .toBe('子账户（来自：李四）')
    expect(accountSelectOptionLabel(account({ id: 'a7', name: '子账户（授权 旧来源）', accessType: 'authorized', ownerSystemAccountName: '李四' })))
      .toBe('子账户（来自：李四）')
  })
})

describe('rememberAccountLabels / accountLabelForId', () => {
  it('批量记录账户标签后可按 id 查询', () => {
    rememberAccountLabels([
      account({ id: 'acc-1', name: '账户一' }),
      account({ id: 'acc-2', name: '账户二', accessType: 'authorized', ownerSystemAccountName: '王五' })
    ])
    expect(accountLabelForId('acc-1')).toBe('账户一')
    expect(accountLabelForId('acc-2')).toBe('账户二（来自：王五）')
    expect(accountLabelForId('missing')).toBeUndefined()
  })

  it('rememberAccountLabel 支持自定义缓存键', () => {
    rememberAccountLabel('acc-3', '账户三', 'custom-key')
    expect(accountLabelForId('acc-3', 'custom-key')).toBe('账户三')
    expect(accountLabelForId('acc-3')).toBeUndefined()
  })
})

describe('rememberAccountSelection / rememberAccountSelections', () => {
  it('记录选中的账户名称', () => {
    const selection: AccountSelection = { id: 'sel-1', name: '选中账户' }
    rememberAccountSelection(selection)
    expect(accountLabelForId('sel-1')).toBe('选中账户')
  })

  it('undefined 选中项被跳过', () => {
    rememberAccountSelections([undefined, { id: 'sel-2', name: '选中二' }])
    expect(accountLabelForId('sel-2')).toBe('选中二')
  })
})

describe('accountSelectionForId', () => {
  it('空白 id 返回 undefined', () => {
    expect(accountSelectionForId(undefined)).toBeUndefined()
    expect(accountSelectionForId('   ')).toBeUndefined()
  })

  it('优先从账户列表解析并继承授权信息', () => {
    const result = accountSelectionForId('opt-1', [account({ id: 'opt-1', name: '列表账户', accessType: 'authorized', ownerSystemAccountName: '赵六' })])
    expect(result).toEqual({ id: 'opt-1', name: '列表账户（来自：赵六）', accessType: 'authorized', ownerSystemAccountName: '赵六' })
  })

  it('账户列表未命中时回退选项列表', () => {
    const options: SelectOption[] = [{ label: '选项账户', value: 'opt-2' }]
    expect(accountSelectionForId('opt-2', [], options)).toEqual({ id: 'opt-2', name: '选项账户' })
  })

  it('前两者未命中时回退缓存', () => {
    rememberAccountLabel('opt-3', '缓存账户')
    expect(accountSelectionForId('opt-3')).toEqual({ id: 'opt-3', name: '缓存账户' })
  })

  it('全部未命中返回 undefined', () => {
    expect(accountSelectionForId('opt-ghost')).toBeUndefined()
  })
})

describe('mergeSelectedAccountOptions', () => {
  it('合并选项列表与选中账户，未知选中项被忽略', () => {
    rememberAccountLabel('m-acc', '缓存账户')
    const options: SelectOption[] = [{ label: '窗口账户', value: 'm-win' }]
    const merged = mergeSelectedAccountOptions(
      'accounts',
      options,
      ['m-win', 'm-acc', 'ghost', undefined],
      [undefined, { id: 'm-sel', name: '选中账户' }]
    )
    expect(merged).toEqual([
      { label: '窗口账户', value: 'm-win' },
      { label: '选中账户', value: 'm-sel' },
      { label: '缓存账户', value: 'm-acc' }
    ])
  })
})
