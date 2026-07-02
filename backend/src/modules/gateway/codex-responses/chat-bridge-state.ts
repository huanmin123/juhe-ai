import { createHash, randomUUID } from 'node:crypto'
import { existsSync } from 'node:fs'
import { mkdir, open, rm } from 'node:fs/promises'
import { dirname, relative, resolve, sep } from 'node:path'
import { gzipSync, gunzipSync } from 'node:zlib'
import type { Request, Response } from 'express'

import { runtimeConfig } from '../../../config/runtime.js'
import type { GroupUsageAccessMetadata } from '../../../storage/repositories.js'
import { OPENAI_RESPONSES_FAMILY } from '../../../domain/provider-protocol.js'
import type {
  CodexContextCompactStateIndex,
  CodexContextPayloadReference,
  CodexContextResponseStateIndex,
  CodexContextStateBoundary
} from '../../../storage/codex-context-state.repository.js'
import { errorLogFields, logger } from '../../../shared/logger.js'
import { getRequestLogger } from '../../../shared/request-context.js'
import type { AuditCaptureContext } from '../audit/capture.service.js'
import { requestGatewayDbService } from '../runtime/gateway-db-service-request.js'
import {
  isOpenAIResponsesToChatCompletionsModelMapping,
  resolveOpenAIAccountModelMapping
} from '../protocols/openai-v1/model-mapping.js'
import type { UpstreamAccount } from '../protocols/openai-v1/route-helpers.js'
import { splitPathAndQuery } from '../protocols/openai-v1/route-helpers.js'
import { requestModel, requestStream } from '../request/metadata.js'
import { parseGatewayJsonBodyInWorker } from '../request/json-parser.js'
import {
  getGatewayRequestBodyState,
  gatewayJsonBodyInlineParseMaxBytes,
  replaceGatewayJsonBody,
  type GatewayRawBodyRequest
} from '../request/body.js'
import { sendGatewayFailureResponse } from '../response/failure-response.js'
import { gatewayErrorPayload } from '../response/responses.js'
import type { GatewayFailureUsageContext } from '../usage/records.js'
import type { ClientCompatibilityCapability } from '../../../domain/types.js'

type JsonRecord = Record<string, unknown>
type CodexContextRestoreFailureOutcome = 'not_found' | 'expired' | 'boundary_mismatch' | 'chain_too_deep' | 'chain_broken'
type CodexCompactionReferenceFailureOutcome = 'not_found' | 'expired' | 'boundary_mismatch' | 'payload_unavailable'
export type CodexResponsesChatBridgeInputRestoreResult =
  | {
    outcome: 'found' | 'no_previous'
    input: unknown[]
    sessionId?: string
    responseCount: number
  }
  | {
    outcome: CodexContextRestoreFailureOutcome | 'payload_unavailable'
    responseId?: string
    sessionId?: string
  }

export interface CodexResponsesChatBridgeCompletion {
  responseId: string
  createdAt: number
  model: string
  outputItems: JsonRecord[]
  response: JsonRecord
}

export type CodexResponsesChatBridgeCompletionHandler = (
  completion: CodexResponsesChatBridgeCompletion
) => void | Promise<void>

export interface CodexResponsesChatBridgeRequestState {
  boundary: CodexContextStateBoundary
  currentBody: JsonRecord
  currentInput: unknown
  previousResponseId?: string
  sessionId?: string
  restored: boolean
}

export interface CodexResponsesChatBridgeCompactSnapshotResult {
  compactId: string
  encryptedContent: string
}

interface CodexResponsesChatBridgeStatePayloadV2 {
  schemaVersion: 2
  responseId: string
  sessionId: string
  previousResponseId?: string
  createdAt: string
  boundary: CodexContextStateBoundary
  request: {
    model?: string
    instructions?: unknown
    input: unknown
  }
  outputItems: JsonRecord[]
}

interface CodexResponsesChatBridgeCompactSnapshotPayloadV2 {
  schemaVersion: 2
  compactId: string
  sessionId: string
  sourceResponseId?: string
  createdAt: string
  boundary: CodexContextStateBoundary
  summary: string
}

