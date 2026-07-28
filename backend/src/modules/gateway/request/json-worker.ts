import { parentPort } from 'node:worker_threads'

import type { OpenAIOAuthCodexNormalizeInput } from '../adapters/gpt-codex/oauth-normalizer.js'

if (import.meta.url.endsWith('.ts')) {
  // tsx auto-registers only on the main thread; source workers must register explicitly.
  const { register } = await import('tsx/esm/api')
  register()
}

const {
  extractGatewayJsonBodyMetadata
} = await import(resolveJsonMetadataScannerModuleUrl()) as typeof import('./json-metadata-scanner.js')

type GatewayJsonWorkerJobType =
  | 'extract_json_body_metadata'
  | 'parse_json_body'
  | 'normalize_openai_oauth_codex_body'
  | 'normalize_openai_oauth_codex_parsed_body'

interface GatewayJsonWorkerRequest {
  id: number
  type: GatewayJsonWorkerJobType
  rawBody?: Uint8Array
  parsedBody?: unknown
  normalizeInput?: OpenAIOAuthCodexNormalizeInput
}

interface GatewayJsonWorkerErrorEnvelope {
  name: string
  message: string
  stack?: string
  cause?: GatewayJsonWorkerErrorEnvelope
  truncated?: boolean
}

interface GatewayJsonWorkerErrorCaptureState {
  remainingBytes: number
  truncated: boolean
}

type GatewayJsonWorkerErrorKind = 'openai_oauth_codex_adapter' | 'gateway_request_validation'

if (!parentPort) {
  throw new Error('网关 JSON worker 缺少 parentPort')
}
const workerPort = parentPort

workerPort.on('message', async (message: GatewayJsonWorkerRequest) => {
  const id = message.id
  try {
    if (message.type === 'extract_json_body_metadata') {
      const rawBody = requiredRawBody(message)
      workerPort.postMessage({
        id,
        ok: true,
        value: extractGatewayJsonBodyMetadata(rawBody)
      })
      return
    }
    if (message.type === 'normalize_openai_oauth_codex_body') {
      const rawBody = requiredRawBody(message)
      if (!message.normalizeInput) {
        throw new Error('OpenAI OAuth Codex 归一化参数缺失', {
          cause: new Error('normalize_input_missing')
        })
      }
      const {
        normalizeOpenAIOAuthCodexRawBody
      } = await import(resolveNormalizerModuleUrl()) as typeof import('../adapters/gpt-codex/oauth-normalizer.js')
      workerPort.postMessage({
        id,
        ok: true,
        value: normalizeOpenAIOAuthCodexRawBody(rawBody, message.normalizeInput)
      })
      return
    }
    if (message.type === 'normalize_openai_oauth_codex_parsed_body') {
      if (!message.normalizeInput) {
        throw new Error('OpenAI OAuth Codex 归一化参数缺失', {
          cause: new Error('normalize_input_missing')
        })
      }
      const {
        normalizeOpenAIOAuthCodexParsedBody
      } = await import(resolveNormalizerModuleUrl()) as typeof import('../adapters/gpt-codex/oauth-normalizer.js')
      workerPort.postMessage({
        id,
        ok: true,
        value: normalizeOpenAIOAuthCodexParsedBody(message.parsedBody, message.normalizeInput)
      })
      return
    }
    const rawBody = requiredRawBody(message)
    workerPort.postMessage({
      id,
      ok: true,
      value: JSON.parse(rawBody.toString('utf8')) as unknown
    })
  } catch (error) {
    workerPort.postMessage(await workerErrorResponse(id, message.type, error))
  }
})

function requiredRawBody(message: GatewayJsonWorkerRequest): Buffer {
  if (!message.rawBody) {
    throw new Error('网关 JSON worker 请求体缺失', {
      cause: new Error('raw_body_missing')
    })
  }
  return Buffer.from(message.rawBody.buffer, message.rawBody.byteOffset, message.rawBody.byteLength)
}

function resolveNormalizerModuleUrl(): string {
  return import.meta.url.endsWith('.ts')
    ? new URL('../adapters/gpt-codex/oauth-normalizer.ts', import.meta.url).href
    : new URL('../adapters/gpt-codex/oauth-normalizer.js', import.meta.url).href
}

function resolveJsonMetadataScannerModuleUrl(): string {
  return import.meta.url.endsWith('.ts')
    ? new URL('./json-metadata-scanner.ts', import.meta.url).href
    : new URL('./json-metadata-scanner.js', import.meta.url).href
}

function resolveGatewayValidationErrorModuleUrl(): string {
  return import.meta.url.endsWith('.ts')
    ? new URL('./validation-error.ts', import.meta.url).href
    : new URL('./validation-error.js', import.meta.url).href
}

async function workerErrorResponse(id: number, jobType: GatewayJsonWorkerJobType, error: unknown): Promise<Record<string, unknown>> {
  const capturedError = captureWorkerError(error)
  const errorKind = await gatewayJsonWorkerExpectedErrorKind(error)
  if (errorKind && isGatewayExpectedErrorLike(error)) {
    return {
      id,
      ok: false,
      failureClass: 'expected',
      errorKind,
      error: capturedError,
      errorCode: error.code,
      errorStatusCode: error.statusCode,
      errorType: error.type,
      errorAccountScoped: error.accountScoped
    }
  }
  return {
    id,
    ok: false,
    failureClass: jobType === 'parse_json_body' && error instanceof SyntaxError
      ? 'expected'
      : 'infrastructure',
    error: capturedError
  }
}

