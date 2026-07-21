import { computed, ref, type ComputedRef } from 'vue'

import type {
  AccountSupportedEndpointMode,
  AccountSummary
} from '@/types/domain'
import type {
  AccountTestModelOption,
  AccountTestOptions
} from '@/api/domains/accounts'
import { extractApiErrorMessage } from '@/shared/apiError'
import type { AccountTestEndpointMode, AccountTestForm } from './accountTestFlow'
import { isAbortError } from './accountTestTaskHelpers'
import {
  loadAccountTestModelCapabilitiesCached,
  loadAccountTestOptionsCached
} from './accountTestOptionsCache'

type UseAccountTestModelsInput = {
  accountScopeParams: ComputedRef<{ systemAccountId: string } | undefined>
  isManagementView: ComputedRef<boolean>
  testForm: AccountTestForm
}

export function useAccountTestModels(input: UseAccountTestModelsInput) {
  const testModelOptions = ref<AccountTestModelOption[]>([])
  const testModelOptionsLoading = ref(false)
  const testModelCapabilitiesLoading = ref(false)
  const testModelsLoading = computed(() => (
    testModelOptionsLoading.value || testModelCapabilitiesLoading.value
  ))
  const testModelsError = ref('')
  const testModelReadonly = ref(false)
  const testEndpointModes = ref<AccountSupportedEndpointMode[]>([])
  let selectedAccount: AccountSummary | undefined
  let loadedOptionsAccountKey = ''
  let activeOptionsRequestKey = ''
  let defaultModel = ''
  let defaultTestEndpointMode: AccountSupportedEndpointMode | undefined
  let optionsAbortController: AbortController | undefined
  let modelAbortController: AbortController | undefined
  let optionsRequestToken = 0
  let modelRequestToken = 0

  function initializeSavedAccountTestOptions(
    account: AccountSummary,
    healthCheckModel = account.healthCheckModel,
    healthCheckEndpointMode = account.healthCheckEndpointMode
  ): void {
    resetTestModels()
    selectedAccount = account
    defaultModel = healthCheckModel.trim()
    defaultTestEndpointMode = healthCheckEndpointMode
    testModelOptions.value = defaultModel
      ? [{ label: defaultModel, value: defaultModel }]
      : []
    testEndpointModes.value = defaultTestEndpointMode ? [defaultTestEndpointMode] : []
    input.testForm.model = defaultModel
    input.testForm.testEndpointMode = defaultTestEndpointMode ?? 'account_default'
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
      const response = await loadAccountTestOptionsCached({
        account,
        isManagementView: input.isManagementView.value,
        options: { signal: controller.signal },
        params: {
          keyword: normalizedKeyword || undefined,
          limit: 50,
          selectedIds
        },
        scopeParams: input.accountScopeParams.value
      })
      if (!isCurrentOptionsRequest(requestToken, account.id)) return undefined
      loadedOptionsAccountKey = accountKey
      testModelOptions.value = normalizeModelOptions(response)
      if (input.testForm.model && !testModelOptions.value.some((option) => option.value === input.testForm.model)) {
        testModelOptions.value.unshift({ label: input.testForm.model, value: input.testForm.model })
      }
      if (!input.testForm.model) {
        input.testForm.model = defaultModel || testModelOptions.value[0]?.value || ''
      }
      if (input.testForm.model === defaultModel) {
        useDefaultTestEndpointMode()
      }
      return response
    } catch (error) {
      if (!isAbortError(error) && isCurrentOptionsRequest(requestToken, account.id)) {
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

  function useFixedTestModel(model: string, endpointModes: AccountSupportedEndpointMode[]): void {
    resetTestModels()
    const normalizedModel = model.trim()
    testModelReadonly.value = true
    testModelOptions.value = normalizedModel
      ? [{ label: normalizedModel, value: normalizedModel }]
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
        testModelOptions.value.push({ label: normalizedModel, value: normalizedModel })
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

  async function updateSelectableTestModel(model: string): Promise<void> {
    if (testModelReadonly.value) return
    const account = selectedAccount
    const normalizedModel = model.trim()
    if (!account || !testModelOptions.value.some((option) => option.value === normalizedModel)) return
    input.testForm.model = normalizedModel
    testModelsError.value = ''
    modelAbortController?.abort()
    modelAbortController = undefined
    const requestToken = nextModelRequestToken()
    if (normalizedModel === defaultModel) {
      testModelCapabilitiesLoading.value = false
      useDefaultTestEndpointMode()
      return
    }

    testModelCapabilitiesLoading.value = true
    const controller = new AbortController()
    modelAbortController = controller
    testEndpointModes.value = []
    input.testForm.testEndpointMode = 'account_default'
    try {
      const response = await loadAccountTestModelCapabilitiesCached({
        account,
        modelId: normalizedModel,
        isManagementView: input.isManagementView.value,
        options: { signal: controller.signal },
        scopeParams: input.accountScopeParams.value
      })
      if (
        !isCurrentModelRequest(requestToken, account.id, normalizedModel)
        || response.id !== normalizedModel
      ) {
        return
      }
      testEndpointModes.value = normalizeEndpointModes(response.testEndpointModes)
      input.testForm.testEndpointMode = testEndpointModes.value[0] ?? 'account_default'
    } catch (error) {
      if (!isAbortError(error) && isCurrentModelRequest(requestToken, account.id, normalizedModel)) {
        testModelsError.value = extractApiErrorMessage(error, '测试模型能力加载失败，请重试')
      }
      throw error
    } finally {
      if (modelAbortController === controller) modelAbortController = undefined
      if (requestToken === modelRequestToken) {
        testModelCapabilitiesLoading.value = false
      }
    }
  }

  function resetTestModels(): void {
    optionsAbortController?.abort()
    modelAbortController?.abort()
    optionsAbortController = undefined
    modelAbortController = undefined
    selectedAccount = undefined
    loadedOptionsAccountKey = ''
    activeOptionsRequestKey = ''
    defaultModel = ''
    defaultTestEndpointMode = undefined
    nextOptionsRequestToken()
    nextModelRequestToken()
    testModelOptionsLoading.value = false
    testModelCapabilitiesLoading.value = false
    testModelsError.value = ''
    testModelReadonly.value = false
    testModelOptions.value = []
    testEndpointModes.value = []
    input.testForm.model = ''
    input.testForm.testEndpointMode = 'account_default'
  }

  function useDefaultTestEndpointMode(): void {
    testEndpointModes.value = defaultTestEndpointMode ? [defaultTestEndpointMode] : []
    input.testForm.testEndpointMode = defaultTestEndpointMode ?? 'account_default'
  }

  function nextOptionsRequestToken(): number {
    optionsRequestToken += 1
    return optionsRequestToken
  }

  function nextModelRequestToken(): number {
    modelRequestToken += 1
    return modelRequestToken
  }

  function isCurrentOptionsRequest(requestToken: number, accountId: string): boolean {
    return requestToken === optionsRequestToken && selectedAccount?.id === accountId
  }

  function isCurrentModelRequest(requestToken: number, accountId: string, model: string): boolean {
    return requestToken === modelRequestToken
      && selectedAccount?.id === accountId
      && input.testForm.model === model
  }

  return {
    initializeSavedAccountTestOptions,
    loadSavedAccountTestOptions,
    loadTestModelOptions,
    resetTestModels,
    restoreTestSelection,
    testEndpointModes,
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
    output.push({ label: option.name.trim() || value, value })
  }
  return output
}

function normalizeEndpointModes(modes: AccountSupportedEndpointMode[]): AccountSupportedEndpointMode[] {
  return [...new Set(modes)]
}

function accountOptionsKey(account: AccountSummary, keyword: string, selectedIds: string[]): string {
  return `${account.id}:${account.configRevision ?? 'uncached'}:${keyword}:${selectedIds.join(',')}`
}

function selectedModelIds(account: AccountSummary, selectedModel: string): string[] {
  return [...new Set([account.healthCheckModel.trim(), selectedModel.trim()].filter(Boolean))]
}