const requestStateSymbol = Symbol.for('juhe-ai.codex-responses-chat-bridge-state')
const codexContextStateTtlMs = 7 * 24 * 60 * 60 * 1000
const maxContextChainDepth = 64
const maxStoredPayloadBytes = 8 * 1024 * 1024
const maxRestoredInputBytes = 32 * 1024 * 1024
const codexCompactionReferencePrefix = 'juhecmp.v2.'
const codexInlineCompactionSummaryPrefix = 'juhecmp.v1.'
const segmentWriteLocks = new Map<string, Promise<void>>()

export async function applyCodexResponsesChatBridgeStatePreflight(input: {
  req: Request
  res: Response
  auditCapture: AuditCaptureContext
  usageContext: GatewayFailureUsageContext
  startedAt: number
  systemAccountId: string
  apiKeyId?: string
  groupId: string
  groupAccess: GroupUsageAccessMetadata
  requestClientCompatibility: ClientCompatibilityCapability
  rawCandidateAccounts: readonly UpstreamAccount[]
  signal?: AbortSignal
}): Promise<'continued' | 'completed'> {
  if (!isChatOnlyCodexResponsesBridgeStateRequest(input.req, input.requestClientCompatibility, input.rawCandidateAccounts)) {
    return 'continued'
  }
  const body = await parseGatewayJsonObject(input.req, input.signal)
  const boundary = codexContextBoundary(input)
  const compactReferenceResult = await resolveCodexCompactionReferencesInInput({
    input: body.input,
    boundary,
    signal: input.signal
  })
  if (compactReferenceResult.outcome !== 'resolved') {
    await sendCodexBridgeStateFailure(input, compactReferenceFailure(compactReferenceResult.outcome))
    return 'completed'
  }
  if (compactReferenceResult.changed) {
    body.input = compactReferenceResult.input
    replaceGatewayJsonBody(input.req, body)
  }
  const previousResponseId = normalizedOptionalText(body.previous_response_id)
  setCodexResponsesChatBridgeRequestState(input.req, {
    boundary,
    currentBody: { ...body },
    currentInput: body.input,
    previousResponseId,
    restored: false
  })
  if (!previousResponseId) {
    input.auditCapture.addGatewayMetadata({
      label: 'codex_responses_chat_bridge_state',
      metadata: {
        mode: 'new_session'
      }
    })
    return 'continued'
  }

  const now = new Date()
  const readResult = await requestGatewayDbService({
    type: 'read_codex_context_response_chain',
    responseId: previousResponseId,
    boundary,
    maxDepth: maxContextChainDepth,
    now: now.toISOString(),
    refreshExpiresAt: expiresAtFrom(now)
  }, { timeoutMs: 10000 })
  if (readResult.outcome !== 'found') {
    input.auditCapture.addGatewayMetadata({
      label: 'codex_responses_chat_bridge_state',
      metadata: {
        mode: 'restore_failed',
        previousResponseId,
        reason: readResult.outcome
      }
    })
    await sendCodexBridgeStateFailure(input, stateRestoreFailure(readResult.outcome))
    return 'completed'
  }

  let restoredInput: unknown[]
  try {
    const payloads = await readPayloadChain(readResult.responses)
    restoredInput = restoreResponsesInputFromPayloads(payloads, body.input)
    assertRestoredInputSize(restoredInput)
  } catch (error) {
    logger.warn(errorLogFields(error, {
      event: 'codex_responses_chat_bridge_state_restore_failed',
      previousResponseId,
      sessionId: readResult.sessionId
    }), 'Codex Responses Chat bridge 状态恢复失败')
    input.auditCapture.addGatewayMetadata({
      label: 'codex_responses_chat_bridge_state',
      metadata: {
        mode: 'restore_failed',
        previousResponseId,
        sessionId: readResult.sessionId,
        reason: 'payload_unavailable'
      }
    })
    await sendCodexBridgeStateFailure(input, {
      statusCode: 404,
      type: 'invalid_request_error',
      code: 'codex_bridge_previous_response_state_unavailable',
      message: 'previous_response_id 对应的服务端上下文文件不存在、已过期或校验失败'
    })
    return 'completed'
  }
  const restoredBody: JsonRecord = {
    ...body,
    input: restoredInput
  }
  delete restoredBody.previous_response_id
  replaceGatewayJsonBody(input.req, restoredBody)
  setCodexResponsesChatBridgeRequestState(input.req, {
    boundary,
    currentBody: { ...body },
    currentInput: body.input,
    previousResponseId,
    sessionId: readResult.sessionId,
    restored: true
  })
  input.auditCapture.addGatewayMetadata({
    label: 'codex_responses_chat_bridge_state',
    metadata: {
      mode: 'restored',
      previousResponseId,
      sessionId: readResult.sessionId,
      restoredResponseCount: readResult.responses.length
    }
  })
  return 'continued'
}

