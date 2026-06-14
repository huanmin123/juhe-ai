import type { ComputedRef } from 'vue'
import { ref } from 'vue'

import { message } from '@/lib/antd'
import {
  accountLabelForId,
  accountSelectionForId,
  accountSelectOptionLabel,
  rememberAccountLabel,
  type AccountSelection
} from '@/shared/accountLabelCache'
import { extractApiErrorMessage } from '@/shared/apiError'
import { isGptVendorCode, isOpenAIProtocolProfile } from '@/shared/providerProtocol'
import { createShortLivedQueryCache } from '@/shared/shortLivedQueryCache'
import type { AccountOptionSummary, ModelCheckRunPayload } from '@/types/domain'

type AccountSelectOption = { label: string; value: string }
type SelectValue = string | string[] | undefined

interface ModelCheckAccountOptionsApi {
  options(params: {
    systemAccountId?: string
    keyword?: string
    status?: 'active'
    schedulable?: 'enabled'
    limit?: number
  }): Promise<AccountOptionSummary[]>
}

interface UseModelCheckAccountOptionsInput {
  accountsApi: ModelCheckAccountOptionsApi
  modelCheckScopeParams: ComputedRef<{ systemAccountId: string } | undefined>
  form: ModelCheckRunPayload
  knownTargetName: (id: string) => string | undefined
}

