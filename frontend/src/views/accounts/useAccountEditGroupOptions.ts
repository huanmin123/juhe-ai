import { ref, type ComputedRef } from 'vue'

import type { AccountScopeParams } from './accountOperationScope'
import { useAccountGroupOptions, type AccountGroupOptionsScope } from './useAccountGroupOptions'

interface UseAccountEditGroupOptionsConfig {
  accountScopeParams: ComputedRef<AccountScopeParams>
  isManagementView: ComputedRef<boolean>
}

export function useAccountEditGroupOptions(config: UseAccountEditGroupOptionsConfig) {
  const optionScope = ref<AccountGroupOptionsScope>({})
  const groupOptions = useAccountGroupOptions({
    isManagementView: () => config.isManagementView.value,
    scope: () => ({
      providerCode: optionScope.value.providerCode,
      systemAccountId: config.isManagementView.value
        ? optionScope.value.systemAccountId || config.accountScopeParams.value?.systemAccountId
        : undefined,
      selectedIds: optionScope.value.selectedIds
    })
  })

  function setEditGroupOptionScope(scope: AccountGroupOptionsScope): void {
    const providerChanged = (optionScope.value.providerCode ?? '') !== (scope.providerCode ?? '')
    const systemAccountChanged = (optionScope.value.systemAccountId ?? '') !== (scope.systemAccountId ?? '')
    optionScope.value = {
      providerCode: scope.providerCode,
      systemAccountId: scope.systemAccountId,
      selectedIds: scope.selectedIds
    }
    if (providerChanged || systemAccountChanged) {
      groupOptions.resetSearch()
    }
  }

  return {
    ...groupOptions,
    setEditGroupOptionScope
  }
}
