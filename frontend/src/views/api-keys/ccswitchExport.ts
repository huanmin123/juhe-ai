import type { ProviderDefinition, RouteStrategyGroupBindingSummary } from '@/types/domain'

export const ccswitchClientOptions = [
  { label: 'Codex', value: 'codex' },
  { label: 'Claude CLI', value: 'claude' },
  { label: 'Claude Desktop', value: 'claude-desktop' },
  { label: 'Gemini', value: 'gemini' },
  { label: 'Grok Build', value: 'grokbuild' },
  { label: 'OpenCode', value: 'opencode' }
] as const

export type CcSwitchClientApp = typeof ccswitchClientOptions[number]['value']

export interface CcSwitchExportGroupOption {
  groupId: string
  groupName: string
  providerCode: string
  providerName: string
  defaultModel: string
}

export interface CcSwitchExportModelOption {
  label: string
  value: string
}

export interface CcSwitchExportInput {
  apiKey: string
  app: CcSwitchClientApp
  model?: string
  endpoint: string
  homepage?: string
  name?: string
}

export function canSubmitCcSwitchExport(input: {
  app?: CcSwitchClientApp
  modelsLoading?: boolean
}): boolean {
  // 导出只需要 endpoint + API key + client app；分组只是可选的模型参考，
  // 模型也可以为空（T34）。不再从分组或 vendor 推断客户端类型。
  return Boolean(input.app && !input.modelsLoading)
}

export function buildCcSwitchExportModelOptions(
  models: readonly CcSwitchExportModelOption[]
): CcSwitchExportModelOption[] {
  const options: CcSwitchExportModelOption[] = []
  const seen = new Set<string>()
  const append = (value: string, label: string) => {
    const normalizedValue = value.trim()
    if (!normalizedValue || seen.has(normalizedValue)) return
    seen.add(normalizedValue)
    options.push({ label: label.trim() || normalizedValue, value: normalizedValue })
  }

  for (const model of models) append(model.value, model.label)
  return options
}

export function isCcSwitchExportModelSelectionValid(
  modelOptions: readonly CcSwitchExportModelOption[],
  model: string
): boolean {
  const selectedModel = model.trim()
  return !selectedModel || modelOptions.some((option) => option.value === selectedModel)
}

export function shouldLoadCcSwitchExportModelOptions(input: {
  groupId: string
  catalogGroupId?: string
  modelsLoading?: boolean
  modelsReady?: boolean
}): boolean {
  const groupId = input.groupId.trim()
  const currentCatalog = input.catalogGroupId === groupId
  return Boolean(
    groupId
    && !(currentCatalog && (input.modelsReady || input.modelsLoading))
  )
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
    if (binding.status !== 'active' || !binding.groupEnabled || seenGroupIds.has(binding.groupId)) {
      return []
    }
    seenGroupIds.add(binding.groupId)
    const providerCode = binding.providerCode?.trim() || ''
    const provider = providerCode ? providerByCode.get(providerCode) : undefined
    return [{
      groupId: binding.groupId,
      groupName: binding.groupName?.trim() || binding.groupId,
      providerCode,
      providerName: provider?.name?.trim() || '自定义协议连接',
      // 分组只作为模型参考，不再提供默认客户端推断依据（T34）。
      defaultModel: ''
    }]
  })
}

function normalizeUrl(value: string): string {
  return value.trim().replace(/\/+$/, '')
}
