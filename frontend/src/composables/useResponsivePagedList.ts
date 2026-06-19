import type { UnwrapNestedRefs } from 'vue'
import { computed, reactive, ref, shallowRef } from 'vue'

export type ResponsivePagedListResult<T> = {
  items: T[]
  page: number
  pageSize: number
  total: number
  hasMore?: boolean
}

export type ResponsivePagedListLoadOptions = {
  append?: boolean
  quiet?: boolean
}

type PaginationState = UnwrapNestedRefs<{ current: number; pageSize: number; total: number }>
type TableShowTotalRange = [number, number]
type TableShowTotalContext = {
  current: number
  currentPageCount: number
  hasMore: boolean
  loadedCount: number
  pageSize: number
}

type UseResponsivePagedListOptions<T, ExtraOptions extends Record<string, unknown>> = {
  pageSize: number
  initialPagination?: { current?: number; pageSize?: number; total?: number }
  showTotal: (total: number, range?: TableShowTotalRange, context?: TableShowTotalContext) => string
  fetchPage: (options: ResponsivePagedListLoadOptions & ExtraOptions, pagination: PaginationState) => Promise<ResponsivePagedListResult<T>>
  mergeItems?: (currentItems: T[], nextItems: T[], options: ResponsivePagedListLoadOptions & ExtraOptions) => T[]
  onLoaded?: (result: ResponsivePagedListResult<T>, options: ResponsivePagedListLoadOptions & ExtraOptions) => void
  onError?: (error: unknown) => void
  requestSignature?: (options: ResponsivePagedListLoadOptions & ExtraOptions, pagination: PaginationState) => unknown
}

