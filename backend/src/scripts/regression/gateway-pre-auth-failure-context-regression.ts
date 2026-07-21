import assert from 'node:assert/strict'
import type { NextFunction, Request, Response } from 'express'

import { preResolveGatewayRuntime } from '../../modules/gateway/request/pre-auth.js'
import { logger } from '../../shared/logger.js'

const capturedErrorEvents: Array<Record<string, unknown>> = []
const originalInfo = logger.info
const originalError = logger.error
const thrownError = new Error('pre-auth-runtime-resolution-regression-marker')

try {
  logger.info = (() => undefined) as typeof logger.info
  logger.error = ((fields: Record<string, unknown>) => {
    capturedErrorEvents.push(fields)
  }) as typeof logger.error

  const request = {
    method: 'GET',
    get originalUrl(): string {
      throw thrownError
    }
  } as unknown as Request
  const response = {} as Response
  let nextError: unknown
  const next: NextFunction = (error?: unknown) => {
    nextError = error
  }

  await preResolveGatewayRuntime(request, response, next)

  assert.equal(nextError, thrownError, 'pre-auth 必须继续把原始异常交给 Express 错误处理中间件')
  const failureEvent = capturedErrorEvents.find((event) => event.event === 'gateway.request.failure')
  assert(failureEvent, 'runtime_resolution 的未预期异常必须进入 error 失败通道')
  assert.equal(failureEvent.stage, 'runtime_resolution')
  assert.equal(failureEvent.failureClass, 'unexpected')
  const capturedError = failureEvent.error as { message?: unknown; stack?: unknown } | undefined
  assert.equal(capturedError?.message, thrownError.message, '阶段日志必须保存真实异常消息，不能生成占位异常')
  assert.match(String(capturedError?.stack), /pre-auth-runtime-resolution-regression-marker/, '阶段日志必须保存真实异常堆栈')
} finally {
  logger.info = originalInfo
  logger.error = originalError
}

console.log('gateway-pre-auth-failure-context-regression passed')
