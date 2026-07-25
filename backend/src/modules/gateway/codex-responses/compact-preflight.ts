import type { Request, Response } from 'express'

import type { GroupUsageAccessMetadata } from '../../../storage/repositories.js'
import type { ClientCompatibilityCapability, GroupSchedulingPolicy } from '../../../domain/types.js'
import { OPENAI_CHAT_COMPLETIONS_FAMILY, OPENAI_RESPONSES_FAMILY } from '../../../domain/provider-protocol.js'
import type { GatewaySettings } from '../policy/account-error-policy.service.js'
import type { AuditCaptureContext } from '../audit/capture.service.js'
import type { GatewayAccountModelPriority } from '../dispatch/model-filter.js'
import { responseHeadersToObject } from '../audit/capture.service.js'
import type { ClientIpAccountAvoidanceTracker } from '../runtime/client-ip-account-avoidance.service.js'
import type { GatewayUpstreamRequestCoordinationContext } from '../dispatch/upstream-dispatch.js'
import type { UpstreamAccount } from '../protocols/openai-v1/route-helpers.js'
import { splitPathAndQuery } from '../protocols/openai-v1/route-helpers.js'
import type { OpenAIGatewayRequestLane } from '../protocols/openai-v1/request-lane.js'
import { parseGatewayJsonBodyInWorker } from '../request/json-parser.js'
import {
  createGatewayRequestBodyState,
  gatewayJsonBodyInlineParseMaxBytes,
  getGatewayRequestBodyState,
  type GatewayRawBodyRequest
} from '../request/body.js'
import { setGatewayModelMappingSourceEndpointFamilyOverride } from '../protocols/openai-v1/model-mapping.js'
import { gatewayErrorPayload } from '../response/responses.js'
import { sendGatewayFailureResponse } from '../response/failure-response.js'
import type { GatewayFailureUsageContext } from '../usage/records.js'
import { fetchFirstAvailableUpstream } from '../dispatch/upstream-dispatch.js'
import { readUpstreamBodyLimited } from '../upstream/body.js'
import { resolveGatewayUsageModel } from '../../providers/drivers/registry.js'
import {
  createCodexResponsesChatBridgeCompactSnapshot,
  codexResponsesContextAllowsAccount,
  hasExplicitCodexResponsesChatBridgeRuntimeAccount,
  prepareCodexResponsesCompactDispatchForAccounts,
  restoreCodexResponsesChatBridgeInputForCompact
} from './chat-bridge-state.js'

type JsonRecord = Record<string, unknown>

export async function applyCodexResponsesChatBridgeCompactPreflight(input: {
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
  dispatchAccounts: readonly UpstreamAccount[]
  activeGatewaySettings: GatewaySettings
  clientIpAccountAvoidanceTracker: ClientIpAccountAvoidanceTracker
  modelPriority: GatewayAccountModelPriority
  requestLane: OpenAIGatewayRequestLane
  groupSchedulingPolicy?: GroupSchedulingPolicy
  requestCoordination: GatewayUpstreamRequestCoordinationContext
  onDispatchedAccount?: (account: UpstreamAccount) => Promise<void>
  signal?: AbortSignal
}): Promise<
  | { outcome: 'continued'; accounts: UpstreamAccount[] }
  | { outcome: 'completed' }
