import { describe, expect, it } from 'vitest'

import {
  displayGroupName,
  groupLabelForId,
  groupSelectOptionLabel,
  groupSelectionForId,
  mergeSelectedGroupOptions,
  rememberGroupLabel,
  rememberGroupLabels,
  rememberGroupSelection,
  rememberGroupSelections,
  type GroupLabelSummary,
  type GroupSelection
} from './groupLabelCache'
import type { SelectOption } from './selectLabelCache'

function group(partial: Partial<GroupLabelSummary> & { id: string; name: string }): GroupLabelSummary {
  return { ...partial }
}

describe('rememberGroupLabels / groupLabelForId', () => {
  it('批量记录分组标签', () => {
    rememberGroupLabels([group({ id: 'g-1', name: '分组一' }), group({ id: 'g-2', name: '  分组二  ' })])
    expect(groupLabelForId('g-1')).toBe('分组一')
    expect(groupLabelForId('g-2')).toBe('分组二')
    expect(groupLabelForId('missing')).toBeUndefined()
  })

  it('rememberGroupSelection / rememberGroupSelections 记录选中项', () => {
    rememberGroupSelection({ id: 'g-3', name: '选中分组' })
    rememberGroupSelections([undefined, { id: 'g-4', name: '选中分组二' }])
    expect(groupLabelForId('g-3')).toBe('选中分组')
    expect(groupLabelForId('g-4')).toBe('选中分组二')
  })
})

describe('groupSelectionForId', () => {
  it('空白 id 返回 undefined', () => {
    expect(groupSelectionForId(undefined)).toBeUndefined()
    expect(groupSelectionForId('  ')).toBeUndefined()
  })

  it('优先从分组列表解析', () => {
    expect(groupSelectionForId('g-10', [group({ id: 'g-10', name: ' 列表分组 ' })]))
      .toEqual({ id: 'g-10', name: '列表分组' })
  })

  it('分组列表未命中时回退选项列表', () => {
    const options: SelectOption[] = [{ label: '选项分组', value: 'g-11' }]
    expect(groupSelectionForId('g-11', [], options)).toEqual({ id: 'g-11', name: '选项分组' })
  })

  it('再未命中时回退缓存，最终未命中返回 undefined', () => {
    rememberGroupLabel('g-12', '缓存分组')
    expect(groupSelectionForId('g-12')).toEqual({ id: 'g-12', name: '缓存分组' })
    expect(groupSelectionForId('g-ghost')).toBeUndefined()
  })
})

describe('displayGroupName', () => {
  it('名称存在时直接返回', () => {
    expect(displayGroupName('名称', 'id')).toBe('名称')
    expect(displayGroupName('  名称  ', 'id')).toBe('  名称  ')
  })

  it('名称缺失时回退缓存', () => {
    rememberGroupLabel('g-name', '缓存名称')
    expect(displayGroupName(undefined, 'g-name')).toBe('缓存名称')
  })

  it('名称与缓存都缺失时按 id 与兜底文案显示', () => {
    expect(displayGroupName(undefined, 'g-x')).toBe('已删除或未知')
    expect(displayGroupName(undefined, 'g-x', '自定义兜底')).toBe('自定义兜底')
    expect(displayGroupName(undefined, undefined)).toBe('-')
  })
})

describe('groupSelectOptionLabel', () => {
  it('默认仅显示名称', () => {
    expect(groupSelectOptionLabel(group({ id: 'g', name: '分组' }))).toBe('分组')
  })

  it('showProvider 追加供应商后缀', () => {
    expect(groupSelectOptionLabel(group({ id: 'g', name: '分组', providerCode: 'openai' }), { showProvider: true }))
      .toBe('分组 (OpenAI 兼容)')
    expect(groupSelectOptionLabel(group({ id: 'g', name: '分组', providerCode: ' custom ' }), { showProvider: true }))
      .toBe('分组 (custom)')
    expect(groupSelectOptionLabel(group({ id: 'g', name: '分组' }), { showProvider: true }))
      .toBe('分组 (未知供应商)')
  })

  it('authorized 分组追加授权来源后缀', () => {
    expect(groupSelectOptionLabel(group({ id: 'g', name: '分组', accessType: 'authorized', ownerSystemAccountName: '张三' })))
      .toBe('分组（来自 张三 授权）')
    expect(groupSelectOptionLabel(group({ id: 'g', name: '分组', accessType: 'authorized' })))
      .toBe('分组（来自 其他用户 授权）')
  })
})

describe('mergeSelectedGroupOptions', () => {
  it('合并选项列表与选中分组，未知项被忽略', () => {
    rememberGroupLabel('g-m-cached', '缓存分组')
    const options: SelectOption[] = [{ label: '窗口分组', value: 'g-m-win' }]
    const merged = mergeSelectedGroupOptions(
      options,
      ['g-m-win', 'g-m-cached', 'ghost', undefined],
      [undefined, { id: 'g-m-sel', name: '选中分组' } satisfies GroupSelection]
    )
    expect(merged).toEqual([
      { label: '窗口分组', value: 'g-m-win' },
      { label: '选中分组', value: 'g-m-sel' },
      { label: '缓存分组', value: 'g-m-cached' }
    ])
  })
})
