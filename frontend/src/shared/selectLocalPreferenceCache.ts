import { authState } from '@/composables/useAuth'
import type { SelectOption } from '@/shared/selectLabelCache'

interface SelectPreferenceRecord {
  count: number
  lastSelectedAt: number
}

interface SelectPreferenceStore {
  version: 1
  updatedAt: number
  records: Record<string, SelectPreferenceRecord>
}

interface SelectOptionWindowStore<T> {
  version: 1
  cachedAt: number
  options: T[]
}

const preferenceStoragePrefix = 'juhe-ai:select-preferences:v1:'
const optionWindowStoragePrefix = 'juhe-ai:select-option-windows:v1:'
const maxPreferenceRecords = 100
const maxOptionWindowSize = 50
const maxStorageKeyPartLength = 120

/**
 * 排序钝化阈值：同一选项累计选择次数达到该值才参与常用上浮排序；
 * 低于阈值的选择不改变数据源默认顺序，避免偶发选择立即重排下拉。
 */
export const minSelectPreferenceRankCount = 3

// per-key 偏好记录快照：排序只读快照，不每次实时读 localStorage；
// 快照在 refreshLocalSelectPreferenceSnapshot 或 removeLocalSelectPreferenceValues 时失效。
// 键与存储键同源（含登录用户身份），用户切换后天然分区，不会读到其他用户的快照。
const preferenceSnapshots = new Map<string, Record<string, SelectPreferenceRecord>>()

export type LocalSelectStorageKeyPart = string | number | boolean | undefined | null

// 选择偏好按登录用户隔离：同一浏览器内不同登录用户的偏好互不影响；
// localStorage 决定了偏好不跨浏览器/设备同步。未登录时归入 anonymous 分区。
export function localSelectStorageKey(parts: LocalSelectStorageKeyPart[]): string {
  return [
    authState.currentUser.value?.id ?? 'anonymous',
    ...parts
  ].map(normalizeKeyPart).join('|')
}

export function readLocalSelectOptionWindow<T>(key: string): T[] | undefined {
  const store = readJson<SelectOptionWindowStore<T>>(optionWindowStorageKey(key))
  if (!store || store.version !== 1 || !Array.isArray(store.options)) return undefined
  return store.options
}

export function writeLocalSelectOptionWindow<T extends { id?: string; value?: string }>(key: string, options: T[]): void {
  const uniqueOptions = uniqueItemsByValue(options).slice(0, maxOptionWindowSize)
  writeJson(optionWindowStorageKey(key), {
    version: 1,
    cachedAt: Date.now(),
    options: uniqueOptions
  } satisfies SelectOptionWindowStore<T>)
}

export function removeLocalSelectOptionWindowValues(key: string, values: string[]): void {
  const normalizedValues = new Set(values.map(normalizeValue).filter(Boolean))
  if (!normalizedValues.size) return
  const options = readLocalSelectOptionWindow<{ id?: string; value?: string }>(key)
  if (!options?.length) return
  writeLocalSelectOptionWindow(
    key,
    options.filter((option) => !normalizedValues.has(normalizeValue(option.id ?? option.value)))
  )
}

export function recordLocalSelectChoices(
  key: string,
  values: Array<string | undefined>,
  options: SelectOption[],
  ignoredValues: Array<string | undefined> = []
): void {
  const ignoredValueSet = new Set(ignoredValues.map(normalizeValue).filter(Boolean))
  const selectedValues = [...new Set(values.map(normalizeValue).filter(Boolean))]
    .filter((value) => !ignoredValueSet.has(value))
  if (!selectedValues.length) return

  const store = readPreferenceStore(key)
  const knownValues = new Set(options.map((option) => normalizeValue(option.value)).filter(Boolean))
  const now = Date.now()
  let changed = false
  for (const value of selectedValues) {
    if (!knownValues.has(value)) continue
    changed = true
    const current = store.records[value]
    store.records[value] = {
      count: Math.min(9999, (current?.count ?? 0) + 1),
      lastSelectedAt: now
    }
  }
  if (!changed) return
  trimPreferenceStore(store)
  writePreferenceStore(key, store)
  // 钝化：这里故意不失效快照。已渲染的下拉继续按既有快照排序，选中动作不在当前视图立即重排；
  // 新排序在组件挂载或下拉打开触发 refreshLocalSelectPreferenceSnapshot 后才生效。
}

export function removeLocalSelectPreferenceValues(key: string, values: string[]): void {
  const normalizedValues = new Set(values.map(normalizeValue).filter(Boolean))
  if (!normalizedValues.size) return
  const store = readPreferenceStore(key)
  let changed = false
  for (const value of normalizedValues) {
    if (store.records[value]) {
      delete store.records[value]
      changed = true
    }
  }
  if (changed) {
    writePreferenceStore(key, store)
    // 快照立即失效：被移除的值（如远程确认已不存在或无权访问的实体）下次排序按无记录处理。
    refreshLocalSelectPreferenceSnapshot(key)
  }
}

