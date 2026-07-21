import { onActivated, onDeactivated, onMounted, onUnmounted, readonly, shallowRef, type DeepReadonly, type ShallowRef } from 'vue'

import type { PageDataConfirmRequest, PageDataConfirmResult } from '@/api/domains/pageData'
import {
  PageDataRequestCacheManager,
  PageDataVisibleConfirmScheduler,
  createPageDataActivationController,
  getDefaultPageDataTabCoordinator,
  type PageDataCacheStorage,
  type PageDataConfirmOutcome,
  type PageDataLoadResult,
  type PageDataRequestCacheManagerOptions,
  type PageDataRequestCacheDefinition,
  type PageDataTabCoordinator
} from '@/shared/pageDataCache'

export interface UsePageDataRequestCacheOptions<T> {
  resolveRequest: () => PageDataRequestCacheDefinition<T>
  confirm: (request: PageDataConfirmRequest) => Promise<PageDataConfirmResult>
  confirmBatchKey?: string
  storage?: PageDataCacheStorage
  tabCoordinator?: PageDataTabCoordinator
  now?: () => Date
  immediate?: boolean
  initialData?: T
  confirmIntervalMs?: number
  activation?: PageDataRequestCacheManagerOptions['activation']
  writeEpoch?: PageDataRequestCacheManagerOptions['writeEpoch']
  activationManaged?: boolean
}

export interface UsePageDataRequestCacheResult<T> {
  data: DeepReadonly<ShallowRef<T | undefined>>
  loading: DeepReadonly<ShallowRef<boolean>>
  error: DeepReadonly<ShallowRef<unknown>>
  currentKey: () => string | undefined
  load: () => Promise<PageDataLoadResult<T>>
  forceRefresh: () => Promise<PageDataLoadResult<T>>
  confirm: () => Promise<PageDataConfirmOutcome<T>>
}

export function usePageDataRequestCache<T>(options: UsePageDataRequestCacheOptions<T>): UsePageDataRequestCacheResult<T> {
  const data = shallowRef<T | undefined>(options.initialData)
  const loading = shallowRef(false)
  const error = shallowRef<unknown>()
  const manager = new PageDataRequestCacheManager<T>({
    confirm: options.confirm,
    confirmBatchKey: options.confirmBatchKey,
    storage: options.storage,
    tabCoordinator: options.tabCoordinator ?? getDefaultPageDataTabCoordinator(),
    now: options.now,
    activation: options.activation,
    writeEpoch: options.writeEpoch
  })
  const applyConfirmOutcome = (outcome: PageDataConfirmOutcome<T>): PageDataConfirmOutcome<T> => {
    if (!disposed && (outcome.state === 'updated' || outcome.state === 'unchanged' || outcome.state === 'unavailable')) {
      data.value = outcome.data
    }
    return outcome
  }
  const confirmCurrent = () => manager.confirmCurrent().then(applyConfirmOutcome)
  let scheduler: PageDataVisibleConfirmScheduler | undefined
  if (!options.activationManaged) {
    scheduler = new PageDataVisibleConfirmScheduler({
      confirm: () => { void confirmCurrent() },
      intervalMs: options.confirmIntervalMs
    })
  }
  const activationController = createPageDataActivationController({
    start: () => { if (scheduler) scheduler.start() },
    stop: () => { if (scheduler) scheduler.stop() },
    onActivate: () => {
      if (!options.activationManaged) void confirmCurrent().catch(() => undefined)
    }
  })
  let operationGeneration = 0
  let removeSubscription: (() => void) | undefined
  let disposed = false

  const run = async (operation: (request: PageDataRequestCacheDefinition<T>) => Promise<PageDataLoadResult<T>>): Promise<PageDataLoadResult<T>> => {
    const generation = ++operationGeneration
    const request = options.resolveRequest()
    loading.value = true
    error.value = undefined
    try {
      const result = await operation(request)
      if (!disposed && generation === operationGeneration && !result.superseded) data.value = result.data
      return generation === operationGeneration ? result : { ...result, superseded: true }
    } catch (nextError) {
      if (!disposed && generation === operationGeneration) error.value = nextError
      throw nextError
    } finally {
      if (!disposed && generation === operationGeneration) loading.value = false
    }
  }

  const load = () => run((request) => manager.load(request))
  const forceRefresh = () => run((request) => manager.forceRefresh(request))

  onMounted(() => {
    removeSubscription = manager.subscribe((record) => {
      if (!disposed) data.value = record?.value
    })
    activationController.mount()
    if (options.immediate !== false) void load().catch(() => undefined)
  })

  onActivated(() => activationController.activate())
  onDeactivated(() => activationController.deactivate())

  onUnmounted(() => {
    disposed = true
    operationGeneration += 1
    activationController.dispose()
    removeSubscription?.()
    removeSubscription = undefined
    manager.close()
  })

  return {
    data: readonly(data),
    loading: readonly(loading),
    error: readonly(error),
    currentKey: () => manager.currentKey,
    load,
    forceRefresh,
    confirm: confirmCurrent
  }
}
