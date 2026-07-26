import assert from 'node:assert/strict'
import http, { type IncomingMessage, type ServerResponse } from 'node:http'
import type { Response } from 'express'

process.env.JUHE_AI_PROCESS_ROLE = 'worker'
process.env.JUHE_AI_WORKER_ROLE = 'ingest-worker'
process.env.JUHE_AI_RUNTIME_MODE = 'standalone'
process.env.JUHE_AI_QUEUE_DRIVER = 'memory'

const { attachAccountSlotRelease } = await import('../../modules/gateway/routes.js')
const {
  isGatewayForcedDownstreamClose,
  markGatewayForcedDownstreamClose
} = await import('../../modules/gateway/upstream/body.js')

async function testCriticalSettlementPrecedesSlotRelease(): Promise<void> {
  const slot = new DeterministicAccountSlot()
  const firstHandlerStarted = deferred<void>()
  const firstHandlerFinished = deferred<void>()
  const criticalGate = deferred<void>()
  const criticalSettled = deferred<void>()
  const accountingGate = deferred<void>()
  const accountingStarted = deferred<void>()
  const secondQueued = deferred<void>()
  const secondEntered = deferred<void>()
  let accountingFinished = false
  let secondHasEntered = false
  let firstReleaseCount = 0

  const server = http.createServer((req, res) => {
    void (async () => {
      if (req.url === '/first') {
        const releaseConcurrency = await slot.acquire()
        const clientAbortController = attachClientAbortDetection(req, res)
        const releaseAccountSlot = attachAccountSlotRelease(asGatewayResponse(res), () => {
          firstReleaseCount += 1
          releaseConcurrency()
        }, {
          deferUntilExplicitRelease: true,
          clientAbortSignal: clientAbortController.signal
        })
        firstHandlerStarted.resolve()
        res.writeHead(200, { 'content-type': 'application/json' })
        res.end(JSON.stringify({ requestNumber: 1 }))

        await criticalGate.promise
        criticalSettled.resolve()
        releaseAccountSlot()
        accountingStarted.resolve()
        await accountingGate.promise
        accountingFinished = true
        firstHandlerFinished.resolve()
        return
      }

      if (req.url === '/second') {
        const releaseConcurrency = await slot.acquire(() => secondQueued.resolve())
        secondHasEntered = true
        secondEntered.resolve()
        res.writeHead(200, { 'content-type': 'application/json' })
        res.end(JSON.stringify({ requestNumber: 2 }))
        releaseConcurrency()
        return
      }

      res.writeHead(404).end()
    })().catch((error: unknown) => destroyWithError(res, error))
  })

  try {
    const origin = await listen(server)
    const firstResponse = requestJson(`${origin}/first`)
    await waitFor(firstHandlerStarted.promise, '首请求进入处理器')
    assert.deepEqual(await waitFor(firstResponse, '首请求 HTTP finish'), { requestNumber: 1 })
    assert.equal(firstReleaseCount, 0, 'HTTP finish 不得早于关键路由状态结算释放账户槽')

    const secondResponse = requestJson(`${origin}/second`)
    await waitFor(secondQueued.promise, '第二请求进入账户槽等待队列')
    assert.equal(secondHasEntered, false, '关键路由状态尚未结算时，第二请求不得取得账户槽')
    assert.equal(firstReleaseCount, 0, '第二请求排队时首请求账户槽必须仍被持有')

    criticalGate.resolve()
    await Promise.all([
      waitFor(criticalSettled.promise, '首请求关键路由状态结算'),
      waitFor(accountingStarted.promise, '首请求进入 usage/audit 收尾'),
      waitFor(secondEntered.promise, '第二请求在关键结算后取得账户槽')
    ])
    assert.equal(accountingFinished, false, '第二请求取得账户槽时，首请求 usage/audit 收尾必须仍被阻塞')
    assert.equal(firstReleaseCount, 1, '关键结算后必须且只能释放一次首请求账户槽')
    assert.deepEqual(await waitFor(secondResponse, '第二请求响应'), { requestNumber: 2 })
  } finally {
    criticalGate.resolve()
    accountingGate.resolve()
    await Promise.allSettled([waitFor(firstHandlerFinished.promise, '首请求处理器清理')])
    await closeServer(server)
  }
}