export function codexResponsesChatBridgeCompletionHandlerForRequest(
  req: Request,
  account: UpstreamAccount,
  model?: string
): CodexResponsesChatBridgeCompletionHandler | undefined {
  const requestState = getCodexResponsesChatBridgeRequestState(req)
  if (!requestState) return undefined
  return async (completion) => {
    try {
      const now = new Date()
      const sessionId = requestState.sessionId ?? completion.responseId
      const payload: CodexResponsesChatBridgeStatePayloadV2 = {
        schemaVersion: 2,
        responseId: completion.responseId,
        sessionId,
        previousResponseId: requestState.previousResponseId,
        createdAt: now.toISOString(),
        boundary: requestState.boundary,
        request: {
          model: normalizedOptionalText(requestState.currentBody.model),
          instructions: requestState.currentBody.instructions,
          input: requestState.currentInput
        },
        outputItems: completion.outputItems
      }
      const stored = await writeStatePayload(payload)
      await requestGatewayDbService({
        type: 'save_codex_context_response_state',
        input: {
          responseId: completion.responseId,
          sessionId,
          previousResponseId: requestState.previousResponseId,
          ...requestState.boundary,
          upstreamAccountId: account.id,
          model: normalizedOptionalText(requestState.currentBody.model),
          upstreamModel: model ?? completion.model,
          storageKey: stored.storageKey,
          storageOffsetBytes: stored.storageOffsetBytes,
          sha256: stored.sha256,
          rawSizeBytes: stored.rawSizeBytes,
          compressedSizeBytes: stored.compressedSizeBytes,
          compression: stored.compression,
          schemaVersion: stored.schemaVersion,
          createdAt: now.toISOString(),
          expiresAt: expiresAtFrom(now)
        }
      }, { timeoutMs: 10000 })
      getRequestLogger().info({
        event: 'codex_responses_chat_bridge_state_saved',
        responseId: completion.responseId,
        previousResponseId: requestState.previousResponseId,
        sessionId,
        accountId: account.id,
        providerCode: account.providerCode,
        providerProtocolProfileId: account.providerProtocolProfileId
      }, 'Codex Responses Chat bridge 状态已保存')
    } catch (error) {
      logger.warn(errorLogFields(error, {
        event: 'codex_responses_chat_bridge_state_save_failed',
        responseId: completion.responseId,
        previousResponseId: requestState.previousResponseId,
        accountId: account.id
      }), 'Codex Responses Chat bridge 状态保存失败')
    }
  }
}

export async function restoreCodexResponsesChatBridgeInputForCompact(input: {
  previousResponseId?: string
  boundary: CodexContextStateBoundary
  currentInput: unknown
}): Promise<CodexResponsesChatBridgeInputRestoreResult> {
  if (!input.previousResponseId) {
    return {
      outcome: 'no_previous',
      input: responsesInputAsItems(input.currentInput),
      responseCount: 0
    }
  }
  const now = new Date()
  const readResult = await requestGatewayDbService({
    type: 'read_codex_context_response_chain',
    responseId: input.previousResponseId,
    boundary: input.boundary,
    maxDepth: maxContextChainDepth,
    now: now.toISOString(),
    refreshExpiresAt: expiresAtFrom(now)
  }, { timeoutMs: 10000 })
  if (readResult.outcome !== 'found') {
    return {
      outcome: readResult.outcome,
      responseId: readResult.responseId,
      sessionId: readResult.sessionId
    }
  }
  try {
    const payloads = await readPayloadChain(readResult.responses)
    const restoredInput = restoreResponsesInputFromPayloads(payloads, input.currentInput)
    assertRestoredInputSize(restoredInput)
    return {
      outcome: 'found',
      input: restoredInput,
      sessionId: readResult.sessionId,
      responseCount: readResult.responses.length
    }
  } catch (error) {
    logger.warn(errorLogFields(error, {
      event: 'codex_responses_chat_bridge_compact_restore_failed',
      previousResponseId: input.previousResponseId,
      sessionId: readResult.sessionId
    }), 'Codex Responses Chat bridge compact 状态恢复失败')
    return {
      outcome: 'payload_unavailable',
      responseId: input.previousResponseId,
      sessionId: readResult.sessionId
    }
  }
}