async function gatewayJsonWorkerExpectedErrorKind(error: unknown): Promise<GatewayJsonWorkerErrorKind | undefined> {
  if (isGatewayExpectedErrorLike(error)
    && error.code === 'invalid_openai_oauth_codex_request') {
    return 'openai_oauth_codex_adapter'
  }
  const {
    GatewayRequestValidationError
  } = await import(resolveGatewayValidationErrorModuleUrl()) as typeof import('./validation-error.js')
  return error instanceof GatewayRequestValidationError ? 'gateway_request_validation' : undefined
}

function captureWorkerError(error: unknown): GatewayJsonWorkerErrorEnvelope {
  const state: GatewayJsonWorkerErrorCaptureState = {
    remainingBytes: gatewayJsonWorkerErrorMaxEnvelopeBytes,
    truncated: false
  }
  const envelope = captureWorkerErrorValue(error, state)
  if (state.truncated) envelope.truncated = true
  return envelope
}

function captureWorkerErrorValue(
  error: unknown,
  state: GatewayJsonWorkerErrorCaptureState,
  depth = 0
): GatewayJsonWorkerErrorEnvelope {
  if (!(error instanceof Error)) {
    return {
      name: 'NonErrorThrown',
      message: captureWorkerErrorText(safeWorkerThrownValueText(error), state)
    }
  }

  const name = safeWorkerErrorProperty(error, 'name') ?? 'Error'
  const message = safeWorkerErrorProperty(error, 'message') ?? ''
  const stack = safeWorkerErrorProperty(error, 'stack')
  const envelope: GatewayJsonWorkerErrorEnvelope = {
    name: captureWorkerErrorText(name || 'Error', state),
    message: captureWorkerErrorText(message, state),
    ...(stack ? { stack: captureWorkerErrorText(stack, state) } : {})
  }
  const cause = safeWorkerErrorValue(error, 'cause')
  if (cause !== undefined) {
    if (depth < gatewayJsonWorkerErrorMaxCauseDepth && state.remainingBytes > 0) {
      envelope.cause = captureWorkerErrorValue(cause, state, depth + 1)
    } else {
      state.truncated = true
    }
  }
  return envelope
}

function safeWorkerThrownValueText(error: unknown): string {
  try {
    return String(error)
  } catch {
    return '[unprintable thrown value]'
  }
}

function captureWorkerErrorText(value: string, state: GatewayJsonWorkerErrorCaptureState): string {
  const maxBytes = Math.min(gatewayJsonWorkerErrorMaxStringBytes, state.remainingBytes)
  const retained = truncateWorkerErrorText(value, maxBytes)
  const retainedBytes = Buffer.byteLength(retained, 'utf8')
  if (retained !== value) state.truncated = true
  state.remainingBytes = Math.max(0, state.remainingBytes - retainedBytes)
  return retained
}

function safeWorkerErrorProperty(error: Error, key: 'name' | 'message' | 'stack'): string | undefined {
  try {
    const value = (error as unknown as Record<string, unknown>)[key]
    return typeof value === 'string' ? value : undefined
  } catch {
    return `[unreadable ${key}]`
  }
}

function safeWorkerErrorValue(error: Error, key: 'cause'): unknown {
  try {
    return (error as unknown as Record<string, unknown>)[key]
  } catch {
    return '[unreadable cause]'
  }
}

function truncateWorkerErrorText(value: string, maxBytes: number): string {
  if (maxBytes <= 0) return ''
  if (value.length <= maxBytes && Buffer.byteLength(value, 'utf8') <= maxBytes) return value
  let low = 0
  let high = Math.min(value.length, maxBytes)
  while (low < high) {
    const midpoint = Math.ceil((low + high) / 2)
    if (Buffer.byteLength(value.slice(0, midpoint), 'utf8') <= maxBytes) low = midpoint
    else high = midpoint - 1
  }
  let end = low
  if (end > 0) {
    const last = value.charCodeAt(end - 1)
    if (last >= 0xD800 && last <= 0xDBFF) end -= 1
  }
  return value.slice(0, end)
}

function isGatewayExpectedErrorLike(error: unknown): error is {
  message: string
  code: string
  statusCode: number
  type: string
  accountScoped: boolean
} {
  return error instanceof Error
    && typeof (error as { code?: unknown }).code === 'string'
    && typeof (error as { statusCode?: unknown }).statusCode === 'number'
    && typeof (error as { type?: unknown }).type === 'string'
    && typeof (error as { accountScoped?: unknown }).accountScoped === 'boolean'
}

const gatewayJsonWorkerErrorMaxStringBytes = 8 * 1024
const gatewayJsonWorkerErrorMaxEnvelopeBytes = 48 * 1024
const gatewayJsonWorkerErrorMaxCauseDepth = 4
