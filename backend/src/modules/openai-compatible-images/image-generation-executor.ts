import { runtimeConfig } from '../../config/runtime.js'
import { GatewayRequestValidationError } from '../gateway/request/validation-error.js'
import type {
  OpenAIToAnthropicImageGenerationExecutor,
  OpenAIToAnthropicImageGenerationInput,
  OpenAIToAnthropicImageGenerationResult,
  OpenAIToAnthropicImageGenerationStreamEvent
} from '../providers/drivers/_shared/openai-anthropic-bridge.js'

type JsonRecord = Record<string, unknown>

export class OpenAICompatibleImageGenerationProviderError extends Error {
  readonly statusCode: number
  readonly type: string
  readonly code: string
  readonly moderationDetails?: JsonRecord

  constructor(input: {
    message: string
    statusCode: number
    type: string
    code: string
    moderationDetails?: JsonRecord
  }) {
    super(input.message)
    this.statusCode = input.statusCode
    this.type = input.type
    this.code = input.code
    this.moderationDetails = input.moderationDetails
  }
}

export function openAICompatibleImageGenerationExecutorForGatewayRequest(): OpenAIToAnthropicImageGenerationExecutor | undefined {
  const endpoint = runtimeConfig.imageGenerationProvider.endpoint
  if (!endpoint) return undefined
  return {
    async generate(input) {
      const providerBody = imageGenerationProviderRequestBody(input.prompt, input.tool)
      const response = await requestImageGenerationProvider(endpoint, providerBody, 'application/json', input.signal)

      const text = await readResponseTextWithLimit(response, runtimeConfig.imageGenerationProvider.maxBodyBytes)
      const parsed = safeParseJson(text)
      if (!response.ok) {
        throw imageGenerationProviderErrorFromResponse(response, parsed)
      }
      return imageGenerationResultFromJson(parsed, input.tool.outputFormat)
    },
    async * generateStream(input) {
      const providerBody = imageGenerationProviderRequestBody(input.prompt, input.tool, { stream: true })
      const response = await requestImageGenerationProvider(endpoint, providerBody, 'text/event-stream', input.signal)
      if (!response.ok) {
        const text = await readResponseTextWithLimit(response, runtimeConfig.imageGenerationProvider.maxBodyBytes)
        throw imageGenerationProviderErrorFromResponse(response, safeParseJson(text))
      }
      if (!response.body || !isTextEventStream(response)) {
        const text = await readResponseTextWithLimit(response, runtimeConfig.imageGenerationProvider.maxBodyBytes)
        yield { type: 'completed', result: imageGenerationResultFromJson(safeParseJson(text), input.tool.outputFormat) }
        return
      }
      yield * iterateImageGenerationProviderSse(response, input)
    }
  }
}

function imageGenerationProviderRequestBody(prompt: string, tool: {
  size?: string
  quality?: string
  outputFormat?: string
  outputCompression?: number
  partialImages?: number
  moderation?: string
  background?: string
}, options: { stream?: boolean } = {}): JsonRecord {
  if (runtimeConfig.imageGenerationProvider.api === 'responses') {
    return imageGenerationResponsesProviderRequestBody(prompt, tool, options)
  }
  return imageGenerationImagesProviderRequestBody(prompt, tool, options)
}

function imageGenerationImagesProviderRequestBody(prompt: string, tool: {
  size?: string
  quality?: string
  outputFormat?: string
  outputCompression?: number
  partialImages?: number
  moderation?: string
  background?: string
}, options: { stream?: boolean } = {}): JsonRecord {
  const body: JsonRecord = {
    model: runtimeConfig.imageGenerationProvider.model,
    prompt,
    n: 1
  }
  setOptionalString(body, 'size', tool.size)
  setOptionalString(body, 'quality', tool.quality)
  setOptionalString(body, 'output_format', tool.outputFormat)
  if (typeof tool.outputCompression === 'number') body.output_compression = tool.outputCompression
  setOptionalString(body, 'moderation', tool.moderation)
  setOptionalString(body, 'background', tool.background)
  if (options.stream) {
    body.stream = true
    if (typeof tool.partialImages === 'number') body.partial_images = Math.max(0, Math.min(3, Math.trunc(tool.partialImages)))
  }
  return body
}