> {
  if (!isOpenAIResponsesCompactPostRequest(input.req)
    || !prepareCodexResponsesCompactDispatchForAccounts(input.req, input.dispatchAccounts)) {
    return {
      outcome: 'continued',
      accounts: input.dispatchAccounts.filter((account) => codexResponsesContextAllowsAccount(input.req, account))
    }
  }
  const bridgeDispatchAccounts = input.dispatchAccounts.filter((account) => (
    hasExplicitCodexResponsesChatBridgeRuntimeAccount(input.req, [account])
  ))
  const body = await parseGatewayJsonObject(input.req, input.signal)
  const previousResponseId = normalizedOptionalText(body.previous_response_id)
  const boundary = {
    systemAccountId: input.systemAccountId,
    apiKeyId: input.apiKeyId,
    groupId: input.groupId,
    providerCode: input.groupAccess.providerCode
  }
  const restoreResult = await restoreCodexResponsesChatBridgeInputForCompact({
    previousResponseId,
    boundary,
    currentInput: body.input
  })
  if (restoreResult.outcome !== 'found' && restoreResult.outcome !== 'no_previous') {
    await sendCompactFailure(input, restoreFailureForCompact(restoreResult.outcome))
    return { outcome: 'completed' }
  }

  const summaryRequest = buildCompactSummaryChatBody({
    model: normalizedOptionalText(body.model) ?? 'codex-compact-summary',
    restoredInput: restoreResult.input
  })
  const syntheticReq = buildSyntheticChatCompletionsRequest(input.req, summaryRequest)
  let upstreamResult: Awaited<ReturnType<typeof fetchFirstAvailableUpstream>> | undefined
  try {
    upstreamResult = await fetchFirstAvailableUpstream(
      syntheticReq,
      bridgeDispatchAccounts,
      input.activeGatewaySettings,
      input.usageContext,
      input.auditCapture,
      undefined,
      input.signal,
      input.clientIpAccountAvoidanceTracker,
      input.requestLane,
      input.groupSchedulingPolicy,
      true,
      input.requestClientCompatibility,
      input.modelPriority,
      undefined,
      false,
      input.requestCoordination,
      true
    )
    await input.onDispatchedAccount?.(upstreamResult.account)
    const readResult = await readUpstreamBodyLimited(upstreamResult.response.body, {
      maxBytes: 1024 * 1024,
      startedAt: input.startedAt,
      signal: input.signal,
      onFirstByte: upstreamResult.markFirstOutput
    })
    upstreamResult.hotQualityAttempt.markFirstByte(readResult.firstByteMs)
    const opaqueUpstreamResponse = !upstreamResult.response.ok && !readResult.truncated
    await upstreamResult.hotQualityAttempt.recordTerminal({
      outcomeClass: readResult.truncated
        ? 'incomplete_response'
        : opaqueUpstreamResponse
          ? 'upstream_response_failure'
          : 'completed_response',
      failureScope: readResult.truncated ? 'protocol_model' : 'none',
      source: opaqueUpstreamResponse ? 'upstream_response' : 'gateway_transport',
      firstByteMs: readResult.firstByteMs
    })
    const summary = extractChatCompletionSummary(readResult.bodyText)
    if (!summary) {
      await sendCompactFailure(input, {
        statusCode: 502,
        type: 'bad_gateway',
        code: 'codex_bridge_compact_summary_empty',
        message: '上游摘要模型没有返回可用的压缩摘要'
      })
      return { outcome: 'completed' }
    }
    const summaryUpstreamModel = resolveGatewayUsageModel(
      upstreamResult.account,
      normalizedOptionalText(summaryRequest.model),
      'chat_completions'
    ).upstreamModel ?? normalizedOptionalText(summaryRequest.model)
    const compactSnapshot = await createCodexResponsesChatBridgeCompactSnapshot({
      sessionId: restoreResult.sessionId,
      sourceResponseId: previousResponseId,
      boundary,
      summary,
      upstreamAccountId: upstreamResult.account.id,
      model: normalizedOptionalText(body.model),
      upstreamModel: summaryUpstreamModel
    })
    const responsePayload = buildCodexCompactResponse({
      compactId: compactSnapshot.compactId,
      encryptedContent: compactSnapshot.encryptedContent
    })
    input.auditCapture.addGatewayMetadata({
      label: 'codex_responses_chat_bridge_compact',
      metadata: {
        mode: 'gateway_summary_compact',
        previousResponseId,
        sessionId: restoreResult.sessionId,
        restoredResponseCount: restoreResult.responseCount,
        accountId: upstreamResult.account.id,
        upstreamStatus: upstreamResult.response.status
      }
    })
    input.res.status(200).json(responsePayload)
    input.auditCapture.finalize({
      outcome: 'success',
      success: true,
      statusCode: 200,
      responseHeaders: responseHeadersToObject(input.res),
      responseBody: JSON.stringify(responsePayload),
      responsePartType: 'gateway_response',
      accountId: upstreamResult.account.id
    })
    return { outcome: 'completed' }
  } catch (error) {
    await upstreamResult?.hotQualityAttempt.recordTerminal({
      outcomeClass: input.signal?.aborted ? 'client_cancellation' : 'read_interruption',
      failureScope: input.signal?.aborted ? 'none' : 'protocol_model',
      source: input.signal?.aborted ? 'request_lifecycle' : 'gateway_transport'
    })
    const message = error instanceof Error ? error.message : String(error)
    await sendCompactFailure(input, {
      statusCode: 502,
      type: 'bad_gateway',
      code: 'codex_bridge_compact_summary_failed',
      message: `上游摘要请求失败：${message}`
    })
    return { outcome: 'completed' }
  } finally {
    upstreamResult?.releaseConcurrency()
  }
}

function buildCompactSummaryChatBody(input: { model: string; restoredInput: unknown[] }): JsonRecord {
  return {
    model: input.model,
    stream: false,
    messages: [
      {
        role: 'system',
        content: [
          '你负责为 Codex Responses 会话做上下文压缩。',
          '输出一段可继续对话的摘要，保留用户目标、关键决策、工具调用结果、文件路径、错误和待办。',
          '不要输出解释、标题、Markdown 包装或无关寒暄。'
        ].join('\n')
      },
      {
        role: 'user',
        content: compactContextText(input.restoredInput)
      }
    ]
  }
}

function compactContextText(input: unknown[]): string {
  const text = JSON.stringify(input)
  const maxChars = 120000
  return text.length <= maxChars ? text : `${text.slice(0, maxChars)}\n[truncated]`
}

