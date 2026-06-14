import { computed, ref, type ComputedRef } from 'vue'

import { api } from '@/api/client'
import { message } from '@/lib/antd'
import { extractApiErrorMessage } from '@/shared/apiError'
import type { AccountTagSummary } from '@/types/domain'
import type { AccountScopeParams } from './accountOperationScope'

interface UseAccountFilterTagOptionsConfig {
  accountScopeParams: ComputedRef<AccountScopeParams>
  isManagementView: ComputedRef<boolean>
}

export function useAccountFilterTagOptions(config: UseAccountFilterTagOptionsConfig) {
  const options = ref<AccountTagSummary[]>([])
  const loading = ref(false)
  const scopeKey = ref('')
  let requestToken = 0

  const disabled = computed(() => config.isManagementView.value && !config.accountScopeParams.value?.systemAccountId)

  async function load(force = false): Promise<void> {
    const nextScopeKey = currentScopeKey()
    if (!nextScopeKey) {
      reset()
      return
    }
    if (!force && scopeKey.value === nextScopeKey) return

    const currentRequestToken = ++requestToken
    const scopeParams = config.accountScopeParams.value
    loading.value = true
    try {
      const nextOptions = config.isManagementView.value
        ? await api.accounts.tags(scopeParams)
        : await api.myAccounts.tags()
      if (currentRequestToken !== requestToken || currentScopeKey() !== nextScopeKey) return
      options.value = nextOptions
      scopeKey.value = nextScopeKey
    } catch (error) {
      console.error(error)
      message.error(extractApiErrorMessage(error, '加载账户标签失败'))
    } finally {
      if (currentRequestToken === requestToken) {
        loading.value = false
      }
    }
  }

  function reset(): void {
    requestToken += 1
    options.value = []
    scopeKey.value = ''
    loading.value = false
  }

  function handleDropdown(open: boolean): void {
    if (open) void load(true)
  }

  function currentScopeKey(): string | undefined {
    if (!config.isManagementView.value) return 'self'
    const systemAccountId = config.accountScopeParams.value?.systemAccountId
    return systemAccountId ? `management:${systemAccountId}` : undefined
  }

  return {
    disabled,
    handleDropdown,
    load,
    loading,
    options,
    reset
  }
}
