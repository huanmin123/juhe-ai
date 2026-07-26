import type { ComputedRef } from 'vue'
import { computed, onActivated, onDeactivated, ref } from 'vue'

import { message } from '@/lib/antd'
import {
  accountLabelForId,
  accountSelectionForId,
  accountSelectOptionLabel,
  rememberAccountLabel,
  type AccountSelection
} from '@/shared/accountLabelCache'
import { extractApiErrorMessage } from '@/shared/apiError'
import { createShortLivedQueryCache } from '@/shared/shortLivedQueryCache'
import type { ModelCheckAccountOption, ModelCheckRunPayload } from '@/types/domain'
import {
  canSelectModelCheckAccount,
  canSelectTrustedModelCheckAccount,
  type ModelCheckAccountProfile
} from './modelCheckProviderCapabilities'

type AccountSelectOption = { label: string; value: string }
type SelectValue = string | string[] | undefined

interface ModelCheckAccountOptionsApi {
  options(params: {
    purpose: 'run' | 'history'
    systemAccountId?: string
    keyword?: string
    limit: number
    selectedIds?: string[]
  }): Promise<ModelCheckAccountOption[]>
}

interface UseModelCheckAccountOptionsInput {
  accountsApi: ModelCheckAccountOptionsApi
  modelCheckScopeParams: ComputedRef<{ systemAccountId: string } | undefined>
  form: ModelCheckRunPayload
  knownTargetName: (id: string) => string | undefined
  identityKey: ComputedRef<string>
}

const accountOptionsInFlight = new Map<string, Promise<ModelCheckAccountOption[]>>()