function imageGenerationResponsesProviderRequestBody(prompt: string, tool: {
  size?: string
  quality?: string
  outputFormat?: string
  outputCompression?: number
  partialImages?: number
  moderation?: string
  background?: string
}, options: { stream?: boolean } = {}): JsonRecord {
  const body: JsonRecord = {
    model: runtimeConfig.imageGenerationProvider.model,
    input: prompt,
    tools: [imageGenerationResponsesProviderTool(tool, options)],
    tool_choice: { type: 'image_generation' }
  }
  if (options.stream) body.stream = true
  return body
}

function imageGenerationResponsesProviderTool(tool: {
  size?: string
  quality?: string
  outputFormat?: string
  outputCompression?: number
  partialImages?: number
  moderation?: string
  background?: string
}, options: { stream?: boolean } = {}): JsonRecord {
  const body: JsonRecord = {
    type: 'image_generation',
    action: 'generate'
  }
  setOptionalString(body, 'size', tool.size)
  setOptionalString(body, 'quality', tool.quality)
  setOptionalString(body, 'output_format', tool.outputFormat)
  if (typeof tool.outputCompression === 'number') body.output_compression = tool.outputCompression
  setOptionalString(body, 'moderation', tool.moderation)
  setOptionalString(body, 'background', tool.background)
  if (options.stream && typeof tool.partialImages === 'number') {
    body.partial_images = Math.max(0, Math.min(3, Math.trunc(tool.partialImages)))
  }
  return body
}

async function requestImageGenerationProvider(
  endpoint: string,
  providerBody: JsonRecord,
  accept: string,
  signal: AbortSignal | undefined
): Promise<Response> {
  const headers = new Headers()
  headers.set('accept', accept)
  headers.set('content-type', 'application/json')
  if (runtimeConfig.imageGenerationProvider.apiKey) {
    headers.set('authorization', `Bearer ${runtimeConfig.imageGenerationProvider.apiKey}`)
  }
  const timeoutController = new AbortController()
  const timeout = setTimeout(() => timeoutController.abort(), runtimeConfig.imageGenerationProvider.timeoutMs)
  const requestSignal = signal
    ? AbortSignal.any([signal, timeoutController.signal])
    : timeoutController.signal
  try {
    return await fetch(endpoint, {
      method: 'POST',
      headers,
      body: JSON.stringify(providerBody),
      signal: requestSignal
    })
  } catch (error) {
    if (timeoutController.signal.aborted) {
      throw new GatewayRequestValidationError(
        '图像生成 provider 请求超时',
        'openai_anthropic_bridge_image_generation_provider_timeout',
        { statusCode: 504, type: 'upstream_error' }
      )
    }
    throw new GatewayRequestValidationError(
      error instanceof Error ? `图像生成 provider 请求失败：${error.message}` : '图像生成 provider 请求失败',
      'openai_anthropic_bridge_image_generation_provider_request_failed',
      { statusCode: 502, type: 'upstream_error' }
    )
  } finally {
    clearTimeout(timeout)
  }
}

async function * iterateImageGenerationProviderSse(
  response: Response,
  input: OpenAIToAnthropicImageGenerationInput
): AsyncIterable<OpenAIToAnthropicImageGenerationStreamEvent> {
  const reader = response.body?.getReader()
  if (!reader) {
    throw invalidProviderResponse('图像生成 provider streaming 响应缺少 body')
  }
  let buffer = ''
  let total = 0
  let completed = false
  while (true) {
    const { done, value } = await reader.read()
    if (done) break
    if (!value) continue
    total += value.byteLength
    if (total > runtimeConfig.imageGenerationProvider.maxBodyBytes) {
      await reader.cancel()
      throw new GatewayRequestValidationError(
        '图像生成 provider 响应体超过读取上限',
        'openai_anthropic_bridge_image_generation_provider_response_too_large',
        { statusCode: 502, type: 'upstream_error' }
      )
    }
    buffer += Buffer.from(value).toString('utf8')
    while (true) {
      const frame = takeNextSseFrame(buffer)
      if (!frame) break
      buffer = frame.rest
      const event = imageGenerationProviderStreamEventFromSseFrame(frame.frame, input.tool.outputFormat)
      if (!event) continue
      if (event.type === 'completed') completed = true
      yield event
    }
  }
  const trailingEvent = imageGenerationProviderStreamEventFromSseFrame(buffer, input.tool.outputFormat)
  if (trailingEvent) {
    if (trailingEvent.type === 'completed') completed = true
    yield trailingEvent
  }
  if (!completed) {
    throw invalidProviderResponse('图像生成 provider streaming 响应缺少最终图片结果')
  }
}

