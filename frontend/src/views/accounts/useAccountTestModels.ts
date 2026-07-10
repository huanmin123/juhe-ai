import { ref, type ComputedRef } from 'vue'

import { api } from '@/api/client'
import type {
  AccountSupportedEndpointMode,
  AccountSummary,
  ProviderModelApiProtocol
} from '@/types/domain'
import type {
  AccountTestModelOption,
  AccountTestOptions
} from '@/api/domains/accounts'
import { accountOperationScopeParams } from './accountOperationScope'
import { accountTestEndpointModesForAccount } from './accountEndpointModes'
import type { AccountTestEndpointMode, AccountTestForm } from './accountTestFlow'

type UseAccountTestModelsInput = {
  accountScopeParams: ComputedRef<{ systemAccountId: string } | undefined>
  isManagementView: ComputedRef<boolean>
  testForm: AccountTestForm
}

export function useAccountTestModels(input: UseAccountTestModelsInput) {
  const testModelOptions = ref<AccountTestModelOption[]>([])
  const testModelsLoading = ref(false)
  const testModelReadonly = ref(false)
  const testEndpointModes = ref<AccountSupportedEndpointMode[]>([])
  let modelRequestToken = 0
  let selectableAccount: AccountSummary | undefined

  async function loadSavedAccountTestOptions(account: AccountSummary): Promise<AccountTestOptions | undefined> {
    const requestToken = nextModelRequestToken()
    selectableAccount = undefined
    testModelsLoading.value = true
    testModelReadonly.value = false
    testModelOptions.value = []
    testEndpointModes.value = []
    input.testForm.model = ''
    input.testForm.testEndpointMode = 'account_default'
    try {
      const response = input.isManagementView.value
        ? await api.accounts.testOptions(
          account.id,
          accountOperationScopeParams(account, input.accountScopeParams.value)
        )
        : await api.myAccounts.testOptions(account.id)
      if (!isCurrentModelRequest(requestToken) || response.accountId !== account.id) return undefined
      selectableAccount = account
      testModelOptions.value = normalizeModelOptions(response.models)
      const defaultModel = response.defaultModel.trim()
      input.testForm.model = testModelOptions.value.some((option) => option.value === defaultModel)
        ? defaultModel
        : testModelOptions.value[0]?.value ?? ''
      refreshSelectableEndpointModes()
      return response
    } finally {
      if (isCurrentModelRequest(requestToken)) {
        testModelsLoading.value = false
      }
    }
  }

  function useFixedTestModel(model: string, endpointModes: AccountSupportedEndpointMode[]): void {
    nextModelRequestToken()
    selectableAccount = undefined
    const normalizedModel = model.trim()
    testModelsLoading.value = false
    testModelReadonly.value = true
    testModelOptions.value = normalizedModel
      ? [{ label: normalizedModel, value: normalizedModel, supportedApiProtocols: [] }]
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
      if (testModelOptions.value.some((option) => option.value === normalizedModel)) {
        refreshSelectableEndpointModes()
      } else if (fallbackEndpointModes.length) {
        testEndpointModes.value = normalizeEndpointModes(fallbackEndpointModes)
      }
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
    if (!testModelOptions.value.some((option) => option.value === normalizedModel)) return
    input.testForm.model = normalizedModel
    refreshSelectableEndpointModes()
  }

  function resetTestModels(): void {
    nextModelRequestToken()
    selectableAccount = undefined
    testModelsLoading.value = false
    testModelReadonly.value = false
    testModelOptions.value = []
    testEndpointModes.value = []
    input.testForm.model = ''
    input.testForm.testEndpointMode = 'account_default'
  }

  function nextModelRequestToken(): number {
    modelRequestToken += 1
    return modelRequestToken
  }

  function isCurrentModelRequest(requestToken: number): boolean {
    return requestToken === modelRequestToken
  }

  function refreshSelectableEndpointModes(): void {
    if (!selectableAccount) return
    const selectedOption = testModelOptions.value.find((option) => option.value === input.testForm.model)
    testEndpointModes.value = endpointModesForModel(selectableAccount, selectedOption?.supportedApiProtocols ?? [])
    if (
      input.testForm.testEndpointMode === 'account_default'
      || !testEndpointModes.value.includes(input.testForm.testEndpointMode)
    ) {
      input.testForm.testEndpointMode = testEndpointModes.value[0] ?? 'account_default'
    }
  }

  return {
    loadSavedAccountTestOptions,
    resetTestModels,
    restoreTestSelection,
    testEndpointModes,
    testModelOptions,
    testModelReadonly,
    testModelsLoading,
    updateSelectableTestModel,
    useFixedTestModel
  }
}

function normalizeModelOptions(options: AccountTestOptions['models']): AccountTestModelOption[] {
  const values = new Set<string>()
  const output: AccountTestModelOption[] = []
  for (const option of options) {
    const value = option.model.trim()
    if (!value || values.has(value)) continue
    values.add(value)
    output.push({
      label: value,
      value,
      supportedApiProtocols: [...new Set(option.supportedApiProtocols)]
    })
  }
  return output
}

function normalizeEndpointModes(modes: AccountSupportedEndpointMode[]): AccountSupportedEndpointMode[] {
  return [...new Set(modes)]
}

function endpointModesForModel(
  account: AccountSummary,
  supportedApiProtocols: ProviderModelApiProtocol[]
): AccountSupportedEndpointMode[] {
  const accountModes = accountTestEndpointModesForAccount(account)
  const protocolModes = normalizeEndpointModes(
    supportedApiProtocols.flatMap((protocol) => endpointModesForProtocol(protocol))
  )
  if (!protocolModes.length) return accountModes
  const supportedModes = new Set(protocolModes)
  return accountModes.filter((mode) => supportedModes.has(mode))
}

function endpointModesForProtocol(protocol: ProviderModelApiProtocol): AccountSupportedEndpointMode[] {
  switch (protocol) {
    case 'chat_completions':
      return ['chat_sse', 'chat_json']
    case 'responses':
      return ['responses_sse', 'responses_json']
    case 'messages':
      return ['messages_sse', 'messages_json']
    case 'message_token_counting':
      return ['message_token_counting']
    case 'generate_content':
      return ['generate_content_json']
    case 'stream_generate_content':
      return ['generate_content_sse']
    case 'count_tokens':
      return ['count_tokens']
    case 'embed_content':
      return ['embed_content']
    default:
      return []
  }
}
