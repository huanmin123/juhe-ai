import type { StudioTask, StudioTaskStatus } from '@/types/domain'

/**
 * 工作室任务展示态判定（媒体工作室设计 §6/§8 强制点 5）：
 * - 待轮询：未终态，或 completed 但结算未落（assetId 尚未返回，仍需轮询取产物）。
 * - 可落定终态：completed+settled（assetId 可空 = 产物缺失合法展示态）、failed
 *   且带 error、cancelled、expired；残缺终态（completed 未结算 / failed 无
 *   error）不落定，继续轮询等待结算回填。
 */

export function isStudioTaskPending(task: StudioTask): boolean {
  if (task.status === 'queued' || task.status === 'in_progress') return true
  return task.status === 'completed' && task.settlement !== 'settled'
}

export function isStudioTaskFinalSnapshot(task: StudioTask): boolean {
  switch (task.status satisfies StudioTaskStatus) {
    case 'completed':
      return task.settlement === 'settled'
    case 'failed':
      return Boolean(task.error)
    case 'cancelled':
    case 'expired':
      return true
    default:
      return false
  }
}

/** 任务展示产物可用性（§8 强制点 5）：仅 settled 且带 assetId 才显示成品；settled 无 assetId = 产物缺失。 */
export type StudioTaskArtifactState = 'pending' | 'ready' | 'missing' | 'failed' | 'stopped'

export function studioTaskArtifactState(task: StudioTask): StudioTaskArtifactState {
  if (task.status === 'failed') return 'failed'
  if (task.status === 'cancelled' || task.status === 'expired') return 'stopped'
  if (task.status === 'completed') {
    return task.settlement === 'settled'
      ? (task.assetId ? 'ready' : 'missing')
      : 'pending'
  }
  return 'pending'
}
