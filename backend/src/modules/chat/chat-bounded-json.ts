export async function readChatJsonResponse(response: Response, maxBytes: number): Promise<unknown> {
  const text = await readResponseTextWithLimit(response, maxBytes)
  try {
    return JSON.parse(text) as unknown
  } catch {
    return { message: text }
  }
}

async function readResponseTextWithLimit(response: Response, maxBytes: number): Promise<string> {
  if (!response.body) return ''
  const reader = response.body.getReader()
  const decoder = new TextDecoder('utf-8', { fatal: true })
  let totalBytes = 0
  let text = ''
  try {
    while (true) {
      const next = await reader.read()
      if (next.done) break
      totalBytes += next.value.byteLength
      if (totalBytes > maxBytes) {
        await reader.cancel()
        throw new Error(`上游响应超过 ${formatKiB(maxBytes)} KiB 上限`)
      }
      text += decoder.decode(next.value, { stream: true })
    }
    return text + decoder.decode()
  } finally {
    reader.releaseLock()
  }
}

function formatKiB(bytes: number): number {
  return Math.max(1, Math.floor(bytes / 1024))
}
