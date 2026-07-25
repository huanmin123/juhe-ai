import { join } from 'node:path'

import { runtimeConfig } from '../../config/runtime.js'
import { chatAssetGeneratedMaxBytes } from '../../storage/chat-asset-storage.js'
import { decodeBase64FieldToTempFile, type ChatImageResultTempFile } from './chat-image-result-stream.js'
import { readChatJsonResponse } from './chat-bounded-json.js'
import { normalizeChatImageOutputFormat, normalizeChatImageQuality, normalizeChatImageSize } from './chat-image-policy.js'
import { validateChatImageEditReferenceLimits, type ChatImageEditReference } from './chat-image-edit-references.js'

export interface ChatImageGenerationRequestInput {
  gatewayBaseUrl: string
  apiKey: string
  model: string
  prompt: string
  size?: string
  quality?: string
  outputFormat?: string
  references?: readonly ChatImageEditReference[]
  traceId?: string
  signal?: AbortSignal
  fetchImpl?: typeof fetch
  tempDir?: string
}

export interface ChatImageGenerationResult extends ChatImageResultTempFile {
  revisedPrompt?: string
}

export class ChatImageGenerationRequestError extends Error {
  constructor(
    message: string,
    public readonly statusCode: number,
    public readonly code: 'image_generation_not_enabled' | 'image_generation_permission_denied' | 'image_generation_rate_limited' | 'image_generation_request_rejected' | 'image_generation_failed'
  ) {
    super(message)
    this.name = 'ChatImageGenerationRequestError'
  }
}

export function buildChatImageGenerationRequest(input: Pick<ChatImageGenerationRequestInput, 'model' | 'prompt'> & Partial<Pick<ChatImageGenerationRequestInput, 'size' | 'quality' | 'outputFormat'>>): {
  path: '/v1/images/generations'
  body: { model: string; prompt: string; n: 1; size: string; quality: string; output_format: string }
} {
  const model = input.model.trim()
  const prompt = input.prompt.trim()
  if (!model) throw new Error('图像生成模型不能为空')
  if (!prompt) throw new Error('图像生成提示词不能为空')
  const size = normalizeChatImageSize(input.size).size
  const quality = normalizeChatImageQuality(input.quality)
  const outputFormat = normalizeChatImageOutputFormat(input.outputFormat)
  return {
    path: '/v1/images/generations',
    body: { model, prompt, n: 1, size, quality, output_format: outputFormat }
  }
}

export async function generateChatImage(input: ChatImageGenerationRequestInput): Promise<ChatImageGenerationResult> {
  const request = buildChatImageGenerationRequest(input)
  const fetchImpl = input.fetchImpl ?? fetch
  const references = input.references ?? []
  const response = references.length > 0
    ? await fetchImpl(joinUrl(input.gatewayBaseUrl, '/v1/images/edits'), {
        method: 'POST',
        headers: {
          authorization: `Bearer ${input.apiKey}`,
          accept: 'application/json',
          ...(input.traceId ? { 'x-trace-id': input.traceId } : {})
        },
        body: await buildChatImageEditForm(request.body, references, input.signal),
        signal: input.signal
      })
    : await fetchImpl(joinUrl(input.gatewayBaseUrl, request.path), {
        method: 'POST',
        headers: {
          authorization: `Bearer ${input.apiKey}`,
          'content-type': 'application/json',
          accept: 'application/json',
          ...(input.traceId ? { 'x-trace-id': input.traceId } : {})
        },
        body: JSON.stringify(request.body),
        signal: input.signal
      })
  if (!response.ok || !response.body) {
    const payload = await readChatJsonResponse(response, 64 * 1024)
    const upstream = readImageGenerationError(payload, `图像生成请求失败（HTTP ${response.status}）`)
    throw new ChatImageGenerationRequestError(upstream.message, response.status, imageGenerationPublicErrorCode(response.status, upstream))
  }
  return await decodeBase64FieldToTempFile(readUtf8Chunks(response.body), {
    field: 'b64_json',
    tempDir: input.tempDir ?? join(runtimeConfig.chatAssetsRoot, '.tmp'),
    maxDecodedBytes: chatAssetGeneratedMaxBytes
  })
}

