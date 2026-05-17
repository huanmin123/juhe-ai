import type { Request } from 'express'

export const gatewayJsonBodyLargeWarningBytes = 2 * 1024 * 1024

export type GatewayJsonBodyParseStatus =
  | 'empty'
  | 'not_json'
  | 'parsed'
  | 'invalid_json'

export interface GatewayRequestBodyState {
  rawBodyBytes: number
  contentType: string
  isJson: boolean
  jsonParseStatus: GatewayJsonBodyParseStatus
  jsonParseWarningBytes: number
  model?: string
  stream?: boolean
}

export type GatewayRawBodyRequest = Request & {
  rawBody?: Buffer
  gatewayRequestBody?: GatewayRequestBodyState
}

export function isGatewayJsonContentType(contentType: unknown): boolean {
  return String(contentType ?? '').toLowerCase().includes('json')
}

export function createGatewayRequestBodyState(input: {
  rawBody: Buffer
  contentType: unknown
  jsonParseStatus: GatewayJsonBodyParseStatus
  parsedBody?: unknown
}): GatewayRequestBodyState {
  const contentType = String(input.contentType ?? '')
  const parsedBody = typeof input.parsedBody === 'object' && input.parsedBody !== null
    ? input.parsedBody as Record<string, unknown>
    : undefined
  return {
    rawBodyBytes: input.rawBody.length,
    contentType,
    isJson: isGatewayJsonContentType(contentType),
    jsonParseStatus: input.jsonParseStatus,
    jsonParseWarningBytes: gatewayJsonBodyLargeWarningBytes,
    model: typeof parsedBody?.model === 'string' ? parsedBody.model : undefined,
    stream: typeof parsedBody?.stream === 'boolean' ? parsedBody.stream : undefined
  }
}

export function getGatewayRequestBodyState(req: Request): GatewayRequestBodyState | undefined {
  return (req as GatewayRawBodyRequest).gatewayRequestBody
}

export function buildGatewayRequestBodySummary(req: Request): Record<string, unknown> | undefined {
  const state = getGatewayRequestBodyState(req)
  if (!state || state.rawBodyBytes <= state.jsonParseWarningBytes) {
    return undefined
  }
  return {
    _gatewayBody: {
      rawBodyBytes: state.rawBodyBytes,
      contentType: state.contentType,
      jsonParseStatus: state.jsonParseStatus,
      jsonParseWarningBytes: state.jsonParseWarningBytes,
      model: state.model ?? (typeof req.body?.model === 'string' ? req.body.model : undefined),
      stream: state.stream ?? (typeof req.body?.stream === 'boolean' ? req.body.stream : undefined)
    }
  }
}
