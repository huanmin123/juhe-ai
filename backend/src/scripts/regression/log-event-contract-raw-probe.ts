import { EventEmitter } from 'node:events'
import type { Request, Response } from 'express'

import { closeLogger } from '../../shared/logger.js'
import { GATEWAY_SLOW_STAGE_THRESHOLD_MS } from '../../shared/logging/runtime-log-policy.js'
import {
  bindRequestContextFields,
  getRequestLogger,
  logRequestStage,
  requestContextMiddleware
} from '../../shared/request-context.js'

class ProbeResponse extends EventEmitter {
  statusCode = 200
  writableEnded = true

  setHeader(): this {
    return this
  }
}

const req = {
  method: 'POST',
  path: '/v1/chat/completions',
  originalUrl: '/v1/chat/completions',
  ip: '127.0.0.1',
  socket: { remoteAddress: '127.0.0.1' },
  header(name: string): string | undefined {
    if (name.toLowerCase() === 'user-agent') return 'raw-json-probe'
    return undefined
  }
} as unknown as Request
const res = new ProbeResponse() as unknown as Response

requestContextMiddleware(req, res, () => {
  bindRequestContextFields({
    systemAccountId: 'system-account-raw-probe',
    role: 'user',
    apiKeyId: 'api-key-raw-probe',
    groupId: 'group-raw-probe',
    trafficSource: 'openai_compatible'
  })
  for (let index = 0; index < 70; index += 1) {
    logRequestStage('route.group_access', {
      requestId: 'request-id-must-not-override-context',
      systemAccountId: 'system-account-raw-probe',
      apiKeyId: 'api-key-raw-probe',
      groupId: 'group-raw-probe',
      trafficSource: 'openai_compatible',
      probeIndex: index
    }, 'success', index === 69
      ? performance.now() - GATEWAY_SLOW_STAGE_THRESHOLD_MS - 1
      : performance.now())
  }
  getRequestLogger().info({ event: 'request_logger_context_probe' }, 'probe')
  res.emit('finish')
})

await new Promise((resolve) => setImmediate(resolve))
await closeLogger()
