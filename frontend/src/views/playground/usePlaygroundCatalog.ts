import { computed, ref } from 'vue'

import { api } from '@/api/client'
import { extractApiErrorMessage } from '@/shared/apiError'
import type { PlaygroundAccount, PlaygroundModelKind, PlaygroundModelOption } from '@/types/domain/playground'

/**
 * 演练场四页共用的账户目录（设计 §3.2/§4.2）：模块级内存缓存，会话内一次拉取，
 * 失败不缓存（下次进入页面重试）。零持久化——刷新即重新拉取。
 */
let accountsCache: PlaygroundAccount[] | null = null
let accountsInflight: Promise<PlaygroundAccount[]> | null = null

export async function loadPlaygroundAccounts(options?: { force?: boolean }): Promise<PlaygroundAccount[]> {
  if (!options?.force) {
    if (accountsCache) return accountsCache
    if (accountsInflight) return accountsInflight
  }
  const request = api.playground.accounts()
    .then((accounts) => {
      accountsCache = accounts
      accountsInflight = null
      return accounts
    })
    .catch((error: unknown) => {
      accountsInflight = null
      throw error
    })
  accountsInflight = request
  return request
}

/** 按模型分类过滤账户模型行；账户未选或无匹配返回空数组。 */
export function resolveModelsForKind(account: PlaygroundAccount | undefined, kind: PlaygroundModelKind): PlaygroundModelOption[] {
  return account?.models.filter((model) => model.kind === kind) ?? []
}

/** 账户→模型两级联动：候选变化时默认落到第一个同类模型。 */
export function firstModelIdOf(models: PlaygroundModelOption[]): string | undefined {
  return models[0]?.modelId
}

/**
 * 单页账户/模型目录状态：账户下拉 → 该账户下指定 kind 的模型下拉联动。
 * 每页独立持有选中的 accountId/modelId（账户或模型变更不影响其他页）。
 */
export function usePlaygroundCatalog(kind: PlaygroundModelKind) {
  const accounts = ref<PlaygroundAccount[]>([])
  const accountsLoading = ref(false)
  const accountsLoadError = ref('')
  const accountId = ref<string>()
  const modelId = ref<string>()

  const currentAccount = computed(() => accounts.value.find((account) => account.accountId === accountId.value))
  const models = computed(() => resolveModelsForKind(currentAccount.value, kind))
  const currentModel = computed(() => models.value.find((model) => model.modelId === modelId.value))
  const accountOptions = computed(() => accounts.value.map((account) => ({
    value: account.accountId,
    label: `${account.accountName}（${account.providerCode}）`
  })))
  const modelOptions = computed(() => models.value.map((model) => ({ value: model.modelId, label: model.modelId })))

  /** 账户下拉 change：重置为该账户第一个同类模型。 */
  function handleAccountChange(): void {
    modelId.value = firstModelIdOf(models.value)
  }

  async function loadCatalog(options?: { force?: boolean }): Promise<void> {
    if (accountsLoading.value) return
    accountsLoading.value = true
    accountsLoadError.value = ''
    try {
      const result = await loadPlaygroundAccounts(options)
      accounts.value = result
      if (!accountId.value || !result.some((account) => account.accountId === accountId.value)) {
        accountId.value = result[0]?.accountId
        modelId.value = firstModelIdOf(resolveModelsForKind(result[0], kind))
        return
      }
      if (modelId.value && !models.value.some((model) => model.modelId === modelId.value)) {
        modelId.value = firstModelIdOf(models.value)
      }
    } catch (error) {
      console.error(error)
      accountsLoadError.value = extractApiErrorMessage(error, '加载演练场账户目录失败')
    } finally {
      accountsLoading.value = false
    }
  }

  return {
    accounts,
    accountsLoading,
    accountsLoadError,
    accountId,
    modelId,
    currentAccount,
    models,
    currentModel,
    accountOptions,
    modelOptions,
    handleAccountChange,
    loadCatalog
  }
}
