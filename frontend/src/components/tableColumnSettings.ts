import { computed, ref, toValue, watch, type ComputedRef, type MaybeRefOrGetter, type Ref } from 'vue'

export type TableColumnFixed = 'none' | 'left' | 'right'

export const tableColumnManualWidthMarker = '__tableColumnManualWidth'

export type TableColumnSetting = {
  key: string
  visible: boolean
  fixed: TableColumnFixed
  width?: number
}

export type TableColumnManagerItem = TableColumnSetting & {
  title: string
  required: boolean
}

export type TableColumnSettingsOptions = {
  requiredKeys?: readonly string[]
  minVisible?: number
}

const tableColumnSettingsStoragePrefix = 'juhe-ai:table-columns'
const tableColumnSettingsStorageVersion = 1

export function useTableColumnSettings(
  storageKey: MaybeRefOrGetter<string>,
  columns: MaybeRefOrGetter<Array<Record<string, any>>>,
  options: MaybeRefOrGetter<TableColumnSettingsOptions> = {}
): {
  managedColumns: ComputedRef<Array<Record<string, any>>>
  columnSettings: Ref<TableColumnSetting[]>
  updateColumnSettings: (nextSettings: TableColumnSetting[]) => void
  updateColumnWidth: (columnKey: string, width: number) => void
  resetColumnSettings: () => void
} {
  const columnSettings = ref<TableColumnSetting[]>([])
  const managedColumns = computed(() => applyTableColumnSettings(toValue(columns), columnSettings.value, toValue(options)))

  watch(() => toValue(storageKey), (nextStorageKey) => {
    columnSettings.value = readTableColumnSettings(nextStorageKey)
  }, { immediate: true })

  function updateColumnSettings(nextSettings: TableColumnSetting[]): void {
    const normalizedSettings = normalizeTableColumnSettings(toValue(columns), nextSettings, toValue(options))
    columnSettings.value = normalizedSettings
    writeTableColumnSettings(toValue(storageKey), normalizedSettings)
  }

  function updateColumnWidth(columnKey: string, width: number): void {
    const normalizedKey = columnKey.trim()
    if (!normalizedKey) return
    const sourceColumns = toValue(columns)
    const column = uniqueColumnsByKey(sourceColumns).find((item) => tableColumnKey(item) === normalizedKey)
    const normalizedWidth = normalizeColumnWidth(width, normalizeColumnWidth(column?.minWidth) ?? 72)
    if (normalizedWidth === undefined) return
    const currentItems = buildTableColumnManagerItems(sourceColumns, columnSettings.value, toValue(options))
    const nextSettings = currentItems.map((item) => (
      item.key === normalizedKey ? { ...item, width: normalizedWidth } : item
    ))
    updateColumnSettings(nextSettings)
  }

  function resetColumnSettings(): void {
    columnSettings.value = []
    removeTableColumnSettings(toValue(storageKey))
  }

  return {
    managedColumns,
    columnSettings,
    updateColumnSettings,
    updateColumnWidth,
    resetColumnSettings
  }
}

export function buildTableColumnManagerItems(
  columns: Array<Record<string, any>>,
  settings: TableColumnSetting[],
  options: TableColumnSettingsOptions = {}
): TableColumnManagerItem[] {
  const sourceColumns = uniqueColumnsByKey(columns)
  const columnByKey = new Map(sourceColumns.map((column) => [tableColumnKey(column), column]))
  const settingByKey = new Map(sanitizeStoredSettings(settings).map((setting) => [setting.key, setting]))
  const requiredKeys = new Set(options.requiredKeys ?? [])
  const orderedKeys = [
    ...settings.map((setting) => setting.key).filter((key) => columnByKey.has(key)),
    ...sourceColumns.map((column) => tableColumnKey(column)).filter((key) => !settingByKey.has(key))
  ]
  const dedupedKeys = [...new Set(orderedKeys)]
  const items = dedupedKeys
    .map((key): TableColumnManagerItem | undefined => {
      const column = columnByKey.get(key)
      if (!column) return undefined
      const setting = settingByKey.get(key)
      const required = requiredKeys.has(key)
      return {
        key,
        title: tableColumnTitle(column, key),
        visible: required ? true : setting?.visible ?? true,
        fixed: setting?.fixed ?? normalizeColumnFixed(column.fixed),
        width: setting?.width,
        required
      }
    })
    .filter((item): item is TableColumnManagerItem => Boolean(item))

  return normalizeTableColumnFixedOrder(ensureMinimumVisibleColumns(items, options.minVisible ?? 1))
}

export function normalizeTableColumnSettings(
  columns: Array<Record<string, any>>,
  settings: TableColumnSetting[],
  options: TableColumnSettingsOptions = {}
): TableColumnSetting[] {
  return buildTableColumnManagerItems(columns, settings, options).map(tableColumnSettingFromItem)
}

export function applyTableColumnSettings(
  columns: Array<Record<string, any>>,
  settings: TableColumnSetting[],
  options: TableColumnSettingsOptions = {}
): Array<Record<string, any>> {
  const sourceColumns = uniqueColumnsByKey(columns)
  const columnByKey = new Map(sourceColumns.map((column) => [tableColumnKey(column), column]))
  return buildTableColumnManagerItems(columns, settings, options)
    .filter((item) => item.visible)
    .map((item) => {
      const column = columnByKey.get(item.key)
      return column ? applyColumnSetting(column, item) : undefined
    })
    .filter((column): column is Record<string, any> => Boolean(column))
}

