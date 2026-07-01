import assert from 'node:assert/strict'
import type { NextFunction, Request, Response } from 'express'

import { preResolveGatewayRuntime } from '../../modules/gateway/request/pre-auth.js'

async function assertModelsRequestPassesPreAuth(originalUrl: string): Promise<void> {
  let nextCalled = false
  const req = {
    method: 'GET',
    originalUrl,
    path: originalUrl.split('?')[0],
    header: () => undefined,
    headers: {}
  } as unknown as Request
  const res = {} as Response
  const next: NextFunction = () => {
    nextCalled = true
  }

  await preResolveGatewayRuntime(req, res, next)
  assert.equal(nextCalled, true, `${originalUrl} 模型列表请求不应在 pre-auth 层被强制认证拦截`)
}

await assertModelsRequestPassesPreAuth('/v1/models')
await assertModelsRequestPassesPreAuth('/models')
await assertModelsRequestPassesPreAuth('/v1beta/models')

console.log('gateway-models-pre-auth-regression passed')
