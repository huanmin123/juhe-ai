import { message } from '@/lib/antd'
import { ref } from 'vue'

import { api } from '@/api/client'
import type { RuntimeLogDetailView, RuntimeLogGrepDetailView, RuntimeLogGrepItem, RuntimeLogSummary } from '@/types/domain'

type RuntimeLogListRecord = RuntimeLogSummary | RuntimeLogGrepItem

export function useRuntimeLogDetailState() {
  const selectedLog = ref<RuntimeLogDetailView>()
  const selectedGrepItem = ref<RuntimeLogGrepDetailView>()
  const detailOpen = ref(false)
  const detailLoading = ref(false)
  const grepDetailOpen = ref(false)
  const grepDetailLoading = ref(false)
  let detailRequestId = 0

  async function openDetail(record: RuntimeLogSummary): Promise<void> {
    const requestId = detailRequestId + 1
    detailRequestId = requestId
    selectedLog.value = record
    detailOpen.value = true
    detailLoading.value = true
    grepDetailLoading.value = false
    try {
      const detail = await api.runtimeLogs.detail(record.id)
      if (detailRequestId === requestId) {
        selectedLog.value = { ...record, ...detail }
      }
    } catch (error) {
      if (detailRequestId !== requestId) return
      console.error(error)
      message.error('加载运行日志详情失败')
    } finally {
      if (detailRequestId === requestId) detailLoading.value = false
    }
  }

  async function openGrepDetail(record: RuntimeLogGrepItem): Promise<void> {
    const requestId = detailRequestId + 1
    detailRequestId = requestId
    selectedGrepItem.value = record
    grepDetailOpen.value = true
    grepDetailLoading.value = true
    detailLoading.value = false
    try {
      const detail = await api.runtimeLogs.grepDetail({
        id: record.id,
        fileName: record.fileName,
        lineNumber: record.lineNumber
      })
      if (detailRequestId === requestId) {
        selectedGrepItem.value = { ...record, ...detail }
      }
    } catch (error) {
      if (detailRequestId !== requestId) return
      console.error(error)
      message.error('加载 grep 匹配行详情失败，请重新搜索')
    } finally {
      if (detailRequestId === requestId) grepDetailLoading.value = false
    }
  }

  function openRuntimeLogDetail(record: RuntimeLogListRecord): void {
    void openDetail(record as RuntimeLogSummary)
  }

  function openRuntimeGrepDetail(record: RuntimeLogListRecord): void {
    void openGrepDetail(record as RuntimeLogGrepItem)
  }

  function closeTransientDetails(): void {
    detailRequestId += 1
    detailOpen.value = false
    grepDetailOpen.value = false
    detailLoading.value = false
    grepDetailLoading.value = false
    selectedLog.value = undefined
    selectedGrepItem.value = undefined
  }

  return {
    closeTransientDetails,
    detailOpen,
    detailLoading,
    grepDetailOpen,
    grepDetailLoading,
    openDetail,
    openGrepDetail,
    openRuntimeGrepDetail,
    openRuntimeLogDetail,
    selectedGrepItem,
    selectedLog
  }
}