export function refreshLocalSelectPreferenceSnapshot(key: string): void {
  preferenceSnapshots.delete(localSelectStorageKey([key]))
}

export function sortSelectOptionsByLocalPreference(
  key: string,
  options: SelectOption[],
  // 历史签名保留：置顶分支已随排序钝化移除，选中值不再影响排序结果。
  _selectedValues: Array<string | undefined> = [],
  ignoredValues: Array<string | undefined> = []
): SelectOption[] {
  const ignoredValueSet = new Set(ignoredValues.map(normalizeValue).filter(Boolean))
  const records = preferenceSnapshotFor(key)
  return [...options]
    .map((option, index) => ({ option, index, value: normalizeValue(option.value), record: records[normalizeValue(option.value)] }))
    .sort((left, right) => {
      const leftRecord = ignoredValueSet.has(left.value) ? undefined : left.record
      const rightRecord = ignoredValueSet.has(right.value) ? undefined : right.record
      const leftRank = leftRecord && leftRecord.count >= minSelectPreferenceRankCount ? leftRecord : undefined
      const rightRank = rightRecord && rightRecord.count >= minSelectPreferenceRankCount ? rightRecord : undefined
      const leftCount = leftRank?.count ?? 0
      const rightCount = rightRank?.count ?? 0
      if (leftCount !== rightCount) return rightCount - leftCount
      if (!leftRank || !rightRank) return left.index - right.index
      if (leftRank.lastSelectedAt !== rightRank.lastSelectedAt) return rightRank.lastSelectedAt - leftRank.lastSelectedAt
      return left.index - right.index
    })
    .map((entry) => entry.option)
}

function preferenceSnapshotFor(key: string): Record<string, SelectPreferenceRecord> {
  const snapshotKey = localSelectStorageKey([key])
  let snapshot = preferenceSnapshots.get(snapshotKey)
  if (!snapshot) {
    snapshot = readPreferenceStore(key).records
    preferenceSnapshots.set(snapshotKey, snapshot)
  }
  return snapshot
}

function readPreferenceStore(key: string): SelectPreferenceStore {
  const store = readJson<SelectPreferenceStore>(preferenceStorageKey(key))
  if (!store || store.version !== 1 || typeof store.records !== 'object' || store.records === null) {
    return emptyPreferenceStore()
  }
  return store
}

function writePreferenceStore(key: string, store: SelectPreferenceStore): void {
  store.updatedAt = Date.now()
  writeJson(preferenceStorageKey(key), store)
}

function emptyPreferenceStore(): SelectPreferenceStore {
  return { version: 1, updatedAt: Date.now(), records: {} }
}

function trimPreferenceStore(store: SelectPreferenceStore): void {
  const entries = Object.entries(store.records)
  if (entries.length <= maxPreferenceRecords) return
  entries
    .sort((left, right) => {
      const leftRecord = left[1]
      const rightRecord = right[1]
      return rightRecord.count - leftRecord.count
        || rightRecord.lastSelectedAt - leftRecord.lastSelectedAt
        || left[0].localeCompare(right[0])
    })
    .slice(maxPreferenceRecords)
    .forEach(([value]) => {
      delete store.records[value]
    })
}

function uniqueItemsByValue<T extends { id?: string; value?: string }>(items: T[]): T[] {
  const seen = new Set<string>()
  const result: T[] = []
  for (const item of items) {
    const value = normalizeValue(item.id ?? item.value)
    if (!value || seen.has(value)) continue
    seen.add(value)
    result.push(item)
  }
  return result
}

function normalizeValue(value: unknown): string {
  return typeof value === 'string' ? value.trim() : ''
}

function normalizeKeyPart(value: LocalSelectStorageKeyPart): string {
  const text = String(value ?? 'default').trim() || 'default'
  return encodeURIComponent(text.slice(0, maxStorageKeyPartLength))
}

function preferenceStorageKey(key: string): string {
  return `${preferenceStoragePrefix}${localSelectStorageKey([key])}`
}

function optionWindowStorageKey(key: string): string {
  return `${optionWindowStoragePrefix}${localSelectStorageKey([key])}`
}

function readJson<T>(key: string): T | undefined {
  if (typeof window === 'undefined') return undefined
  try {
    const text = window.localStorage.getItem(key)
    if (!text) return undefined
    return JSON.parse(text) as T
  } catch {
    return undefined
  }
}

function writeJson(key: string, value: unknown): void {
  if (typeof window === 'undefined') return
  try {
    window.localStorage.setItem(key, JSON.stringify(value))
  } catch {
  }
}
