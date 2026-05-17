import { parentPort } from 'node:worker_threads'

import type { OpenAIOAuthCodexNormalizeInput } from './openai-oauth-codex-normalizer.js'

type GatewayJsonWorkerJobType =
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
} = await import(resolveNormalizerModuleUrl()) as typeof import('./openai-oauth-codex-normalizer.js')

workerPort.on('message', (message: GatewayJsonWorkerRequest) => {
  const id = message.id
  try {
    const rawBody = Buffer.from(message.rawBody)
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
    ? new URL('./openai-oauth-codex-normalizer.ts', import.meta.url).href
    : new URL('./openai-oauth-codex-normalizer.js', import.meta.url).href
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
