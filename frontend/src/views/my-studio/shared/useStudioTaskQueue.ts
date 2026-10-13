import axios from 'axios'
import { onBeforeUnmount, ref } from 'vue'

import { extractApiErrorMessage } from '@/shared/apiError'
import { message } from '@/lib/antd'
import { studioApi, newStudioClientOperationId } from '@/api/domains/studio'
import type { StudioTask, StudioTaskKind } from '@/types/domain'
import { isStudioTaskFinalSnapshot, isStudioTaskPending } from './studioTaskState'
import { startStudioTaskPolling, type StudioTaskPollingControl } from './useStudioTaskPolling'

/**
 * 工作台异步任务队列（图像/视频共用，规划 F3.1/F3.3 交互流）：
 * - 提交即乐观插入队列卡（pending- 前缀草稿卡），受理返回后按 clientOperationId
 *   替换为真实任务卡；
 * - 响应丢失（网络错误/超时，受理结果未知）先 GET /tasks?operation_id= 确认，
 *   已受理沿用任务卡，未受理才移除草稿卡并提示可重发（决策 #9②，防重复付费）；
 * - keepAlive 激活恢复轮询与计时、停用暂停；激活时补一轮服务端同步。
 */

interface OptimisticDraft extends StudioTask {
  // 草稿卡仅存在于前端：id 以 pending- 前缀与真实 stask_ 行区分。
}

function isDraftTask(task: StudioTask): boolean {
  return task.id.startsWith('pending-')
}

function isResponseLost(error: unknown): boolean {
  return axios.isAxiosError(error) && error.response === undefined
}

export interface StudioTaskQueueOptions {
  scopeId: string
  kind: StudioTaskKind
  /** 发起创建请求（携带调用方组装的参数 + 传入的幂等键）。 */
  submit: (clientOperationId: string) => Promise<StudioTask>
  /** 明确失败终态卡片点击重试：由调用方回填参数（换新幂等键新建任务）。 */
  onRetryRequested?: (task: StudioTask) => void
  /** 任务落定回调（completed+settled 出现 assetId 后刷新作品列表）。 */
  onSettled?: (task: StudioTask) => void
  fallbackErrorMessage: string
}

export function useStudioTaskQueue(options: StudioTaskQueueOptions) {
  const queueTasks = ref<StudioTask[]>([])
  const nowMs = ref(Date.now())
  const cancellingTaskId = ref<string | undefined>()
  let pollingControl: StudioTaskPollingControl | undefined
  let elapsedTimer: ReturnType<typeof setInterval> | undefined

  function start(): void {
    if (!pollingControl) {
      pollingControl = startStudioTaskPolling({
        scopeId: () => options.scopeId,
        tasks: () => queueTasks.value,
        applySnapshot: (task) => {
          upsertTask(task)
          if (isStudioTaskFinalSnapshot(task)) options.onSettled?.(task)
        }
      })
    }
    if (!elapsedTimer) {
      elapsedTimer = setInterval(() => {
        nowMs.value = Date.now()
      }, 1000)
    }
  }

  function pause(): void {
    pollingControl?.stop()
    pollingControl = undefined
    if (elapsedTimer) {
      clearInterval(elapsedTimer)
      elapsedTimer = undefined
    }
  }

  onBeforeUnmount(pause)

  function upsertTask(task: StudioTask): void {
    const index = queueTasks.value.findIndex((item) => item.id === task.id)
    if (index >= 0) {
      queueTasks.value.splice(index, 1, task)
      return
    }
    const draftIndex = queueTasks.value.findIndex((item) => isDraftTask(item) && item.clientOperationId === task.clientOperationId)
    if (draftIndex >= 0) {
      queueTasks.value.splice(draftIndex, 1, task)
    }
  }

  function removeDraft(operationId: string): void {
    queueTasks.value = queueTasks.value.filter((item) => !(isDraftTask(item) && item.clientOperationId === operationId))
  }

  /** 服务端同步（激活恢复/提交成功后）：真实待轮询行 + 未确认草稿卡合并。 */
  async function refreshActiveTasks(): Promise<void> {
    try {
      const result = await studioApi.listTasks({ kind: options.kind, limit: 50 })
      const pendingRows = result.rows.filter((row) => isStudioTaskPending(row))
      const drafts = queueTasks.value.filter((draft) =>
        isDraftTask(draft) && !pendingRows.some((row) => row.clientOperationId === draft.clientOperationId))
      queueTasks.value = [...drafts, ...pendingRows]
    } catch (error) {
      console.error(error)
      message.error(extractApiErrorMessage(error, '同步任务状态失败'))
    }
  }

  /** 提交生成（乐观插入 → 受理 → 响应丢失确认）。 */
  async function submitTask(draft: { model: string; promptSummary: string }): Promise<boolean> {
    const operationId = newStudioClientOperationId()
    const now = new Date().toISOString()
    const optimistic: OptimisticDraft = {
      id: `pending-${operationId}`,
      kind: options.kind,
      status: 'queued',
      settlement: 'pending_settled',
      model: draft.model,
      promptSummary: draft.promptSummary,
      clientOperationId: operationId,
      createdAt: now,
      updatedAt: now
    }
    queueTasks.value = [optimistic, ...queueTasks.value]
    try {
      const task = await options.submit(operationId)
      upsertTask(task)
      return true
    } catch (error) {
      if (isResponseLost(error)) {
        // 响应丢失：受理结果未知，先按 operation_id 查询确认，未受理才允许重发。
        try {
          const confirmed = await studioApi.listTasks({ kind: options.kind, operation_id: operationId, limit: 5 })
          const task = confirmed.rows.find((row) => row.clientOperationId === operationId)
          if (task) {
            upsertTask(task)
            message.success('任务已受理成功（响应曾丢失，经任务查询确认）')
            return true
          }
        } catch (confirmError) {
          console.error(confirmError)
        }
        removeDraft(operationId)
        message.error('网络中断且未能确认任务是否受理，为避免重复计费未重发；请稍后在任务列表确认后重试')
        return false
      }
      removeDraft(operationId)
      message.error(extractApiErrorMessage(error, options.fallbackErrorMessage))
      return false
    }
  }

  async function cancelTask(task: StudioTask): Promise<void> {
    cancellingTaskId.value = task.id
    try {
      const updated = await studioApi.cancelTask(task.id)
      upsertTask(updated)
      message.success(updated.status === 'cancelled' ? '任务已取消' : '已停止页面轮询，后台结算不受影响')
    } catch (error) {
      message.error(extractApiErrorMessage(error, '取消任务失败'))
    } finally {
      cancellingTaskId.value = undefined
    }
  }

  return {
    queueTasks,
    nowMs,
    cancellingTaskId,
    isDraftTask,
    start,
    pause,
    refreshActiveTasks,
    submitTask,
    cancelTask
  }
}
