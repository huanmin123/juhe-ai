import { computed, ref, type ComputedRef } from 'vue'

import type {
  AccountSupportedEndpointMode,
  AccountListItem
} from '@/types/domain'
import type { AccountDraftTestPayload } from '@/api/client'
import type {
  AccountTestModelOption,
  AccountTestOptions
} from '@/api/domains/accounts'
import { api } from '@/api/client'
import { extractApiErrorMessage } from '@/shared/apiError'
import { accountOperationScopeParams } from './accountOperationScope'
import type { AccountTestDraftMode } from './accountTestSessionClient'
import type { AccountTestEndpointMode, AccountTestForm } from './accountTestFlow'
import { isAbortError } from './accountTestTaskHelpers'

type UseAccountTestModelsInput = {
  accountScopeParams: ComputedRef<{ systemAccountId: string } | undefined>
  isManagementView: ComputedRef<boolean>
  testForm: AccountTestForm
}

interface DraftTestOptionsContext {
  account: AccountListItem
  defaultOption: AccountTestModelOption
  mode: AccountTestDraftMode
  payload: AccountDraftTestPayload['account']
}

export function useAccountTestModels(input: UseAccountTestModelsInput) {
  const testModelOptions = ref<AccountTestModelOption[]>([])
  const testModelOptionsLoading = ref(false)
  const testModelsLoading = computed(() => testModelOptionsLoading.value)
  const testModelsReady = ref(false)
  const testModelsError = ref('')
  const testEndpointModes = ref<AccountSupportedEndpointMode[]>([])
  let selectedAccount: AccountListItem | undefined
  let loadedOptionsAccountKey = ''
  let activeOptionsRequestKey = ''
  let defaultModel = ''
  let draftTestContext: DraftTestOptionsContext | undefined
  let optionsAbortController: AbortController | undefined
  let optionsRequestToken = 0

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
          testEndpointModes: [healthCheckEndpointMode],
          value: defaultModel
        }]
      : []
    testEndpointModes.value = [healthCheckEndpointMode]
    input.testForm.model = defaultModel
    input.testForm.testEndpointMode = healthCheckEndpointMode
    testModelsReady.value = Boolean(defaultModel && healthCheckEndpointMode)
  }

  async function loadTestModelOptions(
    account = selectedAccount,
    keyword = ''
  ): Promise<AccountTestOptions | undefined> {
    if (!account) return undefined
    if (!selectedAccount) selectedAccount = account
    if (selectedAccount.id !== account.id) return undefined
    const normalizedKeyword = keyword.trim()
    const selectedIds = selectedModelIds(account, input.testForm.model)
    return requestTestModelOptions({
      account,
      optionsKey: accountOptionsKey(account, normalizedKeyword, selectedIds),
      fetcher: (signal) => fetchSavedAccountTestOptions(account, normalizedKeyword, selectedIds, signal),
      applyResponseOptions: (response) => {
        testModelOptions.value = normalizeModelOptions(response)
      }
    })
  }

  const loadSavedAccountTestOptions = loadTestModelOptions

  function initializeDraftTestOptions(
    account: AccountListItem,
    draftPayload: AccountDraftTestPayload['account'],
    model: string,
    endpointModes: AccountSupportedEndpointMode[],
    mode: AccountTestDraftMode
  ): void {
    resetTestModels()
    selectedAccount = account
    const normalizedModel = model.trim()
    const modes = normalizeEndpointModes(endpointModes)
    defaultModel = normalizedModel
    const defaultOption: AccountTestModelOption = {
      label: normalizedModel,
      testEndpointModes: modes,
      value: normalizedModel
    }
    draftTestContext = { account, defaultOption, mode, payload: draftPayload }
    testModelOptions.value = normalizedModel ? [defaultOption] : []
    testEndpointModes.value = modes
    input.testForm.model = normalizedModel
    input.testForm.testEndpointMode = modes[0] ?? 'account_default'
    testModelsReady.value = Boolean(normalizedModel && modes.length)
  }

  async function loadDraftTestModelOptions(keyword = ''): Promise<AccountTestOptions | undefined> {
    const context = draftTestContext
    const account = context?.account
    if (!context || !account) return undefined
    if (selectedAccount?.id !== account.id) return undefined
    const normalizedKeyword = keyword.trim()
    const selectedIds = draftSelectedModelIds(context.defaultOption.value, input.testForm.model)
    return requestTestModelOptions({
      account,
      optionsKey: draftOptionsKey(account, normalizedKeyword, selectedIds),
      fetcher: (signal) => fetchDraftTestOptions(context, normalizedKeyword, selectedIds, signal),
      applyResponseOptions: (response) => {
        testModelOptions.value = mergeDraftModelOptions(context.defaultOption, response)
      }
    })
  }

  async function requestTestModelOptions(request: {
    account: AccountListItem
    optionsKey: string
    fetcher: (signal: AbortSignal) => Promise<AccountTestOptions>
    applyResponseOptions: (response: AccountTestOptions) => void
  }): Promise<AccountTestOptions | undefined> {
    if (loadedOptionsAccountKey === request.optionsKey) return undefined
    if (testModelOptionsLoading.value && activeOptionsRequestKey === request.optionsKey) return undefined
    optionsAbortController?.abort()
    const requestToken = nextOptionsRequestToken()
    const controller = new AbortController()
    optionsAbortController = controller
    activeOptionsRequestKey = request.optionsKey
    testModelOptionsLoading.value = true
    testModelsError.value = ''
    try {
      const response = await request.fetcher(controller.signal)
      if (!isCurrentOptionsRequest(requestToken, request.account.id)) return undefined
      loadedOptionsAccountKey = request.optionsKey
      request.applyResponseOptions(response)
      if (!input.testForm.model) {
        input.testForm.model = defaultModel || testModelOptions.value[0]?.value || ''
      }
      const currentOption = testModelOptions.value.find((option) => option.value === input.testForm.model)
      const selectedOption = currentOption ?? testModelOptions.value[0]
      input.testForm.model = selectedOption?.value ?? ''
      applyTestEndpointModes(selectedOption?.testEndpointModes ?? [], Boolean(currentOption))
      testModelsReady.value = true
      return response
    } catch (error) {
      if (isAbortError(error)) return undefined
      if (isCurrentOptionsRequest(requestToken, request.account.id)) {
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

  function fetchSavedAccountTestOptions(
    account: AccountListItem,
    keyword: string,
    selectedIds: string[],
    signal: AbortSignal
  ): Promise<AccountTestOptions> {
    const params = {
      keyword: keyword || undefined,
      limit: 50,
      selectedIds
    }
    return input.isManagementView.value
      ? api.accounts.testOptions(
        account.id,
        { ...accountOperationScopeParams(account, input.accountScopeParams.value), ...params },
        { signal }
      )
      : api.myAccounts.testOptions(account.id, params, { signal })
  }

  function fetchDraftTestOptions(
    context: DraftTestOptionsContext,
    keyword: string,
    selectedIds: string[],
    signal: AbortSignal
  ): Promise<AccountTestOptions> {
    const payload = {
      account: context.payload,
      keyword: keyword || undefined,
      limit: 50,
      selectedIds
    }
    return input.isManagementView.value
      ? api.accounts.testDraftOptions(
        payload,
        context.mode === 'create'
          ? input.accountScopeParams.value
          : accountOperationScopeParams(context.account, input.accountScopeParams.value),
        { signal }
      )
      : api.myAccounts.testDraftOptions(payload, { signal })
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
          testEndpointModes: normalizeEndpointModes(fallbackEndpointModes),
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
    const normalizedModel = model.trim()
    const option = testModelOptions.value.find((item) => item.value === normalizedModel)
    if (!selectedAccount || !option) return
    input.testForm.model = normalizedModel
    testModelsError.value = ''
    applyTestEndpointModes(option.testEndpointModes, false)
  }

  function resetTestModels(): void {
    optionsAbortController?.abort()
    optionsAbortController = undefined
    selectedAccount = undefined
    loadedOptionsAccountKey = ''
    activeOptionsRequestKey = ''
    defaultModel = ''
    draftTestContext = undefined
    nextOptionsRequestToken()
    testModelOptionsLoading.value = false
    testModelsReady.value = false
    testModelsError.value = ''
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
    initializeDraftTestOptions,
    initializeSavedAccountTestOptions,
    loadDraftTestModelOptions,
    loadSavedAccountTestOptions,
    loadTestModelOptions,
    resetTestModels,
    restoreTestSelection,
    testEndpointModes,
    testModelOptions,
    testModelsError,
    testModelsLoading,
    testModelsReady,
    updateSelectableTestModel,
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
      testEndpointModes: normalizeEndpointModes(option.testEndpointModes),
      value
    })
  }
  return output
}