export async function createCodexResponsesChatBridgeCompactSnapshot(input: {
  sessionId?: string
  sourceResponseId?: string
  boundary: CodexContextStateBoundary
  summary: string
  upstreamAccountId?: string
  model?: string
  upstreamModel?: string
  createdAt?: Date
}): Promise<CodexResponsesChatBridgeCompactSnapshotResult> {
  const now = input.createdAt ?? new Date()
  const compactId = `cmp_${now.getTime().toString(36)}_${randomUUID().slice(0, 8)}`
  const sessionId = input.sessionId ?? input.sourceResponseId ?? compactId
  const summaryDigest = digestText(input.summary)
  const payload: CodexResponsesChatBridgeCompactSnapshotPayloadV2 = {
    schemaVersion: 2,
    compactId,
    sessionId,
    sourceResponseId: input.sourceResponseId,
    createdAt: now.toISOString(),
    boundary: input.boundary,
    summary: input.summary
  }
  const stored = await writeCompactSnapshotPayload(payload)
  await requestGatewayDbService({
    type: 'save_codex_context_compact_state',
    input: {
      compactId,
      sessionId,
      sourceResponseId: input.sourceResponseId,
      summaryDigest,
      ...input.boundary,
      upstreamAccountId: input.upstreamAccountId,
      model: input.model,
      upstreamModel: input.upstreamModel,
      storageKey: stored.storageKey,
      storageOffsetBytes: stored.storageOffsetBytes,
      sha256: stored.sha256,
      rawSizeBytes: stored.rawSizeBytes,
      compressedSizeBytes: stored.compressedSizeBytes,
      compression: stored.compression,
      schemaVersion: stored.schemaVersion,
      createdAt: now.toISOString(),
      expiresAt: expiresAtFrom(now)
    }
  }, { timeoutMs: 10000 })
  return {
    compactId,
    encryptedContent: `${codexCompactionReferencePrefix}${compactId}.${summaryDigest}`
  }
}

async function resolveCodexCompactionReferencesInInput(input: {
  input: unknown
  boundary: CodexContextStateBoundary
  signal?: AbortSignal
}): Promise<
  | { outcome: 'resolved'; input: unknown; changed: boolean }
  | { outcome: CodexCompactionReferenceFailureOutcome; compactId?: string }
> {
  if (!Array.isArray(input.input)) {
    return { outcome: 'resolved', input: input.input, changed: false }
  }
  const resolved: unknown[] = []
  let changed = false
  for (const item of input.input) {
    if (!isPlainObject(item) || !isCodexCompactionInputItem(item)) {
      resolved.push(item)
      continue
    }
    const encryptedContent = normalizedOptionalText(item.encrypted_content)
    if (!encryptedContent?.startsWith(codexCompactionReferencePrefix)) {
      resolved.push(item)
      continue
    }
    const reference = parseCodexCompactionReference(encryptedContent)
    if (!reference) {
      return { outcome: 'payload_unavailable' }
    }
    const summaryResult = await readCodexCompactionSnapshotSummary({
      ...reference,
      boundary: input.boundary
    })
    if (summaryResult.outcome !== 'found') {
      return {
        outcome: summaryResult.outcome,
        compactId: reference.compactId
      }
    }
    resolved.push({
      ...item,
      type: 'compaction_summary',
      encrypted_content: encodeInlineCodexCompactionSummary(summaryResult.summary)
    })
    changed = true
  }
  return {
    outcome: 'resolved',
    input: changed ? resolved : input.input,
    changed
  }
}

async function readCodexCompactionSnapshotSummary(input: {
  compactId: string
  digest: string
  boundary: CodexContextStateBoundary
}): Promise<
  | { outcome: 'found'; summary: string }
  | { outcome: CodexCompactionReferenceFailureOutcome }
