import type {
  ModelCheckProgressEvent,
  ModelCheckRunDetail,
  ModelCheckRunPayload
} from '@/types/domain'
import type { ModelCheckScopeParams, ModelCheckStreamOptions } from './contracts'
import { normalizeApiBaseUrl, queryString, readFetchErrorMessage } from './http'

export async function runModelCheckStream(path: string, payload: ModelCheckRunPayload, streamOptions?: ModelCheckStreamOptions, params?: ModelCheckScopeParams): Promise<ModelCheckRunDetail> {
  const response = await fetch(`${normalizeApiBaseUrl(import.meta.env.VITE_JUHE_AI_API_BASE_URL as string | undefined)}${path}${queryString(params)}`, {
    method: 'POST',
    credentials: 'include',
    headers: {
      accept: 'text/event-stream',
      'content-type': 'application/json'
    },
    body: JSON.stringify(payload),
    signal: streamOptions?.signal
  })
  if (!response.ok) {
    throw new Error(await readFetchErrorMessage(response, path))
  }
  if (!response.body) {
    throw new Error('模型检测进度流不可用')
  }

  const reader = response.body.getReader()
  const decoder = new TextDecoder()
  let buffer = ''
  let completedDetail: ModelCheckRunDetail | undefined
  const handleMessage = (raw: string): void => {
    const event = parseServerSentEvent(raw)
    if (!event.data) return
    const parsedPayload = parseJsonPayload(event.data)
    if (event.event === 'progress') {
      streamOptions?.onProgress?.(parsedPayload as ModelCheckProgressEvent)
      return
    }
    if (event.event === 'complete') {
      completedDetail = parsedPayload as ModelCheckRunDetail
      streamOptions?.onComplete?.(completedDetail)
      return
    }
    if (event.event === 'error') {
      const error = parsedPayload as { message?: string; statusCode?: number }
      streamOptions?.onError?.(error)
      throw new Error(error.message || '模型检测失败')
    }
  }

  while (true) {
    const { done, value } = await reader.read()
    if (done) break
    buffer += decoder.decode(value, { stream: true })
    buffer = flushServerSentEvents(buffer, handleMessage)
  }
  buffer += decoder.decode()
  flushServerSentEvents(buffer, handleMessage, true)
  if (!completedDetail) {
    throw new Error('模型检测进度流未返回完成结果')
  }
  return completedDetail
}

function flushServerSentEvents(buffer: string, handleMessage: (raw: string) => void, flushRemaining = false): string {
  let normalized = buffer.replace(/\r\n/g, '\n')
  let separatorIndex = normalized.indexOf('\n\n')
  while (separatorIndex >= 0) {
    const raw = normalized.slice(0, separatorIndex)
    if (raw.trim()) {
      handleMessage(raw)
    }
    normalized = normalized.slice(separatorIndex + 2)
    separatorIndex = normalized.indexOf('\n\n')
  }
  if (flushRemaining && normalized.trim()) {
    handleMessage(normalized)
    return ''
  }
  return normalized
}

function parseServerSentEvent(raw: string): { event: string; data: string } {
  let event = 'message'
  const dataLines: string[] = []
  for (const line of raw.split('\n')) {
    if (line.startsWith('event:')) {
      event = line.slice(6).trim()
    } else if (line.startsWith('data:')) {
      dataLines.push(line.slice(5).trimStart())
    }
  }
  return { event, data: dataLines.join('\n') }
}

function parseJsonPayload(text: string): unknown {
  try {
    return JSON.parse(text) as unknown
  } catch {
    return { message: text }
  }
}
