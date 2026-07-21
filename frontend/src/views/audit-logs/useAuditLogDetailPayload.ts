import { ref } from 'vue'

import { api } from '@/api/client'
import { message } from '@/lib/antd'
import { loadEntityDetailCached } from '@/shared/entityDetailCache'
import type {
  AuditLogDetail,
  AuditLogPayloadDetail,
  AuditLogSummary
} from '@/types/domain'
import {
  finalizeMergedPayloadBody,
  mergeAuditPayloadWindow
} from './auditPayloadDetails'

const auditPayloadReadWindowBytes = 256 * 1024

type AuditLogDetailPayloadDependencies = {
  loadDetail?: (id: string) => Promise<AuditLogDetail>
  loadPayload?: typeof api.auditLogs.payload
  reportError?: (text: string) => void
  reportWarning?: (text: string) => void
}

export function useAuditLogDetailPayload(dependencies: AuditLogDetailPayloadDependencies = {}) {
  const detailLoading = ref(false)
  const payloadLoadingId = ref('')
  const detail = ref<AuditLogDetail>()
  const selectedPayload = ref<AuditLogPayloadDetail>()
  const detailOpen = ref(false)
  let detailRequestId = 0
  let payloadRequestId = 0

  async function openDetail(record: AuditLogSummary): Promise<void> {
    const requestId = detailRequestId + 1
    detailRequestId = requestId
    payloadRequestId += 1
    payloadLoadingId.value = ''
    detail.value = undefined
    detailOpen.value = true
    detailLoading.value = true
    selectedPayload.value = undefined
    try {
      const nextDetail = await loadEntityDetailCached({
        id: record.id,
        load: () => dependencies.loadDetail?.(record.id) ?? api.auditLogs.detail(record.id),
        namespace: 'audit-log-detail'
      })
      if (requestId === detailRequestId) {
        detail.value = nextDetail
      }
    } catch (error) {
      if (requestId === detailRequestId) {
        console.error(error)
        reportError('加载审计详情失败')
      }
    } finally {
      if (requestId === detailRequestId) {
        detailLoading.value = false
      }
    }
  }

  async function loadPayload(payloadId: string): Promise<void> {
    if (!detail.value) return
    const requestId = payloadRequestId + 1
    payloadRequestId = requestId
    payloadLoadingId.value = payloadId
    selectedPayload.value = undefined
    try {
      const nextPayload = await loadPayloadWindow(payloadId, 0, requestId)
      if (!nextPayload) return
      if (requestId === payloadRequestId) {
        selectedPayload.value = finalizeMergedPayloadBody(nextPayload)
      }
    } catch (error) {
      if (requestId === payloadRequestId) {
        console.error(error)
        reportError('加载原始请求失败')
      }
    } finally {
      if (requestId === payloadRequestId) {
        payloadLoadingId.value = ''
      }
    }
  }

  async function loadNextPayloadWindow(): Promise<void> {
    if (!detail.value || !selectedPayload.value?.bodyTruncated) return
    const payloadId = selectedPayload.value.id
    const requestedOffset = selectedPayload.value.bodyNextOffset
    if (requestedOffset === undefined) return
    const requestId = payloadRequestId + 1
    payloadRequestId = requestId
    payloadLoadingId.value = payloadId
    try {
      const nextPayload = await loadPayloadWindow(payloadId, requestedOffset, requestId)
      if (!nextPayload || requestId !== payloadRequestId || !selectedPayload.value) return
      if (
        nextPayload.bodyBytesReturned <= 0
        || (nextPayload.bodyNextOffset !== undefined && nextPayload.bodyNextOffset <= requestedOffset)
      ) {
        selectedPayload.value = { ...selectedPayload.value, bodyNextOffset: undefined }
        reportWarning('未读取到更多正文，已停止继续加载')
        return
      }
      selectedPayload.value = finalizeMergedPayloadBody(
        mergeAuditPayloadWindow(selectedPayload.value, nextPayload)
      )
    } catch (error) {
      if (requestId === payloadRequestId) {
        console.error(error)
        reportError('加载下一段原始请求失败')
      }
    } finally {
      if (requestId === payloadRequestId) {
        payloadLoadingId.value = ''
      }
    }
  }

  async function loadPayloadWindow(
    payloadId: string,
    offset: number,
    requestId: number
  ): Promise<AuditLogPayloadDetail | undefined> {
    if (!detail.value) return undefined
    const auditLogId = detail.value.id
    const payload = await (dependencies.loadPayload ?? api.auditLogs.payload)(auditLogId, payloadId, {
      offset,
      limit: auditPayloadReadWindowBytes
    })
    if (requestId !== payloadRequestId) return undefined
    return payload
  }

  function reportError(text: string): void {
    if (dependencies.reportError) dependencies.reportError(text)
    else message.error(text)
  }

  function reportWarning(text: string): void {
    if (dependencies.reportWarning) dependencies.reportWarning(text)
    else message.warning(text)
  }

  function closeTransientDetails(): void {
    detailRequestId += 1
    payloadRequestId += 1
    detailOpen.value = false
    detailLoading.value = false
    payloadLoadingId.value = ''
    detail.value = undefined
    selectedPayload.value = undefined
  }

  return {
    closeTransientDetails,
    detail,
    detailLoading,
    detailOpen,
    loadNextPayloadWindow,
    loadPayload,
    openDetail,
    payloadLoadingId,
    selectedPayload
  }
}
