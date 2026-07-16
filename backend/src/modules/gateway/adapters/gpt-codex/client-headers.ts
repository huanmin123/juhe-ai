export const openAICodexOriginator = 'codex_cli_rs'
export const openAICodexVersion = '0.144.4'
export const openAICodexUserAgent = `${openAICodexOriginator}/${openAICodexVersion}`
export const openAICodexResponsesLiteHeader = 'x-openai-internal-codex-responses-lite'

export function normalizeOpenAICodexClientHeaders(headers: Headers, model?: string): void {
  const incomingOriginator = headers.get('originator')?.trim()
  const originator = isCodexOriginator(incomingOriginator)
    ? incomingOriginator
    : openAICodexOriginator
  headers.set('originator', originator)

  headers.set('user-agent', `${originator}/${openAICodexVersion}`)
  headers.delete('version')
  headers.delete('openai-beta')
  if (usesOpenAICodexResponsesLite(model)) {
    headers.set(openAICodexResponsesLiteHeader, 'true')
  } else {
    headers.delete(openAICodexResponsesLiteHeader)
  }
}

export function usesOpenAICodexResponsesLite(model: string | undefined): boolean {
  return model !== undefined && openAICodexResponsesLiteModels.has(model.trim().toLowerCase())
}

function isCodexOriginator(value: string | undefined): value is string {
  return typeof value === 'string' && /^codex(?:_|$)/i.test(value)
}

const openAICodexResponsesLiteModels = new Set([
  'gpt-5.6-sol',
  'gpt-5.6-terra',
  'gpt-5.6-luna'
])
