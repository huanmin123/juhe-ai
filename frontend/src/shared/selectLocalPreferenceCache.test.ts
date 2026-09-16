import { beforeEach, describe, expect, it, vi } from 'vitest'

import { authState } from '@/composables/useAuth'
import type { CurrentUserSummary } from '@/types/domain'
import type { SelectOption } from '@/shared/selectLabelCache'
import {
  localSelectStorageKey,
  readLocalSelectOptionWindow,
  recordLocalSelectChoices,
  removeLocalSelectOptionWindowValues,
  removeLocalSelectPreferenceValues,
  sortSelectOptionsByLocalPreference,
  writeLocalSelectOptionWindow
} from './selectLocalPreferenceCache'

// 用可控的 authState mock 替换真实 useAuth，隔离用户身份对存储键的影响。
vi.mock('@/composables/useAuth', () => ({
  authState: {
    currentUser: { value: undefined as CurrentUserSummary | undefined }
  }
}))

const userKeyPrefix = 'juhe-ai:select-preferences:v1:'
const windowKeyPrefix = 'juhe-ai:select-option-windows:v1:'

const testUser: CurrentUserSummary = { id: 'user-1', username: 'u1', displayName: 'U1', role: 'user', mustChangePassword: false }

beforeEach(() => {
  window.localStorage.clear()
  authState.currentUser.value = undefined
})

describe('localSelectStorageKey', () => {
  it('未登录时使用 anonymous 作为身份段', () => {
    expect(localSelectStorageKey(['scope-a'])).toBe('anonymous|scope-a')
  })

  it('登录后拼接用户 id', () => {
    authState.currentUser.value = testUser
    expect(localSelectStorageKey(['scope-a', 'part-2'])).toBe('user-1|scope-a|part-2')
  })

  it('各类型分段归一化并编码特殊字符', () => {
    expect(localSelectStorageKey(['a b', 'x/y', 42, true, false, null, undefined, '']))
      .toBe('anonymous|a%20b|x%2Fy|42|true|false|default|default|default')
  })

  it('超长分段截断到 120 字符', () => {
    const long = 'a'.repeat(200)
    expect(localSelectStorageKey([long])).toBe(`anonymous|${encodeURIComponent(long.slice(0, 120))}`)
  })
})

describe('本地选项窗口缓存', () => {
  it('写入后可读取且按 id/value 去重、截断到 50 条', () => {
    const many = Array.from({ length: 60 }, (_, index) => ({ id: `id-${index}`, label: `标签-${index}` }))
    writeLocalSelectOptionWindow('win-key', [...many, { id: 'id-0', label: '重复' }])
    const stored = readLocalSelectOptionWindow<{ id: string; label: string }>('win-key')
    expect(stored).toHaveLength(50)
    expect(stored![0]).toEqual({ id: 'id-0', label: '标签-0' })
  })

  it('无 value/id 的项被忽略，优先 id 后 value', () => {
    writeLocalSelectOptionWindow('win-key-2', [
      { id: '', value: 'v1', label: 'a' },
      { value: 'v2', label: 'b' }
    ])
    const stored = readLocalSelectOptionWindow<{ value: string; label: string }>('win-key-2')
    expect(stored).toEqual([{ value: 'v2', label: 'b' }])
  })

  it('损坏或版本不符的数据返回 undefined', () => {
    window.localStorage.setItem(`${windowKeyPrefix}win-bad`, 'not-json')
    window.localStorage.setItem(`${windowKeyPrefix}win-bad-version`, JSON.stringify({ version: 2, options: [] }))
    window.localStorage.setItem(`${windowKeyPrefix}win-bad-options`, JSON.stringify({ version: 1, options: 'x' }))
    expect(readLocalSelectOptionWindow('win-bad')).toBeUndefined()
    expect(readLocalSelectOptionWindow('win-bad-version')).toBeUndefined()
    expect(readLocalSelectOptionWindow('win-bad-options')).toBeUndefined()
    expect(readLocalSelectOptionWindow('win-missing')).toBeUndefined()
  })

  it('removeLocalSelectOptionWindowValues 按 id/value 移除并保留其余', () => {
    writeLocalSelectOptionWindow('win-rm', [
      { id: 'keep-1', label: '保留' },
      { id: 'drop-1', label: '移除' },
      { value: 'drop-2', label: '按 value 移除' }
    ])
    removeLocalSelectOptionWindowValues('win-rm', ['drop-1', 'drop-2'])
    expect(readLocalSelectOptionWindow<{ id: string; label: string }>('win-rm')).toEqual([{ id: 'keep-1', label: '保留' }])
  })

  it('空移除列表不触发读写', () => {
    writeLocalSelectOptionWindow('win-rm-empty', [{ id: 'x', label: 'y' }])
    removeLocalSelectOptionWindowValues('win-rm-empty', ['', '   '])
    expect(readLocalSelectOptionWindow<{ id: string }>('win-rm-empty')).toEqual([{ id: 'x', label: 'y' }])
  })
})

