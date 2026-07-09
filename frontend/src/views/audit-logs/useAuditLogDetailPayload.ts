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

const auditPayloadFullReadWindowBytes = 768 * 1024

export function useAuditLogDetailPayload() {
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
    detailOpen.value = true
    detailLoading.value = true
    selectedPayload.value = undefined
    try {
      const nextDetail = await loadEntityDetailCached({
        id: record.id,
        load: () => api.auditLogs.detail(record.id),
        namespace: 'audit-log-detail'
      })
      if (requestId === detailRequestId) {
        detail.value = nextDetail
      }
    } catch (error) {
      console.error(error)
      message.error('加载审计详情失败')
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
      const nextPayload = await loadCompletePayload(payloadId, requestId)
      if (!nextPayload) return
      if (requestId === payloadRequestId) {
        selectedPayload.value = nextPayload
      }
    } catch (error) {
      console.error(error)
      message.error('加载原始请求失败')
    } finally {
      if (requestId === payloadRequestId) {
        payloadLoadingId.value = ''
      }
    }
  }

  async function loadCompletePayload(
    payloadId: string,
    requestId: number
  ): Promise<AuditLogPayloadDetail | undefined> {
    if (!detail.value) return undefined
    const auditLogId = detail.value.id
    let mergedPayload = await api.auditLogs.payload(auditLogId, payloadId, {
      offset: 0,
      limit: auditPayloadFullReadWindowBytes
    })
    if (requestId !== payloadRequestId) return undefined
    while (mergedPayload.bodyTruncated && mergedPayload.bodyNextOffset !== undefined) {
      const requestedOffset = mergedPayload.bodyNextOffset
      const nextPayload = await api.auditLogs.payload(auditLogId, payloadId, {
        offset: requestedOffset,
        limit: auditPayloadFullReadWindowBytes
      })
      if (requestId !== payloadRequestId) return undefined
      if (nextPayload.bodyBytesReturned <= 0) break
      mergedPayload = mergeAuditPayloadWindow(mergedPayload, nextPayload)
      if (
        nextPayload.bodyTruncated
        && nextPayload.bodyNextOffset !== undefined
        && nextPayload.bodyNextOffset <= requestedOffset
      ) {
        break
      }
    }
    return finalizeMergedPayloadBody(mergedPayload)
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
    loadPayload,
    openDetail,
    payloadLoadingId,
    selectedPayload
  }
}
