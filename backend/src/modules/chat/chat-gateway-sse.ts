export interface OpenAIChatSseResult {
  content: string
  finishReason?: string
  done: boolean
}

export async function collectOpenAIChatSse(
  chunks: AsyncIterable<Uint8Array>,
  maxContentBytes: number,
  onDelta?: (delta: string) => void
): Promise<OpenAIChatSseResult> {
  const decoder = new TextDecoder('utf-8', { fatal: true })
  let buffer = ''
  let content = ''
  let finishReason: string | undefined
  let done = false

  const consumeEvent = (eventText: string): void => {
    const dataLines = eventText.split(/\r?\n/)
      .filter((line) => line.startsWith('data:'))
      .map((line) => line.slice(5).replace(/^ /, ''))
    if (!dataLines.length) return
    const data = dataLines.join('\n')
    if (data === '[DONE]') {
      done = true
      return
    }
    let payload: { choices?: Array<{ delta?: { content?: unknown }; finish_reason?: unknown }>; error?: { message?: unknown } }
    try {
      payload = JSON.parse(data) as typeof payload
    } catch {
      throw new Error('上游返回了无效的 SSE JSON')
    }
    if (payload.error) throw new Error(String(payload.error.message ?? '上游流式请求失败'))
    const choice = payload.choices?.[0]
    const delta = typeof choice?.delta?.content === 'string' ? choice.delta.content : ''
    if (delta) {
      const nextBytes = Buffer.byteLength(content, 'utf8') + Buffer.byteLength(delta, 'utf8')
      if (nextBytes > maxContentBytes) throw new Error('回答内容超过 192 KiB 上限')
      content += delta
      onDelta?.(delta)
    }
    if (typeof choice?.finish_reason === 'string' && choice.finish_reason) finishReason = choice.finish_reason
  }

  for await (const chunk of chunks) {
    buffer += decoder.decode(chunk, { stream: true })
    let boundary = findEventBoundary(buffer)
    while (boundary) {
      consumeEvent(buffer.slice(0, boundary.index))
      buffer = buffer.slice(boundary.index + boundary.length)
      boundary = findEventBoundary(buffer)
    }
  }
  buffer += decoder.decode()
  if (buffer.trim()) consumeEvent(buffer)
  if (!done) throw new Error('上游流式响应缺少 [DONE]')
  return { content, finishReason, done }
}

function findEventBoundary(value: string): { index: number; length: number } | undefined {
  const lf = value.indexOf('\n\n')
  const crlf = value.indexOf('\r\n\r\n')
  if (lf < 0 && crlf < 0) return undefined
  if (crlf >= 0 && (lf < 0 || crlf < lf)) return { index: crlf, length: 4 }
  return { index: lf, length: 2 }
}
