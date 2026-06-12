import { strict as assert } from 'node:assert'
import { readFileSync } from 'node:fs'

const queueFiles = [
  {
    path: '../../modules/gateway/usage-record-queue.service.ts',
    required: ['peekUsageRecordFlushBatch', 'removeUsageRecordFlushBatch']
  },
  {
    path: '../../modules/audit-logs/audit-log-queue.service.ts',
    required: ['peekAuditLogFlushBatch', 'removeAuditLogFlushBatch']
  },
  {
    path: '../../modules/operation-logs/operation-log-queue.service.ts',
    required: ['pendingOperationLogs.slice(0, operationLogBatchSize)']
  },
  {
    path: '../../modules/runtime-logs/runtime-log-index-queue.service.ts',
    required: ['peekRuntimeLogFlushBatch', 'removeRuntimeLogFlushBatch']
  },
  {
    path: '../../modules/record-maintenance/record-maintenance-queue.service.ts',
    required: ['pendingJobs.slice(0, recordMaintenanceBatchSize)', 'removeRecordMaintenanceJobsFromHead']
  }
]

for (const file of queueFiles) {
  const source = readFileSync(new URL(file.path, import.meta.url), 'utf8')
  assert(
    !/pending[A-Za-z]*\s*=\s*\[\s*\.\.\.\s*batch/.test(source),
    `${file.path} 失败重试路径不能用 [...batch, ...pending] 复制整个积压队列`
  )
  assert(
    !/pendingJobs\s*=\s*\[\s*\.\.\.\s*batch\.slice/.test(source),
    `${file.path} 失败重试路径不能用 batch.slice 拼回整个积压队列`
  )
  assert(
    !/sumQueued[A-Za-z]*\([^)]*pending[A-Za-z]*/.test(source),
    `${file.path} 失败重试路径不能为恢复字节数重新 reduce 整个积压队列`
  )
  for (const marker of file.required) {
    assert(source.includes(marker), `${file.path} 缺少失败保持原队列、成功后移除 batch 的边界实现：${marker}`)
  }
}

console.log('队列失败重试边界回归通过：本地队列 flush 失败时保持原队列，不复制全部积压记录')