export function tableColumnSettingFromItem(item: TableColumnSetting): TableColumnSetting {
  return {
    key: item.key,
    visible: item.visible,
    fixed: item.fixed,
    width: normalizeColumnWidth(item.width)
  }
}

export function normalizeTableColumnFixedOrder<T extends TableColumnSetting>(items: T[]): T[] {
  const leftItems: T[] = []
  const normalItems: T[] = []
  const rightItems: T[] = []

  for (const item of items) {
    if (item.fixed === 'left') {
      leftItems.push(item)
    } else if (item.fixed === 'right') {
      rightItems.push(item)
    } else {
      normalItems.push(item)
    }
  }

  return [...leftItems, ...normalItems, ...rightItems]
}

export function tableColumnKey(column: Record<string, any>): string {
  const value = column.key ?? column.dataIndex
  if (Array.isArray(value)) return value.map(String).join('.')
  if (value !== undefined && value !== null && String(value).trim()) return String(value)
  return String(column.title ?? '')
}

export function readTableColumnSettings(storageKey: string): TableColumnSetting[] {
  if (typeof window === 'undefined' || !storageKey) return []
  try {
    const rawValue = window.localStorage.getItem(tableColumnSettingsStorageKey(storageKey))
    if (!rawValue) return []
    const parsed = JSON.parse(rawValue) as { version?: unknown; items?: unknown }
    if (parsed.version !== tableColumnSettingsStorageVersion || !Array.isArray(parsed.items)) return []
    return sanitizeStoredSettings(parsed.items)
  } catch {
    return []
  }
}

export function writeTableColumnSettings(storageKey: string, settings: TableColumnSetting[]): void {
  if (typeof window === 'undefined' || !storageKey) return
  try {
    window.localStorage.setItem(tableColumnSettingsStorageKey(storageKey), JSON.stringify({
      version: tableColumnSettingsStorageVersion,
      items: sanitizeStoredSettings(settings)
    }))
  } catch {
    // localStorage 可能被禁用；列设置降级为当前会话内生效。
  }
}

export function removeTableColumnSettings(storageKey: string): void {
  if (typeof window === 'undefined' || !storageKey) return
  try {
    window.localStorage.removeItem(tableColumnSettingsStorageKey(storageKey))
  } catch {
    // localStorage 可能被禁用；忽略即可。
  }
}

function tableColumnSettingsStorageKey(storageKey: string): string {
  return `${tableColumnSettingsStoragePrefix}:${storageKey}`
}

function uniqueColumnsByKey(columns: Array<Record<string, any>>): Array<Record<string, any>> {
  const columnByKey = new Map<string, Record<string, any>>()
  for (const column of columns) {
    const key = tableColumnKey(column)
    if (!key || columnByKey.has(key)) continue
    columnByKey.set(key, column)
  }
  return [...columnByKey.values()]
}

function tableColumnTitle(column: Record<string, any>, fallback: string): string {
  const title = column.title
  if (typeof title === 'string' || typeof title === 'number') return String(title)
  return fallback
}

function normalizeColumnFixed(value: unknown): TableColumnFixed {
  if (value === 'left' || value === true) return 'left'
  if (value === 'right') return 'right'
  return 'none'
}

function applyColumnSetting(column: Record<string, any>, setting: TableColumnSetting): Record<string, any> {
  const width = normalizeColumnWidth(setting.width)
  const nextColumn = width === undefined ? column : { ...column, width, [tableColumnManualWidthMarker]: true }
  if (setting.fixed === 'none') {
    const { fixed: _fixed, ...restColumn } = nextColumn
    return restColumn
  }
  return {
    ...nextColumn,
    fixed: setting.fixed
  }
}

function sanitizeStoredSettings(items: unknown[]): TableColumnSetting[] {
  const nextSettings: TableColumnSetting[] = []
  const usedKeys = new Set<string>()
  for (const item of items) {
    if (!item || typeof item !== 'object') continue
    const record = item as Record<string, unknown>
    const key = typeof record.key === 'string' ? record.key.trim() : ''
    if (!key || usedKeys.has(key)) continue
    usedKeys.add(key)
    nextSettings.push({
      key,
      visible: typeof record.visible === 'boolean' ? record.visible : true,
      fixed: normalizeColumnFixed(record.fixed),
      width: normalizeColumnWidth(record.width)
    })
  }
  return nextSettings
}

function normalizeColumnWidth(value: unknown, minWidth = 72): number | undefined {
  const width = typeof value === 'number' ? value : Number.parseFloat(String(value ?? ''))
  if (!Number.isFinite(width) || width <= 0) return undefined
  return Math.max(minWidth, Math.round(width))
}

function ensureMinimumVisibleColumns(items: TableColumnManagerItem[], minVisible: number): TableColumnManagerItem[] {
  const visibleTarget = Math.min(items.length, Math.max(0, minVisible))
  if (visibleTarget === 0) return items
  let visibleCount = items.filter((item) => item.visible).length
  if (visibleCount >= visibleTarget) return items
  return items.map((item) => {
    if (visibleCount >= visibleTarget || item.visible) return item
    visibleCount += 1
    return {
      ...item,
      visible: true
    }
  })
}
