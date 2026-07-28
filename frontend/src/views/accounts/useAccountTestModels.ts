import { computed, ref, type ComputedRef } from 'vue'

import type {
  AccountSupportedEndpointMode,
  AccountListItem
} from '@/types/domain'
import type {
  AccountTestModelCapabilities,
  AccountTestModelOption,
  AccountTestOptions
} from '@/api/domains/accounts'
import { api } from '@/api/client'
import { extractApiErrorMessage } from '@/shared/apiError'
import { accountOperationScopeParams } from './accountOperationScope'
import type { AccountTestEndpointMode, AccountTestForm } from './accountTestFlow'
import { isAbortError } from './accountTestTaskHelpers'

type UseAccountTestModelsInput = {
  accountScopeParams: ComputedRef<{ systemAccountId: string } | undefined>
  isManagementView: ComputedRef<boolean>
  testForm: AccountTestForm
}

export function useAccountTestModels(input: UseAccountTestModelsInput) {
  const testModelOptions = ref<AccountTestModelOption[]>([])
  const testModelOptionsLoading = ref(false)
  const testModelsLoading = computed(() => testModelOptionsLoading.value)
  const testModelsError = ref('')
  const testModelReadonly = ref(false)
  const testEndpointModes = ref<AccountSupportedEndpointMode[]>([])
  const testEndpointModesError = ref('')
  const testEndpointModesLoading = ref(false)
  let selectedAccount: AccountListItem | undefined
  let loadedOptionsAccountKey = ''
  let activeOptionsRequestKey = ''
  let activeModelCapabilitiesRequestKey = ''
  let defaultModel = ''
  let optionsAbortController: AbortController | undefined
  let modelCapabilitiesAbortController: AbortController | undefined
  let optionsRequestToken = 0
  let modelCapabilitiesRequestToken = 0
  const modelCapabilitiesCache = new Map<string, AccountTestModelCapabilities>()

  function initializeSavedAccountTestOptions(
    account: AccountListItem,
    healthCheckModel = account.healthCheckModel,
    healthCheckEndpointMode = account.healthCheckEndpointMode
  ): void {
    resetTestModels()
    selectedAccount = account
    defaultModel = healthCheckModel.trim()
    testModelOptions.value = defaultModel
      ? [{
          label: defaultModel,
          value: defaultModel
        }]
      : []
    testEndpointModes.value = [healthCheckEndpointMode]
    input.testForm.model = defaultModel
    input.testForm.testEndpointMode = healthCheckEndpointMode
  }

  async function loadTestModelOptions(
    account = selectedAccount,
    keyword = ''
  ): Promise<AccountTestOptions | undefined> {
    if (!account || testModelReadonly.value) return undefined
    if (!selectedAccount) selectedAccount = account
    if (selectedAccount.id !== account.id) return undefined
    const normalizedKeyword = keyword.trim()
    const selectedIds = selectedModelIds(account, input.testForm.model)
    const accountKey = accountOptionsKey(account, normalizedKeyword, selectedIds)
    if (loadedOptionsAccountKey === accountKey) return undefined
    if (testModelOptionsLoading.value && activeOptionsRequestKey === accountKey) return undefined
    optionsAbortController?.abort()
    const requestToken = nextOptionsRequestToken()
    const controller = new AbortController()
    optionsAbortController = controller
    activeOptionsRequestKey = accountKey
    testModelOptionsLoading.value = true
    testModelsError.value = ''
    try {
      const params = {
        keyword: normalizedKeyword || undefined,
        limit: 50,
        selectedIds
      }
      const response = input.isManagementView.value
        ? await api.accounts.testOptions(
          account.id,
          { ...accountOperationScopeParams(account, input.accountScopeParams.value), ...params },
          { signal: controller.signal }
        )
        : await api.myAccounts.testOptions(account.id, params, { signal: controller.signal })
      if (!isCurrentOptionsRequest(requestToken, account.id)) return undefined
      loadedOptionsAccountKey = accountKey
      testModelOptions.value = normalizeModelOptions(response)
      if (input.testForm.model && !testModelOptions.value.some((option) => option.value === input.testForm.model)) {
        testModelOptions.value.unshift({
          label: input.testForm.model,
          value: input.testForm.model
        })
      }
      if (!input.testForm.model) {
        input.testForm.model = defaultModel || testModelOptions.value[0]?.value || ''
        applyTestEndpointModes([], false)
      }
      return response
    } catch (error) {
      if (isAbortError(error)) return undefined
      if (isCurrentOptionsRequest(requestToken, account.id)) {
        testModelsError.value = extractApiErrorMessage(error, '测试模型列表加载失败，请重试')
      }
      throw error
    } finally {
      if (optionsAbortController === controller) {
        optionsAbortController = undefined
        activeOptionsRequestKey = ''
      }
      if (requestToken === optionsRequestToken) {
        testModelOptionsLoading.value = false
      }
    }
  }

  const loadSavedAccountTestOptions = loadTestModelOptions

  async function loadTestEndpointModeOptions(
    account = selectedAccount
  ): Promise<void> {
    if (!account || testModelReadonly.value) return
    if (!selectedAccount) selectedAccount = account
    if (selectedAccount.id !== account.id) return
    const model = input.testForm.model.trim()
    if (!model) return
    const requestKey = modelCapabilitiesKey(account, model)
    const cached = modelCapabilitiesCache.get(requestKey)
    if (cached) {
      applyTestEndpointModes(normalizeEndpointModes(cached.testEndpointModes), true)
      return
    }
    if (
      testEndpointModesLoading.value
      && activeModelCapabilitiesRequestKey === requestKey
    ) {
      return
    }
    modelCapabilitiesAbortController?.abort()
    const requestToken = nextModelCapabilitiesRequestToken()
    const controller = new AbortController()
    modelCapabilitiesAbortController = controller
    activeModelCapabilitiesRequestKey = requestKey
    testEndpointModesLoading.value = true
    testEndpointModesError.value = ''
    try {
      const response = input.isManagementView.value
        ? await api.accounts.testModelCapabilities(
          account.id,
          model,
          accountOperationScopeParams(account, input.accountScopeParams.value),
          { signal: controller.signal }
        )
        : await api.myAccounts.testModelCapabilities(
          account.id,
          model,
          { signal: controller.signal }
        )
      if (
        !isCurrentModelCapabilitiesRequest(requestToken, account.id, model)
        || response.id !== model
      ) {
        return
      }
      modelCapabilitiesCache.set(requestKey, response)
      const endpointModes = normalizeEndpointModes(response.testEndpointModes)
      applyTestEndpointModes(endpointModes, true)
    } catch (error) {
      if (isAbortError(error)) return
      if (isCurrentModelCapabilitiesRequest(requestToken, account.id, model)) {
        testEndpointModesError.value = extractApiErrorMessage(error, '测试请求形态加载失败，请重试')
      }
      throw error
    } finally {
      if (modelCapabilitiesAbortController === controller) {
        modelCapabilitiesAbortController = undefined
        activeModelCapabilitiesRequestKey = ''
      }
      if (requestToken === modelCapabilitiesRequestToken) {
        testEndpointModesLoading.value = false
      }
    }
  }

  function useFixedTestModel(model: string, endpointModes: AccountSupportedEndpointMode[]): void {
    resetTestModels()
    const normalizedModel = model.trim()
    testModelReadonly.value = true
    testModelOptions.value = normalizedModel
      ? [{
          label: normalizedModel,
          value: normalizedModel
        }]
      : []
    testEndpointModes.value = normalizeEndpointModes(endpointModes)
    input.testForm.model = normalizedModel
    input.testForm.testEndpointMode = testEndpointModes.value[0] ?? 'account_default'
  }

  function restoreTestSelection(
    model: string,
    endpointMode: AccountTestEndpointMode,
    fallbackEndpointModes: AccountSupportedEndpointMode[] = []
  ): void {
    const normalizedModel = model.trim()
    if (normalizedModel) {
      input.testForm.model = normalizedModel
      if (!testModelOptions.value.some((option) => option.value === normalizedModel)) {
        testModelOptions.value.push({
          label: normalizedModel,
          value: normalizedModel
        })
      }
      testEndpointModes.value = normalizeEndpointModes(fallbackEndpointModes)
    }
    if (
      endpointMode !== 'account_default'
      && testEndpointModes.value.includes(endpointMode)
    ) {
      input.testForm.testEndpointMode = endpointMode
    }
  }

  function updateSelectableTestModel(model: string): void {
    if (testModelReadonly.value) return
    const normalizedModel = model.trim()
    const option = testModelOptions.value.find((item) => item.value === normalizedModel)
    if (!selectedAccount || !option) return
    modelCapabilitiesAbortController?.abort()
    modelCapabilitiesAbortController = undefined
    activeModelCapabilitiesRequestKey = ''
    nextModelCapabilitiesRequestToken()
    testEndpointModesLoading.value = false
    input.testForm.model = normalizedModel
    testModelsError.value = ''
    testEndpointModesError.value = ''
    applyTestEndpointModes([], false)
  }

  function resetTestModels(): void {
    optionsAbortController?.abort()
    modelCapabilitiesAbortController?.abort()
    optionsAbortController = undefined
    modelCapabilitiesAbortController = undefined
    selectedAccount = undefined
    loadedOptionsAccountKey = ''
    activeOptionsRequestKey = ''
    activeModelCapabilitiesRequestKey = ''
    defaultModel = ''
    nextOptionsRequestToken()
    nextModelCapabilitiesRequestToken()
    testModelOptionsLoading.value = false
    testEndpointModesLoading.value = false
    testModelsError.value = ''
    testEndpointModesError.value = ''
    testModelReadonly.value = false
    testModelOptions.value = []
    testEndpointModes.value = []
    input.testForm.model = ''
    input.testForm.testEndpointMode = 'account_default'
  }

  function nextOptionsRequestToken(): number {
    optionsRequestToken += 1
    return optionsRequestToken
  }

  function nextModelCapabilitiesRequestToken(): number {
    modelCapabilitiesRequestToken += 1
    return modelCapabilitiesRequestToken
  }

  function isCurrentOptionsRequest(requestToken: number, accountId: string): boolean {
    return requestToken === optionsRequestToken && selectedAccount?.id === accountId
  }

  function isCurrentModelCapabilitiesRequest(
    requestToken: number,
    accountId: string,
    model: string
  ): boolean {
    return requestToken === modelCapabilitiesRequestToken
      && selectedAccount?.id === accountId
      && input.testForm.model === model
  }

  function applyTestEndpointModes(
    endpointModes: AccountSupportedEndpointMode[],
    preserveCurrent: boolean
  ): void {
    const currentEndpointMode = input.testForm.testEndpointMode
    testEndpointModes.value = endpointModes
    input.testForm.testEndpointMode = preserveCurrent
      && currentEndpointMode !== 'account_default'
      && endpointModes.includes(currentEndpointMode)
      ? currentEndpointMode
      : endpointModes[0] ?? 'account_default'
  }

  return {
    initializeSavedAccountTestOptions,
    loadSavedAccountTestOptions,
    loadTestEndpointModeOptions,
    loadTestModelOptions,
    resetTestModels,
    restoreTestSelection,
    testEndpointModes,
    testEndpointModesError,
    testEndpointModesLoading,
    testModelOptions,
    testModelReadonly,
    testModelsError,
    testModelsLoading,
    updateSelectableTestModel,
    useFixedTestModel
  }
}

function normalizeModelOptions(options: AccountTestOptions): AccountTestModelOption[] {
  const values = new Set<string>()
  const output: AccountTestModelOption[] = []
  for (const option of options) {
    const value = option.id.trim()
    if (!value || values.has(value)) continue
    values.add(value)
    output.push({
      label: option.name.trim() || value,
      value
    })
  }
  return output
}

function normalizeEndpointModes(modes: AccountSupportedEndpointMode[]): AccountSupportedEndpointMode[] {
  return [...new Set(modes)]
}

function accountOptionsKey(account: AccountListItem, keyword: string, selectedIds: string[]): string {
  return `${account.id}:${account.configRevision ?? 'uncached'}:${keyword}:${selectedIds.join(',')}`
}

function modelCapabilitiesKey(account: AccountListItem, model: string): string {
  return `${account.id}:${account.configRevision ?? 'uncached'}:${model}`
}

function selectedModelIds(account: AccountListItem, selectedModel: string): string[] {
  return [...new Set([account.healthCheckModel.trim(), selectedModel.trim()].filter(Boolean))]
}