describe('recordLocalSelectChoices / removeLocalSelectPreferenceValues', () => {
  const options: SelectOption[] = [
    { label: '选项甲', value: 'a' },
    { label: '选项乙', value: 'b' },
    { label: '选项丙', value: 'c' }
  ]

  it('记录已知选项的选择次数并去重', () => {
    recordLocalSelectChoices('pref-key', ['a', 'a', '  ', undefined], options)
    recordLocalSelectChoices('pref-key', ['a'], options)
    const store = JSON.parse(window.localStorage.getItem(`${userKeyPrefix}pref-key`)!) as {
      records: Record<string, { count: number; lastSelectedAt: number }>
    }
    expect(store.records.a).toEqual({ count: 2, lastSelectedAt: expect.any(Number) })
    expect(Object.keys(store.records)).toEqual(['a'])
  })

  it('未知选项与忽略列表中的选项不产生任何写入', () => {
    recordLocalSelectChoices('pref-key-2', ['ghost', 'b'], options, ['b'])
    expect(window.localStorage.getItem(`${userKeyPrefix}pref-key-2`)).toBeNull()
  })

  it('空选择列表不写入', () => {
    recordLocalSelectChoices('pref-key-3', [undefined, '  '], options)
    expect(window.localStorage.getItem(`${userKeyPrefix}pref-key-3`)).toBeNull()
  })

  it('removeLocalSelectPreferenceValues 删除已记录项，无命中时不写回', () => {
    recordLocalSelectChoices('pref-key-4', ['a'], options)
    removeLocalSelectPreferenceValues('pref-key-4', ['ghost'])
    const before = window.localStorage.getItem(`${userKeyPrefix}pref-key-4`)
    removeLocalSelectPreferenceValues('pref-key-4', ['a', '  '])
    const store = JSON.parse(window.localStorage.getItem(`${userKeyPrefix}pref-key-4`)!) as { records: Record<string, unknown> }
    expect(before).not.toBeNull()
    expect(store.records).toEqual({})
  })

  it('损坏的偏好数据按空记录处理', () => {
    window.localStorage.setItem(`${userKeyPrefix}pref-bad`, 'not-json')
    recordLocalSelectChoices('pref-bad', ['a'], options)
    const store = JSON.parse(window.localStorage.getItem(`${userKeyPrefix}pref-bad`)!) as {
      records: Record<string, { count: number }>
    }
    expect(store.records.a.count).toBe(1)
  })
})

describe('sortSelectOptionsByLocalPreference', () => {
  const options: SelectOption[] = [
    { label: '低频', value: 'low' },
    { label: '高频', value: 'high' },
    { label: '中频', value: 'mid' },
    { label: '未知', value: 'unknown' }
  ]

  it('按选中状态、次数与最近使用时间排序', () => {
    recordLocalSelectChoices('sort-key', ['low'], options)
    recordLocalSelectChoices('sort-key', ['high'], options)
    recordLocalSelectChoices('sort-key', ['high'], options)
    const sorted = sortSelectOptionsByLocalPreference('sort-key', options, ['mid'])
    expect(sorted.map((option) => option.value)).toEqual(['mid', 'high', 'low', 'unknown'])
  })

  it('次数相同时最近使用的靠前，无记录时保持原顺序', () => {
    recordLocalSelectChoices('sort-key-2', ['low'], options)
    const later = Date.now() + 5_000
    const store = JSON.parse(window.localStorage.getItem(`${userKeyPrefix}sort-key-2`)!) as {
      records: Record<string, { count: number; lastSelectedAt: number }>
    }
    store.records.high = { count: 1, lastSelectedAt: later }
    window.localStorage.setItem(`${userKeyPrefix}sort-key-2`, JSON.stringify(store))
    const sorted = sortSelectOptionsByLocalPreference('sort-key-2', options, [])
    expect(sorted.map((option) => option.value)).toEqual(['high', 'low', 'mid', 'unknown'])
  })

  it('忽略列表中的选项不参与偏好排序', () => {
    recordLocalSelectChoices('sort-key-3', ['high', 'mid'], options)
    const sorted = sortSelectOptionsByLocalPreference('sort-key-3', options, [], ['high', 'mid'])
    expect(sorted.map((option) => option.value)).toEqual(['low', 'high', 'mid', 'unknown'])
  })
})
