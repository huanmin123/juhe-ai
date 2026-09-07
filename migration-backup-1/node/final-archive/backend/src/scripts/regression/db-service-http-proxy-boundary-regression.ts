import { strict as assert } from 'node:assert'
import { EventEmitter } from 'node:events'
import http from 'node:http'

import express from 'express'

import { runtimeConfig } from '../../config/runtime.js'
import { createDbServiceHttpProxy } from '../../modules/db-service/db-service-http-proxy.js'
import { logger } from '../../shared/logger.js'

runtimeConfig.log.consoleEnabled = false
runtimeConfig.log.fileEnabled = false
runtimeConfig.processRole = 'server'
logger.level = 'silent'

const dbServiceIpc = await import('../../modules/db-service/db-service-ipc.js')

class FakeDbServiceChild extends EventEmitter {
  pid = 616161
  connected = true

  send(_message: unknown, callback?: (error?: Error | null) => void): boolean {
    callback?.()
    return true
  }

  kill(): boolean {
    this.connected = false
    return true
  }
}

let heldResponse: http.ServerResponse | undefined
let resolveHeldRequest: (() => void) | undefined
let upstreamHitCount = 0

const upstream = http.createServer((req, res) => {
  upstreamHitCount += 1
  if (req.url?.includes('/hold')) {
    heldResponse = res
    resolveHeldRequest?.()
    return
  }
  if (req.url?.includes('/timeout')) {
    return
  }
  res.statusCode = 200
  res.setHeader('content-type', 'application/json')
  res.end(JSON.stringify({ ok: true }))
})

const proxyApp = express()
proxyApp.use('/__aisys__/api', createDbServiceHttpProxy({ maxInFlight: 1, timeoutMs: 50 }))
const proxy = http.createServer(proxyApp)

try {
  await listen(upstream)
  const fakeChild = new FakeDbServiceChild()
  dbServiceIpc.attachDbServiceProcess(fakeChild as never)
  fakeChild.emit('message', {
    type: 'db_service_ready',
    pid: fakeChild.pid,
    httpHost: '127.0.0.1',
    httpPort: serverAddress(upstream).port
  })
  assert.equal(dbServiceIpc.getDbServiceState().ready, true, '测试前 DB service fake child 应处于 ready')

  await listen(proxy)
  const baseUrl = `http://127.0.0.1:${serverAddress(proxy).port}`

  const heldArrived = new Promise<void>((resolve) => {
    resolveHeldRequest = resolve
  })
  const heldFetch = fetch(`${baseUrl}/__aisys__/api/hold`)
  await heldArrived
  assert.equal(upstreamHitCount, 1, '第一个代理请求应到达 DB service')

  const overloaded = await fetch(`${baseUrl}/__aisys__/api/hold`)
  assert.equal(overloaded.status, 503, 'DB service HTTP proxy 并发已满时应快速返回 503')
  assert.equal(overloaded.headers.get('retry-after'), '1', '代理并发已满应带短 Retry-After')
  assert.deepEqual(await overloaded.json(), { message: '本地数据库服务繁忙，请稍后重试' }, '代理并发已满应返回中文错误')
  assert.equal(upstreamHitCount, 1, '超过代理并发上限的请求不应继续进入 DB service')

  heldResponse?.setHeader('content-type', 'application/json')
  heldResponse?.end(JSON.stringify({ ok: true }))
  const heldResult = await heldFetch
  assert.equal(heldResult.status, 200, '释放后首个代理请求应正常返回')

  const beforeTimeoutHits = upstreamHitCount
  const timedOut = await fetch(`${baseUrl}/__aisys__/api/timeout`)
  assert.equal(timedOut.status, 504, 'DB service HTTP proxy 内部超时应返回 504')
  assert.deepEqual(await timedOut.json(), { message: '本地数据库服务响应超时，请稍后重试' }, '代理超时应返回中文错误')
  assert.equal(upstreamHitCount, beforeTimeoutHits + 1, '超时请求只应发送给 DB service 一次')

  console.log('DB service HTTP proxy 边界回归通过：代理并发上限快速 503，内部超时快速 504')
} finally {
  await closeServer(proxy)
  await closeServer(upstream)
}

async function listen(server: http.Server): Promise<void> {
  if (server.listening) return
  await new Promise<void>((resolve, reject) => {
    server.once('listening', resolve)
    server.once('error', reject)
    server.listen(0, '127.0.0.1')
  })
}

async function closeServer(server: http.Server): Promise<void> {
  if (!server.listening) return
  await new Promise<void>((resolve, reject) => {
    server.close((error) => {
      if (error) {
        reject(error)
        return
      }
      resolve()
    })
  })
}

function serverAddress(server: http.Server): { port: number } {
  const address = server.address()
  assert(address && typeof address !== 'string', '测试服务器应监听 TCP 地址')
  return { port: address.port }
}
