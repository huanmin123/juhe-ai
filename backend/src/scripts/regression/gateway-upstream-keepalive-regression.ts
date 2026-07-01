import { strict as assert } from 'node:assert'
import { readFileSync } from 'node:fs'
import http from 'node:http'
import { dirname, resolve } from 'node:path'
import { fileURLToPath } from 'node:url'

import { runtimeConfig } from '../../config/runtime.js'
import {
  closeGatewayUpstreamAgentsForTest,
  requestUpstream
} from '../../modules/gateway/upstream/request.js'

runtimeConfig.upstreamUrlSecurity.allowPrivateBaseUrls = true

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
  assertBoundedGatewayUpstreamAgentCache()

  console.log('网关 upstream keep-alive 回归通过：直连上游连接可复用，直连/代理 agent 缓存有明确上限')
} finally {
  closeGatewayUpstreamAgentsForTest()
  await new Promise<void>((resolve) => server.close(() => resolve()))
}

async function drainResponse(response: Awaited<ReturnType<typeof requestUpstream>>): Promise<void> {
  if (!response.body) return
  for await (const _chunk of response.body) {
  }
}

function assertBoundedGatewayUpstreamAgentCache(): void {
  const sourcePath = resolve(dirname(fileURLToPath(import.meta.url)), '../../modules/gateway/upstream/request.ts')
  const source = readFileSync(sourcePath, 'utf8')
  assert.doesNotMatch(source, /maxSockets:\s*Infinity/, '网关上游 agent 不应使用无限 maxSockets')
  assert.match(source, /maxSockets:\s*runtimeConfig\.gateway\.upstreamAgentMaxSockets/, '网关上游 agent maxSockets 应读取运行时配置')
  assert.match(source, /maxFreeSockets:\s*runtimeConfig\.gateway\.upstreamAgentMaxFreeSockets/, '网关上游 agent maxFreeSockets 应读取运行时配置')
  assert.match(source, /maxTotalSockets:\s*runtimeConfig\.gateway\.upstreamAgentMaxTotalSockets/, '网关上游 agent maxTotalSockets 应读取运行时配置')
  assert(runtimeConfig.gateway.upstreamAgentMaxSockets >= 2048, '默认单上游 socket 上限应足以承接高并发慢 SSE')
  assert(runtimeConfig.gateway.upstreamAgentMaxTotalSockets >= 8192, '默认总 socket 上限应足以承接多上游高并发慢 SSE')
  assert.match(source, /createAppCache<string,\s*http\.Agent>/, '代理 agent 缓存应使用有上限的 LRU cache')
  assert.match(source, /max:\s*gatewayProxyAgentCacheMaxEntries/, '代理 agent 缓存应配置最大条目数')
  assert.match(source, /ttlMs:\s*gatewayProxyAgentCacheTtlMs/, '代理 agent 缓存应配置 TTL')
  assert.match(source, /dispose:\s*\(agent\)\s*=>\s*{\s*agent\.destroy\(\)/s, '代理 agent 被驱逐时应销毁连接池')
}
