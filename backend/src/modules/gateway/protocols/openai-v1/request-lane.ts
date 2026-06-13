import type { Request } from 'express'

import { getGatewayRequestBodyState, requestBodyHasImageGenerationHint } from '../../request/body.js'

export type OpenAIGatewayRequestLane = 'text' | 'image'

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
  return isImageGenerationModel(model)
}

function requestModelHint(req: Request): string | undefined {
  const bodyModel = typeof req.body?.model === 'string' ? req.body.model : undefined
  return bodyModel ?? getGatewayRequestBodyState(req)?.model
}

function isImageGenerationModel(model: string | undefined): boolean {
  const normalized = model?.trim().toLowerCase()
  return Boolean(normalized && (
    normalized.startsWith('gpt-image')
    || normalized.startsWith('dall-e')
  ))
}
