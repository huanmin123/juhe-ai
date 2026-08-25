import assert from 'node:assert/strict'

import { proxyLatencyGoOwnerHealth, resolveSystemApiHealth } from '../../modules/system-api/system-api-app.js'

assert.deepEqual(await proxyLatencyGoOwnerHealth({}), { enabled: false, ready: true }, '未启用 J3a Go owner 时不应阻断系统健康')

const goOwnerEnv = {
  JUHE_AI_PROXY_LATENCY_JOBS_OWNER: 'go',
  JUHE_AI_PROXY_LATENCY_JOBS_HTTP_URL: 'http://127.0.0.1:3305/proxy-latency/manual'
}

assert.deepEqual(await proxyLatencyGoOwnerHealth({ JUHE_AI_PROXY_LATENCY_JOBS_OWNER: 'go' }), { enabled: true, ready: false }, 'Go owner 缺少 jobs 地址必须 fail-closed')
let remoteFetchCalled = false
assert.deepEqual(await proxyLatencyGoOwnerHealth({
  JUHE_AI_PROXY_LATENCY_JOBS_OWNER: 'go',
  JUHE_AI_PROXY_LATENCY_JOBS_HTTP_URL: 'http://jobs.other-namespace.svc:3305'
}, {
  fetch: async () => {
    remoteFetchCalled = true
    return new Response('{}', { status: 200 })
  }
}), { enabled: true, ready: false }, 'J3a 健康观测不得通过任意网络地址访问 Go jobs')
assert.equal(remoteFetchCalled, false, '非 loopback J3a health endpoint 不得发出请求')
assert.deepEqual(await proxyLatencyGoOwnerHealth(goOwnerEnv, {
  fetch: async (url) => {
    assert.equal(String(url), 'http://127.0.0.1:3305/health', 'J3a 健康必须只读取 Go jobs loopback health')
    return new Response(JSON.stringify({
      ready: true,
      proxyLatencyEnabled: true,
      proxyLatencyReady: true,
      proxyLatencyOwnerHeld: true
    }), { status: 200 })
  }
}), { enabled: true, ready: true }, 'Go jobs J3a owner 已就绪时必须通过')

const ownerLost = await proxyLatencyGoOwnerHealth(goOwnerEnv, {
  fetch: async () => new Response(JSON.stringify({
    ready: true,
    proxyLatencyEnabled: true,
    proxyLatencyReady: true,
    proxyLatencyOwnerHeld: false
  }), { status: 200 })
})
assert.deepEqual(ownerLost, { enabled: true, ready: false }, 'J3a owner lease 丢失必须在 Node 运维健康中可见')
assert.deepEqual(resolveSystemApiHealth({ enabled: false, ready: true }, ownerLost), {
  statusCode: 200,
  status: 'degraded',
  service: 'juhe-ai-db-service',
  accountBalance: { enabled: false, ready: true },
  proxyLatency: { enabled: true, ready: false }
}, 'J3a 降级只影响运维 API health body，不改变 Node runtime readiness HTTP 语义')

console.log('proxy latency jobs health regression passed')
