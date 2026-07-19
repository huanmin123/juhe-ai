import { readUpstreamBodyLimited } from '../../../gateway/upstream/body.js'
import { requestUpstream } from '../../../gateway/upstream/request.js'

export interface TokenExchangeTransportInput {
  url: string
  headers: Headers
  body: string
  proxyUrl?: string
  timeoutMs: number
  maxResponseBytes: number
}

export interface TokenExchangeTransportResponse {
  statusCode: number
  bodyText: string
  truncated: boolean
}

export type TokenExchangeTransport = (
  input: TokenExchangeTransportInput
) => Promise<TokenExchangeTransportResponse>

export const requestTokenExchange: TokenExchangeTransport = async (input) => {
  const response = await requestUpstream(input.url, {
    method: 'POST',
    headers: input.headers,
    body: input.body,
    proxyUrl: input.proxyUrl,
    timeoutMs: input.timeoutMs,
    requestTimeoutMs: input.timeoutMs
  })
  const body = await readUpstreamBodyLimited(response.body, {
    maxBytes: input.maxResponseBytes
  })
  return {
    statusCode: response.status,
    bodyText: body.bodyText,
    truncated: body.truncated
  }
}
