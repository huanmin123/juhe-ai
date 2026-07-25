import { strict as assert } from 'node:assert'
import { generateKeyPairSync, randomBytes, sign } from 'node:crypto'
import {
  createServer as createHttpServer,
  request as httpRequest,
  type IncomingMessage,
  type Server as HttpServer,
  type ServerResponse
} from 'node:http'
import { createServer as createHttpsServer } from 'node:https'
import { connect, createServer as createNetServer, type Server as NetServer, type Socket } from 'node:net'
import { Transform, type Duplex } from 'node:stream'

import { runtimeConfig } from '../../config/runtime.js'
import {
  closeGatewayUpstreamAgentsForTest,
  requestUpstream,
  type GatewayUpstreamResponse
} from '../../modules/gateway/upstream/request.js'

const proxyMarkerHeader = 'x-juhe-test-proxy'
const requestBody = JSON.stringify({ model: 'claude-test', messages: [{ role: 'user', content: 'proxy-boundary' }] })
const originalAllowPrivateBaseUrls = runtimeConfig.upstreamUrlSecurity.allowPrivateBaseUrls
const originalTlsRejectUnauthorized = process.env.NODE_TLS_REJECT_UNAUTHORIZED
runtimeConfig.upstreamUrlSecurity.allowPrivateBaseUrls = true
process.env.NODE_TLS_REJECT_UNAUTHORIZED = '0'

let directOriginHitCount = 0
const proxiedOriginMarkers: string[] = []
const originBodies: string[] = []
const proxyHitCounts = new Map<string, number>()

const originServer = createHttpServer(async (request, response) => {
  const marker = singleHeader(request.headers[proxyMarkerHeader])
  const body = await readRequestBody(request)
  if (!marker) {
    directOriginHitCount += 1
    response.writeHead(409, { 'content-type': 'application/json' })
    response.end(JSON.stringify({ error: 'direct_origin_access' }))
    return
  }
  proxiedOriginMarkers.push(marker)
  originBodies.push(body)
  const responseBody = JSON.stringify({ ok: true, marker, path: request.url })
  response.writeHead(200, {
    'content-length': String(Buffer.byteLength(responseBody)),
    'content-type': 'application/json'
  })
  response.end(responseBody)
})

try {
  await listen(originServer)
  const originPort = serverPort(originServer)
  const httpProxy = createConnectProxy('http', originPort)
  const certificate = createSelfSignedCertificate()
  const httpsProxy = createConnectProxy('https', originPort, certificate)
  const socksProxy = createSocks5Proxy('socks5h', originPort)
  try {
    await Promise.all([listen(httpProxy), listen(httpsProxy), listen(socksProxy)])
    const originBaseUrl = `http://127.0.0.1:${originPort}`
    await assertFetchRequestUsesProxy({
      marker: 'http',
      originUrl: `${originBaseUrl}/v1/messages?proxy=http`,
      proxyUrl: `http://127.0.0.1:${serverPort(httpProxy)}`
    })
    await assertFetchRequestUsesProxy({
      marker: 'https',
      originUrl: `${originBaseUrl}/v1/messages/count_tokens?proxy=https`,
      proxyUrl: `https://127.0.0.1:${serverPort(httpsProxy)}`
    })
    await assertFetchRequestUsesProxy({
      marker: 'socks5h',
      originUrl: `${originBaseUrl}/v1/messages?proxy=socks5h`,
      proxyUrl: `socks5h://127.0.0.1:${serverPort(socksProxy)}`
    })
    await assert.rejects(
      requestUpstream(`${originBaseUrl}/v1/messages?proxy=invalid`, {
        method: 'POST',
        headers: new Headers({
          'anthropic-version': '2023-06-01',
          'content-type': 'application/json'
        }),
        body: Buffer.from(requestBody),
        proxyUrl: 'ftp://127.0.0.1:21',
        requestTimeoutMs: 2_000,
        timeoutMs: 2_000,
        transport: 'fetch'
      }),
      /不支持的代理协议/,
      '无效代理配置必须 fail closed，不能绕过代理直连 origin'
    )

    assert.equal(directOriginHitCount, 0, 'fetch transport 配置代理后不得直连 origin')
    assert.deepEqual(proxiedOriginMarkers, ['http', 'https', 'socks5h'], '三类代理必须分别把流量送达 origin')
    assert.deepEqual(originBodies, [requestBody, requestBody, requestBody], '经代理请求不得损坏 Anthropic JSON 正文')
    assert.equal(proxyHitCounts.get('http'), 1, 'HTTP CONNECT 代理必须被命中一次')
    assert.equal(proxyHitCounts.get('https'), 1, 'HTTPS CONNECT 代理必须被命中一次')
    assert.equal(proxyHitCounts.get('socks5h'), 1, 'SOCKS5H 代理必须被命中一次')

    console.log('gateway fetch proxy transport regression passed: HTTP/HTTPS/SOCKS5H proxies hit, origin direct hits=0')
  } finally {
    closeGatewayUpstreamAgentsForTest()
    await Promise.all([closeServer(httpProxy), closeServer(httpsProxy), closeServer(socksProxy)])
  }
} finally {
  closeGatewayUpstreamAgentsForTest()
  await closeServer(originServer)
  runtimeConfig.upstreamUrlSecurity.allowPrivateBaseUrls = originalAllowPrivateBaseUrls
  if (originalTlsRejectUnauthorized === undefined) {
    delete process.env.NODE_TLS_REJECT_UNAUTHORIZED
  } else {
    process.env.NODE_TLS_REJECT_UNAUTHORIZED = originalTlsRejectUnauthorized
  }
}

