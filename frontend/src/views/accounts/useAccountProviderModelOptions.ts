import { message } from '@/lib/antd'
import { ref, watch, type ComputedRef } from 'vue'

import { api } from '@/api/client'
import type { AccountModelSelectOption } from './accountEditFormPayload'
import type { AccountScopeParams } from './accountOperationScope'

/** 供应商模型目录单次取数窗口（后端 `/providers/models/options` 硬上限）。 */
export const ACCOUNT_MODEL_OPTIONS_LIMIT = 50

/**
 * 目录截断判定：只有「浏览取数」（无 keyword、无 selectedIds）返回满窗口才判定为
 * 截断；带 keyword 的搜索结果和 selectedIds 补拉批次不参与判定（后端在无 keyword
 * 时把 selectedIds 只用于排序、响应仍是 50 条浏览集，有 keyword 时按
 * `selectedIds OR keyword` 补齐，两者都不代表目录全量被截断）。
 */
export function accountModelOptionsTruncated(optionsCount: number): boolean {
  return optionsCount >= ACCOUNT_MODEL_OPTIONS_LIMIT
}

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
  /** 目录 mode 白名单（DOMAIN_MODEL_MODES[域]）；缺省不过滤。 */
  modes?: string[]
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
      options.keyword?.trim() || undefined,
      normalizedModes(options.modes)
    )
  }
}

export function useAccountProviderModelOptions(options: UseAccountProviderModelOptionsOptions) {
  const providerModelOptions = ref<AccountModelSelectOption[]>([])
  const providerModelsLoading = ref(false)
  // 当前目录 scope 下是否完成过至少一次候选加载（v8-D）：模型候选是懒加载
  // （弹窗打开/下拉展开/搜索时请求，切供应商即 reset 清空），“未加载”与
  // “加载后确认为空”必须区分，空态提示只允许在 settled 后出现。
  const providerModelOptionsSettled = ref(false)
  // 当前目录 scope 的最近一次浏览取数是否命中 50 条窗口（下拉底部截断提示依据）。
  // 只由浏览取数判定，搜索/selectedIds 补拉不改写，随 scope 切换/reset 清空。
  const providerModelOptionsTruncated = ref(false)
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
    providerModelOptionsSettled.value = false
    providerModelOptionsTruncated.value = false
    loadingKey = undefined
    loadingPromise = undefined
  }

  async function loadProviderModelOptions(providerCode: string, request: { keyword?: string; selectedIds?: string[]; modes?: string[] } = {}): Promise<void> {
    const code = providerCode.trim()
    if (!code) {
      resetProviderModelOptions()
      return
    }
    const selectedIds = normalizedSelectedModelIds(request.selectedIds)
    const keyword = request.keyword?.trim() || undefined
    const modes = normalizedModes(request.modes)
    const scopeParams = options.modelScopeParams.value
      ? { ...options.modelScopeParams.value }
      : undefined
    const requestCatalogScopeKey = providerModelCatalogScopeKey(
      options.isManagementView.value,
      scopeParams,
      code
    )
    const requestKey = providerModelRequestKey(requestCatalogScopeKey, keyword, selectedIds, modes)
    if (loadingKey === requestKey && loadingPromise) return loadingPromise

    const requestId = latestRequestId + 1
    latestRequestId = requestId
    loadingKey = undefined
    loadingPromise = undefined
    providerModelOptions.value = []
    providerModelsLoading.value = true
    const promise = (async () => {
      try {
        const modelOptions = await loadAccountModelOptions(code, scopeParams, selectedIds, keyword, modes)
        if (isCurrentRequest(requestId, requestCatalogScopeKey)) {
          providerModelOptions.value = modelOptions
          if (!keyword && !selectedIds.length) {
            providerModelOptionsTruncated.value = accountModelOptionsTruncated(modelOptions.length)
          }
        }
      } catch (error) {
        if (!isCurrentRequest(requestId, requestCatalogScopeKey)) return
        console.error(error)
        message.error(options.extractApiErrorMessage(error, '加载供应商模型失败'))
      } finally {
        if (requestId === latestRequestId) {
          providerModelsLoading.value = false
          providerModelOptionsSettled.value = true
          loadingKey = undefined
          loadingPromise = undefined
        }
      }
    })()
    loadingKey = requestKey
    loadingPromise = promise
    return promise
  }

  function providerModelRequestKey(catalogScopeKey: string, keyword?: string, selectedIds: string[] = [], modes: string[] = []): string {
    return JSON.stringify([
      catalogScopeKey,
      keyword ?? '',
      selectedIds,
      modes
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

  return {
    loadProviderModelOptions,
    providerModelOptions,
    providerModelOptionsSettled,
    providerModelOptionsTruncated,
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
  keyword?: string,
  modes: string[] = []
): Promise<AccountModelSelectOption[]> {
  const selectedIdBatches = chunkedSelectedModelIds(selectedIds)
  const models = (await Promise.all((selectedIdBatches.length ? selectedIdBatches : [[]]).map((batch) => (
    api.providers.modelOptions({
      ...scopeParams,
      providerCode,
      limit: ACCOUNT_MODEL_OPTIONS_LIMIT,
      ...(keyword ? { keyword } : {}),
      ...(modes.length ? { modes } : {}),
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

function normalizedModes(values: string[] | undefined): string[] {
  return [...new Set((values ?? []).map((value) => value.trim()).filter(Boolean))]
}

function normalizedSelectedModelIds(values: string[] | undefined): string[] {
  return [...new Set((values ?? []).map((value) => value.trim()).filter(Boolean))]
}

function chunkedSelectedModelIds(values: string[]): string[][] {
  const output: string[][] = []
  for (let index = 0; index < values.length; index += ACCOUNT_MODEL_OPTIONS_LIMIT) {
    output.push(values.slice(index, index + ACCOUNT_MODEL_OPTIONS_LIMIT))
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
