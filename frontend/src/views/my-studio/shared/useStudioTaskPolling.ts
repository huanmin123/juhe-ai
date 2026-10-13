import { studioApi } from '@/api/domains/studio'
import { isStudioTaskFinalSnapshot, isStudioTaskPending } from './studioTaskState'
import type { StudioTask } from '@/types/domain'

/**
 * 工作室任务轮询（媒体工作室设计 §8 强制点 4 / 规划 F4）：复制 chatMediaTaskPolling
 * 模式（依赖注入 + setTimeout 链）。基础 5s；连续失败指数退避封顶 30s；页面不可见
 * 跳过且不计失败、回到可见立即补一轮；inflight 去重；迟到响应按页面作用域 ID 校验
 * 丢弃；残缺终态快照（completed 未结算 / failed 无错误）不落定，下轮继续轮询。
 */
export interface StudioTaskPollingControl {
  stop(): void
}

export interface StudioTaskPollingDependencies {
  /** 页面作用域 ID（工作台路径标识）：切换页面后旧响应按此丢弃。 */
  scopeId(): string
  /** 当前页面任务集合（服务端快照 + 乐观卡）。 */
  tasks(): StudioTask[]
  /** 应用任务快照（就地更新）。 */
  applySnapshot(task: StudioTask): void
}

const baseIntervalMs = 5_000
const maxIntervalMs = 30_000

export function startStudioTaskPolling(deps: StudioTaskPollingDependencies): StudioTaskPollingControl {
  let stopped = false
  let timer: ReturnType<typeof setTimeout> | undefined
  let consecutiveFailures = 0
  let activeScopeId: string | undefined
  const inflight = new Set<string>()

  function schedule(delay: number): void {
    if (stopped) return
    timer = setTimeout(tick, delay)
  }

  async function tick(): Promise<void> {
    if (stopped) return
    timer = undefined
    // 页面不可见暂停：不发请求、不计失败，回到可见时立即补一轮。
    if (typeof document !== 'undefined' && document.visibilityState !== 'visible') {
      schedule(baseIntervalMs)
      return
    }
    const scopeId = deps.scopeId()
    if (scopeId !== activeScopeId) {
      activeScopeId = scopeId
      consecutiveFailures = 0
      inflight.clear()
    }
    const pending = deps.tasks().filter((task) => isStudioTaskPending(task) && !inflight.has(task.id))
    if (pending.length === 0) {
      schedule(baseIntervalMs)
      return
    }
    let anyFailure = false
    await Promise.all(pending.map(async (task) => {
      inflight.add(task.id)
      try {
        const snapshot = await studioApi.getTask(task.id)
        // 页面已切换的迟到响应丢弃；残缺终态快照不落定（下轮重试结算/错误回填）。
        if (!stopped && deps.scopeId() === scopeId && !isStudioTaskFinalSnapshot(snapshot)) return
        if (!stopped && deps.scopeId() === scopeId) deps.applySnapshot(snapshot)
      } catch {
        anyFailure = true
      } finally {
        inflight.delete(task.id)
      }
    }))
    if (stopped) return
    consecutiveFailures = anyFailure ? consecutiveFailures + 1 : 0
    schedule(nextDelay(consecutiveFailures))
  }

  function nextDelay(failures: number): number {
    return Math.min(maxIntervalMs, baseIntervalMs * 2 ** Math.min(failures, 3))
  }

  function onVisibilityChange(): void {
    if (stopped || document.visibilityState !== 'visible') return
    if (timer !== undefined) { clearTimeout(timer); timer = undefined }
    void tick()
  }
  document.addEventListener('visibilitychange', onVisibilityChange)

  schedule(baseIntervalMs)
  return {
    stop() {
      stopped = true
      if (timer !== undefined) { clearTimeout(timer); timer = undefined }
      document.removeEventListener('visibilitychange', onVisibilityChange)
    }
  }
}
