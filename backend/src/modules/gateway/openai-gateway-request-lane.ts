import type { Request } from 'express'

import { getGatewayRequestBodyState } from './openai-gateway-request-body.js'

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
  if (requestBodyHasImageGenerationHint(req.body)) {
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

function requestBodyHasImageGenerationHint(value: unknown): boolean {
  if (typeof value !== 'object' || value === null) {
    return false
  }
  const body = value as Record<string, unknown>
  if (body.type === 'image_generation' || valueContainsImageGenerationType(body.tool_choice)) {
    return true
  }
  return valueContainsImageGenerationType(body.tools)
    || valueContainsImageGenerationType(body.input)
    || valueContainsImageGenerationType(body.output)
}

function valueContainsImageGenerationType(value: unknown, depth = 0): boolean {
  if (depth > 4 || value === null || value === undefined) {
    return false
  }
  if (typeof value === 'string') {
    return value === 'image_generation' || value === 'image_generation_call'
  }
  if (Array.isArray(value)) {
    return value.some((item) => valueContainsImageGenerationType(item, depth + 1))
  }
  if (typeof value !== 'object') {
    return false
  }
  const object = value as Record<string, unknown>
  const type = object.type
  if (type === 'image_generation' || type === 'image_generation_call') {
    return true
  }
  return valueContainsImageGenerationType(object.tool, depth + 1)
    || valueContainsImageGenerationType(object.tools, depth + 1)
    || valueContainsImageGenerationType(object.content, depth + 1)
}
