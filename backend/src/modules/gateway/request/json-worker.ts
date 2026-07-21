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

interface GatewayJsonWorkerRequest {
  id: number
  type: GatewayJsonWorkerJobType
  rawBody: Uint8Array
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

if (!parentPort) {
  throw new Error('网关 JSON worker 缺少 parentPort')
}
const workerPort = parentPort

workerPort.on('message', async (message: GatewayJsonWorkerRequest) => {
  const id = message.id
  try {
    const rawBody = Buffer.from(message.rawBody.buffer, message.rawBody.byteOffset, message.rawBody.byteLength)
    if (message.type === 'extract_json_body_metadata') {
      workerPort.postMessage({
        id,
        ok: true,
        value: extractGatewayJsonBodyMetadata(rawBody)
      })
      return
    }
    if (message.type === 'normalize_openai_oauth_codex_body') {
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
    workerPort.postMessage({
      id,
      ok: true,
      value: JSON.parse(rawBody.toString('utf8')) as unknown
    })
  } catch (error) {
    workerPort.postMessage(workerErrorResponse(id, message.type, error))
  }
})

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

function workerErrorResponse(id: number, jobType: GatewayJsonWorkerJobType, error: unknown): Record<string, unknown> {
  const capturedError = captureWorkerError(error)
  if (isOpenAIOAuthCodexAdapterErrorLike(error)
    && error.code === 'invalid_openai_oauth_codex_request') {
    return {
      id,
      ok: false,
      failureClass: 'expected',
      error: capturedError,
      errorCode: error.code,
      errorStatusCode: error.statusCode,
      errorType: error.type
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
      message: captureWorkerErrorText(String(error), state)
    }
  }

  const envelope: GatewayJsonWorkerErrorEnvelope = {
    name: captureWorkerErrorText(error.name || 'Error', state),
    message: captureWorkerErrorText(error.message, state),
    ...(error.stack ? { stack: captureWorkerErrorText(error.stack, state) } : {})
  }
  if (error.cause !== undefined) {
    if (depth < gatewayJsonWorkerErrorMaxCauseDepth && state.remainingBytes > 0) {
      envelope.cause = captureWorkerErrorValue(error.cause, state, depth + 1)
    } else {
      state.truncated = true
    }
  }
  return envelope
}

function captureWorkerErrorText(value: string, state: GatewayJsonWorkerErrorCaptureState): string {
  const source = Buffer.from(value, 'utf8')
  const retainedBytes = Math.min(
    source.byteLength,
    gatewayJsonWorkerErrorMaxStringBytes,
    state.remainingBytes
  )
  if (retainedBytes < source.byteLength) state.truncated = true
  state.remainingBytes = Math.max(0, state.remainingBytes - retainedBytes)
  return source.subarray(0, retainedBytes).toString('utf8')
}

function isOpenAIOAuthCodexAdapterErrorLike(error: unknown): error is {
  message: string
  code: string
  statusCode: number
  type: string
} {
  return error instanceof Error
    && typeof (error as { code?: unknown }).code === 'string'
    && typeof (error as { statusCode?: unknown }).statusCode === 'number'
    && typeof (error as { type?: unknown }).type === 'string'
}

const gatewayJsonWorkerErrorMaxStringBytes = 8 * 1024
const gatewayJsonWorkerErrorMaxEnvelopeBytes = 48 * 1024
const gatewayJsonWorkerErrorMaxCauseDepth = 4