> {
  const now = new Date()
  const readResult = await requestGatewayDbService({
    type: 'read_codex_context_compact_state',
    compactId: input.compactId,
    boundary: input.boundary,
    now: now.toISOString(),
    refreshExpiresAt: expiresAtFrom(now)
  }, { timeoutMs: 10000 })
  if (readResult.outcome !== 'found') {
    return { outcome: readResult.outcome }
  }
  if (readResult.compact.summaryDigest !== input.digest) {
    return { outcome: 'payload_unavailable' }
  }
  try {
    const payload = await readCompactSnapshotPayload(readResult.compact)
    if (payload.compactId !== input.compactId || digestText(payload.summary) !== input.digest) {
      return { outcome: 'payload_unavailable' }
    }
    return {
      outcome: 'found',
      summary: payload.summary
    }
  } catch (error) {
    logger.warn(errorLogFields(error, {
      event: 'codex_responses_chat_bridge_compact_snapshot_read_failed',
      compactId: input.compactId
    }), 'Codex Responses Chat bridge compact snapshot 读取失败')
    return { outcome: 'payload_unavailable' }
  }
}

function isCodexCompactionInputItem(item: JsonRecord): boolean {
  return item.type === 'compaction' || item.type === 'compaction_summary'
}

function parseCodexCompactionReference(value: string): { compactId: string; digest: string } | undefined {
  const rest = value.slice(codexCompactionReferencePrefix.length)
  const separatorIndex = rest.indexOf('.')
  if (separatorIndex <= 0) return undefined
  const compactId = rest.slice(0, separatorIndex).trim()
  const digest = rest.slice(separatorIndex + 1).trim()
  if (!compactId || !/^[a-f0-9]{64}$/i.test(digest)) return undefined
  return { compactId, digest: digest.toLowerCase() }
}

function encodeInlineCodexCompactionSummary(summary: string): string {
  const payload = Buffer.from(JSON.stringify({ summary }), 'utf8').toString('base64url')
  return `${codexInlineCompactionSummaryPrefix}${payload}`
}

export function getCodexResponsesChatBridgeRequestState(req: Request): CodexResponsesChatBridgeRequestState | undefined {
  return (req as Request & { [requestStateSymbol]?: CodexResponsesChatBridgeRequestState })[requestStateSymbol]
}

function setCodexResponsesChatBridgeRequestState(req: Request, state: CodexResponsesChatBridgeRequestState): void {
  ;(req as Request & { [requestStateSymbol]?: CodexResponsesChatBridgeRequestState })[requestStateSymbol] = state
}

function isChatOnlyCodexResponsesBridgeStateRequest(
  req: Request,
  requestClientCompatibility: ClientCompatibilityCapability,
  accounts: readonly UpstreamAccount[]
): boolean {
  return isOpenAIResponsesPostRequest(req)
    && requestStream(req)
    && hasCodexResponsesChatBridgeRuntimeAccount(req, accounts, requestClientCompatibility)
}

export function hasCodexResponsesChatBridgeRuntimeAccount(
  req: Request,
  accounts: readonly UpstreamAccount[],
  requestClientCompatibility: ClientCompatibilityCapability
): boolean {
  const model = requestModel(req)
  return accounts.some((account) => {
    const explicitMappingBridge = isOpenAIResponsesToChatCompletionsModelMapping(
      resolveOpenAIAccountModelMapping(account, model, OPENAI_RESPONSES_FAMILY)
    )
    return explicitMappingBridge
      || (account.clientCompatibility === 'codex_responses' && requestClientCompatibility === 'codex_responses')
  })
}

function codexContextBoundary(input: {
  systemAccountId: string
  apiKeyId?: string
  groupId: string
  groupAccess: GroupUsageAccessMetadata
}): CodexContextStateBoundary {
  return {
    systemAccountId: input.systemAccountId,
    apiKeyId: input.apiKeyId,
    groupId: input.groupId,
    providerCode: input.groupAccess.providerCode
  }
}

