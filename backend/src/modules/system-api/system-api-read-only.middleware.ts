import type { RequestHandler } from 'express'

import { runtimeConfig } from '../../config/runtime.js'

export const systemApiReadOnlyMessage = '临时发布接管中，非读取接口暂不可用，请稍后重试'
export const systemApiReadOnlyRetryAfterSeconds = 60

const readMethods = new Set(['GET', 'HEAD', 'OPTIONS'])

export const systemApiReadOnlyMethodMiddleware: RequestHandler = (req, res, next) => {
  if (!runtimeConfig.systemApi.readOnly || readMethods.has(req.method.toUpperCase())) {
    next()
    return
  }

  res.setHeader('Cache-Control', 'no-store')
  res.setHeader('Retry-After', String(systemApiReadOnlyRetryAfterSeconds))
  res.status(503).json({ message: systemApiReadOnlyMessage, code: 'system_api_read_only' })
}
