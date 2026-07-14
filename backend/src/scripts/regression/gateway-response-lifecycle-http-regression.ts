import assert from 'node:assert/strict'
import http from 'node:http'
import type { Response } from 'express'

process.env.JUHE_AI_PROCESS_ROLE = 'worker'
process.env.JUHE_AI_WORKER_ROLE = 'ingest-worker'
process.env.JUHE_AI_RUNTIME_MODE = 'standalone'
process.env.JUHE_AI_QUEUE_DRIVER = 'memory'

const { attachAccountSlotRelease } = await import('../../modules/gateway/routes.js')
const { observeGatewayHttpCompletion } = await import('../../modules/gateway/audit/capture.service.js')

let slotAvailable = true
let releaseCount = 0
let connectionCount = 0
let firstBackgroundFinished = false
let releaseFirstBackground: (() => void) | undefined
const firstBackgroundGate = new Promise<void>((resolvePromise) => {
  releaseFirstBackground = resolvePromise
})
let markSecondEntered: (() => void) | undefined
const secondEntered = new Promise<void>((resolvePromise) => {
  markSecondEntered = resolvePromise
})

const server = http.createServer(async (req, res) => {
  await acquireSlot()
  const releaseSlot = attachAccountSlotRelease(res as unknown as Response, () => {
    slotAvailable = true
    releaseCount += 1
  })
  const completion = observeGatewayHttpCompletion(res as unknown as Response)
  const requestNumber = req.url === '/second' ? 2 : 1
  if (requestNumber === 2) markSecondEntered?.()

  res.writeHead(200, { 'content-type': 'application/json' })
  res.end(JSON.stringify({ requestNumber }))

  await completion.wait()
  if (requestNumber === 1) {
    await firstBackgroundGate
    firstBackgroundFinished = true
  }
  releaseSlot()
})
server.on('connection', () => {
  connectionCount += 1
})

const agent = new http.Agent({ keepAlive: true, maxSockets: 1 })

try {
  await new Promise<void>((resolvePromise) => server.listen(0, '127.0.0.1', resolvePromise))
  const address = server.address()
  assert(address && typeof address !== 'string', '测试 HTTP 服务启动失败')
  const origin = `http://127.0.0.1:${address.port}`

  const first = await requestJson(`${origin}/first`)
  assert.deepEqual(first, { requestNumber: 1 })
  assert.equal(firstBackgroundFinished, false, '首请求响应完成后后台收尾应仍可继续运行')

  const secondResponse = requestJson(`${origin}/second`)
  await Promise.race([
    secondEntered,
    new Promise<never>((_resolve, reject) => setTimeout(() => reject(new Error('第二请求在首请求后台收尾期间未取得并发槽')), 1_000))
  ])
  assert.equal(firstBackgroundFinished, false, '第二请求取得并发槽时首请求后台收尾必须仍被阻塞')
  assert.deepEqual(await secondResponse, { requestNumber: 2 })
  assert.equal(connectionCount, 1, '两个请求必须复用同一 keep-alive 客户端连接')
  assert.equal(releaseCount, 2, '每个 HTTP finish 必须各释放一次账户并发槽')
} finally {
  releaseFirstBackground?.()
  agent.destroy()
  await new Promise<void>((resolvePromise) => server.close(() => resolvePromise()))
}

console.log('网关真实 HTTP 生命周期回归通过：keep-alive finish 立即释放并发槽，后台收尾不阻塞下一请求')

async function acquireSlot(): Promise<void> {
  const deadline = Date.now() + 1_000
  while (!slotAvailable) {
    assert(Date.now() < deadline, '等待账户并发槽超时')
    await new Promise<void>((resolvePromise) => setTimeout(resolvePromise, 2))
  }
  slotAvailable = false
}

async function requestJson(url: string): Promise<Record<string, unknown>> {
  return new Promise<Record<string, unknown>>((resolvePromise, rejectPromise) => {
    const request = http.get(url, { agent }, (response) => {
      const chunks: Buffer[] = []
      response.on('data', (chunk: Buffer) => chunks.push(chunk))
      response.on('end', () => {
        try {
          resolvePromise(JSON.parse(Buffer.concat(chunks).toString('utf8')) as Record<string, unknown>)
        } catch (error) {
          rejectPromise(error)
        }
      })
    })
    request.on('error', rejectPromise)
  })
}

