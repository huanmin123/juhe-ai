import type { ComputedRef } from 'vue'
import { computed, onActivated, onBeforeUnmount, onDeactivated, ref } from 'vue'

import { message } from '@/lib/antd'
import {
  accountLabelForId,
  accountSelectionForId,
  accountSelectOptionLabel,
  rememberAccountLabel,
  type AccountSelection
} from '@/shared/accountLabelCache'
import { extractApiErrorMessage } from '@/shared/apiError'
import type { ModelCheckAccountOption, ModelCheckRunPayload } from '@/types/domain'
import {
  canSelectModelCheckAccount,
  canSelectTrustedModelCheckAccount,
  type ModelCheckAccountProfile
} from './modelCheckProviderCapabilities'
import { createModelCheckDemandRequestCoordinator } from './modelCheckDemandRequestCoordinator'

type AccountSelectOption = { label: string; value: string }
type SelectValue = string | string[] | undefined

interface ModelCheckAccountOptionsApi {
  options(params: {
    purpose: 'run' | 'history'
    accountId?: string
    systemAccountId?: string
    keyword?: string
    limit: number
    selectedIds?: string[]
  }, options?: { signal?: AbortSignal }): Promise<ModelCheckAccountOption[]>
}

interface UseModelCheckAccountOptionsInput {
  accountsApi: ModelCheckAccountOptionsApi
  modelCheckScopeParams: ComputedRef<{ systemAccountId: string } | undefined>
  form: ModelCheckRunPayload
  knownTargetName: (id: string) => string | undefined
  identityKey: ComputedRef<string>
}

