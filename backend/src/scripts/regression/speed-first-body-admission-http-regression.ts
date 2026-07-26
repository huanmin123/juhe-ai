import assert from 'node:assert/strict'
import { createServer, type Server } from 'node:http'

import express, { type NextFunction, type Request, type Response } from 'express'

import type { GatewayRuntimeRequest } from '../../modules/gateway/request/pre-auth.js'
import { admitSpeedFirstRequestBody } from '../../modules/gateway/request/speed-first-body-admission.middleware.js'
import {
  clearSpeedFirstBodyAdmissionsForTest,
  speedFirstBodyAdmissionSnapshot
} from '../../modules/gateway/runtime/speed-first-body-admission.service.js'

let handlerHitCount = 0
let releaseFirstHandler: (() => void) | undefined

const app = express()
app.use((req: GatewayRuntimeRequest, _res: Response, next: NextFunction) => {
  req.gatewayRuntime = {
    apiKey: {
      id: 'key_body_http',
      system_account_id: 'sys_body_http',
      route_strategy_id: 'route_body_http',
      route_strategy_mode: 'normal',
      route_strategy_config_json: null,
      selected_group_id: 'group_body_http',
      status: 'active',
      expires_at: null,
      quota_limits_json: null,
      system_account_image_generation_enabled: 1,
      normal_routing_config: { schedulingPreference: 'speed_first', firstByteDeadlineMs: 10_000 }
    },
    settings: {} as never,
    groupAccess: {
      groupOwnerSystemAccountId: 'sys_body_http',
      providerCode: 'gpt',
      groupAccessType: 'owner',
      groupType: 'high_concurrency',
      schedulingPolicy: {
        maxQueueWaitMs: 1000,
        maxQueueSize: 10,
        perApiKeyQueueLimit: 10
      }
    },
    accounts: [{ id: 'acct_body_http', credentialSourceAccountId: undefined, concurrencyLimit: 1 }] as never
  }
  next()
})
app.use(admitSpeedFirstRequestBody)
app.use(express.raw({ type: () => true, limit: '8mb' }))
app.use(async (req: Request, res: Response) => {
  handlerHitCount += 1
  const currentHit = handlerHitCount
  if (currentHit === 1) {
    await new Promise<void>((resolve) => {
      releaseFirstHandler = resolve
    })
  }
  res.json({ hit: currentHit, bytes: Buffer.isBuffer(req.body) ? req.body.length : 0 })
})

const server = createServer(app)
try {
  await listen(server)
  const url = `http://127.0.0.1:${serverPort(server)}/v1/responses`
  const body = JSON.stringify({ model: 'gpt-5.4', input: 'x'.repeat(4 * 1024 * 1024) })
  const firstPromise = postJson(url, body)
  await waitUntil(() => handlerHitCount === 1)

  const imageResponse = await postJson(
    `http://127.0.0.1:${serverPort(server)}/v1/images/generations`,
    JSON.stringify({ model: 'gpt-image-1', prompt: 'speed first must not gate images' })
  )
  assert.equal(imageResponse.status, 200)
  assert.equal(imageResponse.payload.hit, 2, '直接图片请求不得等待 speed-first 正文 admission lease')

  const secondPromise = postJson(url, body)
  await waitUntil(() => speedFirstBodyAdmissionSnapshot().some((state) => (
    state.active === 1 && state.queued === 1
  )))
  assert.equal(handlerHitCount, 2, '第一个文本响应结束前，第二个大文本 Body 不得通过 express.raw 进入业务处理')
  releaseFirstHandler?.()
  const [first, second] = await Promise.all([firstPromise, secondPromise])
  assert.equal(first.status, 200)
  assert.equal(second.status, 200)
  assert.equal(first.payload.bytes, Buffer.byteLength(body))
  assert.equal(second.payload.bytes, Buffer.byteLength(body))
  assert.equal(second.payload.hit, 3, 'lease 释放后第二个大文本 Body 应继续处理')
  assert.equal(handlerHitCount, 3, '图片绕过 admission 不得破坏文本 admission 的按序释放')
  console.log('速度优先正文 admission HTTP 回归通过：图片绕过；4MB 文本请求在 lease 前未进入 express.raw，释放后按序处理')
} finally {
  releaseFirstHandler?.()
  await close(server)
  clearSpeedFirstBodyAdmissionsForTest()
}

async function postJson(url: string, body: string): Promise<{ status: number; payload: { hit: number; bytes: number } }> {
  const response = await fetch(url, {
    method: 'POST',
    headers: { 'content-type': 'application/json', authorization: 'Bearer sk-body-http', connection: 'close' },
    body
  })
  return { status: response.status, payload: await response.json() as { hit: number; bytes: number } }
}

async function listen(server: Server): Promise<void> {
  await new Promise<void>((resolve) => server.listen(0, '127.0.0.1', resolve))
}

function serverPort(server: Server): number {
  const address = server.address()
  assert(address && typeof address === 'object')
  return address.port
}

async function close(server: Server): Promise<void> {
  if (!server.listening) return
  await new Promise<void>((resolve, reject) => server.close((error) => error ? reject(error) : resolve()))
}

async function waitUntil(predicate: () => boolean): Promise<void> {
  const startedAt = Date.now()
  while (!predicate()) {
    if (Date.now() - startedAt > 5000) throw new Error('等待首个大 Body 请求进入处理超时')
    await waitMs(10)
  }
}

async function waitMs(ms: number): Promise<void> {
  await new Promise<void>((resolve) => setTimeout(resolve, ms))
}
