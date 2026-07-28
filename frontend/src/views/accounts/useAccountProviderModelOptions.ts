import { message } from '@/lib/antd'
import { ref, watch, type ComputedRef } from 'vue'

import { api } from '@/api/client'
import type { AccountModelSelectOption } from './accountEditFormPayload'
import type { AccountScopeParams } from './accountOperationScope'

interface UseAccountProviderModelOptionsOptions {
  currentProviderCode: () => string
  extractApiErrorMessage: (error: unknown, fallback: string) => string
  isManagementView: ComputedRef<boolean>
  modelScopeParams: ComputedRef<AccountScopeParams>
}

interface ProviderAccountModelResourceOptions {
  isManagementView: boolean
  providerCode: string
  scopeParams?: AccountScopeParams
  selectedIds?: string[]
  keyword?: string
}

export async function loadAccountProviderModelOptionsResource(
  options: ProviderAccountModelResourceOptions
): Promise<{ data: AccountModelSelectOption[] }> {
  const code = options.providerCode.trim()
  if (!code) return { data: [] }
  const scopeParams = options.scopeParams ? { ...options.scopeParams } : undefined
  const selectedIds = normalizedSelectedModelIds(options.selectedIds)
  return {
    data: await loadAccountModelOptions(
      code,
      scopeParams,
      selectedIds,
      options.keyword?.trim() || undefined
    )
  }
}

export function useAccountProviderModelOptions(options: UseAccountProviderModelOptionsOptions) {
  const providerModelOptions = ref<AccountModelSelectOption[]>([])
  const providerModelsLoading = ref(false)
  let latestRequestId = 0
  let loadingKey: string | undefined
  let loadingPromise: Promise<void> | undefined

  watch(
    currentProviderModelCatalogScopeKey,
    (nextScopeKey, previousScopeKey) => {
      if (nextScopeKey === previousScopeKey) return
      resetProviderModelOptions()
    },
    { flush: 'sync' }
  )

  function resetProviderModelOptions(): void {
    latestRequestId += 1
    providerModelOptions.value = []
    providerModelsLoading.value = false
    loadingKey = undefined
    loadingPromise = undefined
  }

  async function loadProviderModelOptions(providerCode: string, request: { keyword?: string; selectedIds?: string[] } = {}): Promise<void> {
    const code = providerCode.trim()
    if (!code) {
      resetProviderModelOptions()
      return
    }
    const selectedIds = normalizedSelectedModelIds(request.selectedIds)
    const keyword = request.keyword?.trim() || undefined
    const scopeParams = options.modelScopeParams.value
      ? { ...options.modelScopeParams.value }
      : undefined
    const requestCatalogScopeKey = providerModelCatalogScopeKey(
      options.isManagementView.value,
      scopeParams,
      code
    )
    const requestKey = providerModelRequestKey(requestCatalogScopeKey, keyword, selectedIds)
    if (loadingKey === requestKey && loadingPromise) return loadingPromise

    const requestId = latestRequestId + 1
    latestRequestId = requestId
    loadingKey = undefined
    loadingPromise = undefined
    providerModelOptions.value = []
    providerModelsLoading.value = true
    const promise = (async () => {
      try {
        const modelOptions = await loadAccountModelOptions(code, scopeParams, selectedIds, keyword)
        if (isCurrentRequest(requestId, requestCatalogScopeKey)) {
          providerModelOptions.value = modelOptions
        }
      } catch (error) {
        if (!isCurrentRequest(requestId, requestCatalogScopeKey)) return
        console.error(error)
        message.error(options.extractApiErrorMessage(error, '加载供应商模型失败'))
      } finally {
        if (requestId === latestRequestId) {
          providerModelsLoading.value = false
          loadingKey = undefined
          loadingPromise = undefined
        }
      }
    })()
    loadingKey = requestKey
    loadingPromise = promise
    return promise
  }

  function providerModelRequestKey(catalogScopeKey: string, keyword?: string, selectedIds: string[] = []): string {
    return JSON.stringify([
      catalogScopeKey,
      keyword ?? '',
      selectedIds
    ])
  }

  function currentProviderModelCatalogScopeKey(): string {
    return providerModelCatalogScopeKey(
      options.isManagementView.value,
      options.modelScopeParams.value,
      options.currentProviderCode()
    )
  }

  function isCurrentRequest(requestId: number, requestCatalogScopeKey: string): boolean {
    return requestId === latestRequestId
      && requestCatalogScopeKey === currentProviderModelCatalogScopeKey()
  }

  function dedupeModelOptions(options: AccountModelSelectOption[]): AccountModelSelectOption[] {
    return dedupeAccountModelOptions(options)
  }

  return {
    loadProviderModelOptions,
    providerModelOptions,
    providerModelsLoading,
    resetProviderModelOptions
  }
}

function providerModelCatalogScopeKey(
  isManagementView: boolean,
  scopeParams: AccountScopeParams | undefined,
  providerCode: string
): string {
  return JSON.stringify([
    isManagementView ? 'management' : 'self',
    scopeParams?.systemAccountId?.trim() ?? '',
    providerCode.trim()
  ])
}

async function loadAccountModelOptions(
  providerCode: string,
  scopeParams: AccountScopeParams | undefined,
  selectedIds: string[],
  keyword?: string
): Promise<AccountModelSelectOption[]> {
  const selectedIdBatches = chunkedSelectedModelIds(selectedIds)
  const models = (await Promise.all((selectedIdBatches.length ? selectedIdBatches : [[]]).map((batch) => (
    api.providers.modelOptions({
      ...scopeParams,
      providerCode,
      limit: 50,
      ...(keyword ? { keyword } : {}),
      ...(batch.length ? { selectedIds: batch } : {})
    })
  )))).flat()
  return dedupeAccountModelOptions(models.map((item) => {
    return {
      label: item.name,
      value: item.id,
      supportedApiProtocols: item.supportedApiProtocols,
      supportedServiceTiers: item.supportedServiceTiers,
      supportedReasoningEfforts: item.supportedReasoningEfforts,
      defaultReasoningEffort: item.defaultReasoningEffort
    }
  }))
}

function normalizedSelectedModelIds(values: string[] | undefined): string[] {
  return [...new Set((values ?? []).map((value) => value.trim()).filter(Boolean))]
}

function chunkedSelectedModelIds(values: string[]): string[][] {
  const output: string[][] = []
  for (let index = 0; index < values.length; index += 50) {
    output.push(values.slice(index, index + 50))
  }
  return output
}

function dedupeAccountModelOptions(options: AccountModelSelectOption[]): AccountModelSelectOption[] {
  const seen = new Set<string>()
  const output: AccountModelSelectOption[] = []
  for (const option of options) {
    const model = option.value.trim()
    if (!model || seen.has(model)) continue
    seen.add(model)
    output.push({
      label: option.label.trim() || model,
      value: model,
      ...(option.supportedApiProtocols?.length ? { supportedApiProtocols: [...option.supportedApiProtocols] } : {}),
      ...(option.supportedServiceTiers?.length ? { supportedServiceTiers: [...option.supportedServiceTiers] } : {}),
      ...(option.supportedReasoningEfforts?.length ? { supportedReasoningEfforts: [...option.supportedReasoningEfforts] } : {}),
      ...(option.defaultReasoningEffort ? { defaultReasoningEffort: option.defaultReasoningEffort } : {})
    })
  }
  return output
}
