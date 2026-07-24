import { EventEmitter } from 'node:events'
import type { Request, Response } from 'express'

import { closeLogger } from '../../shared/logger.js'
import {
  bindRequestContextFields,
  getRequestLogger,
  logRequestStage,
  markRequestProtocolTerminalOutcome,
  requestContextMiddleware
} from '../../shared/request-context.js'
import { GATEWAY_SLOW_STAGE_THRESHOLD_MS } from '../../shared/logging/runtime-log-policy.js'

class ProbeResponse extends EventEmitter {
  statusCode = 200

  constructor(public writableEnded: boolean) {
    super()
  }

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
const res = new ProbeResponse(true) as unknown as Response

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

const terminalCloseReq = {
  ...req,
  header(name: string): string | undefined {
    if (name.toLowerCase() === 'x-trace-id') return 'trace-terminal-close-probe'
    if (name.toLowerCase() === 'user-agent') return 'terminal-close-probe'
    return undefined
  }
} as unknown as Request
const terminalCloseRes = new ProbeResponse(false) as unknown as Response
requestContextMiddleware(terminalCloseReq, terminalCloseRes, () => {
  logRequestStage('downstream.finish')
  markRequestProtocolTerminalOutcome('success')
  terminalCloseRes.emit('close')
})

const abortedReq = {
  ...req,
  header(name: string): string | undefined {
    if (name.toLowerCase() === 'x-trace-id') return 'trace-aborted-close-probe'
    if (name.toLowerCase() === 'user-agent') return 'aborted-close-probe'
    return undefined
  }
} as unknown as Request
const abortedRes = new ProbeResponse(false) as unknown as Response
requestContextMiddleware(abortedReq, abortedRes, () => {
  logRequestStage('downstream.finish')
  abortedRes.emit('close')
})

const failedTerminalReq = {
  ...req,
  header(name: string): string | undefined {
    if (name.toLowerCase() === 'x-trace-id') return 'trace-failed-terminal-close-probe'
    if (name.toLowerCase() === 'user-agent') return 'failed-terminal-close-probe'
    return undefined
  }
} as unknown as Request
const failedTerminalRes = new ProbeResponse(false) as unknown as Response
requestContextMiddleware(failedTerminalReq, failedTerminalRes, () => {
  logRequestStage('downstream.finish', { failureReason: 'protocol_failed' }, 'expected_failure')
  markRequestProtocolTerminalOutcome('failure')
  failedTerminalRes.emit('close')
})

await new Promise((resolve) => setImmediate(resolve))
await closeLogger()