function mergeDraftModelOptions(
  defaultOption: AccountTestModelOption,
  response: AccountTestOptions
): AccountTestModelOption[] {
  const remoteOptions = normalizeModelOptions(response)
  const remoteDefault = remoteOptions.find((option) => option.value === defaultOption.value)
  const mergedDefault = remoteDefault
    ? {
      ...defaultOption,
      testEndpointModes: [
        ...new Set([...defaultOption.testEndpointModes, ...remoteDefault.testEndpointModes])
      ]
    }
    : defaultOption
  return [mergedDefault, ...remoteOptions.filter((option) => option.value !== defaultOption.value)]
}

function normalizeEndpointModes(modes: AccountSupportedEndpointMode[]): AccountSupportedEndpointMode[] {
  return [...new Set(modes)]
}

function accountOptionsKey(account: AccountListItem, keyword: string, selectedIds: string[]): string {
  return `${account.id}:${account.configRevision ?? 'uncached'}:${keyword}:${selectedIds.join(',')}`
}

function draftOptionsKey(account: AccountListItem, keyword: string, selectedIds: string[]): string {
  return `${account.id}:${keyword}:${selectedIds.join(',')}`
}

function selectedModelIds(account: AccountListItem, selectedModel: string): string[] {
  return [...new Set([account.healthCheckModel.trim(), selectedModel.trim()].filter(Boolean))]
}

function draftSelectedModelIds(defaultModelId: string, selectedModel: string): string[] {
  return [...new Set([defaultModelId.trim(), selectedModel.trim()].filter(Boolean))]
}
