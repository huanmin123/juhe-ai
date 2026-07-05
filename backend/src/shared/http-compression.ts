import compression from 'compression'
import type { Request, Response } from 'express'

export const httpCompressionThresholdBytes = 1024

export function createHttpCompressionMiddleware(): ReturnType<typeof compression> {
  return compression({
    threshold: httpCompressionThresholdBytes,
    filter: shouldCompressHttpResponse
  })
}

function shouldCompressHttpResponse(req: Request, res: Response): boolean {
  if (res.getHeader('content-encoding')) {
    return false
  }
  if (responseHeaderIncludes(res, 'content-type', 'text/event-stream')) {
    return false
  }
  if (responseHeaderIncludes(res, 'content-disposition', 'attachment')) {
    return false
  }
  return compression.filter(req, res)
}

function responseHeaderIncludes(res: Response, name: string, expected: string): boolean {
  const value = res.getHeader(name)
  const values = Array.isArray(value) ? value : [value]
  return values.some((item) => String(item ?? '').toLowerCase().includes(expected))
}
