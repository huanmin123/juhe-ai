export function normalizeOpenAICodexBuiltinTools(body: Record<string, unknown>): void {
  normalizeOpenAICodexBuiltinToolAtPath(body, ['tool_choice', 'type'])

  const tools = Array.isArray(body.tools) ? body.tools : undefined
  if (tools) {
    for (const tool of tools) {
      if (isPlainObject(tool)) {
        normalizeOpenAICodexBuiltinToolAtPath(tool, ['type'])
      }
    }
  }

  const toolChoice = isPlainObject(body.tool_choice) ? body.tool_choice : undefined
  const toolChoiceTools = Array.isArray(toolChoice?.tools) ? toolChoice.tools : undefined
  if (toolChoiceTools) {
    for (const tool of toolChoiceTools) {
      if (isPlainObject(tool)) {
        normalizeOpenAICodexBuiltinToolAtPath(tool, ['type'])
      }
    }
  }
}

function normalizeOpenAICodexBuiltinToolAtPath(source: Record<string, unknown>, path: string[]): void {
  const owner = objectAtPath(source, path.slice(0, -1))
  if (!owner) return
  const key = path[path.length - 1]
  const current = typeof owner[key] === 'string' ? owner[key] : ''
  if (current === 'web_search_preview' || current === 'web_search_preview_2025_03_11') {
    owner[key] = 'web_search'
  }
}

function objectAtPath(source: Record<string, unknown>, path: string[]): Record<string, unknown> | undefined {
  let current: unknown = source
  for (const key of path) {
    if (!isPlainObject(current)) {
      return undefined
    }
    current = current[key]
  }
  return isPlainObject(current) ? current : undefined
}

function isPlainObject(value: unknown): value is Record<string, unknown> {
  return typeof value === 'object' && value !== null && !Array.isArray(value)
}
