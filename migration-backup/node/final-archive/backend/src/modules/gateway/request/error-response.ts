import type { Request, Response } from 'express'

import { responseHeadersToObject, type AuditCaptureContext } from '../audit/capture.service.js'
import { downstreamConnectionClosedMessage } from '../response/client-abort.js'
import { gatewayErrorPayload, gatewayErrorPayloadForProtocol, sendGatewayErrorResponse } from '../response/responses.js'
import { isUpstreamRequestAbortedError } from '../upstream/request.js'
import { OpenAIOAuthCodexAdapterError } from '../adapters/gpt-codex/oauth-adapter.js'
import { gatewayProtocolClientErrorProtocolForRequest } from '../protocols/registry.js'
import { GatewayAgentGuidanceResponse, GatewayLocalProtocolResponse, GatewayRequestValidationError } from './validation-error.js'
import { gatewayRequestAbortSource } from './abort-attribution.js'

interface HandleGatewayRequestKnownErrorResponseInput {
  req: Request
  res: Response
  auditCapture: AuditCaptureContext
  error: unknown
  signal: AbortSignal
}

export function handleGatewayRequestKnownErrorResponse(input: HandleGatewayRequestKnownErrorResponseInput): boolean {
  const { req, res, auditCapture, error, signal } = input

  const abortSource = gatewayRequestAbortSource(req)
  if (signal.aborted && abortSource === 'server_diagnostic_timeout') {
    const statusCode = 504
    const responsePayload = gatewayErrorPayload(
      '服务端账户诊断超时',
      'gateway_timeout',
      'server_diagnostic_timeout'
    )
    const protocol = gatewayProtocolClientErrorProtocolForRequest(req)
    const clientPayload = gatewayErrorPayloadForProtocol(responsePayload, protocol)
    sendGatewayErrorResponse(res, statusCode, responsePayload, { protocol })
    auditCapture.finalize({
      outcome: 'gateway_failed',
      success: false,
      statusCode,
      responseHeaders: responseHeadersToObject(res),
      responseBody: JSON.stringify(clientPayload),
      responsePartType: 'gateway_error',
      errorPhase: 'server_diagnostic',
      errorCode: 'server_diagnostic_timeout',
      errorMessage: responsePayload.error.message
    })
    return true
  }

  if (signal.aborted && abortSource === 'server_diagnostic_cancel') {
    const statusCode = 500
    const responsePayload = gatewayErrorPayload(
      '服务端账户诊断已取消',
      'gateway_cancelled',
      'server_diagnostic_cancelled'
    )
    const protocol = gatewayProtocolClientErrorProtocolForRequest(req)
    const clientPayload = gatewayErrorPayloadForProtocol(responsePayload, protocol)
    sendGatewayErrorResponse(res, statusCode, responsePayload, { protocol })
    auditCapture.finalize({
      outcome: 'gateway_failed',
      success: false,
      statusCode,
      responseHeaders: responseHeadersToObject(res),
      responseBody: JSON.stringify(clientPayload),
      responsePartType: 'gateway_error',
      errorPhase: 'server_diagnostic',
      errorCode: 'server_diagnostic_cancelled',
      errorMessage: responsePayload.error.message
    })
    return true
  }

  if (isUpstreamRequestAbortedError(error) || signal.aborted) {
    auditCapture.finalize({
      outcome: 'downstream_closed',
      success: false,
      statusCode: res.statusCode,
      responseHeaders: responseHeadersToObject(res),
      errorPhase: 'downstream',
      errorCode: 'downstream_connection_closed',
      errorMessage: downstreamConnectionClosedMessage
    })
    if (!res.writableEnded && !res.destroyed) {
      res.end()
    }
    return true
  }

  if (error instanceof GatewayAgentGuidanceResponse && !error.accountScoped) {
    const responseBody = sendAgentGuidanceResponse(res, error)
    auditCapture.finalize({
      outcome: 'success',
      success: true,
      statusCode: 200,
      responseHeaders: responseHeadersToObject(res),
      responseBody,
      responsePartType: 'gateway_response',
      errorPhase: 'request_validation',
      errorCode: error.code,
      errorMessage: error.message
    })
    return true
  }

  if (error instanceof GatewayLocalProtocolResponse) {
    res.status(error.statusCode)
    res.setHeader('content-type', error.contentType)
    if (error.contentType.startsWith('text/event-stream')) {
      res.setHeader('cache-control', 'no-cache')
    }
    res.end(error.body)
    auditCapture.finalize({
      outcome: 'success',
      success: true,
      statusCode: error.statusCode,
      responseHeaders: responseHeadersToObject(res),
      responseBody: error.body,
      responsePartType: 'gateway_response',
      errorPhase: 'request_validation',
      errorCode: error.code,
      errorMessage: error.message
    })
    return true
  }

  if (error instanceof OpenAIOAuthCodexAdapterError || error instanceof GatewayRequestValidationError) {
    const statusCode = error.statusCode
    const responsePayload = gatewayErrorPayload(error.message, error.type, error.code)
    const protocol = gatewayProtocolClientErrorProtocolForRequest(req)
    const clientPayload = gatewayErrorPayloadForProtocol(responsePayload, protocol)
    sendGatewayErrorResponse(res, statusCode, responsePayload, { protocol })
    auditCapture.finalize({
      outcome: 'gateway_failed',
      success: false,
      statusCode,
      responseHeaders: responseHeadersToObject(res),
      responseBody: JSON.stringify(clientPayload),
      responsePartType: 'gateway_error',
      errorPhase: 'request_validation',
      errorCode: error.code,
      errorMessage: error.message
    })
    return true
  }

  return false
}

