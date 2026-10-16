import { ref } from 'vue'

import { api } from '@/api/client'
import { message } from '@/lib/antd'
import type {
  AuditLogDisplayDetail,
  AuditLogDetailSupplement,
  AuditLogPayloadDetail,
  AuditLogListItem
} from '@/types/domain'

type AuditLogDetailPayloadDependencies = {
  loadDetail?: (id: string) => Promise<AuditLogDetailSupplement>
  loadPayload?: typeof api.auditLogs.payload
  reportError?: (text: string) => void
}

export function useAuditLogDetailPayload(dependencies: AuditLogDetailPayloadDependencies = {}) {
  const detailLoading = ref(false)
  const payloadLoadingId = ref('')
  const detail = ref<AuditLogDisplayDetail>()
  const selectedPayload = ref<AuditLogPayloadDetail>()
  const detailOpen = ref(false)
  let detailRequestId = 0
  let payloadRequestId = 0

  async function openDetail(record: AuditLogListItem): Promise<void> {
    await openDetailById(record.id, record)
  }

  // openDetailById 支持从错误组样本事件等场景按 ID 直开详情；record 仅用于
  // 补充列表行字段，detail 接口返回的同名字段始终覆盖它。
  async function openDetailById(id: string, record?: AuditLogListItem): Promise<void> {
    const requestId = detailRequestId + 1
    detailRequestId = requestId
    payloadRequestId += 1
    payloadLoadingId.value = ''
    detail.value = undefined
    detailOpen.value = true
    detailLoading.value = true
    selectedPayload.value = undefined
    try {
      const nextDetail = await (dependencies.loadDetail?.(id) ?? api.auditLogs.detail(id))
      if (requestId === detailRequestId) {
        // 后端 detail 实际返回完整 AuditLogSummary 字段（DTO 类型面按 UI 消费
        // 收窄为 Supplement），按 ID 直开时无列表行兜底，合并后显式断言。
        detail.value = { ...record, ...nextDetail } as AuditLogDisplayDetail
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
      const auditLogId = detail.value.id
      const nextPayload = await (dependencies.loadPayload ?? api.auditLogs.payload)(auditLogId, payloadId)
      if (requestId === payloadRequestId) {
        selectedPayload.value = nextPayload
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

  function reportError(text: string): void {
    if (dependencies.reportError) dependencies.reportError(text)
    else message.error(text)
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
    openDetailById,
    payloadLoadingId,
    selectedPayload
  }
}
