import assert from 'node:assert/strict'
import { EventEmitter } from 'node:events'
import { setTimeout as sleep } from 'node:timers/promises'

import type { Response } from 'express'
import type { Logger } from 'pino'

import { writeResponseChunk } from '../../modules/gateway/upstream/body.js'
import { createTraceId, withRequestContext, type RequestContext } from '../../shared/request-context.js'

interface LogEntry {
  level: 'debug' | 'warn'
  fields: Record<string, unknown>
  message: string
}

class BackpressureResponse extends EventEmitter {
  writableEnded = false
  destroyed = false
  headersSent = true
  writableLength = 128
  writableHighWaterMark = 64

  constructor(private readonly drainDelayMs: number) {
    super()
  }

  write(): boolean {
    setTimeout(() => {
      this.writableLength = 0
      this.emit('drain')
    }, this.drainDelayMs)
    return false
  }
}

const logs: LogEntry[] = []
const testLogger = {
  debug(fields: Record<string, unknown>, message: string) {
    logs.push({ level: 'debug', fields, message })
  },
  warn(fields: Record<string, unknown>, message: string) {
    logs.push({ level: 'warn', fields, message })
  }
} as unknown as Logger

function context(): RequestContext {
  return {
    traceId: createTraceId(),
    startedAt: Date.now(),
    method: 'POST',
    path: '/v1/responses',
    originalUrl: '/v1/responses',
    logger: testLogger
  }
}

await withRequestContext(context(), async () => {
  const result = await writeResponseChunk(new BackpressureResponse(1) as unknown as Response, Buffer.from('short-drain'))
  assert.equal(result.backpressure, true)
  assert.equal(result.logLevel, 'debug')
})

await withRequestContext(context(), async () => {
  const result = await writeResponseChunk(new BackpressureResponse(60) as unknown as Response, Buffer.from('slow-drain'))
  assert.equal(result.backpressure, true)
  assert.equal(result.logLevel, 'warn')
})

await sleep(5)

assert.equal(logs.length, 2)
assert.equal(logs[0]?.level, 'debug')
assert.equal(logs[0]?.fields.event, 'gateway_response_backpressure_drained')
assert.equal(logs[1]?.level, 'warn')
assert.equal(logs[1]?.fields.event, 'gateway_response_backpressure_slow')
assert(logs.every((entry) => entry.fields.event !== 'gateway_response_backpressure_started'))

console.log('网关 backpressure 日志降噪回归通过：短 drain 只记 debug，慢 drain 才写 warn')
