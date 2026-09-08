import type { AccountSummary } from '../../domain/types.js'
import type { ProviderProtocolProfileDefinition } from '../../domain/provider-protocol.js'
import { providerModelSupportsProtocolProfile } from '../../storage/account-model-normalization.js'
import { findProviderProtocolProfileAsync } from '../../storage/repositories.js'
import { listProviderModelCatalogAsync, listProviderModelTestCatalogAsync } from '../model-pricing/model-catalog.service.js'
import type { AccountTestDraftSnapshot } from '../../storage/account-test-tasks.repository.js'
import { discoverAccountUpstreamModels } from './account-test.service.js'
import {
  accountApiKeyPoolEntriesForCandidate,
  fixedAccountApiKeyPoolCandidate
} from './account-api-key-pool-runtime.js'
import { openAIDraftAccountSecret } from './account-test-task-queue.service.js'

export interface AccountModelCatalogRefreshResult {
  addedModels: string[]
  recommendedHealthCheckModel?: string
}

export async function refreshAccountDraftModelCatalogAsync(input: {
  account: AccountSummary
  draftAccount: AccountTestDraftSnapshot
  signal?: AbortSignal
}): Promise<AccountModelCatalogRefreshResult> {
  throwIfAborted(input.signal)
  const profile = await findProviderProtocolProfileAsync(input.account.providerProtocolProfileId ?? '')
  if (!profile) throw new Error('账户协议档案不存在，无法获取上游模型目录')

  const candidate = await openAIDraftAccountSecret(input.draftAccount, input.signal ?? new AbortController().signal)
  throwIfAborted(input.signal)
  const upstreamModels = await discoverDraftAccountUpstreamModelIds(input.account, candidate, input.signal)
  throwIfAborted(input.signal)
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
  throwIfAborted(input.signal)
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
}

async function discoverDraftAccountUpstreamModelIds(
  account: AccountSummary,
  candidate: Awaited<ReturnType<typeof openAIDraftAccountSecret>>,
  signal?: AbortSignal
): Promise<Set<string>> {
  const entries = candidate.type === 'api_key'
    ? accountApiKeyPoolEntriesForCandidate(candidate)
    : []
  if (entries.length < 2) {
    return await discoverDraftCandidateUpstreamModelIds(account, candidate, signal)
  }

  const catalogs: Set<string>[] = []
  for (const entry of entries) {
    throwIfAborted(signal)
    const fixedCandidate = fixedAccountApiKeyPoolCandidate(candidate, entry, { apiKeyRuntimeStateDisabled: true })
    catalogs.push(await discoverDraftCandidateUpstreamModelIds(account, fixedCandidate, signal))
  }
  return intersectAccountUpstreamModelCatalogs(catalogs)
}

async function discoverDraftCandidateUpstreamModelIds(
  account: AccountSummary,
  candidate: Awaited<ReturnType<typeof openAIDraftAccountSecret>>,
  signal?: AbortSignal
): Promise<Set<string>> {
  const upstream = await discoverAccountUpstreamModels(account, {
    candidateAccount: candidate,
    systemAccountId: candidate.systemAccountId,
    groupId: candidate.boundGroupId,
    diagnostics: 'full',
    signal
  })
  return new Set(upstream.modelIds)
}

function throwIfAborted(signal: AbortSignal | undefined): void {
  if (signal?.aborted) {
    throw signal.reason instanceof Error ? signal.reason : new Error('模型目录同步已取消')
  }
}

export function intersectAccountUpstreamModelCatalogs(catalogs: readonly ReadonlySet<string>[]): Set<string> {
  const [firstCatalog, ...remainingCatalogs] = catalogs
  if (!firstCatalog) return new Set()
  const intersection = new Set(firstCatalog)
  for (const catalog of remainingCatalogs) {
    for (const modelId of intersection) {
      if (!catalog.has(modelId)) intersection.delete(modelId)
    }
  }
  return intersection
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
