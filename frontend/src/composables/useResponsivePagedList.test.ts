import { describe, expect, it, vi } from 'vitest'

import { useResponsivePagedList, type ResponsivePagedListLoadOptions, type ResponsivePagedListResult } from './useResponsivePagedList'

interface Item { id: string }

function page(pageNumber: number, ids: string[], total: number, overrides: Partial<ResponsivePagedListResult<Item>> = {}): ResponsivePagedListResult<Item> {
  return { items: ids.map((id) => ({ id })), page: pageNumber, pageSize: 20, total, ...overrides }
}

function createList(overrides: Partial<Parameters<typeof useResponsivePagedList<Item>>[0]> = {}) {
  const fetchPage = vi.fn()
  const list = useResponsivePagedList<Item>({
    pageSize: 20,
    showTotal: (total, range) => `共 ${total}（${range?.[0] ?? 0}-${range?.[1] ?? 0}）`,
    fetchPage: fetchPage as unknown as Parameters<typeof useResponsivePagedList<Item>>[0]['fetchPage'],
    ...overrides
  })
  return { list, fetchPage }
}

describe('useResponsivePagedList 初始状态', () => {
  it('分页默认值与 initialPagination 覆盖', () => {
    expect(createList().list.pagination).toEqual({ current: 1, pageSize: 20, total: 0 })
    const { list } = createList({ initialPagination: { current: 3, pageSize: 50, total: 99 } })
    expect(list.pagination).toEqual({ current: 3, pageSize: 50, total: 99 })
  })

  it('resetPagination 恢复默认分页', () => {
    const { list } = createList()
    list.pagination.current = 5
    list.pagination.pageSize = 99
    list.resetPagination()
    expect(list.pagination).toEqual({ current: 1, pageSize: 20, total: 0 })
  })
})