async function testRealClientAbortReleasesSlotImmediately(): Promise<void> {
  const slot = new DeterministicAccountSlot()
  const holderReady = deferred<void>()
  const holderCriticalGate = deferred<void>()
  const holderFinished = deferred<void>()
  const clientAbortObserved = deferred<void>()
  const successorQueued = deferred<void>()
  const successorEntered = deferred<void>()
  let holderCriticalSettled = false
  let successorHasEntered = false
  let holderReleaseCount = 0

  const server = http.createServer((req, res) => {
    void (async () => {
      if (req.url === '/abort-holder') {
        const releaseConcurrency = await slot.acquire()
        const clientAbortController = attachClientAbortDetection(req, res)
        clientAbortController.signal.addEventListener('abort', () => clientAbortObserved.resolve(), { once: true })
        const releaseAccountSlot = attachAccountSlotRelease(asGatewayResponse(res), () => {
          holderReleaseCount += 1
          releaseConcurrency()
        }, {
          deferUntilExplicitRelease: true,
          clientAbortSignal: clientAbortController.signal
        })
        res.writeHead(200, { 'content-type': 'text/plain' })
        res.write('partial')
        holderReady.resolve()

        await holderCriticalGate.promise
        holderCriticalSettled = true
        releaseAccountSlot()
        holderFinished.resolve()
        return
      }

      if (req.url === '/abort-successor') {
        const releaseConcurrency = await slot.acquire(() => successorQueued.resolve())
        successorHasEntered = true
        successorEntered.resolve()
        res.writeHead(200, { 'content-type': 'application/json' })
        res.end(JSON.stringify({ enteredAfterClientAbort: true }))
        releaseConcurrency()
        return
      }

      res.writeHead(404).end()
    })().catch((error: unknown) => destroyWithError(res, error))
  })

  let abortableClient: ReturnType<typeof requestUntilClosed> | undefined
  try {
    const origin = await listen(server)
    abortableClient = requestUntilClosed(`${origin}/abort-holder`)
    await waitFor(holderReady.promise, '客户端中断场景首请求开始响应')

    const successorResponse = requestJson(`${origin}/abort-successor`)
    await waitFor(successorQueued.promise, '客户端中断场景后继请求排队')
    assert.equal(successorHasEntered, false, '客户端尚未中断时，后继请求不得取得账户槽')

    abortableClient.abort()
    await Promise.all([
      waitFor(abortableClient.closed, '真实客户端连接关闭'),
      waitFor(clientAbortObserved.promise, '服务端观察到真实客户端中断'),
      waitFor(successorEntered.promise, '客户端中断后后继请求取得账户槽')
    ])
    assert.equal(holderCriticalSettled, false, '真实客户端中断必须在关键结算尚未完成时立即释放账户槽')
    assert.equal(holderReleaseCount, 1, '客户端中断与后继请求进入之间必须只释放一次首请求账户槽')
    assert.deepEqual(await waitFor(successorResponse, '客户端中断场景后继响应'), { enteredAfterClientAbort: true })
  } finally {
    abortableClient?.abort()
    holderCriticalGate.resolve()
    await Promise.allSettled([
      waitFor(holderFinished.promise, '客户端中断场景首请求处理器清理'),
      waitFor(abortableClient?.closed ?? Promise.resolve(), '客户端中断场景客户端清理')
    ])
    await closeServer(server)
  }
}

