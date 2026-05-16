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
}): GatewayRequestBodyState {
  const contentType = String(input.contentType ?? '')
  return {
    rawBodyBytes: input.rawBody.length,
    contentType,
    isJson: isGatewayJsonContentType(contentType),
    jsonParseStatus: input.jsonParseStatus,
    jsonParseWarningBytes: gatewayJsonBodyLargeWarningBytes
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
      model: typeof req.body?.model === 'string' ? req.body.model : undefined,
      stream: typeof req.body?.stream === 'boolean' ? req.body.stream : undefined
    }
  }
}
