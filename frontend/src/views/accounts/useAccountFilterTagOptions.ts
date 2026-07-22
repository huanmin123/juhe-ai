import { computed, onUnmounted, ref, type ComputedRef } from 'vue'

import type { PageDataActivation } from '@/composables/usePageDataActivation'
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
  pageDataActivation?: PageDataActivation
}

export function useAccountFilterTagOptions(config: UseAccountFilterTagOptionsConfig) {
  const options = ref<AccountTagSummary[]>([])
  const loading = ref(false)
  const scopeKey = ref('')
  let requestToken = 0

  const disabled = computed(() => config.isManagementView.value && !config.accountScopeParams.value?.systemAccountId)

  async function load(force = false, revalidate = false): Promise<void> {
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
      const nextOptions = await loadAccountTagOptionsCached({
        activation: config.pageDataActivation,
        force,
        isManagementView: config.isManagementView.value,
        revalidate,
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
    if (open) void load()
  }

  function currentScopeKey(): string | undefined {
    return resolveAccountTagOptionsScopeKey(config.isManagementView.value, config.accountScopeParams.value)
  }

  const unregisterRevalidator = config.pageDataActivation?.registerRevalidator(
    'accounts.options',
    () => load(false, true)
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