export function useModelCheckAccountOptions(input: UseModelCheckAccountOptionsInput) {
  const targetOptionsLoading = ref(false)
  const comparisonOptionsLoading = ref(false)
  const historyTargetOptionsLoading = ref(false)
  const targetModelOptionsLoading = ref(false)
  const targetOptions = ref<AccountSelectOption[]>([])
  const comparisonOptions = ref<AccountSelectOption[]>([])
  const historyTargetOptions = ref<AccountSelectOption[]>([])
  const accountProfilesById = ref<Record<string, ModelCheckAccountProfile>>({})
  const selectedTargetAccount = ref<AccountSelection>()
  const selectedComparisonAccount = ref<AccountSelection>()
  const selectedHistoryTargetAccount = ref<AccountSelection>()
  let targetOptionsRequestId = 0
  let comparisonOptionsRequestId = 0
  let historyTargetOptionsRequestId = 0
  let targetSearchTimer: ReturnType<typeof setTimeout> | undefined
  let comparisonSearchTimer: ReturnType<typeof setTimeout> | undefined
  let historySearchTimer: ReturnType<typeof setTimeout> | undefined
  let targetAbortController: AbortController | undefined
  let comparisonAbortController: AbortController | undefined
  let historyAbortController: AbortController | undefined
  let active = true
  const targetModelRequestCoordinator = createModelCheckDemandRequestCoordinator()
  onActivated(() => { active = true })
  onDeactivated(invalidateAccountOptionRequests)
  onBeforeUnmount(invalidateAccountOptionRequests)

  function invalidateAccountOptionRequests() {
    active = false
    clearSearchTimers()
    targetAbortController?.abort()
    comparisonAbortController?.abort()
    historyAbortController?.abort()
    targetModelRequestCoordinator.invalidate()
    targetAbortController = undefined
    comparisonAbortController = undefined
    historyAbortController = undefined
    targetOptionsRequestId += 1
    comparisonOptionsRequestId += 1
    historyTargetOptionsRequestId += 1
    targetModelOptionsLoading.value = false
  }
  const selectedTargetAccountProfile = computed(() => accountProfilesById.value[input.form.targetId])
  const selectedComparisonAccountProfile = computed(() => accountProfilesById.value[input.form.trustedComparisonAccountId ?? ''])

  async function loadTargetOptions(keyword = '') {
    const normalizedKeyword = keyword.trim()
    const systemAccountId = input.modelCheckScopeParams.value?.systemAccountId
    const identityKey = input.identityKey.value
    const selectedIds = [input.form.targetId].filter(Boolean).sort()
    const requestId = ++targetOptionsRequestId
    targetAbortController?.abort()
    const controller = new AbortController()
    targetAbortController = controller
    targetOptionsLoading.value = true
    try {
      const accounts = await input.accountsApi.options({
        purpose: 'run',
        systemAccountId,
        keyword: normalizedKeyword || undefined,
        limit: 50,
        selectedIds
      }, { signal: controller.signal })
      if (!isCurrentAccountOptionRequest(requestId, targetOptionsRequestId, systemAccountId, identityKey)) return
      rememberAccountProfiles(accounts)
      const nextOptions = accounts
        .filter((account) => canSelectModelCheckAccount(account))
        .map(accountTargetOption)
      targetOptions.value = nextOptions
    } catch (error) {
      if (!isCurrentAccountOptionRequest(requestId, targetOptionsRequestId, systemAccountId, identityKey)) return
      if (controller.signal.aborted) return
      console.error(error)
      message.error(extractApiErrorMessage(error, '加载检测目标失败'))
    } finally {
      if (requestId === targetOptionsRequestId) {
        targetOptionsLoading.value = false
        if (targetAbortController === controller) targetAbortController = undefined
      }
    }
  }

  async function loadComparisonOptions(keyword = '') {
    const normalizedKeyword = keyword.trim()
    const systemAccountId = input.modelCheckScopeParams.value?.systemAccountId
    const identityKey = input.identityKey.value
    const targetId = input.form.targetId
    const model = input.form.model
    const selectedIds = [input.form.targetId, input.form.trustedComparisonAccountId ?? ''].filter(Boolean).sort()
    const requestId = ++comparisonOptionsRequestId
    comparisonAbortController?.abort()
    const controller = new AbortController()
    comparisonAbortController = controller
    comparisonOptionsLoading.value = true
    try {
      const accounts = await input.accountsApi.options({
        purpose: 'run',
        systemAccountId,
        keyword: normalizedKeyword || undefined,
        limit: 50,
        selectedIds
      }, { signal: controller.signal })
      if (!isCurrentAccountOptionRequest(requestId, comparisonOptionsRequestId, systemAccountId, identityKey)
        || targetId !== input.form.targetId
        || model !== input.form.model) return
      rememberAccountProfiles(accounts)
      const nextOptions = accounts
        .filter((account) => canSelectTrustedModelCheckAccount(account, {
          excludedAccountId: targetId,
          targetAccount: selectedTargetAccountProfile.value,
          model
        }))
        .map(accountTargetOption)
      comparisonOptions.value = nextOptions
    } catch (error) {
      if (!isCurrentAccountOptionRequest(requestId, comparisonOptionsRequestId, systemAccountId, identityKey)
        || targetId !== input.form.targetId
        || model !== input.form.model) return
      if (controller.signal.aborted) return
      console.error(error)
      message.error(extractApiErrorMessage(error, '加载可信对比账户失败'))
    } finally {
      if (requestId === comparisonOptionsRequestId) {
        comparisonOptionsLoading.value = false
        if (comparisonAbortController === controller) comparisonAbortController = undefined
      }
    }
  }

  async function loadHistoryTargetOptions(keyword = '') {
    const normalizedKeyword = keyword.trim()
    const systemAccountId = input.modelCheckScopeParams.value?.systemAccountId
    const identityKey = input.identityKey.value
    const selectedHistoryTargetId = selectedHistoryTargetAccount.value?.id
    const selectedIds = [selectedHistoryTargetId ?? ''].filter(Boolean).sort()
    const requestId = ++historyTargetOptionsRequestId
    historyAbortController?.abort()
    const controller = new AbortController()
    historyAbortController = controller
    historyTargetOptionsLoading.value = true
    try {
      const accounts = await input.accountsApi.options({
        purpose: 'history',
        systemAccountId,
        keyword: normalizedKeyword || undefined,
        limit: 50,
        selectedIds
      }, { signal: controller.signal })
      if (!isCurrentAccountOptionRequest(requestId, historyTargetOptionsRequestId, systemAccountId, identityKey)
        || selectedHistoryTargetId !== selectedHistoryTargetAccount.value?.id) return
      rememberAccountProfiles(accounts)
      const nextOptions = accounts
        .filter((account) => canSelectModelCheckAccount(account))
        .map(accountTargetOption)
      historyTargetOptions.value = nextOptions
    } catch (error) {
      if (!isCurrentAccountOptionRequest(requestId, historyTargetOptionsRequestId, systemAccountId, identityKey)
        || selectedHistoryTargetId !== selectedHistoryTargetAccount.value?.id) return
      if (controller.signal.aborted) return
      console.error(error)
      message.error(extractApiErrorMessage(error, '加载历史账户筛选项失败'))
    } finally {
      if (requestId === historyTargetOptionsRequestId) {
        historyTargetOptionsLoading.value = false
        if (historyAbortController === controller) historyAbortController = undefined
      }
    }
  }

  async function loadTargetModelOptions(): Promise<void> {
    const accountId = input.form.targetId.trim()
    if (!accountId || accountProfilesById.value[accountId]?.modelCheckModels !== undefined) return
    const systemAccountId = input.modelCheckScopeParams.value?.systemAccountId
    const identityKey = input.identityKey.value
    const requestKey = JSON.stringify([identityKey, systemAccountId ?? '', 'run', accountId])
    targetModelOptionsLoading.value = true
    try {
      const items = await targetModelRequestCoordinator.run(requestKey, (signal) => input.accountsApi.options({
        purpose: 'run',
        systemAccountId,
        accountId,
        limit: 1
      }, { signal }))
      if (!items
        || accountId !== input.form.targetId.trim()
        || identityKey !== input.identityKey.value
        || systemAccountId !== input.modelCheckScopeParams.value?.systemAccountId) return
      const account = items.find((item) => item.id === accountId)
      if (!account) throw new Error('当前检测账户不可用')
      rememberAccountProfile(account)
    } catch (error) {
      if (isAbortError(error)) return
      if (accountId !== input.form.targetId.trim() || identityKey !== input.identityKey.value) return
      console.error(error)
      message.error(extractApiErrorMessage(error, '加载检查模型失败'))
    } finally {
      if (accountId === input.form.targetId.trim() && identityKey === input.identityKey.value) {
        targetModelOptionsLoading.value = false
      }
    }
  }

  function resetAccountOptionsState() {
    clearSearchTimers()
    targetAbortController?.abort()
    comparisonAbortController?.abort()
    historyAbortController?.abort()
    targetModelRequestCoordinator.invalidate()
    targetAbortController = undefined
    comparisonAbortController = undefined
    historyAbortController = undefined
    resetRunAccountSelection()
    selectedHistoryTargetAccount.value = undefined
    targetOptions.value = []
    comparisonOptions.value = []
    historyTargetOptions.value = []
    accountProfilesById.value = {}
    targetOptionsLoading.value = false
    comparisonOptionsLoading.value = false
    historyTargetOptionsLoading.value = false
    targetModelOptionsLoading.value = false
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

  function isCurrentAccountOptionRequest(requestId: number, latestRequestId: number, systemAccountId: string | undefined, identityKey: string) {
    return active
      && requestId === latestRequestId
      && systemAccountId === input.modelCheckScopeParams.value?.systemAccountId
      && identityKey === input.identityKey.value
  }

  function handleTargetSearch(value: string) {
    clearTimeout(targetSearchTimer)
    targetSearchTimer = setTimeout(() => void loadTargetOptions(value), 250)
  }

  function handleTargetValueUpdate(value: SelectValue) {
    const previousTargetId = input.form.targetId
    input.form.targetId = typeof value === 'string' ? value : ''
    if (previousTargetId !== input.form.targetId) {
      targetModelRequestCoordinator.invalidate()
      targetModelOptionsLoading.value = false
    }
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
    if (open) {
      clearTimeout(targetSearchTimer)
      void loadTargetOptions()
    }
  }

  function handleComparisonSearch(value: string) {
    clearTimeout(comparisonSearchTimer)
    comparisonSearchTimer = setTimeout(() => void loadComparisonOptions(value), 250)
  }

  function handleComparisonDropdownVisibleChange(open: boolean) {
    if (open) {
      clearTimeout(comparisonSearchTimer)
      void loadComparisonOptions()
    }
  }

  function handleHistoryTargetSearch(value: string) {
    clearTimeout(historySearchTimer)
    historySearchTimer = setTimeout(() => void loadHistoryTargetOptions(value), 250)
  }

  function handleHistoryTargetDropdownVisibleChange(open: boolean) {
    if (open) {
      clearTimeout(historySearchTimer)
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
      ...Object.fromEntries(accounts.map((account) => [account.id, accountProfile(account, accountProfilesById.value[account.id])]))
    }
  }

  function rememberAccountProfile(account: ModelCheckAccountOption) {
    accountProfilesById.value = {
      ...accountProfilesById.value,
      [account.id]: accountProfile(account, accountProfilesById.value[account.id])
    }
  }

  function accountProfile(account: ModelCheckAccountOption, existing?: ModelCheckAccountProfile): ModelCheckAccountProfile {
    const profile: ModelCheckAccountProfile = {
      id: account.id,
      name: account.name,
      providerCode: account.providerCode,
      providerProtocolProfileId: account.providerProtocolProfileId,
      protocolCode: account.protocolCode,
      protocolVersion: account.protocolVersion
    }
    const models = account.modelCheckModels ?? existing?.modelCheckModels
    if (models !== undefined) profile.modelCheckModels = [...models]
    return profile
  }

  function isAbortError(error: unknown): boolean {
    return error instanceof DOMException && error.name === 'AbortError'
  }

  function selectedAccountForId(id: string | undefined, options: AccountSelectOption[]): AccountSelection | undefined {
    return accountSelectionForId(id, [], options)
  }

  function accountNameForId(id: string, options: AccountSelectOption[]): string | undefined {
    return selectedAccountForId(id, options)?.name || input.knownTargetName(id) || accountLabelForId(id)
  }

  function clearSearchTimers(): void {
    clearTimeout(targetSearchTimer)
    clearTimeout(comparisonSearchTimer)
    clearTimeout(historySearchTimer)
    targetSearchTimer = undefined
    comparisonSearchTimer = undefined
    historySearchTimer = undefined
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
    targetModelOptionsLoading,
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
    loadTargetModelOptions,
    resetAccountOptionsState,
    resetRunAccountSelection,
    comparisonOptionText,
    selectValueOrUndefined,
    targetOptionText
  }
}
