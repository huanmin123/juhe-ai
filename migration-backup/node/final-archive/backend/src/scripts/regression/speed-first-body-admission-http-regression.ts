import assert from 'node:assert/strict'
import { createServer, type Server } from 'node:http'

import express, { type NextFunction, type Request, type Response } from 'express'

import type { GatewayRuntimeRequest } from '../../modules/gateway/request/pre-auth.js'
import { captureGatewayRawBody } from '../../modules/gateway/request/body-middleware.js'
import { admitSpeedFirstRequestBody } from '../../modules/gateway/request/speed-first-body-admission.middleware.js'
import {
  clearSpeedFirstBodyAdmissionsForTest,
  speedFirstBodyAdmissionSnapshot
} from '../../modules/gateway/runtime/speed-first-body-admission.service.js'

let handlerHitCount = 0
let rawParserCompletedCount = 0
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
      normal_routing_config: {
        schedulingPreference: 'speed_first',
        firstByteDeadlineMs: 30_000,
        speedFirstConfig: {
          slowTriggerCount: 3,
          slowWindowSeconds: 120,
          recoverySuccessCount: 3,
          probeIntervalSeconds: 30,
          degradedTtlSeconds: 300,
          maxFirstByteRetriesPerRequest: 2
        }
      }
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
app.use(express.raw({
  type: () => true,
  limit: '8mb',
  verify: () => {
    rawParserCompletedCount += 1
  }
}))
app.use(captureGatewayRawBody)
app.use(async (req: Request, res: Response) => {
  handlerHitCount += 1
  const currentHit = handlerHitCount
  if (currentHit === 1) {
    await new Promise<void>((resolve) => {
      releaseFirstHandler = resolve
    })
  }
  const rawBody = (req as GatewayRuntimeRequest & { rawBody?: Buffer }).rawBody
  res.json({ hit: currentHit, bytes: Buffer.isBuffer(rawBody) ? rawBody.length : 0 })
})

const server = createServer(app)
try {
  await listen(server)
  const url = `http://127.0.0.1:${serverPort(server)}/v1/responses`
  const body = JSON.stringify({ model: 'gpt-5.4', input: 'x'.repeat(4 * 1024 * 1024) })
  const firstPromise = postJson(url, body)
  await waitUntil(() => handlerHitCount === 1)

  const imageResponsePromise = postJson(
    `http://127.0.0.1:${serverPort(server)}/v1/images/generations`,
    JSON.stringify({ model: 'gpt-image-1', prompt: 'a test image' })
  )
  await waitUntil(() => handlerHitCount === 2)
  const imageResponse = await imageResponsePromise
  assert.equal(imageResponse.status, 200)
  assert.equal(imageResponse.payload.hit, 2, 'image lane 必须保留正文 admission bypass')

  const compactionResponsePromise = postJson(
    url,
    JSON.stringify({
      model: 'gpt-5.4',
      input: [{ type: 'compaction_trigger' }]
    })
  )
  const secondPromise = postJson(url, body)
  await waitUntil(() => speedFirstBodyAdmissionSnapshot().some((state) => (
    state.active === 1 && state.queued === 2
  )))
  assert.equal(handlerHitCount, 2, '第一个文本响应结束前，等待中的文本 Body 不得进入业务处理')
  assert.equal(rawParserCompletedCount, 2, 'admission 等待期间普通文本和伪造 compaction_trigger 不得完成 express.raw 解析')
  releaseFirstHandler?.()
  const [first, second, compaction] = await Promise.all([firstPromise, secondPromise, compactionResponsePromise])
  assert.equal(first.status, 200)
  assert.equal(second.status, 200)
  assert.equal(compaction.status, 200)
  assert.equal(first.payload.bytes, Buffer.byteLength(body))
  assert.equal(second.payload.bytes, Buffer.byteLength(body))
  assert.equal(handlerHitCount, 4, 'lease 释放后排队文本请求应继续按序处理')
  assert.equal(rawParserCompletedCount, 4, '所有进入业务处理的请求都应先完成 express.raw')
  console.log('速度优先正文 admission HTTP 回归通过：image bypass 保留；普通文本与伪造 compaction_trigger 均受正文前 admission 保护')
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
