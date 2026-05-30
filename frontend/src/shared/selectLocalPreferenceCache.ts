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

export type LocalSelectStorageKeyPart = string | number | boolean | undefined | null

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
  for (const value of selectedValues) {
    if (!knownValues.has(value)) continue
    const current = store.records[value]
    store.records[value] = {
      count: Math.min(9999, (current?.count ?? 0) + 1),
      lastSelectedAt: now
    }
  }
  trimPreferenceStore(store)
  writePreferenceStore(key, store)
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
  }
}

export function sortSelectOptionsByLocalPreference(
  key: string,
  options: SelectOption[],
  selectedValues: Array<string | undefined>,
  ignoredValues: Array<string | undefined> = []
): SelectOption[] {
  const selectedValueSet = new Set(selectedValues.map(normalizeValue).filter(Boolean))
  const ignoredValueSet = new Set(ignoredValues.map(normalizeValue).filter(Boolean))
  const records = readPreferenceStore(key).records
  return [...options]
    .map((option, index) => ({ option, index, value: normalizeValue(option.value), record: records[normalizeValue(option.value)] }))
    .sort((left, right) => {
      const leftSelected = selectedValueSet.has(left.value)
      const rightSelected = selectedValueSet.has(right.value)
      if (leftSelected !== rightSelected) return leftSelected ? -1 : 1

      const leftRecord = ignoredValueSet.has(left.value) ? undefined : left.record
      const rightRecord = ignoredValueSet.has(right.value) ? undefined : right.record
      const leftCount = leftRecord?.count ?? 0
      const rightCount = rightRecord?.count ?? 0
      if (leftCount !== rightCount) return rightCount - leftCount

      const leftSelectedAt = leftRecord?.lastSelectedAt ?? 0
      const rightSelectedAt = rightRecord?.lastSelectedAt ?? 0
      if (leftSelectedAt !== rightSelectedAt) return rightSelectedAt - leftSelectedAt

      return left.index - right.index
    })
    .map((entry) => entry.option)
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
  return `${preferenceStoragePrefix}${key}`
}

function optionWindowStorageKey(key: string): string {
  return `${optionWindowStoragePrefix}${key}`
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
