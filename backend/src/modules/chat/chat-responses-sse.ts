import { randomUUID } from 'node:crypto'
import { createReadStream } from 'node:fs'
import { open, unlink, type FileHandle } from 'node:fs/promises'
import { tmpdir } from 'node:os'
import { join } from 'node:path'

import { extractBase64FieldChunks } from './chat-image-result-stream.js'
import type { ChatToolCall } from './tools/contracts.js'

export type ChatResponsesEvent =
  | { type: 'text_delta'; delta: string }
  | { type: 'reasoning_delta'; delta: string }
  | { type: 'reasoning_completed'; item: Record<string, unknown> }
  | { type: 'tool_started'; item: Record<string, unknown> }
  | { type: 'tool_updated'; item: Record<string, unknown> }
  | { type: 'tool_completed'; item: Record<string, unknown> }
  | { type: 'image_started'; item: Record<string, unknown> }
  | { type: 'image_updated'; item: Record<string, unknown> }
  | { type: 'image_completed'; item: Record<string, unknown> }
  | { type: 'image_failed'; item: Record<string, unknown> }
  | { type: 'completed'; response: Record<string, unknown> }
  | { type: 'failed'; error: Record<string, unknown> }

export interface ChatResponsesImageResultSinkInput {
  callId: string
  revisedPrompt?: string
  chunks: AsyncIterable<string>
}

interface ChatResponsesCollectionResult {
  content: string
  inputTokens?: number
  outputTokens?: number
  toolCalls: ChatToolCall[]
  continuationItems: unknown[]
}

type ParsedResponseBlock = {
  event?: ChatResponsesEvent
  imageResultData?: string
  completedOutputItem?: { index: number; item: Record<string, unknown> }
}

interface ImageBlockSpool {
  path: string
  handle: FileHandle
  tail: string
  prefix: string
  bytes: number
}