async function parseGatewayJsonObject(req: Request, signal?: AbortSignal): Promise<JsonRecord> {
  if (isPlainObject(req.body)) {
    return { ...req.body }
  }
  const requestWithBody = req as GatewayRawBodyRequest
  if (requestWithBody.gatewayParsedJsonBodyAvailable && isPlainObject(requestWithBody.gatewayParsedJsonBody)) {
    return { ...requestWithBody.gatewayParsedJsonBody }
  }
  const rawBody = requestWithBody.rawBody
  if (!rawBody || rawBody.length === 0) {
    return {}
  }
  const bodyState = getGatewayRequestBodyState(req)
  if (bodyState?.jsonParseStatus === 'invalid_json') {
    return {}
  }
  const parsed = rawBody.length > gatewayJsonBodyInlineParseMaxBytes
    ? await parseGatewayJsonBodyInWorker(rawBody, undefined, signal)
    : JSON.parse(rawBody.toString('utf8')) as unknown
  return isPlainObject(parsed) ? { ...parsed } : {}
}

async function writeStatePayload(payload: CodexResponsesChatBridgeStatePayloadV2): Promise<CodexContextPayloadReference> {
  return writeSegmentPayload(payload.sessionId, payload)
}

async function writeCompactSnapshotPayload(payload: CodexResponsesChatBridgeCompactSnapshotPayloadV2): Promise<CodexContextPayloadReference> {
  return writeSegmentPayload(payload.sessionId, payload)
}

async function writeSegmentPayload(sessionId: string, payload: unknown): Promise<CodexContextPayloadReference> {
  const raw = Buffer.from(JSON.stringify(payload), 'utf8')
  if (raw.length > maxStoredPayloadBytes) {
    throw new Error(`Codex Responses Chat bridge 单条状态超过 ${maxStoredPayloadBytes} 字节上限`)
  }
  const compressed = gzipSync(raw)
  const sha256 = createHash('sha256').update(compressed).digest('hex')
  const storageKey = segmentStorageKey(sessionId, new Date())
  const storageOffsetBytes = await appendSegmentBytes(storageKey, compressed)
  return {
    storageKey,
    storageOffsetBytes,
    sha256,
    rawSizeBytes: raw.length,
    compressedSizeBytes: compressed.length,
    compression: 'gzip',
    schemaVersion: 2
  }
}

async function appendSegmentBytes(storageKey: string, bytes: Buffer): Promise<number> {
  return withSegmentWriteLock(storageKey, async () => {
    const finalPath = resolveStoragePath(storageKey)
    await mkdir(dirname(finalPath), { recursive: true })
    const handle = await open(finalPath, 'a+')
    try {
      const offset = (await handle.stat()).size
      await handle.write(bytes, 0, bytes.length, offset)
      return offset
    } finally {
      await handle.close()
    }
  })
}

async function withSegmentWriteLock<T>(storageKey: string, operation: () => Promise<T>): Promise<T> {
  const previous = segmentWriteLocks.get(storageKey) ?? Promise.resolve()
  let release!: () => void
  const current = new Promise<void>((resolveRelease) => {
    release = resolveRelease
  })
  const tail = previous.then(() => current, () => current)
  segmentWriteLocks.set(storageKey, tail)
  try {
    await previous.catch(() => undefined)
    return await operation()
  } finally {
    release()
    if (segmentWriteLocks.get(storageKey) === tail) {
      segmentWriteLocks.delete(storageKey)
    }
  }
}

async function readPayloadChain(rows: CodexContextResponseStateIndex[]): Promise<CodexResponsesChatBridgeStatePayloadV2[]> {
  const payloads: CodexResponsesChatBridgeStatePayloadV2[] = []
  for (const row of rows) {
    payloads.push(await readStatePayload(row))
  }
  return payloads
}

async function readStatePayload(row: CodexContextResponseStateIndex): Promise<CodexResponsesChatBridgeStatePayloadV2> {
  const parsed = await readSegmentPayload(row)
  if (!isStatePayload(parsed)) {
    throw new Error('Codex Responses Chat bridge 状态文件结构无效')
  }
  return parsed
}

async function readCompactSnapshotPayload(row: CodexContextCompactStateIndex): Promise<CodexResponsesChatBridgeCompactSnapshotPayloadV2> {
  const parsed = await readSegmentPayload(row)
  if (!isCompactSnapshotPayload(parsed)) {
    throw new Error('Codex Responses Chat bridge compact snapshot 文件结构无效')
  }
  return parsed
}

