export function normalizeOpenAICodexBuiltinTools(body: Record<string, unknown>): void {
  const tools = Array.isArray(body.tools) ? body.tools : undefined
  if (tools) {
    const normalizedTools = tools.map((tool) => (
      isPlainObject(tool) ? normalizeOpenAICodexBuiltinTool(tool) : tool
    ))
    if (normalizedTools.some((tool, index) => tool !== tools[index])) {
      body.tools = normalizedTools
    }
  }

  const toolChoice = isPlainObject(body.tool_choice) ? body.tool_choice : undefined
  if (toolChoice) {
    const normalizedToolChoice = normalizeOpenAICodexBuiltinTool(toolChoice)
    const toolChoiceTools = Array.isArray(toolChoice.tools) ? toolChoice.tools : undefined
    if (!toolChoiceTools) {
      if (normalizedToolChoice !== toolChoice) {
        body.tool_choice = normalizedToolChoice
      }
      return
    }
    const normalizedTools = toolChoiceTools.map((tool) => (
      isPlainObject(tool) ? normalizeOpenAICodexBuiltinTool(tool) : tool
    ))
    if (
      normalizedToolChoice !== toolChoice
      || normalizedTools.some((tool, index) => tool !== toolChoiceTools[index])
    ) {
      body.tool_choice = {
        ...normalizedToolChoice,
        tools: normalizedTools
      }
    }
  }
}

function normalizeOpenAICodexBuiltinTool(value: Record<string, unknown>): Record<string, unknown> {
  const current = typeof value.type === 'string' ? value.type : ''
  if (current === 'web_search_preview' || current === 'web_search_preview_2025_03_11') {
    return { ...value, type: 'web_search' }
  }
  return value
}

function isPlainObject(value: unknown): value is Record<string, unknown> {
  return typeof value === 'object' && value !== null && !Array.isArray(value)
}
