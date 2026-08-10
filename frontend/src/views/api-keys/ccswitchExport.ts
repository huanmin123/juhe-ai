import type { ProviderDefinition, RouteStrategyGroupBindingSummary } from '@/types/domain'

export const ccswitchClientOptions = [
  { label: 'Codex', value: 'codex' },
  { label: 'Claude', value: 'claude' },
  { label: 'Gemini', value: 'gemini' },
  { label: 'Grok', value: 'grokbuild' }
] as const

export type CcSwitchClientApp = typeof ccswitchClientOptions[number]['value']

export interface CcSwitchExportGroupOption {
  groupId: string
  groupName: string
  providerCode: string
  providerName: string
  defaultModel: string
}

export interface CcSwitchExportInput {
  apiKey: string
  app: CcSwitchClientApp
  model?: string
  endpoint: string
  homepage?: string
  name?: string
}

export function canSubmitCcSwitchExport(input: { groupId?: string; app?: CcSwitchClientApp; confirmed?: boolean }): boolean {
  return Boolean(input.confirmed && input.groupId?.trim() && input.app)
}

export function buildCcSwitchExportUrl(input: CcSwitchExportInput): string {
  const endpoint = normalizeUrl(input.endpoint)
  const homepage = normalizeUrl(input.homepage || endpoint)
  const params = new URLSearchParams({
    resource: 'provider',
    app: input.app,
    name: input.name?.trim() || 'juhe-ai',
    homepage,
    endpoint,
    apiKey: input.apiKey,
    configFormat: 'json',
    enabled: 'true'
  })
  const model = input.model?.trim()
  if (model) params.set('model', model)
  return 'ccswitch://v1/import?' + params.toString()
}

export function buildCcSwitchExportGroupOptions(
  bindings: readonly RouteStrategyGroupBindingSummary[],
  providers: readonly ProviderDefinition[]
): CcSwitchExportGroupOption[] {
  const providerByCode = new Map(providers.map((provider) => [provider.code, provider]))
  const seenGroupIds = new Set<string>()

  return bindings.flatMap((binding) => {
    const providerCode = binding.providerCode?.trim()
    if (binding.status !== 'active' || !binding.groupEnabled || !providerCode || seenGroupIds.has(binding.groupId)) {
      return []
    }
    seenGroupIds.add(binding.groupId)
    const provider = providerByCode.get(providerCode)
    return [{
      groupId: binding.groupId,
      groupName: binding.groupName?.trim() || binding.groupId,
      providerCode,
      providerName: provider?.name?.trim() || providerCode,
      defaultModel: provider?.defaultHealthCheckModel?.trim() || ''
    }]
  })
}

function normalizeUrl(value: string): string {
  return value.trim().replace(/\/+$/, '')
}