export function useModelCheckAccountOptions(input: UseModelCheckAccountOptionsInput) {
  const targetOptionsLoading = ref(false)
  const comparisonOptionsLoading = ref(false)
  const historyTargetOptionsLoading = ref(false)
  const targetOptions = ref<AccountSelectOption[]>([])
  const comparisonOptions = ref<AccountSelectOption[]>([])
  const historyTargetOptions = ref<AccountSelectOption[]>([])
  const targetOptionsCache = createShortLivedQueryCache<AccountSelectOption[]>({ ttlMs: 10_000 })
  const comparisonOptionsCache = createShortLivedQueryCache<AccountSelectOption[]>({ ttlMs: 10_000 })
  const historyTargetOptionsCache = createShortLivedQueryCache<AccountSelectOption[]>({ ttlMs: 10_000 })
  const selectedTargetAccount = ref<AccountSelection>()
  const selectedComparisonAccount = ref<AccountSelection>()
  const selectedHistoryTargetAccount = ref<AccountSelection>()
  let targetOptionsRequestId = 0
  let comparisonOptionsRequestId = 0
  let historyTargetOptionsRequestId = 0

  async function loadTargetOptions(keyword = '') {
    const normalizedKeyword = keyword.trim()
    const systemAccountId = input.modelCheckScopeParams.value?.systemAccountId
    const requestKey = JSON.stringify([systemAccountId ?? 'self', normalizedKeyword])
    const requestId = ++targetOptionsRequestId
    const cachedOptions = targetOptionsCache.get(requestKey)
    if (cachedOptions) {
      targetOptionsLoading.value = false
      targetOptions.value = cachedOptions
      return
    }
    targetOptionsLoading.value = true
    try {
      const accounts = await input.accountsApi.options({
        systemAccountId,
        keyword: normalizedKeyword || undefined,
        status: 'active',
        schedulable: 'enabled',
        limit: 50
      })
      const nextOptions = accounts
        .filter((account) => isGptVendorCode(account.providerCode) && isOpenAIProtocolProfile(account))
        .filter((account) => Boolean(account.name.trim()))
        .map(accountTargetOption)
      targetOptionsCache.set(requestKey, nextOptions)
      if (requestId === targetOptionsRequestId) {
        targetOptions.value = nextOptions
      }
    } catch (error) {
      console.error(error)
      message.error(extractApiErrorMessage(error, '加载检测目标失败'))
    } finally {
      if (requestId === targetOptionsRequestId) {
        targetOptionsLoading.value = false
      }
    }
  }

  async function loadComparisonOptions(keyword = '') {
    const normalizedKeyword = keyword.trim()
    const systemAccountId = input.modelCheckScopeParams.value?.systemAccountId
    const requestKey = JSON.stringify([systemAccountId ?? 'self', normalizedKeyword, input.form.targetId])
    const requestId = ++comparisonOptionsRequestId
    const cachedOptions = comparisonOptionsCache.get(requestKey)
    if (cachedOptions) {
      comparisonOptionsLoading.value = false
      comparisonOptions.value = cachedOptions
      return
    }
    comparisonOptionsLoading.value = true
    try {
      const accounts = await input.accountsApi.options({
        systemAccountId,
        keyword: normalizedKeyword || undefined,
        status: 'active',
        schedulable: 'enabled',
        limit: 50
      })
      const nextOptions = accounts
        .filter((account) => isGptVendorCode(account.providerCode) && isOpenAIProtocolProfile(account) && account.id !== input.form.targetId)
        .filter((account) => Boolean(account.name.trim()))
        .map(accountTargetOption)
      comparisonOptionsCache.set(requestKey, nextOptions)
      if (requestId === comparisonOptionsRequestId) {
        comparisonOptions.value = nextOptions
      }
    } catch (error) {
      console.error(error)
      message.error(extractApiErrorMessage(error, '加载可信对比账户失败'))
    } finally {
      if (requestId === comparisonOptionsRequestId) {
        comparisonOptionsLoading.value = false
      }
    }
  }

  async function loadHistoryTargetOptions(keyword = '') {
    const normalizedKeyword = keyword.trim()
    const systemAccountId = input.modelCheckScopeParams.value?.systemAccountId
    const requestKey = JSON.stringify([systemAccountId ?? 'self', normalizedKeyword])
    const requestId = ++historyTargetOptionsRequestId
    const cachedOptions = historyTargetOptionsCache.get(requestKey)
    if (cachedOptions) {
      historyTargetOptionsLoading.value = false
      historyTargetOptions.value = cachedOptions
      return
    }
    historyTargetOptionsLoading.value = true
    try {
      const accounts = await input.accountsApi.options({
        systemAccountId,
        keyword: normalizedKeyword || undefined,
        limit: 50
      })
      const nextOptions = accounts
        .filter((account) => isGptVendorCode(account.providerCode) && isOpenAIProtocolProfile(account))
        .filter((account) => Boolean(account.name.trim()))
        .map(accountTargetOption)
      historyTargetOptionsCache.set(requestKey, nextOptions)
      if (requestId === historyTargetOptionsRequestId) {
        historyTargetOptions.value = nextOptions
      }
    } catch (error) {
      console.error(error)
      message.error(extractApiErrorMessage(error, '加载历史账户筛选项失败'))
    } finally {
      if (requestId === historyTargetOptionsRequestId) {
        historyTargetOptionsLoading.value = false
      }
    }
  }

  function resetAccountOptionsState() {
    resetRunAccountSelection()
    selectedHistoryTargetAccount.value = undefined
    targetOptions.value = []
    comparisonOptions.value = []
    historyTargetOptions.value = []
    targetOptionsLoading.value = false
    comparisonOptionsLoading.value = false
    historyTargetOptionsLoading.value = false
    targetOptionsCache.clear()
    comparisonOptionsCache.clear()
    historyTargetOptionsCache.clear()
    targetOptionsRequestId += 1
    comparisonOptionsRequestId += 1
    historyTargetOptionsRequestId += 1
  }

  function resetRunAccountSelection() {
    input.form.targetId = ''
    selectedTargetAccount.value = undefined
    input.form.trustedComparison = false
    input.form.trustedComparisonAccountId = undefined
    selectedComparisonAccount.value = undefined
  }

  function handleTargetSearch(value: string) {
    void loadTargetOptions(value)
  }

  function handleTargetValueUpdate(value: SelectValue) {
    input.form.targetId = typeof value === 'string' ? value : ''
    selectedTargetAccount.value = selectedAccountForId(input.form.targetId, targetOptions.value)
  }

  function handleTargetChange() {
    if (input.form.trustedComparisonAccountId && input.form.trustedComparisonAccountId === input.form.targetId) {
      input.form.trustedComparisonAccountId = undefined
      selectedComparisonAccount.value = undefined
    }
    comparisonOptions.value = comparisonOptions.value.filter((item) => item.value !== input.form.targetId)
  }

  function handleTargetDropdownVisibleChange(open: boolean) {
    if (open && !targetOptions.value.length) {
      void loadTargetOptions()
    }
  }

  function handleComparisonSearch(value: string) {
    void loadComparisonOptions(value)
  }

  function handleComparisonDropdownVisibleChange(open: boolean) {
    if (open && !comparisonOptions.value.length) {
      void loadComparisonOptions()
    }
  }

  function handleHistoryTargetSearch(value: string) {
    void loadHistoryTargetOptions(value)
  }

  function handleHistoryTargetDropdownVisibleChange(open: boolean) {
    if (open && !historyTargetOptions.value.length) {
      void loadHistoryTargetOptions()
    }
  }

  function targetOptionText(id: string) {
    return selectedTargetAccount.value?.id === id
      ? selectedTargetAccount.value.name
      : accountNameForId(id, targetOptions.value) ?? '未记录账户名称'
  }

  function comparisonOptionText(id: string) {
    return selectedComparisonAccount.value?.id === id
      ? selectedComparisonAccount.value.name
      : accountNameForId(id, comparisonOptions.value) ?? '未记录账户名称'
  }

  function selectValueOrUndefined(value?: string) {
    return value?.trim() || undefined
  }

  function accountTargetOption(account: AccountOptionSummary) {
    const label = accountSelectOptionLabel(account)
    rememberAccountLabel(account.id, label)
    return { label, value: account.id }
  }

  function selectedAccountForId(id: string | undefined, options: AccountSelectOption[]): AccountSelection | undefined {
    return accountSelectionForId(id, [], options)
  }

  function accountNameForId(id: string, options: AccountSelectOption[]): string | undefined {
    return selectedAccountForId(id, options)?.name || input.knownTargetName(id) || accountLabelForId(id)
  }

  return {
    comparisonOptions,
    comparisonOptionsLoading,
    historyTargetOptions,
    historyTargetOptionsLoading,
    selectedComparisonAccount,
    selectedHistoryTargetAccount,
    selectedTargetAccount,
    targetOptions,
    targetOptionsLoading,
    handleComparisonDropdownVisibleChange,
    handleComparisonSearch,
    handleHistoryTargetDropdownVisibleChange,
    handleHistoryTargetSearch,
    handleTargetChange,
    handleTargetDropdownVisibleChange,
    handleTargetSearch,
    handleTargetValueUpdate,
    loadComparisonOptions,
    loadHistoryTargetOptions,
    loadTargetOptions,
    resetAccountOptionsState,
    resetRunAccountSelection,
    comparisonOptionText,
    selectValueOrUndefined,
    targetOptionText
  }
}
