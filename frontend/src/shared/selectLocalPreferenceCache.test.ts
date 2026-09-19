import { beforeEach, describe, expect, it, vi } from 'vitest'

import { authState } from '@/composables/useAuth'
import type { CurrentUserSummary } from '@/types/domain'
import type { SelectOption } from '@/shared/selectLabelCache'
import {
  localSelectStorageKey,
  minSelectPreferenceRankCount,
  readLocalSelectOptionWindow,
  recordLocalSelectChoices,
  refreshLocalSelectPreferenceSnapshot,
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

// 生产模块在存储键层面统一按登录用户分区，测试里的裸 key 需经同一包装得到真实存储键。
const preferenceStorageKeyOf = (key: string) => `${userKeyPrefix}${localSelectStorageKey([key])}`
const optionWindowStorageKeyOf = (key: string) => `${windowKeyPrefix}${localSelectStorageKey([key])}`

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
    window.localStorage.setItem(optionWindowStorageKeyOf('win-bad'), 'not-json')
    window.localStorage.setItem(optionWindowStorageKeyOf('win-bad-version'), JSON.stringify({ version: 2, options: [] }))
    window.localStorage.setItem(optionWindowStorageKeyOf('win-bad-options'), JSON.stringify({ version: 1, options: 'x' }))
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
    const store = JSON.parse(window.localStorage.getItem(preferenceStorageKeyOf('pref-key'))!) as {
      records: Record<string, { count: number; lastSelectedAt: number }>
    }
    expect(store.records.a).toEqual({ count: 2, lastSelectedAt: expect.any(Number) })
    expect(Object.keys(store.records)).toEqual(['a'])
  })

  it('未知选项与忽略列表中的选项不产生任何写入', () => {
    recordLocalSelectChoices('pref-key-2', ['ghost', 'b'], options, ['b'])
    expect(window.localStorage.getItem(preferenceStorageKeyOf('pref-key-2'))).toBeNull()
  })

  it('空选择列表不写入', () => {
    recordLocalSelectChoices('pref-key-3', [undefined, '  '], options)
    expect(window.localStorage.getItem(preferenceStorageKeyOf('pref-key-3'))).toBeNull()
  })

  it('removeLocalSelectPreferenceValues 删除已记录项，无命中时不写回', () => {
    recordLocalSelectChoices('pref-key-4', ['a'], options)
    removeLocalSelectPreferenceValues('pref-key-4', ['ghost'])
    const before = window.localStorage.getItem(preferenceStorageKeyOf('pref-key-4'))
    removeLocalSelectPreferenceValues('pref-key-4', ['a', '  '])
    const store = JSON.parse(window.localStorage.getItem(preferenceStorageKeyOf('pref-key-4'))!) as { records: Record<string, unknown> }
    expect(before).not.toBeNull()
    expect(store.records).toEqual({})
  })

  it('损坏的偏好数据按空记录处理', () => {
    window.localStorage.setItem(preferenceStorageKeyOf('pref-bad'), 'not-json')
    recordLocalSelectChoices('pref-bad', ['a'], options)
    const store = JSON.parse(window.localStorage.getItem(preferenceStorageKeyOf('pref-bad'))!) as {
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

  function recordTimes(key: string, value: string, times: number, allOptions: SelectOption[]): void {
    for (let index = 0; index < times; index += 1) {
      recordLocalSelectChoices(key, [value], allOptions)
    }
  }

  it('排序阈值常量为 3', () => {
    expect(minSelectPreferenceRankCount).toBe(3)
  })

  it('累计选择 2 次未达阈值时保持数据源默认顺序', () => {
    recordTimes('sort-key', 'mid', 2, options)
    const sorted = sortSelectOptionsByLocalPreference('sort-key', options, [])
    expect(sorted.map((option) => option.value)).toEqual(['low', 'high', 'mid', 'unknown'])
  })

  it('累计选择 3 次达到阈值后参与常用上浮', () => {
    recordTimes('sort-key-2', 'mid', 3, options)
    const sorted = sortSelectOptionsByLocalPreference('sort-key-2', options, [])
    expect(sorted.map((option) => option.value)).toEqual(['mid', 'low', 'high', 'unknown'])
  })

  it('上浮组内按次数降序、最近使用降序排列，其余保持原顺序', () => {
    const base = Date.now()
    window.localStorage.setItem(preferenceStorageKeyOf('sort-key-3'), JSON.stringify({
      version: 1,
      updatedAt: base,
      records: {
        low: { count: 3, lastSelectedAt: base },
        high: { count: 3, lastSelectedAt: base + 5_000 },
        mid: { count: 4, lastSelectedAt: base }
      }
    }))
    const sorted = sortSelectOptionsByLocalPreference('sort-key-3', options, [])
    expect(sorted.map((option) => option.value)).toEqual(['mid', 'high', 'low', 'unknown'])
  })

  it('当前选中值不再即时置顶', () => {
    const sorted = sortSelectOptionsByLocalPreference('sort-key-4', options, ['unknown'])
    expect(sorted.map((option) => option.value)).toEqual(['low', 'high', 'mid', 'unknown'])
  })

  it('忽略列表中的选项即使达到阈值也不参与偏好排序', () => {
    recordTimes('sort-key-5', 'high', 3, options)
    const sorted = sortSelectOptionsByLocalPreference('sort-key-5', options, [], ['high'])
    expect(sorted.map((option) => option.value)).toEqual(['low', 'high', 'mid', 'unknown'])
  })

  it('达到阈值的选择在快照刷新前不改变当前排序，刷新后生效', () => {
    recordTimes('sort-key-6', 'mid', 2, options)
    const before = sortSelectOptionsByLocalPreference('sort-key-6', options, [])
    expect(before.map((option) => option.value)).toEqual(['low', 'high', 'mid', 'unknown'])

    recordTimes('sort-key-6', 'mid', 1, options)
    const stable = sortSelectOptionsByLocalPreference('sort-key-6', options, [])
    expect(stable.map((option) => option.value)).toEqual(['low', 'high', 'mid', 'unknown'])

    refreshLocalSelectPreferenceSnapshot('sort-key-6')
    const refreshed = sortSelectOptionsByLocalPreference('sort-key-6', options, [])
    expect(refreshed.map((option) => option.value)).toEqual(['mid', 'low', 'high', 'unknown'])
  })

  it('快照建立后直接修改本地存储不影响排序，刷新快照后生效', () => {
    recordTimes('sort-key-7', 'mid', 3, options)
    const before = sortSelectOptionsByLocalPreference('sort-key-7', options, [])
    expect(before.map((option) => option.value)).toEqual(['mid', 'low', 'high', 'unknown'])

    const store = JSON.parse(window.localStorage.getItem(preferenceStorageKeyOf('sort-key-7'))!) as {
      records: Record<string, { count: number; lastSelectedAt: number }>
    }
    store.records.high = { count: 5, lastSelectedAt: Date.now() + 9_000 }
    window.localStorage.setItem(preferenceStorageKeyOf('sort-key-7'), JSON.stringify(store))
    const stable = sortSelectOptionsByLocalPreference('sort-key-7', options, [])
    expect(stable.map((option) => option.value)).toEqual(['mid', 'low', 'high', 'unknown'])

    refreshLocalSelectPreferenceSnapshot('sort-key-7')
    const refreshed = sortSelectOptionsByLocalPreference('sort-key-7', options, [])
    expect(refreshed.map((option) => option.value)).toEqual(['high', 'mid', 'low', 'unknown'])
  })

  it('removeLocalSelectPreferenceValues 后快照失效，下次排序反映移除', () => {
    recordTimes('sort-key-8', 'mid', 3, options)
    const before = sortSelectOptionsByLocalPreference('sort-key-8', options, [])
    expect(before.map((option) => option.value)).toEqual(['mid', 'low', 'high', 'unknown'])

    removeLocalSelectPreferenceValues('sort-key-8', ['mid'])
    const after = sortSelectOptionsByLocalPreference('sort-key-8', options, [])
    expect(after.map((option) => option.value)).toEqual(['low', 'high', 'mid', 'unknown'])
  })
})

describe('按登录用户隔离', () => {
  const options: SelectOption[] = [
    { label: '低频', value: 'low' },
    { label: '高频', value: 'high' }
  ]

  const loginUser = (id: string) => {
    authState.currentUser.value = { ...testUser, id }
  }

  it('不同登录用户的偏好记录写入各自分区，互不影响', () => {
    loginUser('user-a')
    recordLocalSelectChoices('iso-key', ['high'], options)
    expect(window.localStorage.getItem(preferenceStorageKeyOf('iso-key'))).not.toBeNull()

    loginUser('user-b')
    expect(window.localStorage.getItem(preferenceStorageKeyOf('iso-key'))).toBeNull()
    const sortedForB = sortSelectOptionsByLocalPreference('iso-key', options, [])
    expect(sortedForB.map((option) => option.value)).toEqual(['low', 'high'])
  })

  it('快照按用户分区：用户 A 的常用排序不串到用户 B，切回后仍生效', () => {
    loginUser('user-a')
    for (let index = 0; index < minSelectPreferenceRankCount; index += 1) {
      recordLocalSelectChoices('iso-key-2', ['high'], options)
    }
    expect(sortSelectOptionsByLocalPreference('iso-key-2', options, []).map((option) => option.value))
      .toEqual(['high', 'low'])

    loginUser('user-b')
    expect(sortSelectOptionsByLocalPreference('iso-key-2', options, []).map((option) => option.value))
      .toEqual(['low', 'high'])

    loginUser('user-a')
    expect(sortSelectOptionsByLocalPreference('iso-key-2', options, []).map((option) => option.value))
      .toEqual(['high', 'low'])
  })
})