async function assertFetchRequestUsesProxy(input: {
  marker: string
  originUrl: string
  proxyUrl: string
}): Promise<void> {
  const response = await settleWithin(requestUpstream(input.originUrl, {
    method: 'POST',
    headers: new Headers({
      'anthropic-version': '2023-06-01',
      'content-type': 'application/json'
    }),
    body: Buffer.from(requestBody),
    proxyUrl: input.proxyUrl,
    requestTimeoutMs: 2_000,
    timeoutMs: 2_000,
    transport: 'fetch'
  }), 3_000, `${input.marker} 代理请求`)
  assert.equal(response.status, 200, `${input.marker} 代理请求应返回 origin 完整响应`)
  const parsed = JSON.parse(await responseText(response)) as { marker?: string; ok?: boolean }
  assert.equal(parsed.ok, true, `${input.marker} 代理响应应可读`)
  assert.equal(parsed.marker, input.marker, `${input.marker} 代理哨兵必须抵达 origin`)
}

function createConnectProxy(
  marker: string,
  allowedTargetPort: number,
  tls?: { key: string; cert: string }
): HttpServer {
  const requestHandler = (request: IncomingMessage, response: ServerResponse): void => {
    incrementProxyHit(marker)
    let target: URL
    try {
      target = new URL(request.url ?? '')
    } catch {
      response.writeHead(400, { connection: 'close' })
      response.end()
      return
    }
    if (target.hostname !== '127.0.0.1' || Number(target.port) !== allowedTargetPort || target.protocol !== 'http:') {
      response.writeHead(403, { connection: 'close' })
      response.end()
      return
    }
    const headers: Record<string, string | string[] | undefined> = {
      ...request.headers,
      [proxyMarkerHeader]: marker
    }
    delete headers['proxy-authorization']
    delete headers['proxy-connection']
    const upstream = httpRequest(target, { method: request.method ?? 'GET', headers }, (upstreamResponse) => {
      response.writeHead(upstreamResponse.statusCode ?? 502, upstreamResponse.headers)
      upstreamResponse.pipe(response)
    })
    upstream.once('error', () => {
      if (!response.headersSent) response.writeHead(502, { connection: 'close' })
      response.end()
    })
    request.pipe(upstream)
  }
  const server = tls
    ? createHttpsServer(tls, requestHandler)
    : createHttpServer(requestHandler)
  server.on('connect', (request, clientSocket, head) => {
    const target = parseAuthority(request.url)
    if (!target || target.hostname !== '127.0.0.1' || target.port !== allowedTargetPort) {
      clientSocket.end('HTTP/1.1 403 Forbidden\r\nConnection: close\r\n\r\n')
      return
    }
    incrementProxyHit(marker)
    relayMarkedHttpTunnel(clientSocket, head, target.port, marker, () => {
      clientSocket.write('HTTP/1.1 200 Connection Established\r\nProxy-Agent: juhe-test\r\n\r\n')
    })
  })
  return server
}

