import { computed, ref, type ComputedRef } from 'vue'

import type {
  AccountSupportedEndpointMode,
  AccountSummary
} from '@/types/domain'
import type {
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
  let selectedAccount: AccountSummary | undefined
  let loadedOptionsAccountKey = ''
  let activeOptionsRequestKey = ''
  let defaultModel = ''
  let optionsAbortController: AbortController | undefined
  let optionsRequestToken = 0

  function initializeSavedAccountTestOptions(
    account: AccountSummary,
    healthCheckModel = account.healthCheckModel
  ): void {
    resetTestModels()
    selectedAccount = account
    defaultModel = healthCheckModel.trim()
    testModelOptions.value = defaultModel
      ? [{
          label: defaultModel,
          value: defaultModel,
          supportedApiProtocols: [],
          testEndpointModes: []
        }]
      : []
    testEndpointModes.value = []
    input.testForm.model = defaultModel
    input.testForm.testEndpointMode = 'account_default'
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
          value: input.testForm.model,
          supportedApiProtocols: [],
          testEndpointModes: []
        })
      }
      if (!input.testForm.model) {
        input.testForm.model = defaultModel || testModelOptions.value[0]?.value || ''
      }
      updateSelectableTestModel(input.testForm.model)
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

  function useFixedTestModel(model: string, endpointModes: AccountSupportedEndpointMode[]): void {
    resetTestModels()
    const normalizedModel = model.trim()
    testModelReadonly.value = true
    testModelOptions.value = normalizedModel
      ? [{
          label: normalizedModel,
          value: normalizedModel,
          supportedApiProtocols: [],
          testEndpointModes: normalizeEndpointModes(endpointModes)
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
          value: normalizedModel,
          supportedApiProtocols: [],
          testEndpointModes: normalizeEndpointModes(fallbackEndpointModes)
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
    input.testForm.model = normalizedModel
    testModelsError.value = ''
    testEndpointModes.value = normalizeEndpointModes(option.testEndpointModes)
    input.testForm.testEndpointMode = testEndpointModes.value[0] ?? 'account_default'
  }

  function resetTestModels(): void {
    optionsAbortController?.abort()
    optionsAbortController = undefined
    selectedAccount = undefined
    loadedOptionsAccountKey = ''
    activeOptionsRequestKey = ''
    defaultModel = ''
    nextOptionsRequestToken()
    testModelOptionsLoading.value = false
    testModelsError.value = ''
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

  function isCurrentOptionsRequest(requestToken: number, accountId: string): boolean {
    return requestToken === optionsRequestToken && selectedAccount?.id === accountId
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
    output.push({
      label: option.name.trim() || value,
      value,
      supportedApiProtocols: [...new Set(option.supportedApiProtocols)],
      testEndpointModes: normalizeEndpointModes(option.testEndpointModes)
    })
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