export function useResponsivePagedList<T, ExtraOptions extends Record<string, unknown> = Record<never, never>>(
  options: UseResponsivePagedListOptions<T, ExtraOptions>
) {
  const loading = ref(false)
  const mobileLoadingMore = ref(false)
  const hasMore = ref(false)
  const currentPageCount = ref(0)
  const items = shallowRef<T[]>([])
  let loadRequestId = 0
  let loadingRequestId = 0
  let mobileLoadingRequestId = 0
  let inflightLoadKey = ''
  let inflightLoadPromise: Promise<boolean> | undefined
  const pagination = reactive({
    current: options.initialPagination?.current ?? 1,
    pageSize: options.initialPagination?.pageSize ?? options.pageSize,
    total: options.initialPagination?.total ?? 0
  })

  const paginationTotal = computed(() => {
    const minimumTotal = (pagination.current - 1) * pagination.pageSize + currentPageCount.value + (hasMore.value ? 1 : 0)
    return Math.max(pagination.total, minimumTotal)
  })
  const mobileHasMore = computed(() => hasMore.value)
  const tablePagination = computed(() => ({
    current: pagination.current,
    pageSize: pagination.pageSize,
    total: paginationTotal.value,
    hideOnSinglePage: true,
    showSizeChanger: false,
    showTotal: (total: number, range?: TableShowTotalRange) => options.showTotal(total, range, {
      current: pagination.current,
      currentPageCount: currentPageCount.value,
      hasMore: hasMore.value,
      loadedCount: items.value.length,
      pageSize: pagination.pageSize
    })
  }))

  function resetPagination(): void {
    pagination.current = 1
    pagination.pageSize = options.pageSize
  }

  async function loadData(loadOptions = {} as ResponsivePagedListLoadOptions & ExtraOptions): Promise<boolean> {
    const loadKey = requestKey(loadOptions)
    if (loadKey && inflightLoadPromise && inflightLoadKey === loadKey) {
      return inflightLoadPromise
    }
    const requestId = loadRequestId + 1
    loadRequestId = requestId
    if (!loadOptions.quiet) {
      loading.value = true
      loadingRequestId = requestId
    }
    const promise = executeLoadData(requestId, loadOptions)
    if (loadKey) {
      inflightLoadKey = loadKey
      inflightLoadPromise = promise
    }
    try {
      return await promise
    } finally {
      if (loadKey && inflightLoadPromise === promise) {
        inflightLoadPromise = undefined
        inflightLoadKey = ''
      }
    }
  }

  async function executeLoadData(requestId: number, loadOptions: ResponsivePagedListLoadOptions & ExtraOptions): Promise<boolean> {
    try {
      const result = await options.fetchPage(loadOptions, pagination)
      if (requestId !== loadRequestId) return false
      if (!loadOptions.append && result.page > 1 && result.items.length === 0 && result.hasMore === false) {
        pagination.current = 1
        const fallbackResult = await options.fetchPage(loadOptions, pagination)
        if (requestId !== loadRequestId) return false
        applyPageResult(fallbackResult, loadOptions)
      } else {
        applyPageResult(result, loadOptions)
      }
      return true
    } catch (error) {
      if (requestId !== loadRequestId) return false
      options.onError?.(error)
      return false
    } finally {
      if (!loadOptions.quiet && loadingRequestId === requestId) {
        loading.value = false
      }
    }
  }

  function applyPageResult(result: ResponsivePagedListResult<T>, loadOptions: ResponsivePagedListLoadOptions & ExtraOptions): void {
    const nextItems = loadOptions.append
      ? options.mergeItems?.(items.value, result.items, loadOptions) ?? [...items.value, ...result.items]
      : result.items
    const loadedCount = nextItems.length
    pagination.current = result.page
    pagination.pageSize = result.pageSize
    pagination.total = result.total
    hasMore.value = typeof result.hasMore === 'boolean' ? result.hasMore : loadedCount < result.total
    currentPageCount.value = result.items.length
    items.value = nextItems
    options.onLoaded?.(result, loadOptions)
  }

  function removeItems(predicate: (item: T) => boolean): number {
    const previousItems = items.value
    const nextItems = previousItems.filter((item) => !predicate(item))
    const removedCount = previousItems.length - nextItems.length
    if (removedCount <= 0) {
      return 0
    }
    items.value = nextItems
    pagination.total = Math.max(0, pagination.total - removedCount)
    currentPageCount.value = Math.max(0, currentPageCount.value - removedCount)
    return removedCount
  }

  function updateItems(predicate: (item: T) => boolean, updater: (item: T) => T): number {
    let updatedCount = 0
    const nextItems = items.value.map((item) => {
      if (!predicate(item)) {
        return item
      }
      updatedCount += 1
      return updater(item)
    })
    if (updatedCount > 0) {
      items.value = nextItems
    }
    return updatedCount
  }

  function handleTableChange(paginationInfo: unknown): void {
    if (!paginationInfo || typeof paginationInfo !== 'object') return
    const next = paginationInfo as { current?: unknown; pageSize?: unknown }
    const nextCurrent = Number(next.current)
    const nextPageSize = Number(next.pageSize)
    pagination.current = Number.isFinite(nextCurrent) && nextCurrent > 0 ? nextCurrent : 1
    pagination.pageSize = Number.isFinite(nextPageSize) && nextPageSize > 0 ? nextPageSize : options.pageSize
    void loadData()
  }

  async function loadMoreMobile(loadOptions = {} as ExtraOptions): Promise<void> {
    if (!mobileHasMore.value || mobileLoadingMore.value) return
    const previousPage = pagination.current
    const requestId = loadRequestId + 1
    mobileLoadingRequestId = requestId
    mobileLoadingMore.value = true
    pagination.current += 1
    try {
      const loaded = await loadData({ ...loadOptions, append: true, quiet: true })
      if (!loaded && mobileLoadingRequestId === requestId && pagination.current === previousPage + 1) {
        pagination.current = previousPage
      }
    } finally {
      if (mobileLoadingRequestId === requestId) {
        mobileLoadingMore.value = false
      }
    }
  }

  async function refreshMobile(loadOptions = {} as ExtraOptions): Promise<void> {
    resetPagination()
    await loadData(loadOptions as ResponsivePagedListLoadOptions & ExtraOptions)
  }

  function requestKey(loadOptions: ResponsivePagedListLoadOptions & ExtraOptions): string | undefined {
    if (!options.requestSignature) return undefined
    return stableKey({
      options: loadOptions,
      page: pagination.current,
      pageSize: pagination.pageSize,
      signature: options.requestSignature(loadOptions, pagination)
    })
  }

  return {
    hasMore,
    items,
    loading,
    mobileHasMore,
    mobileLoadingMore,
    pagination,
    tablePagination,
    handleTableChange,
    loadData,
    loadMoreMobile,
    removeItems,
    refreshMobile,
    resetPagination,
    updateItems
  }
}

function stableKey(value: unknown): string {
  if (value === null || typeof value !== 'object') return JSON.stringify(value) ?? String(value)
  if (Array.isArray(value)) return `[${value.map((item) => stableKey(item)).join(',')}]`
  const record = value as Record<string, unknown>
  return `{${Object.keys(record).sort().map((key) => `${JSON.stringify(key)}:${stableKey(record[key])}`).join(',')}}`
}