export function useModelCheckAccountOptions(input: UseModelCheckAccountOptionsInput) {
  const targetOptionsLoading = ref(false)
  const comparisonOptionsLoading = ref(false)
  const historyTargetOptionsLoading = ref(false)
  const targetOptions = ref<AccountSelectOption[]>([])
  const comparisonOptions = ref<AccountSelectOption[]>([])
  const historyTargetOptions = ref<AccountSelectOption[]>([])
  const accountProfilesById = ref<Record<string, ModelCheckAccountProfile>>({})
  const targetOptionsCache = createShortLivedQueryCache<AccountSelectOption[]>({ ttlMs: 10_000 })
  const comparisonOptionsCache = createShortLivedQueryCache<AccountSelectOption[]>({ ttlMs: 10_000 })
  const historyTargetOptionsCache = createShortLivedQueryCache<AccountSelectOption[]>({ ttlMs: 10_000 })
  const selectedTargetAccount = ref<AccountSelection>()
  const selectedComparisonAccount = ref<AccountSelection>()
  const selectedHistoryTargetAccount = ref<AccountSelection>()
  let targetOptionsRequestId = 0
  let comparisonOptionsRequestId = 0
  let historyTargetOptionsRequestId = 0
  let active = true
  onActivated(() => { active = true })
  onDeactivated(() => {
    active = false
    targetOptionsRequestId += 1
    comparisonOptionsRequestId += 1
    historyTargetOptionsRequestId += 1
  })
  const selectedTargetAccountProfile = computed(() => accountProfilesById.value[input.form.targetId])
  const selectedComparisonAccountProfile = computed(() => accountProfilesById.value[input.form.trustedComparisonAccountId ?? ''])

  async function loadTargetOptions(keyword = '') {
    const normalizedKeyword = keyword.trim()
    const systemAccountId = input.modelCheckScopeParams.value?.systemAccountId
    const selectedIds = [input.form.targetId].filter(Boolean).sort()
    const requestKey = JSON.stringify(['/account-options', input.identityKey.value, 'run', normalizedKeyword, 50, selectedIds])
    const requestId = ++targetOptionsRequestId
    const cachedOptions = targetOptionsCache.get(requestKey)
    if (cachedOptions) {
      targetOptionsLoading.value = false
      targetOptions.value = cachedOptions
      return
    }
    targetOptionsLoading.value = true
    try {
      const accounts = await loadShared(requestKey, () => input.accountsApi.options({
        purpose: 'run',
        systemAccountId,
        keyword: normalizedKeyword || undefined,
        limit: 50,
        selectedIds
      }))
      rememberAccountProfiles(accounts)
      const nextOptions = accounts
        .filter((account) => canSelectModelCheckAccount(account))
        .map(accountTargetOption)
      targetOptionsCache.set(requestKey, nextOptions)
      if (active && requestId === targetOptionsRequestId && systemAccountId === input.modelCheckScopeParams.value?.systemAccountId) {
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
    const selectedIds = [input.form.targetId, input.form.trustedComparisonAccountId ?? ''].filter(Boolean).sort()
    const requestKey = JSON.stringify(['/account-options', input.identityKey.value, 'run', normalizedKeyword, 50, selectedIds, input.form.targetId, input.form.model])
    const requestId = ++comparisonOptionsRequestId
    const cachedOptions = comparisonOptionsCache.get(requestKey)
    if (cachedOptions) {
      comparisonOptionsLoading.value = false
      comparisonOptions.value = cachedOptions
      return
    }
    comparisonOptionsLoading.value = true
    try {
      const accounts = await loadShared(requestKey, () => input.accountsApi.options({
        purpose: 'run',
        systemAccountId,
        keyword: normalizedKeyword || undefined,
        limit: 50,
        selectedIds
      }))
      rememberAccountProfiles(accounts)
      const nextOptions = accounts
        .filter((account) => canSelectTrustedModelCheckAccount(account, {
          excludedAccountId: input.form.targetId,
          targetAccount: selectedTargetAccountProfile.value,
          model: input.form.model
        }))
        .map(accountTargetOption)
      comparisonOptionsCache.set(requestKey, nextOptions)
      if (active && requestId === comparisonOptionsRequestId && systemAccountId === input.modelCheckScopeParams.value?.systemAccountId) {
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
    const selectedIds = [selectedHistoryTargetAccount.value?.id ?? ''].filter(Boolean).sort()
    const requestKey = JSON.stringify(['/account-options', input.identityKey.value, 'history', normalizedKeyword, 50, selectedIds])
    const requestId = ++historyTargetOptionsRequestId
    const cachedOptions = historyTargetOptionsCache.get(requestKey)
    if (cachedOptions) {
      historyTargetOptionsLoading.value = false
      historyTargetOptions.value = cachedOptions
      return
    }
    historyTargetOptionsLoading.value = true
    try {
      const accounts = await loadShared(requestKey, () => input.accountsApi.options({
        purpose: 'history',
        systemAccountId,
        keyword: normalizedKeyword || undefined,
        limit: 50,
        selectedIds
      }))
      rememberAccountProfiles(accounts)
      const nextOptions = accounts
        .filter((account) => canSelectModelCheckAccount(account))
        .map(accountTargetOption)
      historyTargetOptionsCache.set(requestKey, nextOptions)
      if (active && requestId === historyTargetOptionsRequestId && systemAccountId === input.modelCheckScopeParams.value?.systemAccountId) {
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
    accountProfilesById.value = {}
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

  function accountTargetOption(account: ModelCheckAccountOption) {
    const label = accountSelectOptionLabel(account)
    rememberAccountLabel(account.id, label)
    rememberAccountProfile(account)
    return { label, value: account.id }
  }

  function rememberAccountProfiles(accounts: ModelCheckAccountOption[]) {
    if (!accounts.length) return
    accountProfilesById.value = {
      ...accountProfilesById.value,
      ...Object.fromEntries(accounts.map((account) => [account.id, accountProfile(account)]))
    }
  }

  function rememberAccountProfile(account: ModelCheckAccountOption) {
    accountProfilesById.value = {
      ...accountProfilesById.value,
      [account.id]: accountProfile(account)
    }
  }

  function accountProfile(account: ModelCheckAccountOption): ModelCheckAccountProfile {
    return {
      id: account.id,
      name: account.name,
      providerCode: account.providerCode,
      providerProtocolProfileId: account.providerProtocolProfileId,
      protocolCode: account.protocolCode,
      protocolVersion: account.protocolVersion,
      modelCheckModels: [...account.modelCheckModels]
    }
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
    selectedComparisonAccountProfile,
    selectedHistoryTargetAccount,
    selectedTargetAccount,
    selectedTargetAccountProfile,
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

function loadShared(key: string, loader: () => Promise<ModelCheckAccountOption[]>): Promise<ModelCheckAccountOption[]> {
  const existing = accountOptionsInFlight.get(key)
  if (existing) return existing
  const request = loader().finally(() => {
    if (accountOptionsInFlight.get(key) === request) accountOptionsInFlight.delete(key)
  })
  accountOptionsInFlight.set(key, request)
  return request
}