export async function collectChatResponsesSse(
  chunks: AsyncIterable<Uint8Array>,
  onEvent: (event: ChatResponsesEvent) => void,
  maxBytes = 192 * 1024,
  maxEvents = 65_536,
  onImageResult?: (input: ChatResponsesImageResultSinkInput) => void | Promise<void>
): Promise<ChatResponsesCollectionResult> {
  const decoder = new TextDecoder('utf-8', { fatal: true })
  const encoder = new TextEncoder()
  const maxEventBytes = 64 * 1024
  const maxImageEventBytes = 64 * 1024 * 1024
  const maxAuxiliaryBytes = 192 * 1024
  let buffer = ''
  let content = ''
  let completed = false
  let auxiliaryBytes = 0
  let eventCount = 0
  let inputTokens: number | undefined
  let outputTokens: number | undefined
  let toolCalls: ChatToolCall[] = []
  let continuationItems: unknown[] = []
  const argumentDeltas = new Map<string, string>()
  const completedOutputItems = new Map<number, Record<string, unknown>>()
  const completedImageCallIds = new Set<string>()
  let imageSpool: ImageBlockSpool | undefined
  const consumeEvent = (parsed: ChatResponsesEvent): void => {
    if (parsed.type === 'text_delta') {
      content += parsed.delta
      if (encoder.encode(content).byteLength > maxBytes) throw new Error('模型回答超过 192 KiB 上限')
    } else if (parsed.type === 'reasoning_delta') {
      auxiliaryBytes += encoder.encode(parsed.delta).byteLength
    } else if (parsed.type === 'tool_started' || parsed.type === 'tool_updated' || parsed.type === 'tool_completed') {
      auxiliaryBytes += encoder.encode(JSON.stringify(parsed.item)).byteLength
      if (parsed.type === 'tool_updated') {
        const itemId = stringValue(parsed.item.item_id ?? parsed.item.call_id ?? parsed.item.callId ?? parsed.item.id)
        const delta = stringValue(parsed.item.delta)
        if (itemId && delta) {
          const current = `${argumentDeltas.get(itemId) ?? ''}${delta}`
          if (Buffer.byteLength(current, 'utf8') > 64 * 1024) throw new Error('Responses 单个工具参数超过 64 KiB 上限')
          argumentDeltas.set(itemId, current)
        }
      }
    }
    if (auxiliaryBytes > maxAuxiliaryBytes) throw new Error('模型结构化过程超过 192 KiB 上限')
    if (parsed.type === 'completed') {
      completed = true
      const usage = objectValue(parsed.response.usage)
      inputTokens = nonNegativeInteger(usage.input_tokens) ?? inputTokens
      outputTokens = nonNegativeInteger(usage.output_tokens) ?? outputTokens
      const responseOutput = Array.isArray(parsed.response.output) ? parsed.response.output.map(objectValue) : []
      const output = responseOutput.length
        ? responseOutput
        : [...completedOutputItems.entries()].sort(([left], [right]) => left - right).map(([, item]) => item)
      continuationItems = output
        .filter((item) => ['reasoning', 'function_call'].includes(String(item.type)))
        .map(normalizeResponsesContinuationItem)
      if (Buffer.byteLength(JSON.stringify(continuationItems), 'utf8') > maxAuxiliaryBytes) {
        throw new Error('Responses 工具往返项目超过 192 KiB 上限')
      }
      toolCalls = output
        .map((item, index) => normalizeFunctionCall(item, argumentDeltas, index))
        .filter((value): value is ChatToolCall => value !== undefined)
    }
    onEvent(parsed)
  }
  const consumeBlock = async (block: string): Promise<void> => {
    eventCount += 1
    if (eventCount > maxEvents) throw new Error(`上游 Responses 事件数量超过 ${maxEvents} 上限`)
    const parsed = parseBlock(block)
    if (parsed.completedOutputItem) {
      completedOutputItems.set(parsed.completedOutputItem.index, parsed.completedOutputItem.item)
    }
    const event = parsed.event
    const isImageEvent = event?.type === 'image_started' || event?.type === 'image_updated' || event?.type === 'image_completed' || event?.type === 'image_failed'
    if (encoder.encode(block).byteLength > maxEventBytes && !isImageEvent) throw new Error('上游 Responses 单个事件超过 64 KiB 上限')
    if (event?.type === 'image_completed') {
      const callId = imageCallId(event.item)
      if (!callId || !parsed.imageResultData || completedImageCallIds.has(callId)) return
      completedImageCallIds.add(callId)
      if (onImageResult) {
        await onImageResult({ callId, revisedPrompt: stringValue(event.item.revisedPrompt), chunks: imageResultChunks(parsed.imageResultData) })
      }
    }
    if (event) consumeEvent(event)
  }
  const startImageSpool = async (block: string): Promise<void> => {
    const dataStart = block.search(/(?:^|\r?\n)data:\s*/u)
    if (dataStart < 0) throw new Error('图像 SSE 事件缺少 data 行')
    const marker = block.slice(dataStart).match(/^(?:\r?\n)?data:\s*/u)?.[0]
    if (!marker) throw new Error('图像 SSE data 行无效')
    const json = block.slice(dataStart + marker.length)
    const path = join(tmpdir(), `chat-responses-image-${randomUUID().replace(/-/gu, '')}.json`)
    const handle = await open(path, 'wx')
    const tailLength = Math.min(3, json.length)
    const initial = json.slice(0, json.length - tailLength)
    if (initial) await handle.write(initial)
    imageSpool = {
      path,
      handle,
      tail: json.slice(json.length - tailLength),
      prefix: block.slice(0, maxEventBytes),
      bytes: encoder.encode(json).byteLength
    }
  }
  const consumeImageSpool = async (input: string): Promise<string> => {
    const spool = imageSpool
    if (!spool) return input
    const combined = spool.tail + input
    const boundary = findBoundary(combined)
    if (!boundary) {
      const tailLength = Math.min(3, combined.length)
      const writable = combined.slice(0, combined.length - tailLength)
      if (writable) await spool.handle.write(writable)
      spool.tail = combined.slice(combined.length - tailLength)
      spool.bytes += encoder.encode(input).byteLength
      if (spool.bytes > maxImageEventBytes) throw new Error('图像 SSE 事件超过 64 MiB 上限')
      return ''
    }
    const finalJson = combined.slice(0, boundary.index)
    spool.bytes += encoder.encode(input.slice(0, Math.max(0, boundary.index - spool.tail.length))).byteLength
    if (spool.bytes > maxImageEventBytes) throw new Error('图像 SSE 事件超过 64 MiB 上限')
    if (finalJson) await spool.handle.write(finalJson)
    await spool.handle.close()
    imageSpool = undefined
    try {
      await consumeSpooledImageBlock(spool, consumeEvent, completedImageCallIds, onImageResult, maxEvents, ++eventCount)
    } finally {
      await unlink(spool.path).catch(() => undefined)
    }
    return combined.slice(boundary.index + boundary.length)
  }
  try {
    for await (const chunk of chunks) {
      let decoded = decoder.decode(chunk, { stream: true })
      if (imageSpool) decoded = await consumeImageSpool(decoded)
      buffer += decoded
      const split = await consumeBlocks(buffer, consumeBlock)
      buffer = split.rest
      if (encoder.encode(buffer).byteLength > maxEventBytes) {
        if (!isPendingImageBlock(buffer)) throw new Error('上游 Responses 单个事件超过 64 KiB 上限')
        await startImageSpool(buffer)
        buffer = ''
      }
    }
    let tail = decoder.decode()
    if (imageSpool) tail = await consumeImageSpool(tail)
    if (imageSpool) throw new Error('图像 SSE 事件被截断')
    buffer += tail
    if (encoder.encode(buffer).byteLength > maxEventBytes && !isPendingImageBlock(buffer)) throw new Error('上游 Responses 单个事件超过 64 KiB 上限')
    if (buffer.trim()) await consumeBlock(buffer)
  } catch (error) {
    const pending = imageSpool
    imageSpool = undefined
    if (pending) {
      await pending.handle.close().catch(() => undefined)
      await unlink(pending.path).catch(() => undefined)
    }
    if (completed) return { content, inputTokens, outputTokens, toolCalls, continuationItems }
    throw error
  }
  if (!completed) throw new Error('上游 Responses 流缺少 response.completed')
  return { content, inputTokens, outputTokens, toolCalls, continuationItems }
}