function buildCodexCompactResponse(input: { compactId: string; encryptedContent: string }): JsonRecord {
  const responseId = `resp_${input.compactId.replace(/^cmp_/, '')}`
  return {
    id: responseId,
    object: 'response.compaction',
    created_at: Math.floor(Date.now() / 1000),
    output: [
      {
        id: input.compactId,
        type: 'compaction',
        encrypted_content: input.encryptedContent
      }
    ],
    usage: {
      input_tokens: 0,
      input_tokens_details: {
        cached_tokens: 0
      },
      output_tokens: 0,
      output_tokens_details: {
        reasoning_tokens: 0
      },
      total_tokens: 0
    }
  }
}

function extractChatCompletionSummary(bodyText: string): string | undefined {
  let parsed: unknown
  try {
    parsed = JSON.parse(bodyText) as unknown
  } catch {
    return undefined
  }
  if (!isPlainObject(parsed)) return undefined
  const choices = Array.isArray(parsed.choices) ? parsed.choices : []
  for (const choice of choices) {
    if (!isPlainObject(choice)) continue
    const message = isPlainObject(choice.message) ? choice.message : undefined
    const content = normalizedOptionalText(message?.content)
    if (content) return content
  }
  return undefined
}

function buildSyntheticChatCompletionsRequest(sourceReq: Request, body: JsonRecord): Request {
  const rawBody = Buffer.from(JSON.stringify(body), 'utf8')
  const synthetic = Object.create(sourceReq) as GatewayRawBodyRequest
  synthetic.method = 'POST'
  synthetic.url = '/v1/chat/completions'
  synthetic.originalUrl = '/v1/chat/completions'
  Object.defineProperty(synthetic, 'path', {
    configurable: true,
    enumerable: true,
    value: '/v1/chat/completions'
  })
  synthetic.headers = {
    ...sourceReq.headers,
    accept: 'application/json',
    'content-type': 'application/json'
  }
  synthetic.body = body
  synthetic.rawBody = rawBody
  synthetic.gatewayParsedJsonBodyAvailable = true
  synthetic.gatewayParsedJsonBody = body
  synthetic.gatewayUpstreamBodyCache = undefined
  setGatewayModelMappingSourceEndpointFamilyOverride(synthetic, OPENAI_CHAT_COMPLETIONS_FAMILY)
  synthetic.gatewayRequestBody = createGatewayRequestBodyState({
    rawBody,
    contentType: 'application/json',
    jsonParseStatus: 'parsed',
    parsedBody: body,
    stream: false
  })
  return synthetic
}

async function parseGatewayJsonObject(req: Request, signal?: AbortSignal): Promise<JsonRecord> {
  if (isPlainObject(req.body)) {
    return { ...req.body }
  }
  const requestWithBody = req as GatewayRawBodyRequest
  if (requestWithBody.gatewayParsedJsonBodyAvailable && isPlainObject(requestWithBody.gatewayParsedJsonBody)) {
    return { ...requestWithBody.gatewayParsedJsonBody }
  }
  const bodyState = getGatewayRequestBodyState(req)
  if (bodyState?.jsonParseStatus === 'invalid_json') {
    return {}
  }
  const rawBody = requestWithBody.rawBody
  if (!rawBody || rawBody.length === 0) {
    return {}
  }
  const parsed = rawBody.length > gatewayJsonBodyInlineParseMaxBytes
    ? await parseGatewayJsonBodyInWorker(rawBody, undefined, signal)
    : JSON.parse(rawBody.toString('utf8')) as unknown
  return isPlainObject(parsed) ? { ...parsed } : {}
}

function isOpenAIResponsesCompactPostRequest(req: Request): boolean {
  if (req.method.toUpperCase() !== 'POST') return false
  const { path } = splitPathAndQuery(req.originalUrl || req.path || '')
  return (path.replace(/^\/v1(?=\/|$)/, '') || '/') === '/responses/compact'
}

function restoreFailureForCompact(outcome: string): {
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
      message: 'compact 上下文不属于当前 API Key、分组或供应商边界'
    }
  }
  if (outcome === 'chain_too_deep') {
    return {
      statusCode: 413,
      type: 'invalid_request_error',
      code: 'codex_bridge_compact_chain_too_deep',
      message: 'compact 上下文链过长，无法在当前网关限制内压缩'
    }
  }
  return {
    statusCode: 404,
    type: 'invalid_request_error',
    code: 'codex_bridge_compact_context_not_found',
    message: 'compact 对应的服务端上下文不存在、已过期或校验失败'
  }
}

async function sendCompactFailure(input: {
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

function normalizedOptionalText(value: unknown): string | undefined {
  return typeof value === 'string' && value.trim().length > 0 ? value.trim() : undefined
}

function isPlainObject(value: unknown): value is JsonRecord {
  return typeof value === 'object' && value !== null && !Array.isArray(value)
}
