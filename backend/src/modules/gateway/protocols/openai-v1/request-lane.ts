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
 * All gateway requests use the same availability-first failover rule. If the
 * current attempt does not produce a deliverable result, the dispatcher may
 * continue with another candidate regardless of endpoint or request lane.
 */
export function automaticUpstreamReplayAllowedAfterDispatch(
  _req: UpstreamReplayRequest,
  _lane: OpenAIGatewayRequestLane
): boolean {
  return true
}

function requestInspectionBody(req: UpstreamReplayRequest): Record<string, unknown> | undefined {
  if (isRecord(req.body)) return req.body
  if (req.gatewayParsedJsonBodyAvailable && isRecord(req.gatewayParsedJsonBody)) {
    return req.gatewayParsedJsonBody
  }
  return undefined
}

function isRecord(value: unknown): value is Record<string, unknown> {
  return typeof value === 'object' && value !== null && !Array.isArray(value)
}

export function resolveOpenAIGatewayRequestLane(req: Request): OpenAIGatewayRequestLane {
  if (isOpenAIGatewayImageEndpointOrModelRequest(req)) {
    return 'image'
  }

  const bodyState = getGatewayRequestBodyState(req)
  if (
    bodyState?.imageGeneration
    || requestBodyHasImageGenerationHint(req.body)
    || requestBodyRequestsImageOutput(req)
  ) {
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
    || normalized.startsWith('imagen-')
    || normalized.startsWith('nano-banana')
    || /(?:^|-)gemini(?:[^/]*-)?image(?:-|$)/.test(normalized)
  ))
}

function requestBodyRequestsImageOutput(req: UpstreamReplayRequest): boolean {
  const body = requestInspectionBody(req)
  if (!body) return false
  const generationConfig = isRecord(body.generationConfig)
    ? body.generationConfig
    : isRecord(body.generation_config)
      ? body.generation_config
      : undefined
  const modalities = generationConfig?.responseModalities ?? generationConfig?.response_modalities
  if (Array.isArray(modalities) && modalities.some((value) => (
    typeof value === 'string' && value.trim().toLowerCase() === 'image'
  ))) {
    return true
  }
  const mime = generationConfig?.responseMimeType ?? generationConfig?.response_mime_type
  return typeof mime === 'string' && /^image\//i.test(mime.trim())
}
