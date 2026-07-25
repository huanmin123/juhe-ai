import type { AccountSummary } from '../../domain/types.js'
import type { ProviderProtocolProfileDefinition } from '../../domain/provider-protocol.js'
import { providerModelSupportsProtocolProfile } from '../../storage/account-model-normalization.js'
import { findProviderProtocolProfileAsync } from '../../storage/repositories.js'
import { listProviderModelCatalogAsync, listProviderModelTestCatalogAsync } from '../model-pricing/model-catalog.service.js'
import type { AccountTestDraftSnapshot } from '../../storage/account-test-tasks.repository.js'
import { discoverAccountUpstreamModels } from './account-test.service.js'
import { openAIDraftAccountSecret } from './account-test-task-queue.service.js'

export interface AccountModelCatalogRefreshResult {
  addedModels: string[]
  recommendedHealthCheckModel?: string
}

const accountModelCatalogDiscoveryTimeoutMs = 10_000

export async function refreshAccountDraftModelCatalogAsync(input: {
  account: AccountSummary
  draftAccount: AccountTestDraftSnapshot
}): Promise<AccountModelCatalogRefreshResult> {
  const profile = await findProviderProtocolProfileAsync(input.account.providerProtocolProfileId ?? '')
  if (!profile) throw new Error('账户协议档案不存在，无法获取上游模型目录')

  const controller = new AbortController()
  const timeout = setTimeout(() => controller.abort(), accountModelCatalogDiscoveryTimeoutMs)
  try {
    const candidate = await openAIDraftAccountSecret(input.draftAccount, controller.signal)
    const upstream = await discoverAccountUpstreamModels(input.account, {
      candidateAccount: candidate,
      systemAccountId: candidate.systemAccountId,
      groupId: candidate.boundGroupId,
      diagnostics: 'full',
      signal: controller.signal
    })
    const upstreamModels = new Set(upstream.modelIds)
    const [localModels, testModels] = await Promise.all([
      listProviderModelCatalogAsync({
        providerCode: input.account.providerCode,
        systemAccountId: input.account.ownerSystemAccountId ?? input.account.systemAccountId,
        includeUnpriced: true
      }),
      listProviderModelTestCatalogAsync({
        providerCode: input.account.providerCode,
        systemAccountId: input.account.ownerSystemAccountId ?? input.account.systemAccountId
      })
    ])
    return {
      addedModels: accountModelCatalogAdditions({
        supportedModels: input.account.supportedModels ?? [],
        upstreamModelIds: upstreamModels,
        localModels,
        profile
      }),
      recommendedHealthCheckModel: recommendedAccountHealthCheckModel({
        configuredHealthCheckModel: input.account.healthCheckModel,
        upstreamModelIds: upstreamModels,
        testModels,
        profile
      })
    }
  } finally {
    clearTimeout(timeout)
  }
}

export function recommendedAccountHealthCheckModel(input: {
  configuredHealthCheckModel?: string
  upstreamModelIds: ReadonlySet<string>
  testModels: readonly { model: string; supportedApiProtocols: readonly string[] }[]
  profile: ProviderProtocolProfileDefinition
}): string | undefined {
  const candidates = input.testModels.filter((item) => (
    input.upstreamModelIds.has(item.model)
    && providerModelSupportsProtocolProfile(item.supportedApiProtocols, input.profile)
  ))
  const configured = input.configuredHealthCheckModel?.trim()
  if (configured && candidates.some((item) => item.model === configured)) return configured
  return candidates[0]?.model
}

export function accountModelCatalogAdditions(input: {
  supportedModels: readonly string[]
  upstreamModelIds: ReadonlySet<string>
  localModels: readonly { model: string; supportedApiProtocols: readonly string[] }[]
  profile: ProviderProtocolProfileDefinition
}): string[] {
  const selectedModels = new Set(input.supportedModels.map((model) => model.trim()).filter(Boolean))
  const additions: string[] = []
  const seen = new Set<string>()
  for (const item of input.localModels) {
    const model = item.model.trim()
    if (!model || seen.has(model)) continue
    seen.add(model)
    if (!providerModelSupportsProtocolProfile(item.supportedApiProtocols, input.profile)) continue
    if (!input.upstreamModelIds.has(model) || selectedModels.has(model)) continue
    additions.push(model)
  }
  return additions
}