async function readSegmentPayload(row: CodexContextPayloadReference): Promise<unknown> {
  if (row.compressedSizeBytes > maxStoredPayloadBytes) {
    throw new Error('Codex Responses Chat bridge 状态文件超过读取上限')
  }
  const path = resolveStoragePath(row.storageKey)
  const compressed = Buffer.alloc(row.compressedSizeBytes)
  const handle = await open(path, 'r')
  let bytesRead = 0
  try {
    while (bytesRead < compressed.length) {
      const result = await handle.read(
        compressed,
        bytesRead,
        compressed.length - bytesRead,
        row.storageOffsetBytes + bytesRead
      )
      if (result.bytesRead === 0) break
      bytesRead += result.bytesRead
    }
  } finally {
    await handle.close()
  }
  if (bytesRead !== row.compressedSizeBytes) {
    throw new Error('Codex Responses Chat bridge 状态文件大小不匹配')
  }
  const sha256 = createHash('sha256').update(compressed).digest('hex')
  if (sha256 !== row.sha256) {
    throw new Error('Codex Responses Chat bridge 状态文件校验失败')
  }
  const raw = gunzipSync(compressed)
  if (raw.length !== row.rawSizeBytes || raw.length > maxStoredPayloadBytes) {
    throw new Error('Codex Responses Chat bridge 状态文件解压大小异常')
  }
  const parsed = JSON.parse(raw.toString('utf8')) as unknown
  return parsed
}

export async function deleteCodexContextStorageKeys(storageKeys: readonly string[]): Promise<number> {
  let deleted = 0
  for (const key of storageKeys) {
    const path = resolveStoragePath(key)
    if (!existsSync(path)) continue
    await rm(path, { force: true })
    deleted += 1
  }
  return deleted
}

function restoreResponsesInputFromPayloads(payloads: CodexResponsesChatBridgeStatePayloadV2[], currentInput: unknown): unknown[] {
  const restored: unknown[] = []
  for (const payload of payloads) {
    appendInstructionAsMessage(restored, payload.request.instructions)
    restored.push(...responsesInputAsItems(payload.request.input))
    restored.push(...cloneArray(payload.outputItems))
  }
  restored.push(...responsesInputAsItems(currentInput))
  return restored
}

function appendInstructionAsMessage(output: unknown[], instructions: unknown): void {
  const text = normalizedOptionalText(instructions)
  if (!text) return
  output.push({
    type: 'message',
    role: 'system',
    content: [
      {
        type: 'input_text',
        text
      }
    ]
  })
}

function responsesInputAsItems(input: unknown): unknown[] {
  if (typeof input === 'string') {
    return [{
      type: 'message',
      role: 'user',
      content: [
        {
          type: 'input_text',
          text: input
        }
      ]
    }]
  }
  if (Array.isArray(input)) {
    return cloneArray(input)
  }
  if (isPlainObject(input)) {
    return [cloneJson(input)]
  }
  return []
}

function cloneArray(input: unknown[]): unknown[] {
  return input.map((item) => cloneJson(item))
}

function cloneJson<T>(input: T): T {
  return JSON.parse(JSON.stringify(input)) as T
}

function assertRestoredInputSize(input: unknown): void {
  const bytes = Buffer.byteLength(JSON.stringify(input), 'utf8')
  if (bytes > maxRestoredInputBytes) {
    throw new Error(`Codex Responses Chat bridge 恢复后的上下文超过 ${maxRestoredInputBytes} 字节上限`)
  }
}

async function sendCodexBridgeStateFailure(input: {
  req: Request
  res: Response
  auditCapture: AuditCaptureContext
  usageContext: GatewayFailureUsageContext
  startedAt: number
}, failure: { statusCode: number; type: string; code: string; message: string }): Promise<void> {
  const responsePayload = gatewayErrorPayload(failure.message, failure.type, failure.code)
  await sendGatewayFailureResponse({
    req: input.req,
    res: input.res,
    auditCapture: input.auditCapture,
    usageContext: input.usageContext,
    startedAt: input.startedAt,
    statusCode: failure.statusCode,
    responsePayload,
    audit: {
      outcome: 'gateway_failed',
      errorPhase: 'request_validation',
      errorCode: failure.code,
      errorMessage: failure.message
    }
  })
}

