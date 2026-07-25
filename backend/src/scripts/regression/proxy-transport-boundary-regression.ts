import { strict as assert } from 'node:assert'
import { createServer, request as httpRequest, type IncomingMessage, type Server, type ServerResponse } from 'node:http'
import { connect } from 'node:net'
import type { Duplex } from 'node:stream'

import { proxyTestServiceTestHooks } from '../../modules/proxies/proxy-test.service.js'

const responseBodyLimit = 512 * 1024
const completeStatuses = [200, 302, 400, 401, 403, 404, 405, 407, 429, 500, 502, 503]
const completeResponseBodies = [
  {
    id: 'empty',
    contentType: 'text/plain',
    body: ''
  },
  {
    id: 'provider-json-error',
    contentType: 'application/json',
    body: JSON.stringify({
      error: {
        type: 'proxy_error',
        code: 'account_banned',
        message: '401 unauthorized; 429 rate limited; 500 proxy unavailable'
      }
    })
  },
  {
    id: 'provider-text-error',
    contentType: 'text/plain',
    body: 'UPSTREAM ACCOUNT DEAD / PROXY BROKEN / PLEASE RETRY'
  },
  {
    id: 'provider-success-shaped',
    contentType: 'application/json',
    body: JSON.stringify({ ok: true, status: 'healthy' })
  }
] as const

let targetHitCount = 0
let cappedResponseEnded = false

const targetServer = createServer((request, response) => {
  targetHitCount += 1
  const pathname = new URL(request.url ?? '/', 'http://127.0.0.1').pathname

  if (pathname.startsWith('/status/')) {
    const statusCode = Number(pathname.slice('/status/'.length))
    const bodyId = new URL(request.url ?? '/', 'http://127.0.0.1').searchParams.get('body') ?? 'provider-json-error'
    const responseBody = completeResponseBodies.find((candidate) => candidate.id === bodyId) ?? completeResponseBodies[1]
    response.writeHead(statusCode, {
      'content-length': String(Buffer.byteLength(responseBody.body)),
      'content-type': responseBody.contentType
    })
    response.end(responseBody.body)
    return
  }

  if (pathname === '/truncated') {
    response.writeHead(200, {
      'content-length': '128',
      'content-type': 'text/plain'
    })
    response.write('partial')
    setTimeout(() => response.socket?.destroy(), 15)
    return
  }

  if (pathname === '/slow-drip') {
    response.writeHead(200, { 'content-type': 'text/plain' })
    const interval = setInterval(() => response.write('x'), 15)
    response.once('close', () => clearInterval(interval))
    return
  }

  if (pathname === '/body-cap') {
    const prefix = Buffer.alloc(responseBodyLimit + 1, 97)
    const suffix = Buffer.alloc(16, 98)
    response.writeHead(200, {
      'content-length': String(prefix.byteLength + suffix.byteLength),
      'content-type': 'text/plain'
    })
    response.write(prefix)
    const endTimer = setTimeout(() => {
      cappedResponseEnded = true
      response.end(suffix)
    }, 60)
    response.once('close', () => {
      if (!response.writableEnded) clearTimeout(endTimer)
    })
    return
  }

  response.writeHead(404)
  response.end()
})

const forwardedRequestUrls: string[] = []
const proxyServer = createServer(forwardProxyRequestHandler({
  onRequest: (request) => forwardedRequestUrls.push(request.url ?? '')
}))
proxyServer.on('connect', (request, clientSocket, head) => {
  const authority = request.url ?? ''
  const separatorIndex = authority.lastIndexOf(':')
  const host = authority.slice(0, separatorIndex)
  const port = Number(authority.slice(separatorIndex + 1))
  if (!host || !Number.isInteger(port) || port <= 0) {
    clientSocket.end('HTTP/1.1 400 Bad Request\r\nConnection: close\r\n\r\n')
    return
  }

  const upstreamSocket = connect({ host, port })
  upstreamSocket.once('connect', () => {
    clientSocket.write('HTTP/1.1 200 Connection Established\r\n\r\n')
    if (head.byteLength > 0) upstreamSocket.write(head)
    upstreamSocket.pipe(clientSocket)
    clientSocket.pipe(upstreamSocket)
  })
  upstreamSocket.once('error', () => {
    clientSocket.end('HTTP/1.1 502 Bad Gateway\r\nConnection: close\r\n\r\n')
  })
  clientSocket.once('error', () => upstreamSocket.destroy())
})

