import assert from 'node:assert/strict'
import http from 'node:http'
import { gunzipSync } from 'node:zlib'

import express from 'express'

import { createHttpCompressionMiddleware, httpCompressionThresholdBytes } from '../../shared/http-compression.js'

const app = express()
app.use(createHttpCompressionMiddleware())
app.get('/json', (_req, res) => {
  res.json({
    message: '压缩回归',
    payload: 'x'.repeat(httpCompressionThresholdBytes * 4)
  })
})
app.get('/events', (_req, res) => {
  res.writeHead(200, {
    'content-type': 'text/event-stream; charset=utf-8',
    'cache-control': 'no-cache, no-transform'
  })
  res.end(`event: ready\ndata: ${JSON.stringify({ ok: true })}\n\n`)
})

const server = app.listen(0, '127.0.0.1')

try {
  await new Promise<void>((resolve) => server.once('listening', resolve))
  const address = server.address()
  assert(address && typeof address === 'object', '压缩回归测试服务未获得监听端口')
  const baseUrl = `http://127.0.0.1:${address.port}`

  const compressed = await requestBuffer(`${baseUrl}/json`, { 'accept-encoding': 'gzip' })
  assert.equal(compressed.statusCode, 200)
  assert.equal(compressed.headers['content-encoding'], 'gzip', '可压缩 JSON 响应应按客户端协商返回 gzip')
  const decompressed = JSON.parse(gunzipSync(compressed.body).toString('utf8')) as { payload?: string }
  assert.equal(decompressed.payload?.length, httpCompressionThresholdBytes * 4, 'gzip 解压后响应内容应保持完整')

  const plain = await requestBuffer(`${baseUrl}/json`, { 'accept-encoding': 'identity' })
  assert.equal(plain.statusCode, 200)
  assert.equal(plain.headers['content-encoding'], undefined, '客户端不接受压缩时应保持普通 JSON 响应')
  assert.equal((JSON.parse(plain.body.toString('utf8')) as { payload?: string }).payload?.length, httpCompressionThresholdBytes * 4)

  const events = await requestBuffer(`${baseUrl}/events`, { 'accept-encoding': 'gzip' })
  assert.equal(events.statusCode, 200)
  assert.equal(events.headers['content-encoding'], undefined, 'SSE 响应不应被压缩')
  assert.match(events.body.toString('utf8'), /event: ready/)
} finally {
  await new Promise<void>((resolve, reject) => {
    server.close((error) => {
      if (error) reject(error)
      else resolve()
    })
  })
}

console.log('http-compression-regression passed')

function requestBuffer(url: string, headers: Record<string, string>): Promise<{
  statusCode: number
  headers: http.IncomingHttpHeaders
  body: Buffer
}> {
  return new Promise((resolve, reject) => {
    const req = http.request(url, { headers }, (res) => {
      const chunks: Buffer[] = []
      res.on('data', (chunk: Buffer) => chunks.push(chunk))
      res.on('end', () => {
        resolve({
          statusCode: res.statusCode ?? 0,
          headers: res.headers,
          body: Buffer.concat(chunks)
        })
      })
    })
    req.on('error', reject)
    req.end()
  })
}
