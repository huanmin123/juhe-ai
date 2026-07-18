import type { Response } from 'express'

import {
  copyResponseHeaders,
  type GatewayUpstreamResponse
} from '../upstream/request.js'

export function prepareUpstreamResponseForDownstream(
  res: Response,
  upstreamResponse: GatewayUpstreamResponse,
  shouldHandleAsStream: boolean
): void {
  if (res.headersSent) {
    return
  }
  res.status(upstreamResponse.status)
  copyResponseHeaders(upstreamResponse, res)
  if (shouldHandleAsStream && !res.hasHeader('content-type')) {
    res.setHeader('content-type', 'text/event-stream; charset=utf-8')
  }
  if (shouldHandleAsStream) {
    setGatewayStreamResponseHeaders(res)
    flushResponseHeadersIfSupported(res)
  }
}

export function flushResponseHeadersIfSupported(res: Response): void {
  const flushHeaders = (res as { flushHeaders?: unknown }).flushHeaders
  if (typeof flushHeaders === 'function') {
    flushHeaders.call(res)
  }
}

export function setGatewayStreamResponseHeaders(res: Response): void {
  if (!res.hasHeader('cache-control')) {
    res.setHeader('cache-control', 'no-cache, no-transform')
  }
  res.setHeader('x-accel-buffering', 'no')
}