async function testForcedGatewayCloseWaitsForCriticalSettlement(): Promise<void> {
  const slot = new DeterministicAccountSlot()
  const holderReady = deferred<void>()
  const forceCloseGate = deferred<void>()
  const forcedCloseObserved = deferred<void>()
  const criticalGate = deferred<void>()
  const criticalSettled = deferred<void>()
  const accountingGate = deferred<void>()
  const accountingStarted = deferred<void>()
  const holderFinished = deferred<void>()
  const successorQueued = deferred<void>()
  const successorEntered = deferred<void>()
  let forcedCloseAbortSignal: AbortSignal | undefined
  let successorHasEntered = false
  let accountingFinished = false
  let holderReleaseCount = 0

  const server = http.createServer((req, res) => {
    void (async () => {
      if (req.url === '/forced-holder') {
        const releaseConcurrency = await slot.acquire()
        const clientAbortController = attachClientAbortDetection(req, res)
        forcedCloseAbortSignal = clientAbortController.signal
        const releaseAccountSlot = attachAccountSlotRelease(asGatewayResponse(res), () => {
          holderReleaseCount += 1
          releaseConcurrency()
        }, {
          deferUntilExplicitRelease: true,
          clientAbortSignal: clientAbortController.signal
        })
        res.writeHead(200, { 'content-type': 'text/plain' })
        res.write('partial')
        holderReady.resolve()

        await forceCloseGate.promise
        const responseClosed = responseClose(res)
        markGatewayForcedDownstreamClose(asGatewayResponse(res), 'regression_forced_close')
        res.destroy()
        await responseClosed
        forcedCloseObserved.resolve()

        await criticalGate.promise
        criticalSettled.resolve()
        releaseAccountSlot()
        accountingStarted.resolve()
        await accountingGate.promise
        accountingFinished = true
        holderFinished.resolve()
        return
      }

      if (req.url === '/forced-successor') {
        const releaseConcurrency = await slot.acquire(() => successorQueued.resolve())
        successorHasEntered = true
        successorEntered.resolve()
        res.writeHead(200, { 'content-type': 'application/json' })
        res.end(JSON.stringify({ enteredAfterCriticalSettlement: true }))
        releaseConcurrency()
        return
      }

      res.writeHead(404).end()
    })().catch((error: unknown) => destroyWithError(res, error))
  })

  let forcedCloseClient: ReturnType<typeof requestUntilClosed> | undefined
  try {
    const origin = await listen(server)
    forcedCloseClient = requestUntilClosed(`${origin}/forced-holder`)
    await waitFor(holderReady.promise, '网关主动断连场景首请求开始响应')

    const successorResponse = requestJson(`${origin}/forced-successor`)
    await waitFor(successorQueued.promise, '网关主动断连场景后继请求排队')
    forceCloseGate.resolve()
    await Promise.all([
      waitFor(forcedCloseObserved.promise, '服务端网关主动断连'),
      waitFor(forcedCloseClient.closed, '客户端观察到网关主动断连')
    ])
    assert.equal(forcedCloseAbortSignal?.aborted, false, '网关主动断连不得伪装成客户端取消')
    assert.equal(holderReleaseCount, 0, '网关主动断连不得早于关键路由状态结算释放账户槽')
    assert.equal(successorHasEntered, false, '网关主动断连后、关键结算前，后继请求仍不得取得账户槽')

    criticalGate.resolve()
    await Promise.all([
      waitFor(criticalSettled.promise, '网关主动断连后的关键路由状态结算'),
      waitFor(accountingStarted.promise, '网关主动断连请求进入 usage/audit 收尾'),
      waitFor(successorEntered.promise, '关键结算后后继请求取得账户槽')
    ])
    assert.equal(accountingFinished, false, '网关主动断连请求的 usage/audit 收尾不得继续占用账户槽')
    assert.equal(holderReleaseCount, 1, '网关主动断连请求必须在关键结算后恰好释放一次账户槽')
    assert.deepEqual(
      await waitFor(successorResponse, '网关主动断连场景后继响应'),
      { enteredAfterCriticalSettlement: true }
    )
  } finally {
    forceCloseGate.resolve()
    criticalGate.resolve()
    accountingGate.resolve()
    forcedCloseClient?.abort()
    await Promise.allSettled([
      waitFor(holderFinished.promise, '网关主动断连场景首请求处理器清理'),
      waitFor(forcedCloseClient?.closed ?? Promise.resolve(), '网关主动断连场景客户端清理')
    ])
    await closeServer(server)
  }
}

class DeterministicAccountSlot {
  private occupied = false
  private readonly waiters: Array<() => void> = []