describe('loadData', () => {
  it('加载首页并应用结果', async () => {
    const { list, fetchPage } = createList()
    fetchPage.mockResolvedValue(page(1, ['a', 'b'], 30, { hasMore: false }))
    await expect(list.loadData()).resolves.toBe(true)
    expect(fetchPage).toHaveBeenCalledTimes(1)
    expect(list.items.value.map((item) => item.id)).toEqual(['a', 'b'])
    expect(list.pagination).toEqual({ current: 1, pageSize: 20, total: 30 })
    expect(list.loading.value).toBe(false)
    expect(list.hasMore.value).toBe(false)
  })

  it('hasMore 未提供时按“已加载数小于总数”推断', async () => {
    const { list, fetchPage } = createList()
    fetchPage.mockResolvedValue(page(1, ['a'], 30))
    await list.loadData()
    expect(list.hasMore.value).toBe(true)
  })

  it('quiet 加载不切换 loading', async () => {
    const { list, fetchPage } = createList()
    fetchPage.mockResolvedValue(page(1, ['a'], 1))
    await list.loadData({ quiet: true })
    expect(list.loading.value).toBe(false)
  })

  it('请求失败调用 onError 并返回 false', async () => {
    const onError = vi.fn()
    const { list, fetchPage } = createList({ onError })
    const failure = new Error('加载失败')
    fetchPage.mockRejectedValue(failure)
    await expect(list.loadData()).resolves.toBe(false)
    expect(onError).toHaveBeenCalledWith(failure)
    expect(list.loading.value).toBe(false)
  })

  it('结果标记 superseded 时不应用', async () => {
    const { list, fetchPage } = createList()
    fetchPage.mockResolvedValue(page(1, ['a'], 1, { superseded: true }))
    await expect(list.loadData()).resolves.toBe(false)
    expect(list.items.value).toEqual([])
  })

  it('shouldApply 返回 false 时不应用', async () => {
    const { list, fetchPage } = createList()
    fetchPage.mockResolvedValue(page(1, ['a'], 1))
    await expect(list.loadData({ shouldApply: () => false })).resolves.toBe(false)
    expect(list.items.value).toEqual([])
  })

  it('页码大于 1 且空页无更多时回退第 1 页重拉', async () => {
    const { list, fetchPage } = createList()
    // fetchPage 收到的是 reactive pagination 引用，调用时立即快照，避免后续应用结果影响断言。
    const paginationSnapshots: Array<Record<string, unknown>> = []
    fetchPage.mockImplementation(async (_options, pagination) => {
      paginationSnapshots.push({ ...pagination })
      if (paginationSnapshots.length === 1) return page(4, [], 0, { hasMore: false })
      return page(1, ['a'], 1, { hasMore: false })
    })
    list.pagination.current = 4
    await expect(list.loadData()).resolves.toBe(true)
    expect(fetchPage).toHaveBeenCalledTimes(2)
    expect(paginationSnapshots[1]).toEqual({ current: 1, pageSize: 20, total: 0 })
    expect(list.pagination.current).toBe(1)
    expect(list.items.value.map((item) => item.id)).toEqual(['a'])
  })

  it('页码大于 1 且空页但 hasMore 为 true 时不回退', async () => {
    const { list, fetchPage } = createList()
    list.pagination.current = 4
    fetchPage.mockResolvedValue(page(4, [], 0, { hasMore: true }))
    await expect(list.loadData()).resolves.toBe(true)
    expect(fetchPage).toHaveBeenCalledTimes(1)
    expect(list.pagination.current).toBe(4)
  })

  it('append 追加结果并支持自定义 mergeItems', async () => {
    const { list, fetchPage } = createList()
    fetchPage.mockResolvedValueOnce(page(1, ['a'], 10))
    await list.loadData()
    fetchPage.mockResolvedValueOnce(page(2, ['b'], 10))
    await list.loadData({ append: true })
    expect(list.items.value.map((item) => item.id)).toEqual(['a', 'b'])

    const custom = useResponsivePagedList<Item>({
      pageSize: 20,
      showTotal: () => '',
      fetchPage: vi.fn().mockResolvedValue(page(1, ['x'], 1)),
      mergeItems: (current, next) => [...next, ...current]
    })
    await custom.loadData()
    await custom.loadData({ append: true })
    expect(custom.items.value.map((item) => item.id)).toEqual(['x', 'x'])
  })

  it('transformItems 在应用前转换条目', async () => {
    const transformItems = vi.fn((items: Item[]) => items.map((item) => ({ id: `${item.id}!` })))
    const { list, fetchPage } = createList({ transformItems })
    fetchPage.mockResolvedValue(page(1, ['a'], 1))
    await list.loadData()
    expect(list.items.value.map((item) => item.id)).toEqual(['a!'])
  })

  it('onLoaded 在应用结果后回调', async () => {
    const onLoaded = vi.fn()
    const result = page(1, ['a'], 1)
    const { list, fetchPage } = createList({ onLoaded })
    fetchPage.mockResolvedValue(result)
    await list.loadData({ quiet: true })
    expect(onLoaded).toHaveBeenCalledWith(result, { quiet: true })
  })

  it('相同 requestSignature 的并发加载共享请求', async () => {
    let resolveFetch!: (value: ResponsivePagedListResult<Item>) => void
    const fetchPage = vi.fn(() => new Promise<ResponsivePagedListResult<Item>>((done) => { resolveFetch = done }))
    const list = useResponsivePagedList<Item>({
      pageSize: 20,
      showTotal: () => '',
      fetchPage: fetchPage as unknown as Parameters<typeof useResponsivePagedList<Item>>[0]['fetchPage'],
      requestSignature: (options) => (options as { keyword?: string }).keyword
    })
    const first = list.loadData({ keyword: 'k' } as ResponsivePagedListLoadOptions)
    const second = list.loadData({ keyword: 'k' } as ResponsivePagedListLoadOptions)
    resolveFetch(page(1, ['a'], 1))
    await expect(first).resolves.toBe(true)
    await expect(second).resolves.toBe(true)
    expect(fetchPage).toHaveBeenCalledTimes(1)
  })
})

describe('applyResult', () => {
  it('直接应用结果并更新派生状态', () => {
    const { list } = createList()
    list.applyResult(page(2, ['a', 'b', 'c'], 8, { currentPageCount: 3, hasMore: true }))
    expect(list.pagination).toEqual({ current: 2, pageSize: 20, total: 8 })
    expect(list.hasMore.value).toBe(true)
  })
})

describe('removeItems / updateItems', () => {
  it('removeItems 过滤条目并同步 total', async () => {
    const { list, fetchPage } = createList()
    fetchPage.mockResolvedValue(page(1, ['a', 'b', 'c'], 3, { currentPageCount: 3 }))
    await list.loadData()
    expect(list.removeItems((item) => item.id === 'b')).toBe(1)
    expect(list.items.value.map((item) => item.id)).toEqual(['a', 'c'])
    expect(list.pagination.total).toBe(2)
    expect(list.removeItems(() => false)).toBe(0)
  })

  it('updateItems 更新命中条目', async () => {
    const { list, fetchPage } = createList()
    fetchPage.mockResolvedValue(page(1, ['a', 'b'], 2))
    await list.loadData()
    expect(list.updateItems((item) => item.id === 'a', (item) => ({ id: `${item.id}-updated` }))).toBe(1)
    expect(list.items.value.map((item) => item.id)).toEqual(['a-updated', 'b'])
    expect(list.updateItems(() => false, (item) => item)).toBe(0)
  })
})