function sendAgentGuidanceResponse(res: Response, guidance: GatewayAgentGuidanceResponse): string {
  if (guidance.protocol === 'gemini') {
    if (guidance.stream) {
      const body = geminiGuidanceSse(guidance)
      res.status(200)
      res.setHeader('content-type', 'text/event-stream; charset=utf-8')
      res.setHeader('cache-control', 'no-cache')
      res.end(body)
      return body
    }
    const body = geminiGuidanceJson(guidance)
    res.status(200).json(body)
    return JSON.stringify(body)
  }
  if (guidance.protocol === 'messages') {
    if (guidance.stream) {
      const body = anthropicMessagesGuidanceSse(guidance)
      res.status(200)
      res.setHeader('content-type', 'text/event-stream; charset=utf-8')
      res.setHeader('cache-control', 'no-cache')
      res.end(body)
      return body
    }
    const body = anthropicMessagesGuidanceJson(guidance)
    res.status(200).json(body)
    return JSON.stringify(body)
  }
  if (guidance.protocol === 'responses') {
    if (guidance.stream) {
      const body = responsesGuidanceSse(guidance)
      res.status(200)
      res.setHeader('content-type', 'text/event-stream; charset=utf-8')
      res.setHeader('cache-control', 'no-cache')
      res.end(body)
      return body
    }
    const body = responsesGuidanceJson(guidance)
    res.status(200).json(body)
    return JSON.stringify(body)
  }
  if (guidance.stream) {
    const body = chatGuidanceSse(guidance)
    res.status(200)
    res.setHeader('content-type', 'text/event-stream; charset=utf-8')
    res.setHeader('cache-control', 'no-cache')
    res.end(body)
    return body
  }
  const body = chatGuidanceJson(guidance)
  res.status(200).json(body)
  return JSON.stringify(body)
}

function anthropicMessagesGuidanceJson(guidance: GatewayAgentGuidanceResponse): Record<string, unknown> {
  const created = Math.floor(Date.now() / 1000)
  return {
    id: `msg_guidance_${created}`,
    type: 'message',
    role: 'assistant',
    model: guidance.model,
    content: [{ type: 'text', text: guidance.message }],
    stop_reason: 'end_turn',
    stop_sequence: null,
    usage: zeroAnthropicMessagesUsage()
  }
}

