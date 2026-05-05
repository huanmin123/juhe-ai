import { computed, reactive, ref } from 'vue'

export function useAccountMobilePagination(pageSize: number, totalCount: () => number, loadData: () => Promise<void>) {
  const mobileLoadingMore = ref(false)
  const mobileRefreshing = ref(false)
  const mobileVisibleCount = ref(pageSize)
  const accountPagination = reactive({ current: 1, pageSize })

  const mobileHasMore = computed(() => mobileVisibleCount.value < totalCount())
  const tablePagination = computed(() => ({
    current: accountPagination.current,
    pageSize: accountPagination.pageSize,
    total: totalCount(),
    hideOnSinglePage: true,
    showSizeChanger: false,
    showTotal: (total: number) => `共 ${total} 个账户`
  }))

  function resetPagination() {
    accountPagination.current = 1
    mobileVisibleCount.value = pageSize
  }

  function handleTableChange(pagination: unknown) {
    if (!pagination || typeof pagination !== 'object') return
    const nextPagination = pagination as { current?: number; pageSize?: number }
    accountPagination.current = nextPagination.current ?? accountPagination.current
    accountPagination.pageSize = nextPagination.pageSize ?? accountPagination.pageSize
  }

  function loadMoreMobile() {
    if (mobileLoadingMore.value || !mobileHasMore.value) return
    mobileLoadingMore.value = true
    window.setTimeout(() => {
      mobileVisibleCount.value = Math.min(mobileVisibleCount.value + pageSize, totalCount())
      mobileLoadingMore.value = false
    }, 260)
  }

  async function refreshMobile() {
    if (mobileRefreshing.value) return
    mobileRefreshing.value = true
    try {
      resetPagination()
      await loadData()
    } finally {
      mobileRefreshing.value = false
    }
  }

  function clampPagination() {
    const maxPage = Math.max(1, Math.ceil(totalCount() / accountPagination.pageSize))
    accountPagination.current = Math.min(accountPagination.current, maxPage)
    mobileVisibleCount.value = Math.min(Math.max(mobileVisibleCount.value, pageSize), Math.max(totalCount(), pageSize))
  }

  return {
    accountPagination,
    mobileHasMore,
    mobileLoadingMore,
    mobileRefreshing,
    mobileVisibleCount,
    tablePagination,
    clampPagination,
    handleTableChange,
    loadMoreMobile,
    refreshMobile,
    resetPagination
  }
}