const stalledConnectSockets = new Set<Duplex>()
let stalledConnectAcceptedCount = 0
let stalledConnectEndedCount = 0
const stalledConnectProxyServer = createServer()
stalledConnectProxyServer.on('connect', (_request, clientSocket) => {
  stalledConnectAcceptedCount += 1
  stalledConnectSockets.add(clientSocket)
  clientSocket.once('close', () => stalledConnectSockets.delete(clientSocket))
  clientSocket.once('error', () => stalledConnectSockets.delete(clientSocket))
  clientSocket.once('end', () => {
    stalledConnectEndedCount += 1
    clientSocket.end()
  })
  clientSocket.resume()
})

let rejectedConnectStatusCode = 407
const rejectedConnectProxyServer = createServer()
rejectedConnectProxyServer.on('connect', (_request, clientSocket) => {
  clientSocket.end([
    `HTTP/1.1 ${rejectedConnectStatusCode} ${rejectedConnectStatusCode === 407 ? 'Proxy Authentication Required' : 'Bad Gateway'}`,
    'Proxy-Authenticate: Basic realm="test"',
    'Content-Length: 0',
    'Connection: close',
    '',
    ''
  ].join('\r\n'))
})

let forwardOnlyRequestCount = 0
let forwardOnlyConnectCount = 0
let forwardOnlyAbsoluteUrl = ''
let forwardOnlyProxyAuthorization = ''
const forwardOnlyProxyServer = createServer(forwardProxyRequestHandler({
  onRequest: (request) => {
    forwardOnlyRequestCount += 1
    forwardOnlyAbsoluteUrl = request.url ?? ''
    forwardOnlyProxyAuthorization = typeof request.headers['proxy-authorization'] === 'string'
      ? request.headers['proxy-authorization']
      : ''
  }
}))
forwardOnlyProxyServer.on('connect', (_request, clientSocket) => {
  forwardOnlyConnectCount += 1
  clientSocket.end('HTTP/1.1 405 Method Not Allowed\r\nContent-Length: 0\r\nConnection: close\r\n\r\n')
})

