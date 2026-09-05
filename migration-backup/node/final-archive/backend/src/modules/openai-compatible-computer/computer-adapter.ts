import type { Request } from 'express'

import { runtimeConfig } from '../../config/runtime.js'
import type {
  OpenAIToAnthropicComputerExecutor,
  OpenAIToAnthropicComputerRuntimeInput,
  OpenAIToAnthropicComputerRuntimeResult
} from '../providers/drivers/_shared/openai-anthropic-bridge.js'

let openAICompatibleComputerExecutorForTest: OpenAIToAnthropicComputerExecutor | undefined
const configuredComputerHttpExecutor: OpenAIToAnthropicComputerExecutor = {
  run: runConfiguredComputerHttpAdapter
}

export function setOpenAICompatibleComputerExecutorForTest(
  executor: OpenAIToAnthropicComputerExecutor | undefined
): void {
  openAICompatibleComputerExecutorForTest = executor
}

export function openAICompatibleComputerExecutorForGatewayRequest(
  _req: Request
): OpenAIToAnthropicComputerExecutor | undefined {
  if (openAICompatibleComputerExecutorForTest) return openAICompatibleComputerExecutorForTest
  if (runtimeConfig.hostedToolRuntimes.computer !== 'local_runtime') return undefined
  if (!runtimeConfig.computerAdapter.enabled || !runtimeConfig.computerAdapter.endpoint) return undefined
  return configuredComputerHttpExecutor
}

async function runConfiguredComputerHttpAdapter(
  input: OpenAIToAnthropicComputerRuntimeInput
): Promise<OpenAIToAnthropicComputerRuntimeResult> {
  const endpoint = runtimeConfig.computerAdapter.endpoint
  if (!endpoint) {
    throw new Error('Computer browser adapter endpoint is not configured')
  }
  const abort = createComputerAdapterAbortController(input.signal, runtimeConfig.computerAdapter.timeoutMs)
  try {
    const response = await fetch(endpoint, {
      method: 'POST',
      headers: {
        accept: 'application/json',
        'content-type': 'application/json'
      },
      body: JSON.stringify({
        body: input.body,
        tool: input.tool,
        stream: input.stream
      }),
      signal: abort.controller.signal
    })
    const text = await readResponseTextWithLimit(response, runtimeConfig.computerAdapter.maxBodyBytes)
    if (!response.ok) {
      throw new Error(`Computer browser adapter HTTP ${response.status}: ${text.slice(0, 512)}`)
    }
    let parsed: unknown
    try {
      parsed = JSON.parse(text)
    } catch {
      throw new Error('Computer browser adapter response is not valid JSON')
    }
    return normalizeComputerAdapterResult(parsed)
  } catch (error) {
    if (abort.timedOut()) {
      throw new Error('Computer browser adapter request timed out')
    }
    throw error
  } finally {
    abort.dispose()
  }
}

function normalizeComputerAdapterResult(value: unknown): OpenAIToAnthropicComputerRuntimeResult {
  if (!isRecord(value)) {
    throw new Error('Computer browser adapter response must be a JSON object')
  }
  const message = stringValue(value.message) ?? 'Computer browser adapter completed.'
  const callRecord = objectValue(value.call)
  return {
    message,
    call: callRecord ? {
      callId: stringValue(callRecord.call_id) ?? stringValue(callRecord.callId),
      status: stringValue(callRecord.status),
      actions: arrayRecordValue(callRecord.actions),
      metadata: objectValue(callRecord.metadata)
    } : undefined,
    metadata: {
      ...(objectValue(value.metadata) ?? {}),
      adapter: 'http_browser'
    }
  }
}

function createComputerAdapterAbortController(signal: AbortSignal | undefined, timeoutMs: number): {
  controller: AbortController
  timedOut: () => boolean
  dispose: () => void
} {
  const controller = new AbortController()
  let didTimeout = false
  const timeout = setTimeout(() => {
    didTimeout = true
    controller.abort()
  }, timeoutMs)
  const onAbort = () => controller.abort()
  signal?.addEventListener('abort', onAbort, { once: true })
  return {
    controller,
    timedOut: () => didTimeout,
    dispose: () => {
      clearTimeout(timeout)
      signal?.removeEventListener('abort', onAbort)
    }
  }
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
      throw new Error('Computer browser adapter response body exceeded limit')
    }
    chunks.push(value)
  }
  return Buffer.concat(chunks.map((chunk) => Buffer.from(chunk))).toString('utf8')
}

function isRecord(value: unknown): value is Record<string, unknown> {
  return typeof value === 'object' && value !== null && !Array.isArray(value)
}

function objectValue(value: unknown): Record<string, unknown> | undefined {
  return isRecord(value) ? value : undefined
}

function stringValue(value: unknown): string | undefined {
  return typeof value === 'string' && value.trim() ? value.trim() : undefined
}

function arrayRecordValue(value: unknown): Array<Record<string, unknown>> {
  if (!Array.isArray(value)) return []
  return value.filter(isRecord)
}
