import { computed, ref, type ComputedRef } from 'vue'

import { message } from '@/lib/antd'
import { extractApiErrorMessage } from '@/shared/apiError'
import type { AccountTagSummary } from '@/types/domain'
import type { AccountScopeParams } from './accountOperationScope'
import {
  loadAccountTagOptionsCached,
  readAccountTagOptionsCache,
  resolveAccountTagOptionsScopeKey
} from './accountTagOptionsCache'

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
    const cachedOptions = !force ? readAccountTagOptionsCache(nextScopeKey) : undefined
    if (cachedOptions) {
      options.value = cachedOptions
      scopeKey.value = nextScopeKey
      loading.value = false
      return
    }

    const currentRequestToken = ++requestToken
    const scopeParams = config.accountScopeParams.value
    loading.value = true
    try {
      const nextOptions = await loadAccountTagOptionsCached({
        force,
        isManagementView: config.isManagementView.value,
        scopeParams
      })
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
    return resolveAccountTagOptionsScopeKey(config.isManagementView.value, config.accountScopeParams.value)
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
