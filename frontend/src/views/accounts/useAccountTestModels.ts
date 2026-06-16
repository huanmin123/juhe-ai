import { message } from '@/lib/antd'
import { computed, ref, type ComputedRef } from 'vue'

import { api } from '@/api/client'
import type { AccountSummary, ProviderDefinition, ProviderModelPricing } from '@/types/domain'
import { GPT_VENDOR_CODE } from '@/shared/providerProtocol'
import {
  buildTestModelOptions,
  defaultTestModelForAccountSelection,
  isOpenAICompatibleTestSelection,
  providerCodeForAccountSelection,
  providerDefaultTestModelForAccountSelection
} from './accountDerivedState'
import { type AccountTestForm, nextTestModel } from './accountTestFlow'

type UseAccountTestModelsInput = {
  providers: ComputedRef<ProviderDefinition[]>
  testForm: AccountTestForm
  testTargetAccountSelection: ComputedRef<AccountSummary | AccountSummary[] | undefined>
}

export function useAccountTestModels(input: UseAccountTestModelsInput) {
  const testModelsLoading = ref(false)
  const testModelsLoadingProviderCode = ref('')
  const providerModels = ref<ProviderModelPricing[]>([])
  const providerModelsProviderCode = ref('')
  const modelRequestId = ref(0)
  const testTargetProviderCode = computed(() => providerCodeForAccountSelection(input.testTargetAccountSelection.value))
  const providerDefaultTestModel = computed(() => providerDefaultTestModelForAccountSelection(
    input.providers.value,
    input.testTargetAccountSelection.value
  ))
  const testModelOptions = computed(() => buildTestModelOptions(
    providerModels.value,
    input.testTargetAccountSelection.value,
    providerDefaultTestModel.value
  ))
  const defaultTestModel = computed(() => (
    defaultTestModelForAccountSelection(input.testTargetAccountSelection.value, providerDefaultTestModel.value)
  ))
  const isOpenAICompatibleTestTarget = computed(() => isOpenAICompatibleTestSelection(input.testTargetAccountSelection.value))

  async function loadTestModels(): Promise<void> {
    if (!isOpenAICompatibleTestTarget.value) {
      modelRequestId.value += 1
      testModelsLoading.value = false
      testModelsLoadingProviderCode.value = ''
      providerModels.value = []
      providerModelsProviderCode.value = ''
      input.testForm.model = nextTestModel(input.testForm.model, testModelOptions.value, defaultTestModel.value)
      return
    }
    const providerCode = testTargetProviderCode.value || GPT_VENDOR_CODE
    if (providerModelsProviderCode.value !== providerCode) {
      providerModels.value = []
      providerModelsProviderCode.value = providerCode
    }
    if (providerModels.value.length) return
    if (testModelsLoading.value && testModelsLoadingProviderCode.value === providerCode) return
    const requestId = modelRequestId.value + 1
    const requestProviderCode = providerCode
    modelRequestId.value = requestId
    testModelsLoading.value = true
    testModelsLoadingProviderCode.value = requestProviderCode
    try {
      const models = await api.providers.models(requestProviderCode)
      if (!isCurrentModelRequest(requestId, requestProviderCode)) return
      providerModels.value = models
      input.testForm.model = nextTestModel(input.testForm.model || defaultTestModel.value, testModelOptions.value, defaultTestModel.value)
    } catch (error) {
      if (!isCurrentModelRequest(requestId, requestProviderCode)) return
      console.error(error)
      input.testForm.model = nextTestModel(input.testForm.model, testModelOptions.value, defaultTestModel.value)
      message.warning('测试模型列表加载失败，已使用默认模型')
    } finally {
      if (modelRequestId.value === requestId) {
        testModelsLoading.value = false
        testModelsLoadingProviderCode.value = ''
      }
    }
  }

  function isCurrentModelRequest(requestId: number, providerCode: string): boolean {
    return (
      modelRequestId.value === requestId &&
      providerModelsProviderCode.value === providerCode &&
      (testTargetProviderCode.value || GPT_VENDOR_CODE) === providerCode
    )
  }

  function defaultModelForSelection(account: AccountSummary | AccountSummary[] | undefined): string {
    return defaultTestModelForAccountSelection(
      account,
      providerDefaultTestModelForAccountSelection(input.providers.value, account)
    )
  }

  return {
    defaultModelForSelection,
    loadTestModels,
    testModelOptions,
    testModelsLoading
  }
}