async function consumeBlocks(input: string, onBlock: (block: string) => Promise<void>): Promise<{ rest: string }> {
  let rest = input
  while (true) {
    const lf = rest.indexOf('\n\n')
    const crlf = rest.indexOf('\r\n\r\n')
    const index = crlf >= 0 && (lf < 0 || crlf < lf) ? crlf : lf
    if (index < 0) return { rest }
    const length = index === crlf ? 4 : 2
    await onBlock(rest.slice(0, index))
    rest = rest.slice(index + length)
  }
}

function parseBlock(block: string): ParsedResponseBlock {
  const eventName = block.match(/^event:\s*(.+)$/m)?.[1]?.trim()
  const data = block.split(/\r?\n/).filter((line) => line.startsWith('data:')).map((line) => line.slice(5).trimStart()).join('\n')
  if (!data || data === '[DONE]') return {}
  if (eventName && isExplicitImageBlock(eventName, eventName)) return parseImageBlock(eventName, data)
  let payload: Record<string, unknown>
  try { payload = JSON.parse(data) as Record<string, unknown> } catch { return {} }
  const type = String(payload.type ?? eventName ?? '')
  if (isExplicitImageBlock(eventName, type)) return parseImageBlock(eventName ?? type, data)
  if (type === 'response.output_text.delta') return { event: { type: 'text_delta', delta: String(payload.delta ?? '') } }
  if (type === 'response.reasoning_summary_text.delta' || type === 'response.reasoning_text.delta') return { event: { type: 'reasoning_delta', delta: String(payload.delta ?? '') } }
  if (type === 'response.output_item.added') {
    const item = objectValue(payload.item)
    if (String(item.type) === 'image_generation_call') {
      const callId = imageCallId(item)
      return { event: { type: 'image_started', item: callId ? { ...item, callId } : item } }
    }
    return ['function_call', 'computer_call', 'web_search_call', 'file_search_call'].includes(String(item.type)) ? { event: { type: 'tool_started', item } } : {}
  }
  if (type === 'response.function_call_arguments.delta') return { event: { type: 'tool_updated', item: payload } }
  if (type === 'response.output_item.done') {
    const item = objectValue(payload.item)
    const index = nonNegativeInteger(payload.output_index)
    const completedOutputItem = index === undefined ? undefined : { index, item }
    return ['function_call', 'computer_call', 'web_search_call', 'file_search_call'].includes(String(item.type))
      ? { event: { type: 'tool_completed', item }, completedOutputItem }
      : String(item.type) === 'reasoning'
        ? { event: { type: 'reasoning_completed', item }, completedOutputItem }
        : {}
  }
  if (type === 'response.completed') return { event: { type: 'completed', response: objectValue(payload.response) } }
  if (type === 'response.failed') return { event: { type: 'failed', error: objectValue(payload.response ?? payload.error) } }
  return {}
}