describe('handleTableChange', () => {
  it('合法分页对象触发重新加载', async () => {
    const { list, fetchPage } = createList()
    fetchPage.mockResolvedValue(page(2, ['x'], 5))
    list.handleTableChange({ current: 2, pageSize: 20 })
    await Promise.resolve()
    expect(fetchPage).toHaveBeenCalledTimes(1)
    expect(list.pagination.current).toBe(2)
  })

  it('相同分页不触发加载', async () => {
    const { list, fetchPage } = createList()
    fetchPage.mockResolvedValue(page(1, [], 0))
    await list.loadData()
    list.handleTableChange({ current: 1, pageSize: 20 })
    expect(fetchPage).toHaveBeenCalledTimes(1)
  })

  it('非法输入被忽略或回退默认', async () => {
    const { list, fetchPage } = createList()
    fetchPage.mockResolvedValue(page(1, [], 0))
    list.handleTableChange(undefined)
    list.handleTableChange('x')
    list.handleTableChange({ current: 'bad', pageSize: -1 })
    await Promise.resolve()
    expect(list.pagination.current).toBe(1)
    expect(list.pagination.pageSize).toBe(20)
    expect(fetchPage).not.toHaveBeenCalled()
  })
})

describe('loadMoreMobile / refreshMobile', () => {
  it('无更多或加载中时不动作', async () => {
    const { list, fetchPage } = createList()
    await list.loadMoreMobile()
    expect(fetchPage).not.toHaveBeenCalled()
  })

  it('加载下一页并追加，失败时回退页码', async () => {
    const { list, fetchPage } = createList()
    fetchPage.mockResolvedValueOnce(page(1, ['a'], 5, { hasMore: true }))
    await list.loadData()
    fetchPage.mockResolvedValueOnce(page(2, ['b'], 5, { hasMore: false }))
    await list.loadMoreMobile()
    expect(list.items.value.map((item) => item.id)).toEqual(['a', 'b'])
    expect(list.pagination.current).toBe(2)
    expect(list.mobileLoadingMore.value).toBe(false)

    // 再次刷新为有更多后失败回退。
    fetchPage.mockResolvedValueOnce(page(1, ['a'], 5, { hasMore: true }))
    await list.refreshMobile()
    fetchPage.mockRejectedValueOnce(new Error('fail'))
    await list.loadMoreMobile()
    expect(list.pagination.current).toBe(1)
    expect(list.mobileLoadingMore.value).toBe(false)
  })

  it('refreshMobile 重置分页后加载', async () => {
    const { list, fetchPage } = createList()
    const paginationSnapshots: Array<Record<string, unknown>> = []
    fetchPage.mockImplementation(async (_options, pagination) => {
      paginationSnapshots.push({ ...pagination })
      return page(1, ['a'], 1)
    })
    list.pagination.current = 7
    await list.refreshMobile()
    expect(fetchPage).toHaveBeenCalledTimes(1)
    expect(paginationSnapshots[0]).toEqual({ current: 1, pageSize: 20, total: 0 })
    expect(list.pagination.current).toBe(1)
  })
})

describe('invalidatePendingLoads', () => {
  it('作废进行中的请求与移动端加载状态', async () => {
    let resolveFetch!: (value: ResponsivePagedListResult<Item>) => void
    const fetchPage = vi.fn(() => new Promise<ResponsivePagedListResult<Item>>((done) => { resolveFetch = done }))
    const list = useResponsivePagedList<Item>({
      pageSize: 20,
      showTotal: () => '',
      fetchPage: fetchPage as unknown as Parameters<typeof useResponsivePagedList<Item>>[0]['fetchPage']
    })
    const pending = list.loadData()
    list.invalidatePendingLoads()
    resolveFetch(page(1, ['a'], 1))
    await expect(pending).resolves.toBe(false)
    expect(list.items.value).toEqual([])
    expect(list.mobileLoadingMore.value).toBe(false)
  })
})

describe('tablePagination', () => {
  it('total 至少覆盖已加载页所需最小值', async () => {
    const { list, fetchPage } = createList()
    fetchPage.mockResolvedValue(page(3, ['a'], 1, { currentPageCount: 1, hasMore: true }))
    await list.loadData()
    const table = list.tablePagination.value
    expect(table.current).toBe(3)
    // 最小总数 = (3-1)*20 + 1 + 1 = 42
    expect(table.total).toBe(42)
    expect(table.hideOnSinglePage).toBe(true)
    expect(table.showSizeChanger).toBe(false)
  })

  it('showTotal 透传上下文', async () => {
    const showTotal = vi.fn(() => '文案')
    const { list, fetchPage } = createList({ showTotal })
    fetchPage.mockResolvedValue(page(1, ['a', 'b'], 5, { currentPageCount: 2 }))
    await list.loadData()
    const text = list.tablePagination.value.showTotal(5, [1, 2])
    expect(text).toBe('文案')
    expect(showTotal).toHaveBeenCalledWith(5, [1, 2], {
      current: 1, currentPageCount: 2, hasMore: true, loadedCount: 2, pageSize: 20
    })
  })
})