function createSocks5Proxy(marker: string, allowedTargetPort: number): NetServer {
  return createNetServer((clientSocket) => {
    let buffer = Buffer.alloc(0)
    let stage: 'greeting' | 'request' = 'greeting'
    const onData = (chunk: Buffer): void => {
      buffer = Buffer.concat([buffer, chunk])
      if (stage === 'greeting') {
        if (buffer.byteLength < 2) return
        const methodCount = buffer[1] ?? 0
        if (buffer.byteLength < 2 + methodCount) return
        const methods = buffer.subarray(2, 2 + methodCount)
        buffer = buffer.subarray(2 + methodCount)
        if (methods.indexOf(0x00) < 0) {
          clientSocket.end(Buffer.from([0x05, 0xff]))
          return
        }
        clientSocket.write(Buffer.from([0x05, 0x00]))
        stage = 'request'
      }
      if (stage !== 'request') return
      const request = parseSocks5ConnectRequest(buffer)
      if (!request) return
      buffer = buffer.subarray(request.byteLength)
      clientSocket.off('data', onData)
      clientSocket.pause()
      if (request.command !== 0x01 || request.hostname !== '127.0.0.1' || request.port !== allowedTargetPort) {
        clientSocket.end(Buffer.from([0x05, 0x02, 0x00, 0x01, 0, 0, 0, 0, 0, 0]))
        return
      }
      incrementProxyHit(marker)
      relayMarkedHttpTunnel(clientSocket, buffer, request.port, marker, () => {
        clientSocket.write(Buffer.from([0x05, 0x00, 0x00, 0x01, 127, 0, 0, 1, 0, 0]))
      })
    }
    clientSocket.on('data', onData)
  })
}

function relayMarkedHttpTunnel(
  clientSocket: Duplex,
  head: Buffer,
  targetPort: number,
  marker: string,
  onConnected: () => void
): void {
  clientSocket.pause()
  const upstreamSocket = connect({ host: '127.0.0.1', port: targetPort })
  const markerTransform = createProxyMarkerTransform(marker)
  clientSocket.pipe(markerTransform).pipe(upstreamSocket)
  upstreamSocket.pipe(clientSocket)
  upstreamSocket.once('connect', () => {
    onConnected()
    if (head.byteLength > 0) markerTransform.write(head)
    clientSocket.resume()
  })
  upstreamSocket.once('error', () => clientSocket.destroy())
  clientSocket.once('error', () => upstreamSocket.destroy())
  clientSocket.once('close', () => upstreamSocket.destroy())
}

function createProxyMarkerTransform(marker: string): Transform {
  let pending = Buffer.alloc(0)
  let injected = false
  return new Transform({
    transform(chunk: Buffer, _encoding, callback): void {
      if (injected) {
        callback(null, chunk)
        return
      }
      pending = Buffer.concat([pending, chunk])
      const headerEnd = pending.indexOf('\r\n\r\n')
      if (headerEnd < 0) {
        if (pending.byteLength > 64 * 1024) callback(new Error('代理测试请求头过大'))
        else callback()
        return
      }
      injected = true
      const marked = Buffer.concat([
        pending.subarray(0, headerEnd),
        Buffer.from(`\r\n${proxyMarkerHeader}: ${marker}\r\n\r\n`),
        pending.subarray(headerEnd + 4)
      ])
      pending = Buffer.alloc(0)
      callback(null, marked)
    },
    flush(callback): void {
      if (!injected && pending.byteLength > 0) this.push(pending)
      callback()
    }
  })
}