function findBoundary(value: string): { index: number; length: number } | undefined {
  const lf = value.indexOf('\n\n')
  const crlf = value.indexOf('\r\n\r\n')
  if (lf < 0 && crlf < 0) return undefined
  return crlf >= 0 && (lf < 0 || crlf < lf) ? { index: crlf, length: 4 } : { index: lf, length: 2 }
}

async function consumeSpooledImageBlock(
  spool: ImageBlockSpool,
  consumeEvent: (event: ChatResponsesEvent) => void,
  completedCallIds: Set<string>,
  onImageResult: ((input: ChatResponsesImageResultSinkInput) => void | Promise<void>) | undefined,
  maxEvents: number,
  eventCount: number
): Promise<void> {
  if (eventCount > maxEvents) throw new Error(`上游 Responses 事件数量超过 ${maxEvents} 上限`)
  const eventName = spool.prefix.match(/^event:\s*(.+)$/m)?.[1]?.trim()
  if (eventName === 'response.completed') {
    const response = await readSpooledCompletedResponse(spool.path)
    const images = completedResponseImages(response)
    for (let index = 0; index < images.length; index += 1) {
      const image = images[index]!
      if (completedCallIds.has(image.callId)) continue
      completedCallIds.add(image.callId)
      if (onImageResult) {
        await onImageResult({
          callId: image.callId,
          revisedPrompt: image.revisedPrompt,
          chunks: extractSpooledImageResultChunks(spool.path, index)
        })
      }
      consumeEvent({
        type: 'image_completed',
        item: {
          callId: image.callId,
          status: 'completed',
          ...(image.revisedPrompt ? { revisedPrompt: image.revisedPrompt } : {})
        }
      })
    }
    consumeEvent({ type: 'completed', response })
    return
  }
  const parsed = parseBlock(spool.prefix)
  const event = parsed.event
  if (!event) return
  if (event.type === 'image_completed') {
    const callId = imageCallId(event.item)
    if (!callId || completedCallIds.has(callId)) return
    completedCallIds.add(callId)
    if (onImageResult) {
      await onImageResult({ callId, revisedPrompt: stringValue(event.item.revisedPrompt), chunks: extractBase64FieldChunks(fileTextChunks(spool.path), 'result') })
    }
  }
  consumeEvent(event)
}

async function readSpooledCompletedResponse(path: string): Promise<Record<string, unknown>> {
  let sanitized = ''
  let inString = false
  let escaped = false
  let stringToken = ''
  let stringTokenEscaped = false
  let stringIsValue = false
  let stringIsImageResult = false
  let lastStringToken: string | undefined
  let awaitingValue = false
  let valueKey: string | undefined

  for await (const chunk of fileTextChunks(path)) {
    for (const character of chunk) {
      if (inString) {
        if (stringIsImageResult) {
          if (escaped) throw new Error('生成图片 Base64 不允许 JSON 转义')
          if (character === '\\') {
            escaped = true
            continue
          }
          if (character === '"') {
            sanitized += '"'
            inString = false
            stringIsImageResult = false
            lastStringToken = undefined
          }
          continue
        }
        sanitized += character
        if (escaped) {
          escaped = false
          stringTokenEscaped = true
          continue
        }
        if (character === '\\') {
          escaped = true
          stringTokenEscaped = true
          continue
        }
        if (character === '"') {
          inString = false
          lastStringToken = stringIsValue || stringTokenEscaped ? undefined : stringToken
          continue
        }
        if (!stringTokenEscaped && stringToken.length <= 64) stringToken += character
        continue
      }

      sanitized += character
      if (character === '"') {
        inString = true
        escaped = false
        stringToken = ''
        stringTokenEscaped = false
        stringIsValue = awaitingValue
        stringIsImageResult = awaitingValue && isImageResultField(valueKey)
        awaitingValue = false
        valueKey = undefined
        lastStringToken = undefined
        continue
      }
      if (/\s/u.test(character)) continue
      if (character === ':') {
        valueKey = lastStringToken
        awaitingValue = lastStringToken !== undefined
        lastStringToken = undefined
        continue
      }
      if (awaitingValue) {
        awaitingValue = false
        valueKey = undefined
      }
      lastStringToken = undefined
    }
    if (Buffer.byteLength(sanitized, 'utf8') > 512 * 1024) throw new Error('上游 Responses 终态元数据超过 512 KiB 上限')
  }
  if (inString) throw new Error('上游 Responses 终态 JSON 被截断')
  let payload: Record<string, unknown>
  try { payload = JSON.parse(sanitized) as Record<string, unknown> } catch { throw new Error('上游 Responses 终态 JSON 无法解析') }
  return objectValue(payload.response)
}