function takeNextSseFrame(buffer: string): { frame: string; rest: string } | undefined {
  const crlfIndex = buffer.indexOf('\r\n\r\n')
  const lfIndex = buffer.indexOf('\n\n')
  const index = crlfIndex === -1
    ? lfIndex
    : lfIndex === -1 ? crlfIndex : Math.min(crlfIndex, lfIndex)
  if (index < 0) return undefined
  const delimiterLength = buffer.startsWith('\r\n\r\n', index) ? 4 : 2
  return {
    frame: buffer.slice(0, index),
    rest: buffer.slice(index + delimiterLength)
  }
}

function imageGenerationProviderStreamEventFromSseFrame(
  frame: string,
  outputFormat: string | undefined
): OpenAIToAnthropicImageGenerationStreamEvent | undefined {
  const parsedFrame = parseSseFrame(frame)
  if (!parsedFrame || parsedFrame.data === '[DONE]') return undefined
  const parsed = safeParseJson(parsedFrame.data)
  const record = isPlainObject(parsed) ? parsed : {}
  const eventType = parsedFrame.event ?? stringValue(record.type)
  if (eventType === 'image_generation.partial_image' || eventType === 'response.image_generation_call.partial_image') {
    const imageBase64 = stringValue(record.b64_json) ?? stringValue(record.partial_image_b64)
    if (!imageBase64 || !looksLikeBase64(imageBase64)) {
      throw invalidProviderResponse('图像生成 provider partial image 响应缺少 b64_json')
    }
    return {
      type: 'partial_image',
      partial: {
        imageBase64,
        partialImageIndex: integerValue(record.partial_image_index)
      }
    }
  }
  if (
    eventType === 'image_generation.completed'
    || eventType === 'response.image_generation_call.completed'
    || eventType === 'response.completed'
  ) {
    return {
      type: 'completed',
      result: imageGenerationResultFromProviderRecord(record, outputFormat)
    }
  }
  if (
    eventType === 'error'
    || eventType === 'response.failed'
    || eventType === 'response.image_generation_call.failed'
    || isPlainObject(record.error)
  ) {
    const response = objectValue(record.response)
    throw imageGenerationProviderErrorFromResponse({ status: 502 } as Response, objectValue(record.error) ?? objectValue(response?.error) ?? record)
  }
  return undefined
}

function parseSseFrame(frame: string): { event?: string; data: string } | undefined {
  const dataLines: string[] = []
  let event: string | undefined
  for (const rawLine of frame.split(/\r?\n/)) {
    if (!rawLine || rawLine.startsWith(':')) continue
    const colonIndex = rawLine.indexOf(':')
    const field = colonIndex >= 0 ? rawLine.slice(0, colonIndex) : rawLine
    const rawValue = colonIndex >= 0 ? rawLine.slice(colonIndex + 1) : ''
    const value = rawValue.startsWith(' ') ? rawValue.slice(1) : rawValue
    if (field === 'event') event = value
    if (field === 'data') dataLines.push(value)
  }
  if (!dataLines.length) return undefined
  return { event, data: dataLines.join('\n') }
}

function isTextEventStream(response: Response): boolean {
  return (response.headers.get('content-type') ?? '').toLowerCase().includes('text/event-stream')
}

async function readResponseTextWithLimit(response: Response, maxBodyBytes: number): Promise<string> {
  if (!response.body) return await response.text()
  const reader = response.body.getReader()
  const chunks: Uint8Array[] = []
  let total = 0
  while (true) {
    const { done, value } = await reader.read()
    if (done) break
    if (!value) continue
    total += value.byteLength
    if (total > maxBodyBytes) {
      await reader.cancel()
      throw new GatewayRequestValidationError(
        '图像生成 provider 响应体超过读取上限',
        'openai_anthropic_bridge_image_generation_provider_response_too_large',
        { statusCode: 502, type: 'upstream_error' }
      )
    }
    chunks.push(value)
  }
  return Buffer.concat(chunks.map((chunk) => Buffer.from(chunk))).toString('utf8')
}

