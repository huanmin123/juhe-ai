import { strict as assert } from 'node:assert'
import { EventEmitter } from 'node:events'
import type { Request, Response } from 'express'

import { runtimeConfig } from '../../config/runtime.js'
import {
  clearSystemApiDbAccessAdmissionStateForTest,
  resolveSystemApiDbAccessMode,
  setSystemApiDbAccessAdmissionStateForTest,
  setSystemApiDbAccessMode,
  shouldTouchSessionForSystemApiRequest,
  systemApiDbServiceAdmissionControl,
  systemApiDbServiceMaxInFlight,
  systemApiLongReadMaxInFlight
} from '../../modules/system-api/system-api-db-access.js'
import { logger } from '../../shared/logger.js'

logger.level = 'silent'

const originalDatabaseDriver = runtimeConfig.databaseDriver

function assertAccessModeMetadata(): void {
  assert.equal(
    resolveSystemApiDbAccessMode(requestFor('GET', '/__aisys__/api/auth/me'), '/__aisys__/api'),
    'read',
    'GET /auth/me 是当前用户资料读取，不能被 session touch 或写 admission 污染'
  )
  assert.equal(
    resolveSystemApiDbAccessMode(requestFor('HEAD', '/__aisys__/api/accounts'), '/__aisys__/api'),
    'read',
    'HEAD /accounts 应按 GET 规则识别为纯读'
  )
  assert.equal(
    resolveSystemApiDbAccessMode(requestFor('HEAD', '/__aisys__/api/auth/me'), '/__aisys__/api'),
    'read',
    'HEAD /auth/me 应按 GET 规则识别为纯读'
  )
  assert.equal(
    resolveSystemApiDbAccessMode(requestFor('POST', '/__aisys__/api/accounts/import/preview'), '/__aisys__/api'),
    'read',
    'POST /accounts/import/preview 是导入预览，必须允许显式标记为 read'
  )
  assert.equal(
    resolveSystemApiDbAccessMode(requestFor('POST', '/__aisys__/api/my-accounts/import/preview'), '/__aisys__/api'),
    'read',
    'POST /my-accounts/import/preview 是用户作用域导入预览，也必须允许显式标记为 read'
  )
  assert.equal(
    resolveSystemApiDbAccessMode(requestFor('POST', '/__aisys__/api/accounts/export'), '/__aisys__/api'),
    'write',
    'POST /accounts/export 会记录操作日志，不能标记为纯 read'
  )
  assert.equal(
    resolveSystemApiDbAccessMode(requestFor('GET', '/__aisys__/api/accounts'), '/__aisys__/api'),
    'read',
    'GET /accounts 是 AI 账户管理列表纯读，不能被 session touch 或写 admission 污染'
  )
  assert.equal(
    resolveSystemApiDbAccessMode(requestFor('GET', '/__aisys__/api/accounts/tags'), '/__aisys__/api'),
    'read',
    'GET /accounts/tags 是 AI 账户管理首屏纯读'
  )
  assert.equal(
    resolveSystemApiDbAccessMode(requestFor('GET', '/__aisys__/api/providers/options'), '/__aisys__/api'),
    'read',
    'GET /providers/options 是管理端选项纯读'
  )
  assert.equal(
    resolveSystemApiDbAccessMode(requestFor('GET', '/__aisys__/api/proxies/options'), '/__aisys__/api'),
    'read',
    'GET /proxies/options 是管理端选项纯读'
  )
  assert.equal(
    resolveSystemApiDbAccessMode(requestFor('GET', '/__aisys__/api/api-keys'), '/__aisys__/api'),
    'read',
    'GET /api-keys 是管理列表纯读'
  )
  assert.equal(
    resolveSystemApiDbAccessMode(requestFor('GET', '/__aisys__/api/api-keys/key_1/secret'), '/__aisys__/api'),
    'readWithSideEffect',
    'GET /api-keys/:id/secret 会记录查看密钥操作日志，不能标记为纯 read'
  )
  assert.equal(
    resolveSystemApiDbAccessMode(requestFor('GET', '/__aisys__/api/groups'), '/__aisys__/api'),
    'read',
    'GET /groups 是管理列表纯读'
  )
  assert.equal(
    resolveSystemApiDbAccessMode(requestFor('POST', '/__aisys__/api/proxies/proxy_1/test'), '/__aisys__/api'),
    'write',
    '未显式标注的非 GET 路由必须按 write 保守处理'
  )
  assert.equal(
    resolveSystemApiDbAccessMode(requestFor('GET', '/__aisys__/api/runtime-logs/grep'), '/__aisys__/api'),
    'longRead',
    'GET /runtime-logs/grep 必须走 longRead 并发边界'
  )
  assert.equal(
    resolveSystemApiDbAccessMode(requestFor('GET', '/__aisys__/api/audit-logs/search-hot'), '/__aisys__/api'),
    'longRead',
    'GET /audit-logs/search-hot 必须走 longRead 并发边界'
  )

  const readResponse = new FakeResponse()
  setSystemApiDbAccessMode(readResponse as unknown as Response, 'read')
  assert.equal(shouldTouchSessionForSystemApiRequest(readResponse as unknown as Response), false, '显式 read 请求不能被 requireAuth 重新 touch 成写请求')

  const authMeResponse = new FakeResponse()
  setSystemApiDbAccessMode(authMeResponse as unknown as Response, 'read')
  assert.equal(shouldTouchSessionForSystemApiRequest(authMeResponse as unknown as Response), false, 'auth/me 当前用户资料读取不能 touch session')

  const sideEffectResponse = new FakeResponse()
  setSystemApiDbAccessMode(sideEffectResponse as unknown as Response, 'readWithSideEffect')
  assert.equal(shouldTouchSessionForSystemApiRequest(sideEffectResponse as unknown as Response), true, '未拆副作用的 readWithSideEffect 请求必须保留 session touch')
}