  async acquire(onQueued?: () => void): Promise<() => void> {
    if (this.occupied) {
      const admitted = deferred<void>()
      this.waiters.push(() => admitted.resolve())
      onQueued?.()
      await admitted.promise
    } else {
      this.occupied = true
    }

    let released = false
    return () => {
      if (released) return
      released = true
      const next = this.waiters.shift()
      if (next) {
        next()
      } else {
        this.occupied = false
      }
    }
  }
}

function attachClientAbortDetection(req: IncomingMessage, res: ServerResponse): AbortController {
  const controller = new AbortController()
  const gatewayResponse = asGatewayResponse(res)
  req.once('aborted', () => {
    if (!isGatewayForcedDownstreamClose(gatewayResponse)) controller.abort()
  })
  res.once('close', () => {
    if (!isGatewayForcedDownstreamClose(gatewayResponse) && !res.writableFinished) {
      controller.abort()
    }
  })
  return controller
}

function asGatewayResponse(res: ServerResponse): Response {
  const gatewayResponse = res as unknown as Response
  gatewayResponse.locals ??= {}
  return gatewayResponse
}

function requestJson(url: string): Promise<Record<string, unknown>> {
  return new Promise<Record<string, unknown>>((resolvePromise, rejectPromise) => {
    const request = http.get(url, (response) => {
      const chunks: Buffer[] = []
      response.on('data', (chunk: Buffer) => chunks.push(chunk))
      response.on('end', () => {
        try {
          resolvePromise(JSON.parse(Buffer.concat(chunks).toString('utf8')) as Record<string, unknown>)
        } catch (error) {
          rejectPromise(error)
        }
      })
      response.on('error', rejectPromise)
    })
    request.on('error', rejectPromise)
  })
}

function requestUntilClosed(url: string): {
  abort: () => void
  closed: Promise<void>
} {
  const closed = deferred<void>()
  const request = http.get(url, (response) => {
    response.resume()
    response.once('aborted', () => closed.resolve())
    response.once('close', () => closed.resolve())
    response.once('end', () => closed.resolve())
    response.once('error', () => closed.resolve())
  })
  request.once('close', () => closed.resolve())
  request.once('error', () => closed.resolve())
  return {
    abort: () => request.destroy(),
    closed: closed.promise
  }
}

function responseClose(res: ServerResponse): Promise<void> {
  if (res.destroyed) return Promise.resolve()
  return new Promise<void>((resolvePromise) => res.once('close', resolvePromise))
}

async function listen(server: http.Server): Promise<string> {
  await new Promise<void>((resolvePromise, rejectPromise) => {
    server.once('error', rejectPromise)
    server.listen(0, '127.0.0.1', resolvePromise)
  })
  const address = server.address()
  assert(address && typeof address !== 'string', '测试 HTTP 服务启动失败')
  return `http://127.0.0.1:${address.port}`
}

async function closeServer(server: http.Server): Promise<void> {
  if (!server.listening) return
  server.closeAllConnections?.()
  await new Promise<void>((resolvePromise) => server.close(() => resolvePromise()))
}

function destroyWithError(res: ServerResponse, error: unknown): void {
  res.destroy(error instanceof Error ? error : new Error(String(error)))
}

function deferred<T>() {
  let resolvePromise!: (value: T | PromiseLike<T>) => void
  const promise = new Promise<T>((resolve) => {
    resolvePromise = resolve
  })
  return {
    promise,
    resolve: resolvePromise
  }
}

async function waitFor<T>(promise: Promise<T>, label: string): Promise<T> {
  let timer: NodeJS.Timeout | undefined
  try {
    return await Promise.race([
      promise,
      new Promise<never>((_resolve, reject) => {
        timer = setTimeout(() => reject(new Error(`等待超时：${label}`)), 2_000)
        timer.unref()
      })
    ])
  } finally {
    if (timer) clearTimeout(timer)
  }
}

await testCriticalSettlementPrecedesSlotRelease()
await testRealClientAbortReleasesSlotImmediately()
await testForcedGatewayCloseWaitsForCriticalSettlement()

console.log('网关真实 HTTP 生命周期回归通过：关键路由结算先于槽释放，记账不占槽，客户端中断立即释放，网关主动断连不提前释放')
