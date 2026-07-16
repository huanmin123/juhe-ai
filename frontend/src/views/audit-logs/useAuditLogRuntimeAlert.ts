import { computed, ref } from 'vue'

import { api } from '@/api/client'
import type { AuditLogRuntime } from '@/types/domain'

export function useAuditLogRuntimeAlert() {
  const runtime = ref<AuditLogRuntime>()
  let auditRuntimeRequestSeq = 0

  const auditRuntimeRiskReasons = computed(() => {
    const info = runtime.value
    if (!info) return []
    const reasons: string[] = []
    if (info.flushLastError) reasons.push(`最近写入失败：${info.flushLastError}`)
    if (positiveRuntimeCount(info.droppedSuccessCount)) reasons.push(`成功审计丢弃 ${info.droppedSuccessCount} 条`)
    if (positiveRuntimeCount(info.droppedFailureCount)) reasons.push(`失败审计丢弃 ${info.droppedFailureCount} 条`)
    if (positiveRuntimeCount(info.droppedOverflowCount)) reasons.push(`队列溢出丢弃 ${info.droppedOverflowCount} 条`)
    if (positiveRuntimeCount(info.droppedOversizeCount)) reasons.push(`超限审计丢弃 ${info.droppedOversizeCount} 条`)
    if (positiveRuntimeCount(info.transport.failedCount)) reasons.push(`审计传输处理失败 ${info.transport.failedCount} 次`)
    if (positiveRuntimeCount(info.transport.rejectedCount)) reasons.push(`审计传输容量拒绝 ${info.transport.rejectedCount} 次`)
    return reasons
  })

  const auditRuntimeAlertVisible = computed(() => auditRuntimeRiskReasons.value.length > 0)
  const auditRuntimeAlertDescription = computed(() => {
    const info = runtime.value
    if (!info) return ''
    const reasons = auditRuntimeRiskReasons.value
    const workerText = info.worker.available
      ? `后台进程${runtimeReadyText(info.worker.ready)}`
      : '后台进程状态不可用'
    return `${reasons.join('；')}。${workerText}。`
  })

  async function refreshAuditRuntimeQuietly(): Promise<void> {
    const requestSeq = ++auditRuntimeRequestSeq
    try {
      const runtimeInfo = await api.auditLogs.runtime()
      if (requestSeq !== auditRuntimeRequestSeq) return
      runtime.value = runtimeInfo
    } catch (error) {
      if (requestSeq !== auditRuntimeRequestSeq) return
      console.error(error)
    }
  }

  function cancelAuditRuntimeRequest(): void {
    auditRuntimeRequestSeq += 1
  }

  return {
    auditRuntimeSettings: computed(() => runtime.value?.settings),
    auditRuntimeAlertDescription,
    auditRuntimeAlertVisible,
    cancelAuditRuntimeRequest,
    refreshAuditRuntimeQuietly
  }
}

function runtimeReadyText(value: boolean | null): string {
  if (value === true) return '已就绪'
  if (value === false) return '未就绪'
  return '状态未知'
}

function positiveRuntimeCount(value: number | null | undefined): boolean {
  return typeof value === 'number' && value > 0
}
