import { strict as assert } from 'node:assert'
import http from 'node:http'

import {
  closeGatewayUpstreamAgentsForTest,
  requestUpstream
} from '../../modules/gateway/openai-gateway-upstream.js'

let connectionCount = 0
const server = http.createServer((req, res) => {
  req.resume()
  res.setHeader('content-type', 'application/json')
  res.end('{"ok":true}')
})
server.on('connection', () => {
  connectionCount += 1
})

try {
  await new Promise<void>((resolve) => server.listen(0, '127.0.0.1', resolve))
  const address = server.address()
  assert(address && typeof address === 'object', '测试 HTTP 服务启动失败')
  const url = `http://127.0.0.1:${address.port}/v1/responses`

  await drainResponse(await requestUpstream(url, { method: 'GET', headers: new Headers() }))
  await drainResponse(await requestUpstream(url, { method: 'GET', headers: new Headers() }))
  assert.equal(connectionCount, 1, '网关直连上游请求应复用 keep-alive 连接')

  console.log('网关 upstream keep-alive 回归通过：直连上游连接可复用')
} finally {
  closeGatewayUpstreamAgentsForTest()
  await new Promise<void>((resolve) => server.close(() => resolve()))
}

async function drainResponse(response: Awaited<ReturnType<typeof requestUpstream>>): Promise<void> {
  if (!response.body) return
  for await (const _chunk of response.body) {
  }
}