function parseAuthority(value: string | undefined): { hostname: string; port: number } | undefined {
  if (!value) return undefined
  try {
    const url = new URL(`http://${value}`)
    const port = Number(url.port)
    return Number.isInteger(port) && port > 0
      ? { hostname: url.hostname, port }
      : undefined
  } catch {
    return undefined
  }
}

function parseSocks5ConnectRequest(buffer: Buffer): {
  byteLength: number
  command: number
  hostname: string
  port: number
} | undefined {
  if (buffer.byteLength < 4) return undefined
  const addressType = buffer[3]
  let offset = 4
  let hostname = ''
  if (addressType === 0x01) {
    if (buffer.byteLength < offset + 4 + 2) return undefined
    hostname = [...buffer.subarray(offset, offset + 4)].join('.')
    offset += 4
  } else if (addressType === 0x03) {
    const length = buffer[offset]
    if (length === undefined || buffer.byteLength < offset + 1 + length + 2) return undefined
    hostname = buffer.subarray(offset + 1, offset + 1 + length).toString('utf8')
    offset += 1 + length
  } else if (addressType === 0x04) {
    if (buffer.byteLength < offset + 16 + 2) return undefined
    hostname = buffer.subarray(offset, offset + 16).toString('hex')
    offset += 16
  } else {
    throw new Error(`不支持的 SOCKS5 地址类型：${String(addressType)}`)
  }
  const port = buffer.readUInt16BE(offset)
  return { byteLength: offset + 2, command: buffer[1] ?? 0, hostname, port }
}

function createSelfSignedCertificate(): { key: string; cert: string } {
  const { privateKey, publicKey } = generateKeyPairSync('rsa', { modulusLength: 2048 })
  const signatureAlgorithm = derSequence(derOid('1.2.840.113549.1.1.11'), derNull())
  const commonName = derSequence(
    derSet(derSequence(derOid('2.5.4.3'), derUtf8String('127.0.0.1')))
  )
  const now = Date.now()
  const subjectAltName = derSequence(der(0x87, Buffer.from([127, 0, 0, 1])))
  const extensions = der(0xa3, derSequence(
    derSequence(derOid('2.5.29.17'), derOctetString(subjectAltName))
  ))
  const tbsCertificate = derSequence(
    der(0xa0, derInteger(Buffer.from([2]))),
    derInteger(randomBytes(16)),
    signatureAlgorithm,
    commonName,
    derSequence(derUtcTime(new Date(now - 60_000)), derUtcTime(new Date(now + 60 * 60_000))),
    commonName,
    publicKey.export({ type: 'spki', format: 'der' }),
    extensions
  )
  const certificate = derSequence(
    tbsCertificate,
    signatureAlgorithm,
    derBitString(sign('sha256', tbsCertificate, privateKey))
  )
  return {
    key: privateKey.export({ type: 'pkcs8', format: 'pem' }).toString(),
    cert: toPem('CERTIFICATE', certificate)
  }
}

function der(tag: number, value: Buffer): Buffer {
  return Buffer.concat([Buffer.from([tag]), derLength(value.byteLength), value])
}

function derSequence(...values: Buffer[]): Buffer {
  return der(0x30, Buffer.concat(values))
}

function derSet(...values: Buffer[]): Buffer {
  return der(0x31, Buffer.concat(values))
}

function derInteger(value: Buffer): Buffer {
  let normalized = value
  while (normalized.byteLength > 1 && normalized[0] === 0 && (normalized[1]! & 0x80) === 0) {
    normalized = normalized.subarray(1)
  }
  if ((normalized[0]! & 0x80) !== 0) normalized = Buffer.concat([Buffer.from([0]), normalized])
  return der(0x02, normalized)
}

