import { parentPort } from 'node:worker_threads'

import type { OpenAIOAuthCodexNormalizeInput } from '../adapters/gpt-codex/oauth-normalizer.js'

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

if (!parentPort) {
  throw new Error('网关 JSON worker 缺少 parentPort')
}
const workerPort = parentPort
const {
  OpenAIOAuthCodexAdapterError,
  normalizeOpenAIOAuthCodexRawBody
} = await import(resolveNormalizerModuleUrl()) as typeof import('../adapters/gpt-codex/oauth-normalizer.js')
const {
  extractGatewayJsonBodyMetadata
} = await import(resolveJsonMetadataScannerModuleUrl()) as typeof import('./json-metadata-scanner.js')

workerPort.on('message', (message: GatewayJsonWorkerRequest) => {
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
        throw new Error('OpenAI OAuth Codex 归一化参数缺失')
      }
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
    workerPort.postMessage(workerErrorResponse(id, error))
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

function workerErrorResponse(id: number, error: unknown): Record<string, unknown> {
  if (error instanceof OpenAIOAuthCodexAdapterError) {
    return {
      id,
      ok: false,
      errorMessage: error.message,
      errorCode: error.code,
      errorStatusCode: error.statusCode,
      errorType: error.type
    }
  }
  return {
    id,
    ok: false,
    errorMessage: error instanceof Error ? error.message : String(error)
  }
}
