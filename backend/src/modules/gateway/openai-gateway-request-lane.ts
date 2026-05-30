import type { Request } from 'express'

import { getGatewayRequestBodyState, requestBodyHasImageGenerationHint } from './openai-gateway-request-body.js'

export type OpenAIGatewayRequestLane = 'text' | 'image'

export function resolveOpenAIGatewayRequestLane(req: Request): OpenAIGatewayRequestLane {
  const path = String(req.path || req.originalUrl || '').split('?')[0]?.toLowerCase() ?? ''
  if (path === '/images' || path.startsWith('/images/') || path === '/v1/images' || path.startsWith('/v1/images/')) {
    return 'image'
  }

  const model = requestModelHint(req)
  if (isImageGenerationModel(model)) {
    return 'image'
  }
  const bodyState = getGatewayRequestBodyState(req)
  if (bodyState?.imageGeneration || requestBodyHasImageGenerationHint(req.body)) {
    return 'image'
  }
  return 'text'
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