async function buildChatImageEditForm(
  request: ReturnType<typeof buildChatImageGenerationRequest>['body'],
  references: readonly ChatImageEditReference[],
  signal?: AbortSignal
): Promise<FormData> {
  validateChatImageEditReferenceLimits(references)
  const form = new FormData()
  form.set('model', request.model)
  form.set('prompt', request.prompt)
  form.set('size', request.size)
  form.set('quality', request.quality)
  form.set('output_format', request.output_format)
  for (const reference of references) {
    const bytes = await readReferenceBytes(reference, signal)
    form.append('image[]', new Blob([bytes], { type: reference.mimeType }), reference.filename)
  }
  return form
}

async function readReferenceBytes(reference: ChatImageEditReference, signal?: AbortSignal): Promise<ArrayBuffer> {
  const chunks: Buffer[] = []
  let bytes = 0
  try {
    for await (const rawChunk of reference.stream) {
      if (signal?.aborted) throw signal.reason ?? new DOMException('Aborted', 'AbortError')
      const chunk = Buffer.isBuffer(rawChunk) ? rawChunk : Buffer.from(rawChunk)
      bytes += chunk.byteLength
      if (bytes > reference.bytes) throw new Error('引用图片流超过声明字节数')
      chunks.push(chunk)
    }
    if (bytes !== reference.bytes) throw new Error('引用图片流与声明字节数不一致')
    const output = new Uint8Array(bytes)
    output.set(Buffer.concat(chunks, bytes))
    return output.buffer
  } catch (error) {
    reference.stream.destroy()
    throw error
  }
}

function joinUrl(baseUrl: string, path: string): string {
  const normalized = baseUrl.trim().replace(/\/+$/u, '')
  if (!/^https?:\/\//u.test(normalized)) throw new Error('聊天网关地址无效')
  return `${normalized}${path}`
}

function readImageGenerationError(payload: unknown, fallback: string): { message: string; type?: string } {
  const record = payload && typeof payload === 'object' && !Array.isArray(payload) ? payload as Record<string, unknown> : {}
  const error = record.error && typeof record.error === 'object' && !Array.isArray(record.error) ? record.error as Record<string, unknown> : {}
  return {
    message: typeof error.message === 'string' && error.message.trim() ? error.message.trim() : fallback,
    ...(typeof error.type === 'string' && error.type.trim() ? { type: error.type.trim() } : {})
  }
}

function imageGenerationPublicErrorCode(
  statusCode: number,
  upstream: { message: string; type?: string }
): ChatImageGenerationRequestError['code'] {
  if (
    statusCode === 403
    && upstream.type?.toLowerCase() === 'permission_error'
    && /\bimage generation is not enabled for (?:this|the) group\b/i.test(upstream.message)
  ) return 'image_generation_not_enabled'
  if (statusCode === 401 || statusCode === 403) return 'image_generation_permission_denied'
  if (statusCode === 429) return 'image_generation_rate_limited'
  if (statusCode === 400 || statusCode === 422) return 'image_generation_request_rejected'
  return 'image_generation_failed'
}

async function* readUtf8Chunks(body: ReadableStream<Uint8Array>): AsyncGenerator<string> {
  const reader = body.getReader()
  const decoder = new TextDecoder('utf-8', { fatal: true })
  let completed = false
  try {
    while (true) {
      const next = await reader.read()
      if (next.done) {
        completed = true
        break
      }
      if (next.value?.byteLength) yield decoder.decode(next.value, { stream: true })
    }
    const tail = decoder.decode()
    if (tail) yield tail
  } finally {
    if (!completed) await reader.cancel().catch(() => undefined)
    reader.releaseLock()
  }
}