function anthropicMessagesGuidanceSse(guidance: GatewayAgentGuidanceResponse): string {
  const created = Math.floor(Date.now() / 1000)
  const id = `msg_guidance_${created}`
  return [
    anthropicSse('message_start', {
      type: 'message_start',
      message: {
        id,
        type: 'message',
        role: 'assistant',
        model: guidance.model,
        content: [],
        stop_reason: null,
        stop_sequence: null,
        usage: { input_tokens: 0, output_tokens: 0 }
      }
    }),
    anthropicSse('content_block_start', {
      type: 'content_block_start',
      index: 0,
      content_block: { type: 'text', text: '' }
    }),
    anthropicSse('content_block_delta', {
      type: 'content_block_delta',
      index: 0,
      delta: { type: 'text_delta', text: guidance.message }
    }),
    anthropicSse('content_block_stop', {
      type: 'content_block_stop',
      index: 0
    }),
    anthropicSse('message_delta', {
      type: 'message_delta',
      delta: { stop_reason: 'end_turn', stop_sequence: null },
      usage: { output_tokens: 0 }
    }),
    anthropicSse('message_stop', {
      type: 'message_stop'
    })
  ].join('')
}

function geminiGuidanceJson(guidance: GatewayAgentGuidanceResponse): Record<string, unknown> {
  return {
    candidates: [{
      content: {
        role: 'model',
        parts: [{ text: guidance.message }]
      },
      finishReason: 'STOP'
    }],
    usageMetadata: zeroGeminiUsage(),
    modelVersion: guidance.model
  }
}

function geminiGuidanceSse(guidance: GatewayAgentGuidanceResponse): string {
  return [
    geminiSse({
      candidates: [{
        content: {
          role: 'model',
          parts: [{ text: guidance.message }]
        }
      }],
      modelVersion: guidance.model
    }),
    geminiSse({
      candidates: [{
        finishReason: 'STOP'
      }],
      usageMetadata: zeroGeminiUsage(),
      modelVersion: guidance.model
    })
  ].join('')
}

function chatGuidanceJson(guidance: GatewayAgentGuidanceResponse): Record<string, unknown> {
  const created = Math.floor(Date.now() / 1000)
  return {
    id: `chatcmpl_guidance_${created}`,
    object: 'chat.completion',
    created,
    model: guidance.model,
    choices: [{
      index: 0,
      message: {
        role: 'assistant',
        content: guidance.message
      },
      finish_reason: 'stop'
    }],
    usage: zeroChatUsage()
  }
}

function chatGuidanceSse(guidance: GatewayAgentGuidanceResponse): string {
  const created = Math.floor(Date.now() / 1000)
  const id = `chatcmpl_guidance_${created}`
  return [
    chatSse({ id, object: 'chat.completion.chunk', created, model: guidance.model, choices: [{ index: 0, delta: { role: 'assistant' }, finish_reason: null }] }),
    chatSse({ id, object: 'chat.completion.chunk', created, model: guidance.model, choices: [{ index: 0, delta: { content: guidance.message }, finish_reason: null }] }),
    chatSse({ id, object: 'chat.completion.chunk', created, model: guidance.model, choices: [{ index: 0, delta: {}, finish_reason: 'stop' }] }),
    'data: [DONE]\n\n'
  ].join('')
}

function responsesGuidanceJson(guidance: GatewayAgentGuidanceResponse): Record<string, unknown> {
  const created = Math.floor(Date.now() / 1000)
  const responseId = `resp_guidance_${created}`
  const messageId = `msg_guidance_${created}`
  const output = [responsesGuidanceMessageItem(messageId, guidance.message)]
  return responsesGuidanceSnapshot({
    responseId,
    created,
    model: guidance.model,
    output,
    status: 'completed'
  })
}

