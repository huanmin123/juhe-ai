import { message } from '@/lib/antd'
import { computed, ref, type ComputedRef } from 'vue'

import { api } from '@/api/client'
import type { AccountSummary, ProviderDefinition } from '@/types/domain'
import {
  buildTestModelOptions,
  defaultTestModelForAccountSelection,
  isGatewaySupportedTestSelection,
  providerCodeForAccountSelection,
  providerDefaultTestModelForAccountSelection,
  providerSystemDefaultTestModelForAccountSelection
} from './accountDerivedState'
import { accountOperationScopeParams } from './accountOperationScope'
import { type AccountTestForm, nextTestModel } from './accountTestFlow'

type UseAccountTestModelsInput = {
  accountScopeParams: ComputedRef<{ systemAccountId: string } | undefined>
  providers: ComputedRef<ProviderDefinition[]>
  testForm: AccountTestForm
  testTargetAccountSelection: ComputedRef<AccountSummary | AccountSummary[] | undefined>
}

export function useAccountTestModels(input: UseAccountTestModelsInput) {
  const testModelsLoading = ref(false)
  const scopedProviders = ref<ProviderDefinition[]>([])
  const scopedProvidersRequestKey = ref('')
  const modelRequestId = ref(0)
  const testTargetProviderCode = computed(() => providerCodeForAccountSelection(input.testTargetAccountSelection.value))
  const testTargetScopeParams = computed(() => accountSelectionScopeParams(
    input.testTargetAccountSelection.value,
    input.accountScopeParams.value
  ))
  const testTargetRequestKey = computed(() => (
    `${testTargetProviderCode.value}\u0001${testTargetScopeParams.value?.systemAccountId ?? ''}`
  ))
  const providersForSelection = computed(() => (
    scopedProvidersRequestKey.value === testTargetRequestKey.value && scopedProviders.value.length
      ? scopedProviders.value
      : input.providers.value
  ))
  const providerDefaultTestModel = computed(() => providerDefaultTestModelForAccountSelection(
    providersForSelection.value,
    input.testTargetAccountSelection.value
  ))
  const providerSystemDefaultTestModel = computed(() => providerSystemDefaultTestModelForAccountSelection(
    providersForSelection.value,
    input.testTargetAccountSelection.value
  ))
  const testModelOptions = computed(() => buildTestModelOptions(
    input.testTargetAccountSelection.value,
    providerDefaultTestModel.value,
    providerSystemDefaultTestModel.value
  ))
  const defaultTestModel = computed(() => (
    defaultTestModelForAccountSelection(
      input.testTargetAccountSelection.value,
      providerDefaultTestModel.value,
      providerSystemDefaultTestModel.value
    )
  ))
  const isGatewaySupportedTestTarget = computed(() => isGatewaySupportedTestSelection(input.testTargetAccountSelection.value))

  async function loadTestModels(): Promise<void> {
    if (!isGatewaySupportedTestTarget.value) {
      modelRequestId.value += 1
      testModelsLoading.value = false
      scopedProviders.value = []
      scopedProvidersRequestKey.value = ''
      input.testForm.model = nextTestModel(input.testForm.model, testModelOptions.value, defaultTestModel.value)
      return
    }
    const providerCode = testTargetProviderCode.value
    if (!providerCode) {
      modelRequestId.value += 1
      testModelsLoading.value = false
      scopedProviders.value = []
      scopedProvidersRequestKey.value = ''
      input.testForm.model = nextTestModel(input.testForm.model, testModelOptions.value, defaultTestModel.value)
      return
    }
    const requestKey = testTargetRequestKey.value
    if (scopedProvidersRequestKey.value !== requestKey) {
      scopedProviders.value = []
      scopedProvidersRequestKey.value = ''
    }
    if (scopedProvidersRequestKey.value === requestKey) return
    const requestId = modelRequestId.value + 1
    const requestScopeParams = testTargetScopeParams.value
    const requestInitialModel = input.testForm.model
    const requestInitialDefaultModel = defaultTestModel.value
    modelRequestId.value = requestId
    testModelsLoading.value = true
    try {
      const providers = await api.providers.options(requestScopeParams)
      if (!isCurrentModelRequest(requestId, requestKey)) return
      scopedProviders.value = providers
      scopedProvidersRequestKey.value = requestKey
      const shouldApplyScopedDefault = input.testForm.model === requestInitialModel
        && (!requestInitialModel || requestInitialModel === requestInitialDefaultModel)
      input.testForm.model = nextTestModel(
        shouldApplyScopedDefault ? '' : input.testForm.model,
        testModelOptions.value,
        defaultTestModel.value
      )
    } catch (error) {
      if (!isCurrentModelRequest(requestId, requestKey)) return
      console.error(error)
      input.testForm.model = nextTestModel(input.testForm.model, testModelOptions.value, defaultTestModel.value)
      message.warning('默认测试模型信息加载失败，已使用账户支持模型')
    } finally {
      if (modelRequestId.value === requestId) {
        testModelsLoading.value = false
      }
    }
  }

  function isCurrentModelRequest(requestId: number, requestKey: string): boolean {
    return (
      modelRequestId.value === requestId &&
      testTargetRequestKey.value === requestKey
    )
  }

  function defaultModelForSelection(account: AccountSummary | AccountSummary[] | undefined): string {
    const providers = providersForSelection.value
    return defaultTestModelForAccountSelection(
      account,
      providerDefaultTestModelForAccountSelection(providers, account),
      providerSystemDefaultTestModelForAccountSelection(providers, account)
    )
  }

  return {
    defaultModelForSelection,
    loadTestModels,
    testModelOptions,
    testModelsLoading
  }
}

function accountSelectionScopeParams(
  selection: AccountSummary | AccountSummary[] | undefined,
  fallback?: { systemAccountId: string }
): { systemAccountId: string } | undefined {
  const accounts = Array.isArray(selection) ? selection : selection ? [selection] : []
  if (!accounts.length) return fallback
  const scopeIds = [...new Set(accounts
    .map((account) => accountOperationScopeParams(account, fallback)?.systemAccountId)
    .filter((systemAccountId): systemAccountId is string => Boolean(systemAccountId)))]
  return scopeIds.length === 1 ? { systemAccountId: scopeIds[0] } : fallback
}
