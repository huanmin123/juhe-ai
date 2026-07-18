import { onMounted, onUnmounted, readonly, shallowRef, type DeepReadonly, type ShallowRef } from 'vue'

import {
  PageDataCacheController,
  PageDataVisibleConfirmScheduler,
  getDefaultPageDataTabCoordinator,
  type PageDataCacheControllerOptions,
  type PageDataConfirmOutcome,
  type PageDataLoadResult
} from '@/shared/pageDataCache'

export interface UsePageDataCacheOptions<T> extends PageDataCacheControllerOptions<T> {
  immediate?: boolean
  initialData?: T
  confirmIntervalMs?: number
}

export interface UsePageDataCacheResult<T> {
  data: DeepReadonly<ShallowRef<T | undefined>>
  loading: DeepReadonly<ShallowRef<boolean>>
  error: DeepReadonly<ShallowRef<unknown>>
  load: () => Promise<PageDataLoadResult<T>>
  forceRefresh: () => Promise<PageDataLoadResult<T>>
  confirm: () => Promise<PageDataConfirmOutcome<T>>
}

export function usePageDataCache<T>(options: UsePageDataCacheOptions<T>): UsePageDataCacheResult<T> {
  const data = shallowRef<T | undefined>(options.initialData)
  const loading = shallowRef(false)
  const error = shallowRef<unknown>()
  const controller = new PageDataCacheController<T>({
    ...options,
    tabCoordinator: options.tabCoordinator ?? getDefaultPageDataTabCoordinator()
  })
  const applyConfirmOutcome = (outcome: PageDataConfirmOutcome<T>): PageDataConfirmOutcome<T> => {
    if (!disposed && (outcome.state === 'updated' || outcome.state === 'unchanged' || outcome.state === 'unavailable')) {
      data.value = outcome.data
    }
    return outcome
  }
  const confirmCurrent = () => controller.requestConfirm().then(applyConfirmOutcome)
  const scheduler = new PageDataVisibleConfirmScheduler({
    confirm: () => { void confirmCurrent() },
    intervalMs: options.confirmIntervalMs
  })
  let operationGeneration = 0
  let removeSubscription: (() => void) | undefined
  let disposed = false

  const applyResult = (result: PageDataLoadResult<T>): PageDataLoadResult<T> => {
    if (!disposed) data.value = result.data
    return result
  }

  const run = async (operation: () => Promise<PageDataLoadResult<T>>): Promise<PageDataLoadResult<T>> => {
    const generation = ++operationGeneration
    loading.value = true
    error.value = undefined
    try {
      const result = await operation()
      return generation === operationGeneration ? applyResult(result) : { ...result, superseded: true }
    } catch (nextError) {
      if (!disposed && generation === operationGeneration) error.value = nextError
      throw nextError
    } finally {
      if (!disposed && generation === operationGeneration) loading.value = false
    }
  }

  const load = () => run(() => controller.load())
  const forceRefresh = () => run(() => controller.refresh())

  onMounted(() => {
    removeSubscription = controller.subscribe((record) => {
      if (!disposed) data.value = record?.value
    })
    scheduler.start()
    if (options.immediate !== false) void load().catch(() => undefined)
  })

  onUnmounted(() => {
    disposed = true
    operationGeneration += 1
    scheduler.stop()
    removeSubscription?.()
    removeSubscription = undefined
    controller.close()
  })

  return {
    data: readonly(data),
    loading: readonly(loading),
    error: readonly(error),
    load,
    forceRefresh,
    confirm: confirmCurrent
  }
}