function responsesGuidanceSse(guidance: GatewayAgentGuidanceResponse): string {
  const created = Math.floor(Date.now() / 1000)
  const responseId = `resp_guidance_${created}`
  const messageId = `msg_guidance_${created}`
  const contentIndex = 0
  const completedItem = responsesGuidanceMessageItem(messageId, guidance.message)
  const startedItem = { ...completedItem, status: 'in_progress', content: [] }
  const textPart = { type: 'output_text', text: guidance.message, annotations: [] }
  const completedSnapshot = responsesGuidanceSnapshot({
    responseId,
    created,
    model: guidance.model,
    output: [completedItem],
    status: 'completed'
  })
  return [
    responsesSse('response.created', {
      type: 'response.created',
      response: responsesGuidanceSnapshot({ responseId, created, model: guidance.model, output: [], status: 'in_progress' })
    }),
    responsesSse('response.in_progress', {
      type: 'response.in_progress',
      response: responsesGuidanceSnapshot({ responseId, created, model: guidance.model, output: [], status: 'in_progress' })
    }),
    responsesSse('response.output_item.added', {
      type: 'response.output_item.added',
      output_index: 0,
      item: startedItem
    }),
    responsesSse('response.content_part.added', {
      type: 'response.content_part.added',
      item_id: messageId,
      output_index: 0,
      content_index: contentIndex,
      part: { type: 'output_text', text: '', annotations: [] }
    }),
    responsesSse('response.output_text.delta', {
      type: 'response.output_text.delta',
      item_id: messageId,
      output_index: 0,
      content_index: contentIndex,
      delta: guidance.message
    }),
    responsesSse('response.output_text.done', {
      type: 'response.output_text.done',
      item_id: messageId,
      output_index: 0,
      content_index: contentIndex,
      text: guidance.message
    }),
    responsesSse('response.content_part.done', {
      type: 'response.content_part.done',
      item_id: messageId,
      output_index: 0,
      content_index: contentIndex,
      part: textPart
    }),
    responsesSse('response.output_item.done', {
      type: 'response.output_item.done',
      output_index: 0,
      item: completedItem
    }),
    responsesSse('response.completed', {
      type: 'response.completed',
      response: completedSnapshot
    })
  ].join('')
}

function responsesGuidanceMessageItem(messageId: string, text: string): Record<string, unknown> {
  return {
    id: messageId,
    type: 'message',
    status: 'completed',
    role: 'assistant',
    content: [{ type: 'output_text', text, annotations: [] }]
  }
}

function responsesGuidanceSnapshot(input: {
  responseId: string
  created: number
  model: string
  output: Array<Record<string, unknown>>
  status: 'in_progress' | 'completed'
}): Record<string, unknown> {
  return {
    id: input.responseId,
    object: 'response',
    created_at: input.created,
    status: input.status,
    completed_at: input.status === 'completed' ? input.created : null,
    error: null,
    incomplete_details: null,
    instructions: null,
    max_output_tokens: null,
    model: input.model,
    output: input.output,
    output_text: input.output
      .flatMap((item) => Array.isArray(item.content) ? item.content : [])
      .filter((part): part is Record<string, unknown> => Boolean(part) && typeof part === 'object' && !Array.isArray(part))
      .map((part) => typeof part.text === 'string' ? part.text : '')
      .join(''),
    parallel_tool_calls: false,
    previous_response_id: null,
    reasoning: { effort: null, summary: null },
    store: false,
    temperature: null,
    text: { format: { type: 'text' } },
    tool_choice: 'auto',
    tools: [],
    top_p: null,
    truncation: 'disabled',
    usage: input.status === 'completed' ? zeroResponsesUsage() : null,
    user: null,
    metadata: { gateway_guidance: true }
  }
}

function chatSse(payload: Record<string, unknown>): string {
  return `data: ${JSON.stringify(payload)}\n\n`
}

function responsesSse(event: string, payload: Record<string, unknown>): string {
  return `event: ${event}\ndata: ${JSON.stringify(payload)}\n\n`
}

function anthropicSse(event: string, payload: Record<string, unknown>): string {
  return `event: ${event}\ndata: ${JSON.stringify(payload)}\n\n`
}

function geminiSse(payload: Record<string, unknown>): string {
  return `data: ${JSON.stringify(payload)}\n\n`
}

function zeroChatUsage(): Record<string, unknown> {
  return {
    prompt_tokens: 0,
    completion_tokens: 0,
    total_tokens: 0
  }
}

function zeroResponsesUsage(): Record<string, unknown> {
  return {
    input_tokens: 0,
    output_tokens: 0,
    total_tokens: 0,
    input_tokens_details: { cached_tokens: 0 },
    output_tokens_details: { reasoning_tokens: 0 }
  }
}

function zeroAnthropicMessagesUsage(): Record<string, unknown> {
  return {
    input_tokens: 0,
    output_tokens: 0
  }
}

function zeroGeminiUsage(): Record<string, unknown> {
  return {
    promptTokenCount: 0,
    candidatesTokenCount: 0,
    totalTokenCount: 0
  }
}
