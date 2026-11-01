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

/** 按页面能力域过滤账户（对话页只列能对话的账户、生图页只列能生图的——AI 账户域分类口径，2026-10-31 用户裁决）。 */
export function resolveAccountsForKind(accounts: PlaygroundAccount[], kind: PlaygroundModelKind): PlaygroundAccount[] {
  return accounts.filter((account) => account.models.some((model) => model.kind === kind))
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
  // 账户下拉只列本页面能力域的账户（有 ≥1 个同类模型；AI 账户已按域分类）。
  const kindAccounts = computed(() => resolveAccountsForKind(accounts.value, kind))
  const accountOptions = computed(() => kindAccounts.value.map((account) => ({
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
      const candidates = kindAccounts.value
      if (!accountId.value || !candidates.some((account) => account.accountId === accountId.value)) {
        // 默认落第一个具备本页能力域的账户（无则不选，下拉空态承接）。
        accountId.value = candidates[0]?.accountId
        modelId.value = firstModelIdOf(resolveModelsForKind(candidates[0], kind))
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
