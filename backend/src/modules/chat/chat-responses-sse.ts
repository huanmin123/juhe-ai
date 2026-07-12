export type ChatResponsesEvent =
  | { type: 'text_delta'; delta: string }
  | { type: 'tool_started'; item: Record<string, unknown> }
  | { type: 'tool_updated'; item: Record<string, unknown> }
  | { type: 'tool_completed'; item: Record<string, unknown> }
  | { type: 'completed'; response: Record<string, unknown> }
  | { type: 'failed'; error: Record<string, unknown> }

export async function collectChatResponsesSse(
  chunks: AsyncIterable<Uint8Array>,
  onEvent: (event: ChatResponsesEvent) => void,
  maxBytes = 192 * 1024
): Promise<{ content: string; events: ChatResponsesEvent[] }> {
  const decoder = new TextDecoder('utf-8', { fatal: true })
  let buffer = ''
  let content = ''
  const events: ChatResponsesEvent[] = []
  for await (const chunk of chunks) {
    buffer += decoder.decode(chunk, { stream: true })
    const split = consumeBlocks(buffer, (block) => {
      const parsed = parseBlock(block)
      if (!parsed) return
      if (parsed.type === 'text_delta') {
        content += parsed.delta
        if (new TextEncoder().encode(content).byteLength > maxBytes) throw new Error('模型回答超过 192 KiB 上限')
      }
      events.push(parsed)
      onEvent(parsed)
    })
    buffer = split.rest
  }
  buffer += decoder.decode()
  const parsed = parseBlock(buffer)
  if (parsed) {
    if (parsed.type === 'text_delta') {
      content += parsed.delta
      if (new TextEncoder().encode(content).byteLength > maxBytes) throw new Error('模型回答超过 192 KiB 上限')
    }
    events.push(parsed)
    onEvent(parsed)
  }
  return { content, events }
}

function consumeBlocks(input: string, onBlock: (block: string) => void): { rest: string } {
  let rest = input
  while (true) {
    const lf = rest.indexOf('\n\n')
    const crlf = rest.indexOf('\r\n\r\n')
    const index = crlf >= 0 && (lf < 0 || crlf < lf) ? crlf : lf
    if (index < 0) return { rest }
    const length = index === crlf ? 4 : 2
    onBlock(rest.slice(0, index))
    rest = rest.slice(index + length)
  }
}

function parseBlock(block: string): ChatResponsesEvent | undefined {
  const eventName = block.match(/^event:\s*(.+)$/m)?.[1]?.trim()
  const data = block.split(/\r?\n/).filter((line) => line.startsWith('data:')).map((line) => line.slice(5).trimStart()).join('\n')
  if (!data || data === '[DONE]') return undefined
  let payload: Record<string, unknown>
  try { payload = JSON.parse(data) as Record<string, unknown> } catch { return undefined }
  const type = String(payload.type ?? eventName ?? '')
  if (type === 'response.output_text.delta') return { type: 'text_delta', delta: String(payload.delta ?? '') }
  if (type === 'response.output_item.added') {
    const item = objectValue(payload.item)
    return ['function_call', 'computer_call', 'web_search_call', 'file_search_call'].includes(String(item.type)) ? { type: 'tool_started', item } : undefined
  }
  if (type === 'response.function_call_arguments.delta') return { type: 'tool_updated', item: payload }
  if (type === 'response.output_item.done') {
    const item = objectValue(payload.item)
    return ['function_call', 'computer_call', 'web_search_call', 'file_search_call'].includes(String(item.type)) ? { type: 'tool_completed', item } : undefined
  }
  if (type === 'response.completed') return { type: 'completed', response: objectValue(payload.response) }
  if (type === 'response.failed') return { type: 'failed', error: objectValue(payload.response ?? payload.error) }
  return undefined
}

function objectValue(value: unknown): Record<string, unknown> { return value && typeof value === 'object' && !Array.isArray(value) ? value as Record<string, unknown> : {} }
