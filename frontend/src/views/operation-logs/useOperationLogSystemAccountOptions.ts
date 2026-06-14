import type { ComputedRef, Ref } from 'vue'

import { useRemoteSystemAccountOptions } from '@/composables/useRemoteSystemAccountOptions'

interface UseOperationLogSystemAccountOptionsConfig {
  actorSystemAccountFilter: Ref<string>
  affectedSystemAccountFilter: Ref<string>
  isManagementView: ComputedRef<boolean>
  operationScopeSystemAccountFilter: Ref<string>
}

export function useOperationLogSystemAccountOptions(config: UseOperationLogSystemAccountOptionsConfig) {
  const actor = useRemoteSystemAccountOptions({
    enabled: () => config.isManagementView.value,
    selectedIds: () => [config.actorSystemAccountFilter.value]
  })
  const affected = useRemoteSystemAccountOptions({
    enabled: () => config.isManagementView.value,
    selectedIds: () => [config.affectedSystemAccountFilter.value]
  })
  const operationScope = useRemoteSystemAccountOptions({
    enabled: () => config.isManagementView.value,
    selectedIds: () => [config.operationScopeSystemAccountFilter.value]
  })

  function resetAllSearches(): void {
    actor.resetSearch()
    affected.resetSearch()
    operationScope.resetSearch()
  }

  return {
    actor,
    affected,
    operationScope,
    resetAllSearches
  }
}
