import { ref, type ComputedRef } from 'vue'

import { api } from '@/api/client'
import type {
  AccountSupportedEndpointMode,
  AccountSummary
} from '@/types/domain'
import type {
  AccountTestModelOption,
  AccountTestOptions
} from '@/api/domains/accounts'
import { accountOperationScopeParams } from './accountOperationScope'
import type { AccountTestEndpointMode, AccountTestForm } from './accountTestFlow'
import { prioritizeAccountTestEndpointModes } from './accountEndpointModes'

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

  async function loadSavedAccountTestOptions(account: AccountSummary): Promise<AccountTestOptions | undefined> {
    const requestToken = nextModelRequestToken()
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
      testModelOptions.value = normalizeModelOptions(response.models)
      testEndpointModes.value = prioritizeAccountTestEndpointModes(
        normalizeEndpointModes(response.testEndpointModes),
        account.healthCheckEndpointMode
      )
      const defaultModel = response.defaultModel.trim()
      input.testForm.model = testModelOptions.value.some((option) => option.value === defaultModel)
        ? defaultModel
        : testModelOptions.value[0]?.value ?? ''
      input.testForm.testEndpointMode = testEndpointModes.value[0] ?? 'account_default'
      return response
    } finally {
      if (isCurrentModelRequest(requestToken)) {
        testModelsLoading.value = false
      }
    }
  }

  function useFixedTestModel(model: string, endpointModes: AccountSupportedEndpointMode[]): void {
    nextModelRequestToken()
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
      if (!testEndpointModes.value.length && fallbackEndpointModes.length) {
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
  }

  function resetTestModels(): void {
    nextModelRequestToken()
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
