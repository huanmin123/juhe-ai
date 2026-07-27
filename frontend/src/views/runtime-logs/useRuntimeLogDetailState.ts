import { message } from '@/lib/antd'
import { ref } from 'vue'

import { api } from '@/api/client'
import type { RuntimeLogGrepItem, RuntimeLogSummary } from '@/types/domain'

type RuntimeLogListRecord = RuntimeLogSummary | RuntimeLogGrepItem

export function useRuntimeLogDetailState() {
  const selectedLog = ref<RuntimeLogSummary>()
  const selectedGrepItem = ref<RuntimeLogGrepItem>()
  const detailOpen = ref(false)
  const grepDetailOpen = ref(false)
  let detailRequestId = 0

  async function openDetail(record: RuntimeLogSummary): Promise<void> {
    const requestId = detailRequestId + 1
    detailRequestId = requestId
    selectedLog.value = record
    detailOpen.value = true
    try {
      const detail = await api.runtimeLogs.detail(record.id)
      if (detailRequestId === requestId) {
        selectedLog.value = detail
      }
    } catch (error) {
      console.error(error)
      message.error('加载运行日志详情失败')
    }
  }

  function openGrepDetail(record: RuntimeLogGrepItem): void {
    selectedGrepItem.value = record
    grepDetailOpen.value = true
  }

  function openRuntimeLogDetail(record: RuntimeLogListRecord): void {
    void openDetail(record as RuntimeLogSummary)
  }

  function openRuntimeGrepDetail(record: RuntimeLogListRecord): void {
    openGrepDetail(record as RuntimeLogGrepItem)
  }

  function closeTransientDetails(): void {
    detailRequestId += 1
    detailOpen.value = false
    grepDetailOpen.value = false
    selectedLog.value = undefined
    selectedGrepItem.value = undefined
  }

  return {
    closeTransientDetails,
    detailOpen,
    grepDetailOpen,
    openDetail,
    openGrepDetail,
    openRuntimeGrepDetail,
    openRuntimeLogDetail,
    selectedGrepItem,
    selectedLog
  }
}
