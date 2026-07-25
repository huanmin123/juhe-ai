import type { Request } from 'express'

import {
  getGatewayRequestBodyState,
  requestBodyHasImageGenerationHint,
  type GatewayRawBodyRequest
} from '../../request/body.js'

export type OpenAIGatewayRequestLane = 'text' | 'image'

type UpstreamReplayRequest = Pick<Request, 'method' | 'originalUrl' | 'path'>
  & Partial<Pick<GatewayRawBodyRequest,
    | 'body'
    | 'gatewayParsedJsonBodyAvailable'
    | 'gatewayParsedJsonBody'
    | 'gatewayRequestBody'
  >>

/**
 * Requests that can create a resource or execute provider-hosted work are not
 * safe to replay automatically. Once the upstream transport has been invoked,
 * the gateway cannot know whether the provider accepted the request.
 */
export function automaticUpstreamReplayAllowedAfterDispatch(
  req: UpstreamReplayRequest,
  lane: OpenAIGatewayRequestLane
): boolean {
  if (lane === 'image') return false

  const method = req.method.toUpperCase()
  if (method === 'GET' || method === 'HEAD') return true
  if (method !== 'POST') return false

  const path = (req.originalUrl || req.path || '').split('?', 1)[0]
  if (/\/responses$/i.test(path) && responsesRequestRequiresAtMostOnce(req)) {
    return false
  }
  const anthropicEndpoint = anthropicReplayEndpoint(path)
  if (anthropicEndpoint === 'messages' && anthropicMessagesRequestRequiresAtMostOnce(req)) {
    return false
  }

  return /\/(?:chat\/completions|responses|embeddings)$/i.test(path)
    || anthropicEndpoint !== undefined
    || /\/models\/[^/]+:(?:generateContent|streamGenerateContent|countTokens|embedContent)$/i.test(path)
    || /\/interactions\/[^/]+$/i.test(path)
}

function anthropicReplayEndpoint(path: string): 'messages' | 'count_tokens' | undefined {
  const normalized = path.toLowerCase()
  if (normalized === '/messages' || normalized === '/v1/messages') return 'messages'
  if (normalized === '/messages/count_tokens' || normalized === '/v1/messages/count_tokens') return 'count_tokens'
  return undefined
}

function anthropicMessagesRequestRequiresAtMostOnce(req: UpstreamReplayRequest): boolean {
  const body = replayInspectionBody(req)
  if (!body) {
    // As with Responses, an uninspectable request cannot be positively proven
    // to exclude provider-hosted tools or remote state.
    return true
  }

  if (anthropicMcpServersRequireAtMostOnce(body.mcp_servers) || body.container !== undefined) {
    return true
  }

  return anthropicToolSelectionRequiresAtMostOnce(body.tools)
}

function responsesRequestRequiresAtMostOnce(req: UpstreamReplayRequest): boolean {
  const body = replayInspectionBody(req)
  if (!body) {
    // Large JSON is intentionally not parsed on the main thread, and adapters
    // may expose neither body representation. Without positive foreground/tool
    // metadata, replay safety cannot be established.
    return true
  }
  if (body.background === true) return true

  return responsesToolSelectionRequiresAtMostOnce(body.tools)
    || responsesToolSelectionRequiresAtMostOnce(body.tool_choice)
}

function replayInspectionBody(req: UpstreamReplayRequest): Record<string, unknown> | undefined {
  if (isRecord(req.body)) return req.body
  if (req.gatewayParsedJsonBodyAvailable && isRecord(req.gatewayParsedJsonBody)) {
    return req.gatewayParsedJsonBody
  }
  return undefined
}

function responsesToolSelectionRequiresAtMostOnce(value: unknown): boolean {
  if (Array.isArray(value)) {
    return value.some(responsesToolSelectionRequiresAtMostOnce)
  }
  if (!isRecord(value)) return false

  const type = typeof value.type === 'string' ? value.type.trim().toLowerCase() : ''
  if (!type) return false
  if (type === 'allowed_tools') {
    return responsesToolSelectionRequiresAtMostOnce(value.tools)
  }

  // Function/custom tools are returned to the client for execution. Every
  // other known or future Responses tool type may execute provider-side work.
  return type !== 'function' && type !== 'custom'
}

function anthropicToolSelectionRequiresAtMostOnce(value: unknown): boolean {
  if (value === undefined) return false
  if (!Array.isArray(value)) return true

  return value.some((item) => {
    if (!isRecord(item)) return true
    if (!Object.hasOwn(item, 'type')) return false
    if (typeof item.type !== 'string') return true
    const type = item.type.trim().toLowerCase()

    // Ordinary Anthropic client tools normally omit type; explicit non-custom
    // types are provider-hosted/server tools and may already have executed.
    return type !== 'custom'
  })
}

function anthropicMcpServersRequireAtMostOnce(value: unknown): boolean {
  if (value === undefined) return false
  if (Array.isArray(value)) return value.length > 0
  return true
}

function isRecord(value: unknown): value is Record<string, unknown> {
  return typeof value === 'object' && value !== null && !Array.isArray(value)
}

export function resolveOpenAIGatewayRequestLane(req: Request): OpenAIGatewayRequestLane {
  if (isOpenAIGatewayImageEndpointOrModelRequest(req)) {
    return 'image'
  }

  const bodyState = getGatewayRequestBodyState(req)
  if (bodyState?.imageGeneration || requestBodyHasImageGenerationHint(req.body)) {
    return 'image'
  }
  return 'text'
}

export function isOpenAIGatewayImageEndpointOrModelRequest(req: Request): boolean {
  const path = String(req.path || req.originalUrl || '').split('?')[0]?.toLowerCase() ?? ''
  if (path === '/images' || path.startsWith('/images/') || path === '/v1/images' || path.startsWith('/v1/images/')) {
    return true
  }

  const model = requestModelHint(req)
  return isOpenAIGatewayImageGenerationModel(model)
}

function requestModelHint(req: Request): string | undefined {
  const bodyModel = typeof req.body?.model === 'string' ? req.body.model : undefined
  return bodyModel ?? getGatewayRequestBodyState(req)?.model
}

export function isOpenAIGatewayImageGenerationModel(model: string | undefined): boolean {
  const normalized = model?.trim().toLowerCase()
  return Boolean(normalized && (
    normalized.startsWith('gpt-image')
    || normalized.startsWith('dall-e')
  ))
}