function derOid(value: string): Buffer {
  const parts = value.split('.').map(Number)
  assert(parts.length >= 2, `OID 非法：${value}`)
  const bytes = [parts[0]! * 40 + parts[1]!]
  for (const part of parts.slice(2)) {
    const encoded = [part & 0x7f]
    let remaining = Math.floor(part / 128)
    while (remaining > 0) {
      encoded.unshift(0x80 | (remaining & 0x7f))
      remaining = Math.floor(remaining / 128)
    }
    bytes.push(...encoded)
  }
  return der(0x06, Buffer.from(bytes))
}

function derNull(): Buffer {
  return der(0x05, Buffer.alloc(0))
}

function derUtf8String(value: string): Buffer {
  return der(0x0c, Buffer.from(value, 'utf8'))
}

function derUtcTime(value: Date): Buffer {
  const year = String(value.getUTCFullYear() % 100).padStart(2, '0')
  const text = `${year}${String(value.getUTCMonth() + 1).padStart(2, '0')}${String(value.getUTCDate()).padStart(2, '0')}${String(value.getUTCHours()).padStart(2, '0')}${String(value.getUTCMinutes()).padStart(2, '0')}${String(value.getUTCSeconds()).padStart(2, '0')}Z`
  return der(0x17, Buffer.from(text, 'ascii'))
}

function derOctetString(value: Buffer): Buffer {
  return der(0x04, value)
}

function derBitString(value: Buffer): Buffer {
  return der(0x03, Buffer.concat([Buffer.from([0]), value]))
}

function derLength(length: number): Buffer {
  if (length < 0x80) return Buffer.from([length])
  const bytes: number[] = []
  let remaining = length
  while (remaining > 0) {
    bytes.unshift(remaining & 0xff)
    remaining = Math.floor(remaining / 256)
  }
  return Buffer.from([0x80 | bytes.length, ...bytes])
}

function toPem(label: string, value: Buffer): string {
  const base64 = value.toString('base64').match(/.{1,64}/g)?.join('\n') ?? ''
  return `-----BEGIN ${label}-----\n${base64}\n-----END ${label}-----\n`
}

function incrementProxyHit(marker: string): void {
  proxyHitCounts.set(marker, (proxyHitCounts.get(marker) ?? 0) + 1)
}

function singleHeader(value: string | string[] | undefined): string | undefined {
  return Array.isArray(value) ? value[0] : value
}

async function readRequestBody(request: IncomingMessage): Promise<string> {
  const chunks: Buffer[] = []
  for await (const chunk of request) chunks.push(Buffer.isBuffer(chunk) ? chunk : Buffer.from(chunk))
  return Buffer.concat(chunks).toString('utf8')
}

async function responseText(response: GatewayUpstreamResponse): Promise<string> {
  const chunks: Buffer[] = []
  if (!response.body) return ''
  for await (const chunk of response.body) chunks.push(Buffer.from(chunk))
  return Buffer.concat(chunks).toString('utf8')
}

async function settleWithin<T>(promise: Promise<T>, timeoutMs: number, label: string): Promise<T> {
  let timeout: NodeJS.Timeout | undefined
  try {
    return await Promise.race([
      promise,
      new Promise<never>((_resolve, reject) => {
        timeout = setTimeout(() => reject(new Error(`${label} 未在 ${timeoutMs}ms 内完成`)), timeoutMs)
      })
    ])
  } finally {
    if (timeout) clearTimeout(timeout)
  }
}

function listen(server: HttpServer | NetServer): Promise<void> {
  server.listen(0, '127.0.0.1')
  if (server.listening) return Promise.resolve()
  return new Promise((resolve, reject) => {
    server.once('listening', resolve)
    server.once('error', reject)
  })
}

function serverPort(server: HttpServer | NetServer): number {
  const address = server.address()
  if (!address || typeof address === 'string') throw new Error('测试服务地址不可用')
  return address.port
}

async function closeServer(server: HttpServer | NetServer): Promise<void> {
  if (!server.listening) return
  if ('closeAllConnections' in server && typeof server.closeAllConnections === 'function') {
    server.closeAllConnections()
  }
  await new Promise<void>((resolve, reject) => {
    server.close((error) => error ? reject(error) : resolve())
  })
}