function completedResponseImages(response: Record<string, unknown>): Array<{ callId: string; revisedPrompt?: string }> {
  const output = Array.isArray(response.output) ? response.output : []
  const images: Array<{ callId: string; revisedPrompt?: string }> = []
  for (const value of output) {
    const item = objectValue(value)
    if (String(item.type) !== 'image_generation_call') continue
    if (!Object.hasOwn(item, 'result') && !Object.hasOwn(item, 'b64_json')) continue
    const callId = imageCallId(item)
    if (!callId) throw new Error('上游 Responses 生成图片缺少 callId')
    images.push({ callId, revisedPrompt: stringValue(item.revised_prompt ?? item.revisedPrompt) })
  }
  return images
}

async function* extractSpooledImageResultChunks(path: string, targetIndex: number): AsyncGenerator<string> {
  let inString = false
  let escaped = false
  let stringToken = ''
  let stringTokenEscaped = false
  let stringIsValue = false
  let stringIsImageResult = false
  let selectedResult = false
  let currentIndex = -1
  let emitted = false
  let lastStringToken: string | undefined
  let awaitingValue = false
  let valueKey: string | undefined

  for await (const chunk of fileTextChunks(path)) {
    let output = ''
    for (const character of chunk) {
      if (inString) {
        if (stringIsImageResult) {
          if (escaped || character === '\\') throw new Error('生成图片 Base64 不允许 JSON 转义')
          if (character === '"') {
            inString = false
            stringIsImageResult = false
            if (selectedResult) {
              if (output) { yield output; emitted = true }
              if (!emitted) throw new Error('生成图片 Base64 不能为空')
              return
            }
            selectedResult = false
            lastStringToken = undefined
            continue
          }
          if (selectedResult) output += character
          continue
        }
        if (escaped) {
          escaped = false
          stringTokenEscaped = true
          continue
        }
        if (character === '\\') {
          escaped = true
          stringTokenEscaped = true
          continue
        }
        if (character === '"') {
          inString = false
          lastStringToken = stringIsValue || stringTokenEscaped ? undefined : stringToken
          continue
        }
        if (!stringTokenEscaped && stringToken.length <= 64) stringToken += character
        continue
      }

      if (character === '"') {
        inString = true
        escaped = false
        stringToken = ''
        stringTokenEscaped = false
        stringIsValue = awaitingValue
        stringIsImageResult = awaitingValue && isImageResultField(valueKey)
        if (stringIsImageResult) {
          currentIndex += 1
          selectedResult = currentIndex === targetIndex
        }
        awaitingValue = false
        valueKey = undefined
        lastStringToken = undefined
        continue
      }
      if (/\s/u.test(character)) continue
      if (character === ':') {
        valueKey = lastStringToken
        awaitingValue = lastStringToken !== undefined
        lastStringToken = undefined
        continue
      }
      if (awaitingValue) {
        awaitingValue = false
        valueKey = undefined
      }
      lastStringToken = undefined
    }
    if (output) { yield output; emitted = true }
  }
  throw new Error(`生成图片 Base64 第 ${targetIndex + 1} 项缺失或截断`)
}

function isImageResultField(value: string | undefined): boolean {
  return value === 'result' || value === 'b64_json'
}

async function* fileTextChunks(path: string): AsyncGenerator<string> {
  const stream = createReadStream(path, { encoding: 'utf8', highWaterMark: 16 * 1024 })
  for await (const chunk of stream) {
    const value = String(chunk)
    yield value
  }
}

