export interface CcSwitchExportInput {
  apiKey: string
  endpoint: string
  homepage?: string
  name?: string
}

export function buildCcSwitchExportUrl(input: CcSwitchExportInput): string {
  const endpoint = normalizeUrl(input.endpoint)
  const homepage = normalizeUrl(input.homepage || endpoint)
  const params = new URLSearchParams({
    resource: 'provider',
    app: 'codex',
    model: 'gpt-5.5',
    name: input.name?.trim() || 'juhe-ai',
    homepage,
    endpoint,
    apiKey: input.apiKey,
    configFormat: 'json',
    enabled: 'true'
  })
  return `ccswitch://v1/import?${params.toString()}`
}

function normalizeUrl(value: string): string {
  return value.trim().replace(/\/+$/, '')
}
