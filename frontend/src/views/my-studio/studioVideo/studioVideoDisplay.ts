import type { StudioModelOption, StudioTask } from '@/types/domain'
import { formatStudioCostEstimate } from '../studioDisplayCommon'
import { studioCapabilityUnitPrice } from '../shared/studioCapabilities'
import type { StudioTaskArtifactState } from '../shared/studioTaskState'

/** 视频工作台展示纯函数（时长/画幅档位、成本预估、产物状态文案）。 */

/** 标量档位展示：数值加"秒"，字符串原样（如 "4s"、"720p"）。 */
export function formatSecondsOption(value: string | number): string {
  return typeof value === 'number' ? `${value} 秒` : value
}

/** 成本预估：单价取目录能力载荷 unitPrice（按秒），数量=所选时长秒数；未选时长、非数值档位或无价 → "无法估算"。 */
export function studioVideoCostEstimate(model: StudioModelOption | undefined, seconds?: string | number): string {
  const unitPrice = model ? studioCapabilityUnitPrice(model.capabilities) : undefined
  const quantity = typeof seconds === 'number'
    ? seconds
    : (typeof seconds === 'string' && /^\d+(\.\d+)?$/.test(seconds) ? Number(seconds) : Number.NaN)
  return formatStudioCostEstimate(unitPrice, Number.isFinite(quantity) ? quantity : Number.NaN)
}

/** 任务产物状态文案（§8 强制点 5：settled 前不显示成品；产物悬空按缺失渲染）。 */
export function videoTaskArtifactLabel(state: StudioTaskArtifactState): string {
  const labels: Record<StudioTaskArtifactState, string> = {
    pending: '生成中',
    ready: '可播放',
    missing: '产物缺失',
    failed: '生成失败',
    stopped: '已停止'
  }
  return labels[state]
}

/** 乐观队列卡 + 历史任务行合并键（草稿卡 pending- 前缀置顶展示）。 */
export function sortVideoTasksForDisplay(tasks: StudioTask[]): StudioTask[] {
  const rank = (task: StudioTask): number => {
    if (task.id.startsWith('pending-')) return 0
    if (task.status === 'queued' || task.status === 'in_progress') return 1
    if (task.status === 'completed' && task.settlement !== 'settled') return 2
    return 3
  }
  return [...tasks].sort((a, b) => rank(a) - rank(b))
}