function stateRestoreFailure(outcome: CodexContextRestoreFailureOutcome): {
  statusCode: number
  type: string
  code: string
  message: string
} {
  if (outcome === 'boundary_mismatch') {
    return {
      statusCode: 403,
      type: 'invalid_request_error',
      code: 'codex_bridge_previous_response_boundary_mismatch',
      message: 'previous_response_id 不属于当前 API Key、分组或供应商边界'
    }
  }
  if (outcome === 'chain_too_deep') {
    return {
      statusCode: 413,
      type: 'invalid_request_error',
      code: 'codex_bridge_previous_response_chain_too_deep',
      message: 'previous_response_id 上下文链过长，请先压缩上下文后继续'
    }
  }
  if (outcome === 'chain_broken') {
    return {
      statusCode: 404,
      type: 'invalid_request_error',
      code: 'codex_bridge_previous_response_chain_broken',
      message: 'previous_response_id 上下文链不完整或已被清理'
    }
  }
  return {
    statusCode: 404,
    type: 'invalid_request_error',
    code: 'codex_bridge_previous_response_not_found',
    message: 'previous_response_id 对应的服务端上下文不存在或已过期'
  }
}

function compactReferenceFailure(outcome: CodexCompactionReferenceFailureOutcome): {
  statusCode: number
  type: string
  code: string
  message: string
} {
  if (outcome === 'boundary_mismatch') {
    return {
      statusCode: 403,
      type: 'invalid_request_error',
      code: 'codex_bridge_compact_boundary_mismatch',
      message: 'compact snapshot 不属于当前 API Key、分组或供应商边界'
    }
  }
  return {
    statusCode: 404,
    type: 'invalid_request_error',
    code: 'codex_bridge_compact_snapshot_not_found',
    message: 'compact snapshot 不存在、已过期或校验失败'
  }
}

function digestText(value: string): string {
  return createHash('sha256').update(value, 'utf8').digest('hex')
}

function segmentStorageKey(sessionId: string, date: Date): string {
  const hourKey = date.toISOString().slice(0, 13).replace(/[-T:]/g, '')
  return `sessions/${safePathSegment(sessionId)}/segments/${hourKey}.json.gz`
}

function resolveStoragePath(storageKey: string): string {
  const normalizedKey = storageKey.replace(/\\/g, '/').replace(/^\/+/, '')
  if (normalizedKey.includes('..')) {
    throw new Error('Codex context storage key 非法')
  }
  const root = resolve(runtimeConfig.codexContextRoot)
  const target = resolve(root, normalizedKey)
  const rel = relative(root, target)
  if (!rel || rel.startsWith('..') || rel.startsWith(`..${sep}`) || resolve(root, rel) !== target) {
    throw new Error('Codex context storage key 超出数据目录')
  }
  return target
}

function safePathSegment(input: string): string {
  const normalized = input.trim()
  const readablePrefix = normalized.replace(/[^A-Za-z0-9_.-]/g, '_').slice(0, 96) || 'session'
  const digest = createHash('sha256').update(normalized, 'utf8').digest('hex').slice(0, 24)
  return `${readablePrefix}-${digest}`
}

function expiresAtFrom(now: Date): string {
  return new Date(now.getTime() + codexContextStateTtlMs).toISOString()
}

function normalizedOptionalText(value: unknown): string | undefined {
  return typeof value === 'string' && value.trim().length > 0 ? value.trim() : undefined
}

function isOpenAIResponsesPostRequest(req: Request): boolean {
  if (req.method.toUpperCase() !== 'POST') return false
  const { path } = splitPathAndQuery(req.originalUrl || req.path || '')
  return (path.replace(/^\/v1(?=\/|$)/, '') || '/') === '/responses'
}

function isStatePayload(value: unknown): value is CodexResponsesChatBridgeStatePayloadV2 {
  return isPlainObject(value)
    && value.schemaVersion === 2
    && typeof value.responseId === 'string'
    && typeof value.sessionId === 'string'
    && isPlainObject(value.boundary)
    && isPlainObject(value.request)
    && Array.isArray(value.outputItems)
}

function isCompactSnapshotPayload(value: unknown): value is CodexResponsesChatBridgeCompactSnapshotPayloadV2 {
  return isPlainObject(value)
    && value.schemaVersion === 2
    && typeof value.compactId === 'string'
    && typeof value.sessionId === 'string'
    && isPlainObject(value.boundary)
    && typeof value.summary === 'string'
}

function isPlainObject(value: unknown): value is JsonRecord {
  return typeof value === 'object' && value !== null && !Array.isArray(value)
}
