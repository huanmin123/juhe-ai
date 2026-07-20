import type { RequestHandler } from 'express'

import { runtimeConfig } from '../../config/runtime.js'

export const systemApiReadOnlyMessage = '临时发布接管中，非读取接口暂不可用，请稍后重试'
export const systemApiReadOnlyRetryAfterSeconds = 60

const readMethods = new Set(['GET', 'HEAD', 'OPTIONS'])

// These endpoints start/read account probes or test sessions; keep the list
// aligned with account route additions so temporary takeover never blocks
// internal diagnostics while management mutations remain read-only.
const nonManagementPostPaths = [
  /^\/(?:my-)?accounts(?:\/[^/]+)?\/test\/?$/,
  /^\/(?:my-)?accounts\/test-draft\/?$/,
  /^\/(?:my-)?accounts\/test-sessions(?:\/[^/]+\/(?:heartbeat|complete|cancel))?\/?$/,
  /^\/(?:my-)?accounts\/test-tasks\/[^/]+\/cancel\/?$/,
  /^\/(?:my-)?accounts(?:\/[^/]+)?\/balance\/refresh\/?$/,
  /^\/(?:my-)?accounts\/balance\/test-draft\/?$/
]

export const systemApiReadOnlyMethodMiddleware: RequestHandler = (req, res, next) => {
  const method = req.method.toUpperCase()
  const path = req.path || '/'
  const isNonManagementPost = method === 'POST' && nonManagementPostPaths.some((pattern) => pattern.test(path))
  if (!runtimeConfig.systemApi.readOnly || readMethods.has(method) || isNonManagementPost) {
    next()
    return
  }

  res.setHeader('Cache-Control', 'no-store')
  res.setHeader('Retry-After', String(systemApiReadOnlyRetryAfterSeconds))
  res.status(503).json({ message: systemApiReadOnlyMessage, code: 'system_api_read_only' })
}
