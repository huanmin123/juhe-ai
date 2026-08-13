import { createHmac, randomUUID } from 'node:crypto'

import { runtimeConfig } from '../../config/runtime.js'
import type {
  OperationLogDetailSupplement,
  OperationLogInput,
  OperationLogListOptions,
  OperationLogListResult
} from '../../storage/operation-log-types.js'

export const operationLogGoInputPath = '/__aiinternal__/v1/operation-logs'
const operationLogGoListPath = '/__aiinternal__/v1/operation-logs/list'
const operationLogGoDetailPath = '/__aiinternal__/v1/operation-logs/detail/'
const operationLogGoInputSignatureDomain = 'juhe-ai/operation-log-input/v1'

export function normalizeOperationLogRpcPayload(payload: unknown): unknown {
  if (!payload || typeof payload !== 'object') return payload
  const envelope = payload as { operationLog?: Record<string, unknown> }
  if (!envelope.operationLog) return payload
  return {
    ...envelope,
    operationLog: {
      ...envelope.operationLog,
      metadata: jsonValueOr(envelope.operationLog.metadata, {}),
      changes: jsonValueOr(envelope.operationLog.changes, [])
    }
  }
}

function jsonValueOr<T>(value: T, fallback: T): T {
  try {
    JSON.stringify(value)
    return value
  } catch {
    return fallback
  }
}

export async function dispatchOperationLogToGo(input: OperationLogInput & Required<Pick<OperationLogInput, 'id' | 'createdAt'>>): Promise<void> {
  await post(operationLogGoInputPath, { schemaVersion: 1, operationLog: input }, false)
}

export async function listOperationLogsFromGo(options: OperationLogListOptions, viewerId?: string): Promise<OperationLogListResult> {
  return post(operationLogGoListPath, { options: { ...options, viewerId } }, true) as Promise<OperationLogListResult>
}

export async function getOperationLogDetailFromGo(id: string, viewerId?: string): Promise<OperationLogDetailSupplement | undefined> {
  const result = await post(`${operationLogGoDetailPath}${encodeURIComponent(id)}`, { viewerId }, true, true)
  return result as OperationLogDetailSupplement | undefined
}

async function post(path: string, payload: unknown, expectJson: boolean, allowNotFound = false): Promise<unknown> {
  const origin = runtimeConfig.operationLogInputUrl
  const secret = runtimeConfig.operationLogInputSecret
  const timeoutMs = runtimeConfig.operationLogInputTimeoutMs
  if (!origin || !secret || !timeoutMs) throw new Error('F4 Go 操作日志输入未配置')
  const body = Buffer.from(JSON.stringify(normalizeOperationLogRpcPayload(payload)))
  const timestamp = new Date().toISOString()
  const nonce = randomUUID()
  const signature = createHmac('sha256', secret)
    .update(operationLogGoInputSignatureDomain)
    .update('\n')
    .update(timestamp)
    .update('\n')
    .update(nonce)
    .update('\n')
    .update(body)
    .digest('hex')
  const response = await fetch(new URL(path, `${origin}/`), {
    method: 'POST',
    headers: { 'content-type': 'application/json', 'content-length': String(body.byteLength), 'x-juhe-ai-signature': `v1=${signature}`, 'x-juhe-ai-timestamp': timestamp, 'x-juhe-ai-nonce': nonce },
    body: new Uint8Array(body).buffer,
    signal: AbortSignal.timeout(timeoutMs)
  })
  if (allowNotFound && response.status === 404) return undefined
  if (!expectJson && response.status !== 204) throw new Error(`F4 Go 操作日志 RPC 被拒绝: ${response.status}`)
  if (expectJson && !response.ok) throw new Error(`F4 Go 操作日志 RPC 被拒绝: ${response.status}`)
  if (!expectJson) { await response.body?.cancel(); return undefined }
  return response.json()
}
