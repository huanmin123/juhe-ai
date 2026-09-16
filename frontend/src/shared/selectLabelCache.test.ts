import { beforeEach, describe, expect, it } from 'vitest'

import { mergeSelectedSelectOptions, rememberSelectOption, rememberSelectOptions, selectLabelForValue, selectedOptionForValue, type SelectOption } from './selectLabelCache'

// 模块内缓存按 cacheKey 隔离：每个用例使用独立 key，避免相互影响。
let cacheKey: string

beforeEach(() => {
  cacheKey = `test-cache-${Math.random().toString(36).slice(2)}`
})

describe('rememberSelectOption / rememberSelectOptions', () => {
  it('记录后可按值查询', () => {
    rememberSelectOption(cacheKey, ' id-1 ', ' 名称一 ')
    expect(selectLabelForValue(cacheKey, 'id-1')).toBe('名称一')
  })

  it('空白值或空白标签不会写入缓存', () => {
    rememberSelectOption(cacheKey, '', 'x')
    rememberSelectOption(cacheKey, '  ', 'x')
    rememberSelectOption(cacheKey, 'id-2', '')
    rememberSelectOption(cacheKey, 'id-3', '   ')
    rememberSelectOption(cacheKey, undefined, undefined)
    expect(selectLabelForValue(cacheKey, 'id-2')).toBeUndefined()
    expect(selectLabelForValue(cacheKey, 'id-3')).toBeUndefined()
  })

  it('rememberSelectOptions 批量记录', () => {
    rememberSelectOptions(cacheKey, [
      { label: '甲', value: 'a' },
      { label: '乙', value: 'b' }
    ])
    expect(selectLabelForValue(cacheKey, 'a')).toBe('甲')
    expect(selectLabelForValue(cacheKey, 'b')).toBe('乙')
  })

  it('不同 cacheKey 之间相互隔离', () => {
    rememberSelectOption(cacheKey, 'x', '标签')
    expect(selectLabelForValue(`${cacheKey}-other`, 'x')).toBeUndefined()
  })
})

describe('selectLabelForValue', () => {
  it('未记录的值返回 undefined', () => {
    expect(selectLabelForValue(cacheKey, 'missing')).toBeUndefined()
  })

  it('空值返回 undefined', () => {
    rememberSelectOption(cacheKey, 'x', '标签')
    expect(selectLabelForValue(cacheKey, undefined)).toBeUndefined()
    expect(selectLabelForValue(cacheKey, '')).toBeUndefined()
  })
})

describe('selectedOptionForValue', () => {
  it('优先返回选项列表中的匹配项并保留 disabled', () => {
    const options: SelectOption[] = [{ label: '列表项', value: 'v1', disabled: true }]
    expect(selectedOptionForValue(cacheKey, ' v1 ', options)).toEqual({ label: '列表项', value: 'v1', disabled: true })
  })

  it('选项列表未命中时回退缓存', () => {
    rememberSelectOption(cacheKey, 'v2', '缓存项')
    expect(selectedOptionForValue(cacheKey, 'v2', [])).toEqual({ label: '缓存项', value: 'v2' })
  })

  it('都未命中返回 undefined', () => {
    expect(selectedOptionForValue(cacheKey, 'v3', [])).toBeUndefined()
  })

  it('空值返回 undefined', () => {
    expect(selectedOptionForValue(cacheKey, '  ', [{ label: 'x', value: 'y' }])).toBeUndefined()
    expect(selectedOptionForValue(cacheKey, undefined)).toBeUndefined()
  })
})

describe('mergeSelectedSelectOptions', () => {
  it('合并选项列表、选中项与缓存补全且不重复', () => {
    rememberSelectOption(cacheKey, 'cached', '缓存标签')
    const options: SelectOption[] = [{ label: '窗口项', value: 'w1' }]
    const merged = mergeSelectedSelectOptions(
      cacheKey,
      options,
      ['w1', 'cached', 'unknown', '  '],
      [undefined, { label: '选中项', value: 's1' }, { label: '  ', value: 's2' }]
    )
    expect(merged).toEqual([
      { label: '窗口项', value: 'w1' },
      { label: '选中项', value: 's1' },
      { label: '缓存标签', value: 'cached' }
    ])
  })

  it('合并过程会记住选项标签供后续查询', () => {
    mergeSelectedSelectOptions(cacheKey, [{ label: '新项', value: 'n1' }], [], [])
    expect(selectLabelForValue(cacheKey, 'n1')).toBe('新项')
  })

  it('未知 id 无法补全时被忽略', () => {
    const merged = mergeSelectedSelectOptions(cacheKey, [], ['ghost'])
    expect(merged).toEqual([])
  })
})