try {
  await listen(targetServer)
  await listen(proxyServer)
  await listen(stalledConnectProxyServer)
  await listen(rejectedConnectProxyServer)
  await listen(forwardOnlyProxyServer)
  const targetPort = serverPort(targetServer)
  const proxyPort = serverPort(proxyServer)
  const stalledConnectProxyPort = serverPort(stalledConnectProxyServer)
  const rejectedConnectProxyPort = serverPort(rejectedConnectProxyServer)
  const forwardOnlyProxyPort = serverPort(forwardOnlyProxyServer)
  const targetBaseUrl = `http://127.0.0.1:${targetPort}`
  const proxyUrl = `http://127.0.0.1:${proxyPort}`

  for (const statusCode of completeStatuses) {
    for (const responseBody of completeResponseBodies) {
      const item = await settleWithin(proxyTestServiceTestHooks.testTarget({
        name: `HTTP ${statusCode} / ${responseBody.id}`,
        targetUrl: `${targetBaseUrl}/status/${statusCode}?body=${responseBody.id}`,
        proxyUrl,
        deadlineAtMs: Date.now() + 2_000
      }), 3_000, `HTTP ${statusCode} / ${responseBody.id} 完整响应`)
      assert.equal(item.status, 'passed', `完整 HTTP ${statusCode} / ${responseBody.id} 只能证明代理传输 framing 完整`)
      assert.equal(item.httpStatus, statusCode, `HTTP ${statusCode} / ${responseBody.id} 应保留为诊断字段；代理收到 ${forwardedRequestUrls.at(-1)}`)
      assert.match(item.message, /状态码仅供诊断/, `HTTP ${statusCode} / ${responseBody.id} 消息必须声明状态码仅供诊断`)
    }
  }
  assert.equal(
    forwardedRequestUrls.length,
    completeStatuses.length * completeResponseBodies.length,
    '状态码/正文矩阵中的每个样本都必须真实经过代理'
  )

  const forwardOnlyItem = await settleWithin(proxyTestServiceTestHooks.testTarget({
    name: '仅正向 HTTP 代理',
    targetUrl: `${targetBaseUrl}/status/200`,
    proxyUrl: `http://proxy-user:proxy-pass@127.0.0.1:${forwardOnlyProxyPort}`,
    deadlineAtMs: Date.now() + 2_000
  }), 3_000, '仅正向 HTTP 代理')
  assert.equal(forwardOnlyItem.status, 'passed', '仅支持 absolute-form 的合法 HTTP 代理必须通过检测')
  assert.equal(forwardOnlyRequestCount, 1, 'HTTP 目标必须向正向代理发送一次普通请求')
  assert.equal(forwardOnlyConnectCount, 0, 'HTTP 目标不得错误使用 CONNECT')
  assert.equal(forwardOnlyAbsoluteUrl, `${targetBaseUrl}/status/200`, '正向代理请求目标必须使用 absolute-form URL')
  assert.equal(
    forwardOnlyProxyAuthorization,
    `Basic ${Buffer.from('proxy-user:proxy-pass').toString('base64')}`,
    '正向代理认证必须写入 Proxy-Authorization'
  )

  for (const statusCode of [407, 502]) {
    rejectedConnectStatusCode = statusCode
    const hitsBeforeRejectedConnect = targetHitCount
    const rejectedConnectItem = await settleWithin(proxyTestServiceTestHooks.testTarget({
      name: `CONNECT ${statusCode}`,
      targetUrl: `https://127.0.0.1:${targetPort}/status/200`,
      proxyUrl: `http://127.0.0.1:${rejectedConnectProxyPort}`,
      deadlineAtMs: Date.now() + 800
    }), 1_200, `代理 CONNECT ${statusCode}`)
    assert.equal(rejectedConnectItem.status, 'failed', `代理 CONNECT ${statusCode} 必须是 transport failure，不能冒充目标完整 HTTP 响应`)
    assert.equal(rejectedConnectItem.httpStatus, undefined, '代理 CONNECT 状态不得写入目标 HTTP 诊断字段')
    assert.match(rejectedConnectItem.message, new RegExp(`CONNECT 返回 HTTP ${statusCode}`), '代理 CONNECT 拒绝应保留诊断状态')
    assert.equal(targetHitCount, hitsBeforeRejectedConnect, '代理 CONNECT 拒绝后不得命中目标服务')
  }

  const hitsBeforeExpiredBudget = targetHitCount
  const expiredBudgetItem = await settleWithin(proxyTestServiceTestHooks.testTarget({
    name: '已耗尽预算',
    targetUrl: `${targetBaseUrl}/status/200`,
    proxyUrl,
    deadlineAtMs: Date.now() - 1
  }), 500, '未形成真实 attempt 的预算耗尽')
  assert.equal(expiredBudgetItem.status, 'unknown', '未形成真实 attempt 的预算耗尽必须是 unknown')
  assert.equal(targetHitCount, hitsBeforeExpiredBudget, '预算耗尽后不得再发起上游请求')

  const invalidConfigItem = await settleWithin(proxyTestServiceTestHooks.testTarget({
    name: '无效代理配置',
    targetUrl: `${targetBaseUrl}/status/200`,
    proxyUrl: 'ftp://127.0.0.1:21',
    deadlineAtMs: Date.now() + 500
  }), 500, '无效代理配置')
  assert.equal(invalidConfigItem.status, 'unknown', '代理配置在 attempt 前无效时必须是 unknown')
  assert.equal(targetHitCount, hitsBeforeExpiredBudget, '无效代理配置不得命中目标服务')

  const noProviderBase = proxyTestServiceTestHooks.baseConnectivityItem([], 0)
  assert.equal(noProviderBase.status, 'unknown', '没有启用供应商时必须是 unknown')
  assert.equal(proxyTestServiceTestHooks.summarizeItems([noProviderBase]).status, 'unknown', '无供应商报告不得持久化为 failed')
  assert.equal(proxyTestServiceTestHooks.refreshFailureState('后台 worker 不可用', {
    expectedConfigUpdatedAt: '2026-01-01T00:00:00.000Z',
    lastTestedAt: '2026-01-01T00:00:00.000Z'
  }).testStatus, 'unknown', 'worker/基础设施异常不得伪装成代理失败')

  const cappedStartedAt = Date.now()
  const cappedResponse = await settleWithin(proxyTestServiceTestHooks.requestTarget({
    targetUrl: `${targetBaseUrl}/body-cap`,
    proxyUrl,
    timeoutMs: 2_000
  }), 3_000, '512KiB body cap 完整响应')
  assert.equal(cappedResponse.statusCode, 200, '超过收集上限但完整 end 的响应应保留 HTTP 诊断状态')
  assert.equal(Buffer.byteLength(cappedResponse.bodyText), responseBodyLimit, '响应正文收集必须严格限制为 512KiB')
  assert.equal(cappedResponseEnded, true, '达到 512KiB 只能停止收集，必须继续 drain 到真实 end')
  assert.ok(Date.now() - cappedStartedAt >= 45, '代理检测不得在达到 body cap 时提前 resolve')

  const truncatedItem = await settleWithin(proxyTestServiceTestHooks.testTarget({
    name: '断尾响应',
    targetUrl: `${targetBaseUrl}/truncated`,
    proxyUrl,
    deadlineAtMs: Date.now() + 1_000
  }), 1_500, '断尾响应')
  assert.equal(truncatedItem.status, 'failed', 'response aborted/error/close-before-end 必须是 transport failure')

  const dripStartedAt = Date.now()
  const slowDripItem = await settleWithin(proxyTestServiceTestHooks.testTarget({
    name: '慢滴流响应',
    targetUrl: `${targetBaseUrl}/slow-drip`,
    proxyUrl,
    deadlineAtMs: Date.now() + 120
  }), 900, '慢滴流绝对总超时')
  const dripElapsedMs = Date.now() - dripStartedAt
  assert.equal(slowDripItem.status, 'failed', '持续有数据的慢滴流也必须受绝对总超时约束')
  assert.ok(dripElapsedMs >= 80 && dripElapsedMs < 600, `绝对总超时应有界生效，实际 ${dripElapsedMs}ms`)

  const stalledConnectItem = await settleWithin(proxyTestServiceTestHooks.testTarget({
    name: 'CONNECT 卡死',
    targetUrl: `https://127.0.0.1:${targetPort}/status/200`,
    proxyUrl: `http://127.0.0.1:${stalledConnectProxyPort}`,
    deadlineAtMs: Date.now() + 120
  }), 900, '代理 CONNECT 卡死绝对总超时')
  assert.equal(stalledConnectItem.status, 'failed', '已建立真实代理 CONNECT 尝试后的绝对总超时必须是 transport failure')
  await sleep(300)
  assert.equal(stalledConnectEndedCount, 1, '绝对总超时后测试代理必须收到客户端 end')
  assert.equal(stalledConnectSockets.size, 0, `绝对总超时 settle 后必须释放仍在等待 CONNECT 的底层 socket：${stalledConnectItem.message} / accepted=${stalledConnectAcceptedCount}`)

  const closedProxyPort = await reserveClosedPort()
  const connectionItem = await settleWithin(proxyTestServiceTestHooks.testTarget({
    name: '代理连接失败',
    targetUrl: `${targetBaseUrl}/status/200`,
    proxyUrl: `http://127.0.0.1:${closedProxyPort}`,
    deadlineAtMs: Date.now() + 500
  }), 1_000, '代理连接失败')
  assert.equal(connectionItem.status, 'failed', '代理连接失败必须是 transport failure')

  const tlsItem = await settleWithin(proxyTestServiceTestHooks.testTarget({
    name: 'TLS 失败',
    targetUrl: `https://127.0.0.1:${targetPort}/status/200`,
    proxyUrl,
    deadlineAtMs: Date.now() + 800
  }), 1_200, 'TLS 失败')
  assert.equal(tlsItem.status, 'failed', 'TLS 握手失败必须是 transport failure')

  console.log(`代理传输边界回归通过：${completeStatuses.length * completeResponseBodies.length} 个完整 HTTP 状态/正文样本仅作诊断，断尾/连接/TLS/绝对总超时失败，body cap 继续 drain，未知态不伪装失败`)
} finally {
  for (const socket of stalledConnectSockets) socket.destroy()
  await closeServer(forwardOnlyProxyServer)
  await closeServer(rejectedConnectProxyServer)
  await closeServer(stalledConnectProxyServer)
  await closeServer(proxyServer)
  await closeServer(targetServer)
}

