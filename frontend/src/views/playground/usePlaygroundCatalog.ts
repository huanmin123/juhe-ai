import { computed, ref } from 'vue'

import { api } from '@/api/client'
import { extractApiErrorMessage } from '@/shared/apiError'
import type { ApiKeySummary } from '@/types/domain'
import type { PlaygroundModelKind, PlaygroundModelOption } from '@/types/domain/playground'

/**
 * 演练场六页共用的 API Key 目录（2026-11-22 Key 化：先选 Key 后选模型）：
 * 模块级内存缓存，会话内一次拉取，失败不缓存（下次进入页面重试）。
 * 零持久化——刷新即重新拉取。Key 列表复用控制台 self-scope 列表 API，
 * 客户端过滤 status==='active'（契约裁决，绝不展示完整密钥，仅名称 + 掩码前/后缀）。
 */
let keysCache: ApiKeySummary[] | null = null
let keysInflight: Promise<ApiKeySummary[]> | null = null

export async function loadPlaygroundKeys(options?: { force?: boolean }): Promise<ApiKeySummary[]> {
  if (!options?.force) {
    if (keysCache) return keysCache
    if (keysInflight) return keysInflight
  }
  const request = api.myApiKeys.list()
    .then((result) => {
      const keys = result.items.filter((key) => key.status === 'active')
      keysCache = keys
      keysInflight = null
      return keys
    })
    .catch((error: unknown) => {
      keysInflight = null
      throw error
    })
  keysInflight = request
  return request
}

/** Key→模型目录按 apiKeyId 缓存（会话内一次；失败不缓存，切回重试）。 */
const keyModelsCache = new Map<string, PlaygroundModelOption[]>()
const keyModelsInflight = new Map<string, Promise<PlaygroundModelOption[]>>()

export async function loadPlaygroundKeyModels(apiKeyId: string, options?: { force?: boolean }): Promise<PlaygroundModelOption[]> {
  if (!options?.force) {
    const cached = keyModelsCache.get(apiKeyId)
    if (cached) return cached
    const inflight = keyModelsInflight.get(apiKeyId)
    if (inflight) return inflight
  }
  const request = api.playground.keyModels(apiKeyId)
    .then((models) => {
      keyModelsCache.set(apiKeyId, models)
      keyModelsInflight.delete(apiKeyId)
      return models
    })
    .catch((error: unknown) => {
      keyModelsInflight.delete(apiKeyId)
      throw error
    })
  keyModelsInflight.set(apiKeyId, request)
  return request
}

/** 按页面能力域过滤模型行（契约：只展示本页 kind 的模型，不展示全部）；无匹配返回空数组。 */
export function resolveModelsForKind(models: PlaygroundModelOption[], kind: PlaygroundModelKind): PlaygroundModelOption[] {
  return models.filter((model) => model.kind === kind)
}

/** Key→模型两级联动：候选变化时默认落到第一个同类模型。 */
export function firstModelIdOf(models: PlaygroundModelOption[]): string | undefined {
  return models[0]?.modelId
}

/** Key 下拉 label：Key 名称 + 掩码前/后缀辅助识别（同控制台 formatKeyPreview 口径），绝不拼接完整密钥。 */
export function apiKeyOptionLabel(key: Pick<ApiKeySummary, 'name' | 'keyPrefix' | 'keySuffix'>): string {
  const head = key.keyPrefix.slice(0, 6)
  const tail = key.keySuffix.slice(-6)
  return head && tail ? `${key.name}（${head}…${tail}）` : key.name
}

/**
 * 单页 Key/模型目录状态：Key 下拉 → 该 Key 下指定 kind 的模型下拉联动。
 * 每页独立持有选中的 apiKeyId/modelId（Key 或模型变更不影响其他页）。
 */
export function usePlaygroundCatalog(kind: PlaygroundModelKind) {
  const keys = ref<ApiKeySummary[]>([])
  const keysLoading = ref(false)
  const keysLoadError = ref('')
  const apiKeyId = ref<string>()
  const modelId = ref<string>()

  const models = ref<PlaygroundModelOption[]>([])
  const modelsLoading = ref(false)
  const modelsLoadError = ref('')
  /** 请求序号守卫：快速切换 Key 时丢弃迟到的旧 Key 模型响应。 */
  let modelsRequestSeq = 0

  const currentKey = computed(() => keys.value.find((key) => key.id === apiKeyId.value))
  const currentModel = computed(() => models.value.find((model) => model.modelId === modelId.value))
  const keyOptions = computed(() => keys.value.map((key) => ({ value: key.id, label: apiKeyOptionLabel(key) })))
  const modelOptions = computed(() => models.value.map((model) => ({ value: model.modelId, label: model.modelId })))

  /** 拉取所选 Key 的模型并按本页能力域过滤；seq 守卫下旧响应静默丢弃（返回空数组）。 */
  async function loadModelsForKey(keyId: string, options?: { force?: boolean }): Promise<PlaygroundModelOption[]> {
    const seq = ++modelsRequestSeq
    modelsLoading.value = true
    modelsLoadError.value = ''
    try {
      const kindModels = resolveModelsForKind(await loadPlaygroundKeyModels(keyId, options), kind)
      if (seq !== modelsRequestSeq) return []
      models.value = kindModels
      return kindModels
    } catch (error) {
      if (seq === modelsRequestSeq) {
        console.error(error)
        modelsLoadError.value = extractApiErrorMessage(error, '加载演练场模型列表失败')
      }
      return []
    } finally {
      if (seq === modelsRequestSeq) modelsLoading.value = false
    }
  }

  /** Key 下拉 change：模型下拉重置并加载该 Key 的本域模型，默认落第一个同类模型。 */
  function handleApiKeyChange(): void {
    const keyId = apiKeyId.value
    modelId.value = undefined
    if (!keyId) {
      modelsRequestSeq += 1
      models.value = []
      modelsLoading.value = false
      modelsLoadError.value = ''
      return
    }
    void loadModelsForKey(keyId).then((kindModels) => {
      if (apiKeyId.value === keyId) modelId.value = firstModelIdOf(kindModels)
    })
  }

  async function loadCatalog(options?: { force?: boolean }): Promise<void> {
    if (keysLoading.value) return
    keysLoading.value = true
    keysLoadError.value = ''
    try {
      keys.value = await loadPlaygroundKeys(options)
      if (!apiKeyId.value || !keys.value.some((key) => key.id === apiKeyId.value)) {
        // 默认落第一个 active Key（无则不选，下拉空态承接）。
        apiKeyId.value = keys.value[0]?.id
      }
    } catch (error) {
      console.error(error)
      keysLoadError.value = extractApiErrorMessage(error, '加载演练场 API Key 列表失败')
    } finally {
      keysLoading.value = false
    }
    // Key 就绪后联动加载模型；已有选中模型仍在本域清单内则保留，否则落第一个同类模型。
    if (!apiKeyId.value) return
    const kindModels = await loadModelsForKey(apiKeyId.value, options)
    if (modelId.value && kindModels.some((model) => model.modelId === modelId.value)) return
    modelId.value = firstModelIdOf(kindModels)
  }

  return {
    keys,
    keysLoading,
    keysLoadError,
    apiKeyId,
    modelId,
    currentKey,
    models,
    modelsLoading,
    modelsLoadError,
    currentModel,
    keyOptions,
    modelOptions,
    handleApiKeyChange,
    loadModelsForKey,
    loadCatalog
  }
}