function assertSqliteAdmissionByAccessMode(): void {
  runtimeConfig.databaseDriver = 'sqlite'

  clearSystemApiDbAccessAdmissionStateForTest()
  setSystemApiDbAccessAdmissionStateForTest({ writeInFlight: systemApiDbServiceMaxInFlight })
  const readResult = runAdmission('POST', '/__aisys__/api/accounts/import/preview')
  assert.equal(readResult.nextCalled, true, '显式 read 路由不应被 SQLite 写 admission 拦截')
  assert.equal(readResult.response.statusCode, undefined, '显式 read 路由不应返回 busy 响应')

  clearSystemApiDbAccessAdmissionStateForTest()
  setSystemApiDbAccessAdmissionStateForTest({ writeInFlight: systemApiDbServiceMaxInFlight })
  const accountsListResult = runAdmission('GET', '/__aisys__/api/accounts')
  assert.equal(accountsListResult.nextCalled, true, 'AI 账户列表纯读不应被 SQLite 写 admission 拦截')
  assert.equal(accountsListResult.response.statusCode, undefined, 'AI 账户列表纯读不应返回 busy 响应')

  clearSystemApiDbAccessAdmissionStateForTest()
  setSystemApiDbAccessAdmissionStateForTest({ writeInFlight: systemApiDbServiceMaxInFlight })
  const writeResult = runAdmission('POST', '/__aisys__/api/accounts/export')
  assert.equal(writeResult.nextCalled, false, 'write 路由应受 SQLite 写 admission 控制')
  assert.equal(writeResult.response.statusCode, 503, 'SQLite 写 admission 满载时 write 路由应返回 503')
  assert.equal(writeResult.response.body?.code, 'system_api_busy', 'SQLite 写 admission 满载应返回稳定错误码')

  clearSystemApiDbAccessAdmissionStateForTest()
  setSystemApiDbAccessAdmissionStateForTest({ writeInFlight: systemApiDbServiceMaxInFlight })
  const secretRevealResult = runAdmission('GET', '/__aisys__/api/api-keys/key_1/secret')
  assert.equal(secretRevealResult.nextCalled, false, 'GET /api-keys/:id/secret 带操作日志副作用，应受 SQLite 写 admission 控制')
  assert.equal(secretRevealResult.response.statusCode, 503, 'SQLite 写 admission 满载时 secret reveal 应返回 503')
  assert.equal(secretRevealResult.response.body?.code, 'system_api_busy', 'secret reveal 满载应返回稳定错误码')

  clearSystemApiDbAccessAdmissionStateForTest()
  setSystemApiDbAccessAdmissionStateForTest({ writeInFlight: systemApiDbServiceMaxInFlight })
  const authMeResult = runAdmission('GET', '/__aisys__/api/auth/me')
  assert.equal(authMeResult.nextCalled, true, 'GET /auth/me 纯读不应受 SQLite 写 admission 控制')
  assert.equal(authMeResult.response.statusCode, undefined, 'GET /auth/me 纯读不应返回 busy 响应')

  clearSystemApiDbAccessAdmissionStateForTest()
  setSystemApiDbAccessAdmissionStateForTest({ writeInFlight: systemApiDbServiceMaxInFlight })
  const longReadResult = runAdmission('GET', '/__aisys__/api/runtime-logs/grep')
  assert.equal(longReadResult.nextCalled, true, 'longRead 不应被 SQLite 写 admission 拦截')
  assert.equal(longReadResult.response.timeoutMs, 120_000, 'longRead 应设置独立 deadline')
  longReadResult.response.emit('finish')

  clearSystemApiDbAccessAdmissionStateForTest()
  setSystemApiDbAccessAdmissionStateForTest({ longReadInFlight: systemApiLongReadMaxInFlight })
  const blockedLongReadResult = runAdmission('GET', '/__aisys__/api/audit-logs/search-hot')
  assert.equal(blockedLongReadResult.nextCalled, false, 'longRead 应受独立并发边界控制')
  assert.equal(blockedLongReadResult.response.statusCode, 503, 'longRead 满载时应返回 503')
  assert.equal(blockedLongReadResult.response.body?.code, 'system_api_long_read_busy', 'longRead 满载应返回稳定错误码')
}