function isPendingImageBlock(block: string): boolean {
  return /(?:^|\r?\n)event:\s*[^\r\n]*image_generation_call/u.test(block)
    || /"type"\s*:\s*"image_generation_call"/u.test(block)
}

function isExplicitImageBlock(eventName: string | undefined, type: string): boolean {
  return eventName?.includes('image_generation_call') === true
    || type.includes('image_generation_call')
    || type === 'image_generation'
    || type.startsWith('image_generation.')
}

function parseImageBlock(eventName: string, data: string): ParsedResponseBlock {
  const callId = extractJsonString(data, 'call_id') ?? extractJsonString(data, 'item_id') ?? extractJsonString(data, 'id')
  const status = extractJsonString(data, 'status')
  const revisedPrompt = extractJsonString(data, 'revised_prompt')
  const item = {
    ...(callId ? { callId } : {}),
    ...(status ? { status } : {}),
    ...(revisedPrompt ? { revisedPrompt } : {})
  }
  const hasResult = hasJsonStringField(data, 'result') || hasJsonStringField(data, 'b64_json')
  const type = eventName || extractJsonString(data, 'type') || ''
  if (type.includes('failed')) return { event: { type: 'image_failed', item } }
  if (type.includes('partial_image') || type.includes('in_progress')) return { event: { type: 'image_updated', item } }
  if (type.includes('completed') || type.includes('done') || type.includes('output_item')) {
    if (hasResult) return { event: { type: 'image_completed', item }, imageResultData: data }
    return { event: { type: type.includes('added') ? 'image_started' : 'image_failed', item } }
  }
  return { event: { type: 'image_started', item } }
}

function extractJsonString(data: string, field: string): string | undefined {
  const match = data.match(new RegExp(`"${field}"\\s*:\\s*"([^"\\\\]{1,512})"`, 'u'))
  return match?.[1]
}

function hasJsonStringField(data: string, field: string): boolean {
  return new RegExp(`"${field}"\\s*:\\s*"`, 'u').test(data)
}

async function* imageResultChunks(data: string): AsyncGenerator<string> {
  const field = data.match(/"(?:result|b64_json)"\s*:\s*"/u)
  if (!field || field.index === undefined) return
  const start = field.index + field[0].length
  const end = data.lastIndexOf('"')
  if (end <= start) return
  for (let offset = start; offset < end; offset += 4096) yield data.slice(offset, Math.min(end, offset + 4096))
}

function normalizeFunctionCall(
  item: Record<string, unknown>,
  argumentDeltas: ReadonlyMap<string, string>,
  sourceOrder: number
): ChatToolCall | undefined {
  if (String(item.type) !== 'function_call') return undefined
  const callId = stringValue(item.call_id ?? item.callId)
  const toolName = stringValue(item.name)
  const itemId = stringValue(item.id)
  const argumentsJson = stringValue(item.arguments)
    ?? (itemId ? argumentDeltas.get(itemId) : undefined)
    ?? (callId ? argumentDeltas.get(callId) : undefined)
  if (!callId || !toolName || !argumentsJson) throw new Error('Responses function_call 缺少 call_id、name 或 arguments')
  return { callId, toolName, argumentsJson, sourceOrder }
}

function normalizeResponsesContinuationItem(item: Record<string, unknown>): Record<string, unknown> {
  if (String(item.type) !== 'function_call') return item
  const callId = stringValue(item.call_id ?? item.callId)
  if (!callId) return item
  const fallbackId = `fc_${callId.replace(/^call[_-]/u, '').replace(/[^A-Za-z0-9_-]/gu, '_').slice(0, 60)}`
  return {
    ...item,
    id: stringValue(item.id) ?? fallbackId,
    status: stringValue(item.status) ?? 'completed'
  }
}

function objectValue(value: unknown): Record<string, unknown> { return value && typeof value === 'object' && !Array.isArray(value) ? value as Record<string, unknown> : {} }
function nonNegativeInteger(value: unknown): number | undefined { const number = Number(value); return Number.isSafeInteger(number) && number >= 0 ? number : undefined }

function imageCallId(item: Record<string, unknown>): string | undefined {
  const value = item.callId ?? item.call_id ?? item.id
  return typeof value === 'string' && value.trim() ? value.trim() : undefined
}

function stringValue(value: unknown): string | undefined { return typeof value === 'string' && value.trim() ? value.trim() : undefined }