function imageGenerationDataItem(value: unknown): JsonRecord | undefined {
  const record = isPlainObject(value) ? value : {}
  const data = Array.isArray(record.data) ? record.data : []
  const first = data[0]
  return isPlainObject(first) ? first : undefined
}

function imageGenerationOutputItem(value: unknown): JsonRecord | undefined {
  const record = isPlainObject(value) ? value : {}
  const output = Array.isArray(record.output) ? record.output : []
  return output.map(objectValue).find((item) => item?.type === 'image_generation_call')
}

function imageGenerationResultFromJson(value: unknown, outputFormat: string | undefined): OpenAIToAnthropicImageGenerationResult {
  return imageGenerationResultFromProviderRecord(objectValue(value) ?? {}, outputFormat)
}

function imageGenerationResultFromProviderRecord(record: JsonRecord, outputFormat: string | undefined): OpenAIToAnthropicImageGenerationResult {
  const first = imageGenerationDataItem(record)
  const response = objectValue(record.response)
  const item = objectValue(record.item)
  const outputItem = imageGenerationOutputItem(record)
    ?? imageGenerationOutputItem(response)
    ?? (item?.type === 'image_generation_call' ? item : undefined)
  const imageBase64 = stringValue(record.b64_json)
    ?? stringValue(record.result)
    ?? stringValue(record.partial_image_b64)
    ?? stringValue(item?.result)
    ?? stringValue(outputItem?.result)
    ?? stringValue(first?.b64_json)
  if (!imageBase64 || !looksLikeBase64(imageBase64)) {
    throw invalidProviderResponse('图像生成 provider 响应缺少 data[0].b64_json')
  }
  return {
    imageBase64,
    revisedPrompt: stringValue(record.revised_prompt)
      ?? stringValue(record.prompt)
      ?? stringValue(item?.revised_prompt)
      ?? stringValue(item?.prompt)
      ?? stringValue(outputItem?.revised_prompt)
      ?? stringValue(outputItem?.prompt)
      ?? stringValue(first?.revised_prompt)
      ?? stringValue(first?.prompt),
    outputFormat: stringValue(outputFormat)
  }
}

function imageGenerationProviderErrorFromResponse(response: Response, parsed: unknown): OpenAICompatibleImageGenerationProviderError {
  const error = isPlainObject(parsed) ? objectValue(parsed.error) ?? parsed : {}
  return new OpenAICompatibleImageGenerationProviderError({
    message: stringValue(error.message) ?? `图像生成 provider 返回 HTTP ${response.status}`,
    code: stringValue(error.code) ?? 'openai_anthropic_bridge_image_generation_provider_error',
    statusCode: response.status >= 400 && response.status < 500 ? 400 : 502,
    type: stringValue(error.type) ?? 'upstream_error',
    moderationDetails: objectValue(error.moderation_details)
  })
}

function invalidProviderResponse(message: string): GatewayRequestValidationError {
  return new GatewayRequestValidationError(
    message,
    'openai_anthropic_bridge_image_generation_provider_invalid_response',
    { statusCode: 502, type: 'upstream_error' }
  )
}

function setOptionalString(target: JsonRecord, key: string, value: string | undefined): void {
  if (value) target[key] = value
}

function safeParseJson(text: string): unknown {
  if (!text) return {}
  try {
    return JSON.parse(text) as unknown
  } catch {
    return {}
  }
}

function isPlainObject(value: unknown): value is JsonRecord {
  return typeof value === 'object' && value !== null && !Array.isArray(value)
}

function objectValue(value: unknown): JsonRecord | undefined {
  return isPlainObject(value) ? value : undefined
}

function stringValue(value: unknown): string | undefined {
  return typeof value === 'string' && value.trim() ? value : undefined
}

function integerValue(value: unknown): number | undefined {
  const number = typeof value === 'number'
    ? value
    : typeof value === 'string' && value.trim() ? Number(value) : NaN
  if (!Number.isFinite(number)) return undefined
  return Math.trunc(number)
}

function looksLikeBase64(value: string): boolean {
  return /^[A-Za-z0-9+/]+={0,2}$/.test(value.replace(/\s+/g, ''))
}
