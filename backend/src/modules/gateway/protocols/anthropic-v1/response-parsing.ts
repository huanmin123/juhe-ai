/**
 * Anthropic 响应体解析工具
 * 供账户测试、诊断等场景使用
 */

/**
 * 从 Anthropic 响应体中提取错误消息
 * 支持 JSON 格式和 SSE 流式格式
 */
export function parseAnthropicUpstreamMessage(bodyText: string): string | undefined {
  if (!bodyText) return undefined
  const jsonMessage = parseAnthropicJsonMessage(bodyText)
  if (jsonMessage) return jsonMessage
  for (const event of parseAnthropicSseEvents(bodyText)) {
    if (event.event === 'error') {
      const message = parseAnthropicJsonMessage(event.data)
      if (message) return message
    }
  }
  return undefined
}

/**
 * 从 Anthropic SSE 流中提取流式失败消息
 * 仅识别 SSE error 事件
 */
export function parseAnthropicStreamFailureMessage(bodyText: string): string | undefined {
  for (const event of parseAnthropicSseEvents(bodyText)) {
    if (event.event === 'error') {
      return parseAnthropicJsonMessage(event.data) ?? 'Anthropic 流式响应失败'
    }
  }
  return undefined
}

/**
 * 从 Anthropic 响应体中提取输出文本
 * 支持 JSON 格式（content 数组）和 SSE 流式格式（content_block_delta 事件）
 */
export function extractAnthropicResponseOutputText(bodyText: string): string | undefined {
  if (!bodyText) return undefined
  const jsonText = extractAnthropicJsonOutputText(bodyText)
  if (jsonText) return jsonText
  const chunks: string[] = []
  for (const event of parseAnthropicSseEvents(bodyText)) {
    try {
      const payload = JSON.parse(event.data) as Record<string, unknown>
      const delta = typeof payload.delta === 'object' && payload.delta !== null
        ? payload.delta as Record<string, unknown>
        : undefined
      const text = stringValue(delta?.text)
      if (text) chunks.push(text)
    } catch {
      // 忽略无效的 SSE 行
    }
  }
  return chunks.length ? chunks.join('') : undefined
}

/** 从 JSON 格式 Anthropic 响应体中提取 content 文本 */
function extractAnthropicJsonOutputText(bodyText: string): string | undefined {
  try {
    const payload = JSON.parse(bodyText) as Record<string, unknown>
    const content = Array.isArray(payload.content) ? payload.content : []
    const parts = content
      .map((item) => typeof item === 'object' && item !== null ? stringValue((item as Record<string, unknown>).text) : '')
      .filter(Boolean)
    return parts.length ? parts.join('') : undefined
  } catch {
    return undefined
  }
}

/** 从 JSON 文本中解析 Anthropic 错误消息 */
function parseAnthropicJsonMessage(text: string): string | undefined {
  try {
    const payload = JSON.parse(text) as Record<string, unknown>
    const error = typeof payload.error === 'object' && payload.error !== null
      ? payload.error as Record<string, unknown>
      : undefined
    return stringValue(error?.message) || stringValue(payload.message)
  } catch {
    return undefined
  }
}

/** 将 SSE 文本解析为 event/data 结构数组 */
function parseAnthropicSseEvents(text: string): Array<{ event: string; data: string }> {
  const events: Array<{ event: string; data: string }> = []
  const blocks = text.split(/\r?\n\r?\n/)
  for (const block of blocks) {
    const lines = block.split(/\r?\n/)
    let event = ''
    const data: string[] = []
    for (const line of lines) {
      if (line.startsWith('event:')) {
        event = line.slice(6).trim()
      } else if (line.startsWith('data:')) {
        data.push(line.slice(5).trimStart())
      }
    }
    if (data.length) {
      events.push({ event, data: data.join('\n') })
    }
  }
  return events
}

function stringValue(value: unknown): string {
  return typeof value === 'string' ? value.trim() : ''
}
