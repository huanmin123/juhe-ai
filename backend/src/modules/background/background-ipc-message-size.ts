import { estimateJsonLikeBytes } from '../../shared/queue-size.js'
import type { BackgroundWorkerMessage } from './background-ipc.types.js'

export const usageRecordWorkerMessageMaxBytes = 8 * 1024 * 1024
export const regularWorkerMessageMaxBytes = 8 * 1024 * 1024

const workerMessageEstimateMaxBytes = Math.max(usageRecordWorkerMessageMaxBytes, regularWorkerMessageMaxBytes) + 1
const workerMessageEstimateMaxNodes = 20_000
const workerMessageBytesCache = new WeakMap<object, number>()

export function estimateWorkerMessageBytes(message: BackgroundWorkerMessage): number {
  if (typeof message === 'object' && message !== null) {
    const cached = workerMessageBytesCache.get(message)
    if (cached !== undefined) {
      return cached
    }
  }

  let bytes: number
  switch (message.type) {
    case 'background_worker_usage_records':
      bytes = message.items.reduce((sum, item) => Math.min(workerMessageEstimateMaxBytes, sum + estimateJsonBytes(item) + 256), 128)
      break
    case 'background_worker_public_api_logs':
      bytes = message.items.reduce((sum, item) => Math.min(workerMessageEstimateMaxBytes, sum + estimateJsonBytes(item) + 256), 128)
      break
    case 'background_worker_record_maintenance':
      bytes = message.items.reduce((sum, item) => Math.min(workerMessageEstimateMaxBytes, sum + estimateJsonBytes(item) + 256), 128)
      break
    case 'background_worker_account_test_tasks':
      bytes = message.taskIds.reduce((sum, taskId) => Math.min(workerMessageEstimateMaxBytes, sum + Buffer.byteLength(taskId, 'utf8') + 64), 128)
      break
    case 'background_worker_account_test_cancel':
      bytes = Buffer.byteLength(message.taskId, 'utf8') + 128
      break
    case 'background_worker_status_request':
    case 'background_worker_status_response':
    case 'background_worker_ready':
    case 'server_account_runtime_clear':
    case 'gateway_runtime_cache_invalidate':
    case 'gateway_quota_snapshot_update':
      bytes = 512
      break
    default:
      bytes = 512
      break
  }
  if (typeof message === 'object' && message !== null) {
    workerMessageBytesCache.set(message, bytes)
  }
  return bytes
}

function estimateJsonBytes(value: unknown): number {
  return estimateJsonLikeBytes(value, {
    maxBytes: workerMessageEstimateMaxBytes,
    maxNodes: workerMessageEstimateMaxNodes
  })
}
