import { computed, onUnmounted, ref, type ComputedRef } from 'vue'

import type { PageDataActivation } from '@/composables/usePageDataActivation'
import { message } from '@/lib/antd'
import type { PageDataActivationHandle } from '@/shared/pageDataActivationCoordinator'
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
  pageDataActivation?: PageDataActivation
}

export function useAccountFilterTagOptions(config: UseAccountFilterTagOptionsConfig) {
  const options = ref<AccountTagSummary[]>([])
  const loading = ref(false)
  const scopeKey = ref('')
  let requestToken = 0

  const disabled = computed(() => config.isManagementView.value && !config.accountScopeParams.value?.systemAccountId)

  async function load(
    force = false,
    revalidate = false,
    activation: PageDataActivationHandle | undefined = config.pageDataActivation
  ): Promise<void> {
    const nextScopeKey = currentScopeKey()
    if (!nextScopeKey) {
      reset()
      return
    }
    if (!force && !revalidate && scopeKey.value === nextScopeKey) return
    const cachedOptions = !force && !revalidate ? readAccountTagOptionsCache(nextScopeKey) : undefined
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
      const result = await loadAccountTagOptionsCached({
        activation,
        force,
        isManagementView: config.isManagementView.value,
        revalidate,
        scopeParams
      })
      if (result.superseded || currentRequestToken !== requestToken || currentScopeKey() !== nextScopeKey) return
      options.value = result.data
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
    if (open) void load()
  }

  function currentScopeKey(): string | undefined {
    return resolveAccountTagOptionsScopeKey(config.isManagementView.value, config.accountScopeParams.value)
  }

  const unregisterRevalidator = config.pageDataActivation?.registerRevalidator(
    'accounts.options',
    (activation) => load(false, true, activation)
  )
  onUnmounted(() => unregisterRevalidator?.())

  return {
    disabled,
    handleDropdown,
    load,
    loading,
    options,
    reset
  }
}