function assertPostgresSkipsSqliteWriteAdmission(): void {
  runtimeConfig.databaseDriver = 'postgres'
  clearSystemApiDbAccessAdmissionStateForTest()
  setSystemApiDbAccessAdmissionStateForTest({ writeInFlight: systemApiDbServiceMaxInFlight })
  const writeResult = runAdmission('POST', '/__aisys__/api/accounts/export')
  assert.equal(writeResult.nextCalled, true, 'PostgreSQL 模式不应套 SQLite 式全局写 admission')
  assert.equal(writeResult.response.statusCode, undefined, 'PostgreSQL 模式不应因为 SQLite 写 admission 返回 busy')
}

function runAdmission(method: string, path: string): { nextCalled: boolean; response: FakeResponse } {
  const req = requestFor(method, path)
  const response = new FakeResponse()
  setSystemApiDbAccessMode(response as unknown as Response, resolveSystemApiDbAccessMode(req, '/__aisys__/api'))
  let nextCalled = false
  systemApiDbServiceAdmissionControl(req as Request, response as unknown as Response, () => {
    nextCalled = true
  })
  return { nextCalled, response }
}

function requestFor(method: string, path: string): Pick<Request, 'method' | 'path' | 'originalUrl' | 'setTimeout'> {
  const req = {
    method,
    path,
    originalUrl: path,
    setTimeout: () => req
  }
  return req as unknown as Pick<Request, 'method' | 'path' | 'originalUrl' | 'setTimeout'>
}

class FakeResponse extends EventEmitter {
  locals: Record<string, unknown> = {}
  headers: Record<string, string> = {}
  statusCode: number | undefined
  timeoutMs: number | undefined
  body: { code?: string; message?: string } | undefined

  setHeader(name: string, value: string): this {
    this.headers[name.toLowerCase()] = value
    return this
  }

  status(statusCode: number): this {
    this.statusCode = statusCode
    return this
  }

  json(body: { code?: string; message?: string }): this {
    this.body = body
    this.emit('finish')
    return this
  }

  setTimeout(timeoutMs: number): this {
    this.timeoutMs = timeoutMs
    return this
  }
}

try {
  assertAccessModeMetadata()
  assertSqliteAdmissionByAccessMode()
  assertPostgresSkipsSqliteWriteAdmission()
  console.log('System API DB access 回归通过：读路由不受写 admission 拦截，readWithSideEffect/write/longRead 元数据可识别，导入预览为读，导出不是纯读，PG 不套 SQLite 写 admission')
} finally {
  runtimeConfig.databaseDriver = originalDatabaseDriver
  clearSystemApiDbAccessAdmissionStateForTest()
}