function forwardProxyRequestHandler(options: { onRequest?: (request: IncomingMessage) => void } = {}) {
  return (request: IncomingMessage, response: ServerResponse): void => {
    options.onRequest?.(request)
    let targetUrl: URL
    try {
      targetUrl = new URL(request.url ?? '')
      if (targetUrl.protocol !== 'http:') throw new Error('只接受 HTTP 正向目标')
    } catch {
      response.writeHead(400, { connection: 'close' })
      response.end()
      return
    }
    const headers = { ...request.headers }
    delete headers['proxy-authorization']
    delete headers['proxy-connection']
    headers.host = targetUrl.host
    const upstream = httpRequest(targetUrl, {
      method: request.method ?? 'GET',
      headers
    }, (upstreamResponse) => {
      response.writeHead(upstreamResponse.statusCode ?? 502, upstreamResponse.headers)
      upstreamResponse.pipe(response)
    })
    upstream.once('error', () => {
      if (!response.headersSent) response.writeHead(502, { connection: 'close' })
      response.end()
    })
    request.pipe(upstream)
  }
}

async function settleWithin<T>(promise: Promise<T>, timeoutMs: number, label: string): Promise<T> {
  let timeout: NodeJS.Timeout | undefined
  try {
    return await Promise.race([
      promise,
      new Promise<never>((_resolve, reject) => {
        timeout = setTimeout(() => reject(new Error(`${label} Promise 未在 ${timeoutMs}ms 内 settle`)), timeoutMs)
      })
    ])
  } finally {
    if (timeout) clearTimeout(timeout)
  }
}

function listen(server: Server): Promise<void> {
  server.listen(0, '127.0.0.1')
  if (server.listening) return Promise.resolve()
  return new Promise((resolve, reject) => {
    server.once('listening', resolve)
    server.once('error', reject)
  })
}

function serverPort(server: Server): number {
  const address = server.address()
  if (!address || typeof address === 'string') throw new Error('测试服务地址不可用')
  return address.port
}

async function reserveClosedPort(): Promise<number> {
  const server = createServer()
  await listen(server)
  const port = serverPort(server)
  await closeServer(server)
  return port
}

function sleep(ms: number): Promise<void> {
  return new Promise((resolve) => setTimeout(resolve, ms))
}

async function closeServer(server: Server): Promise<void> {
  if (!server.listening) return
  server.closeAllConnections?.()
  await new Promise<void>((resolve, reject) => {
    server.close((error) => error ? reject(error) : resolve())
  })
}
