export type ChatResponsesEvent =
  | { type: 'text_delta'; delta: string }
  | { type: 'reasoning_delta'; delta: string }
  | { type: 'tool_started'; item: Record<string, unknown> }
  | { type: 'tool_updated'; item: Record<string, unknown> }
  | { type: 'tool_completed'; item: Record<string, unknown> }
  | { type: 'completed'; response: Record<string, unknown> }
  | { type: 'failed'; error: Record<string, unknown> }

export async function collectChatResponsesSse(
  chunks: AsyncIterable<Uint8Array>,
  onEvent: (event: ChatResponsesEvent) => void,
  maxBytes = 192 * 1024,
  maxEvents = 65_536
): Promise<{ content: string; inputTokens?: number; outputTokens?: number }> {
  const decoder = new TextDecoder('utf-8', { fatal: true })
  const encoder = new TextEncoder()
  const maxEventBytes = 64 * 1024
  const maxAuxiliaryBytes = 192 * 1024
  let buffer = ''
  let content = ''
  let completed = false
  let auxiliaryBytes = 0
  let eventCount = 0
  let inputTokens: number | undefined
  let outputTokens: number | undefined
  const consumeEvent = (parsed: ChatResponsesEvent): void => {
    if (parsed.type === 'text_delta') {
      content += parsed.delta
      if (encoder.encode(content).byteLength > maxBytes) throw new Error('模型回答超过 192 KiB 上限')
    } else if (parsed.type === 'reasoning_delta') {
      auxiliaryBytes += encoder.encode(parsed.delta).byteLength
    } else if (parsed.type === 'tool_started' || parsed.type === 'tool_updated' || parsed.type === 'tool_completed') {
      auxiliaryBytes += encoder.encode(JSON.stringify(parsed.item)).byteLength
    }
    if (auxiliaryBytes > maxAuxiliaryBytes) throw new Error('模型结构化过程超过 192 KiB 上限')
    if (parsed.type === 'completed') {
      completed = true
      const usage = objectValue(parsed.response.usage)
      inputTokens = nonNegativeInteger(usage.input_tokens) ?? inputTokens
      outputTokens = nonNegativeInteger(usage.output_tokens) ?? outputTokens
    }
    onEvent(parsed)
  }
  const consumeBlock = (block: string): void => {
    eventCount += 1
    if (eventCount > maxEvents) throw new Error(`上游 Responses 事件数量超过 ${maxEvents} 上限`)
    if (encoder.encode(block).byteLength > maxEventBytes) throw new Error('上游 Responses 单个事件超过 64 KiB 上限')
    const parsed = parseBlock(block)
    if (parsed) consumeEvent(parsed)
  }
  for await (const chunk of chunks) {
    buffer += decoder.decode(chunk, { stream: true })
    const split = consumeBlocks(buffer, consumeBlock)
    buffer = split.rest
    if (encoder.encode(buffer).byteLength > maxEventBytes) throw new Error('上游 Responses 单个事件超过 64 KiB 上限')
  }
  buffer += decoder.decode()
  if (encoder.encode(buffer).byteLength > maxEventBytes) throw new Error('上游 Responses 单个事件超过 64 KiB 上限')
  if (buffer.trim()) consumeBlock(buffer)
  if (!completed) throw new Error('上游 Responses 流缺少 response.completed')
  return { content, inputTokens, outputTokens }
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
  if (type === 'response.reasoning_summary_text.delta' || type === 'response.reasoning_text.delta') return { type: 'reasoning_delta', delta: String(payload.delta ?? '') }
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
function nonNegativeInteger(value: unknown): number | undefined { const number = Number(value); return Number.isSafeInteger(number) && number >= 0 ? number : undefined }
