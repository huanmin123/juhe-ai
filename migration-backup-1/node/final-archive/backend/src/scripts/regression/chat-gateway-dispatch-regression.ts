import assert from 'node:assert/strict'

const { runtimeConfig } = await import('../../config/runtime.js')
const {
  ChatGatewayUnavailableError,
  dispatchChatGatewayRequest,
  resolveChatGatewayOrigin,
  setChatGatewayEndpointResolverForTest
} = await import('../../modules/chat/chat-gateway-dispatch.js')

const previous = {
  runtimeMode: runtimeConfig.runtimeMode,
  performanceNodeRole: runtimeConfig.performanceNodeRole,
  processRole: runtimeConfig.processRole,
  port: runtimeConfig.port
}

try {
  runtimeConfig.runtimeMode = 'standalone'
  runtimeConfig.performanceNodeRole = 'combined'
  runtimeConfig.processRole = 'db-service'
  runtimeConfig.port = 39123
  assert.equal(await resolveChatGatewayOrigin(), 'http://127.0.0.1:39123', 'standalone 必须直接使用同一应用实例，不读取 Redis 注册表')

  runtimeConfig.runtimeMode = 'performance'
  runtimeConfig.performanceNodeRole = 'control'
  runtimeConfig.processRole = 'db-service'
  setChatGatewayEndpointResolverForTest(async () => [
    { instanceId: 'gateway-1', origin: 'http://127.0.0.1:3101' },
    { instanceId: 'gateway-2', origin: 'http://127.0.0.1:3102' }
  ])
  assert.equal(await resolveChatGatewayOrigin(), 'http://127.0.0.1:3101', 'control 必须选择已注册 Gateway，而非自身端口')
  assert.equal(await resolveChatGatewayOrigin(), 'http://127.0.0.1:3102', '多个 Gateway 必须轮询分担站内聊天请求')

  const originalFetch = globalThis.fetch
  const requestedOrigins: string[] = []
  globalThis.fetch = (async (input: string | URL | Request) => {
    requestedOrigins.push(new URL(String(input)).origin)
    return new Response(new ReadableStream<Uint8Array>())
  }) as typeof fetch
  try {
    const first = await dispatchChatGatewayRequest('/v1/responses', { method: 'POST' })
    const second = await dispatchChatGatewayRequest('/v1/responses', { method: 'POST' })
    assert.deepEqual(requestedOrigins, ['http://127.0.0.1:3101', 'http://127.0.0.1:3102'], '活跃 SSE 必须优先分散到较空闲 Gateway')
    await first.body?.cancel()
    await second.body?.cancel()
  } finally {
    globalThis.fetch = originalFetch
  }

  setChatGatewayEndpointResolverForTest(async () => [
    { instanceId: 'gateway-1', origin: 'http://127.0.0.1:3101' },
    { instanceId: 'gateway-2', origin: 'http://127.0.0.1:3102' }
  ])
  const transportOrigins: string[] = []
  globalThis.fetch = (async (input: string | URL | Request) => {
    const origin = new URL(String(input)).origin
    transportOrigins.push(origin)
    if (origin === 'http://127.0.0.1:3101') throw new TypeError('gateway connection refused')
    return new Response(new ReadableStream<Uint8Array>())
  }) as typeof fetch
  try {
    await assert.rejects(dispatchChatGatewayRequest('/v1/responses', { method: 'POST' }), /connection refused/)
    const healthyResponse = await dispatchChatGatewayRequest('/v1/responses', { method: 'POST' })
    assert.deepEqual(transportOrigins, ['http://127.0.0.1:3101', 'http://127.0.0.1:3102'], '连接失败的 Gateway 在冷却期内不得继续接收站内聊天请求')
    await healthyResponse.body?.cancel()
  } finally {
    globalThis.fetch = originalFetch
  }

  setChatGatewayEndpointResolverForTest(async () => [
    { instanceId: 'gateway-1', origin: 'http://127.0.0.1:3101' },
    { instanceId: 'gateway-2', origin: 'http://127.0.0.1:3102' }
  ])
  const burstOrigins: string[] = []
  globalThis.fetch = (async (input: string | URL | Request) => {
    burstOrigins.push(new URL(String(input)).origin)
    return new Response(new ReadableStream<Uint8Array>())
  }) as typeof fetch
  try {
    const holder = await dispatchChatGatewayRequest('/v1/responses', { method: 'POST' })
    burstOrigins.length = 0
    const burst = await Promise.all(Array.from({ length: 32 }, () => (
      dispatchChatGatewayRequest('/v1/responses', { method: 'POST' })
    )))
    assert.equal(burstOrigins.filter((origin) => origin === 'http://127.0.0.1:3101').length, 16, '并发突发请求不得全部选择同一低负载 Gateway')
    assert.equal(burstOrigins.filter((origin) => origin === 'http://127.0.0.1:3102').length, 16, '并发突发请求必须在占位后保持均衡')
    await holder.body?.cancel()
    await Promise.all(burst.map(async (response) => await response.body?.cancel()))
  } finally {
    globalThis.fetch = originalFetch
  }

  let discoveryCalls = 0
  let completeDiscovery: ((endpoints: Array<{ instanceId: string; origin: string }>) => void) | undefined
  const discovery = new Promise<Array<{ instanceId: string; origin: string }>>((resolve) => {
    completeDiscovery = resolve
  })
  setChatGatewayEndpointResolverForTest(async () => {
    discoveryCalls += 1
    return await discovery
  })
  const concurrentResolves = Array.from({ length: 32 }, () => resolveChatGatewayOrigin())
  assert.equal(discoveryCalls, 1, '缓存失效后的并发聊天请求必须共用一次 Redis Gateway 发现')
  completeDiscovery?.([{ instanceId: 'gateway-1', origin: 'http://127.0.0.1:3101' }])
  assert.deepEqual(await Promise.all(concurrentResolves), Array.from({ length: 32 }, () => 'http://127.0.0.1:3101'))

  setChatGatewayEndpointResolverForTest(async () => [])
  await assert.rejects(resolveChatGatewayOrigin(), ChatGatewayUnavailableError, '无可用 Gateway 时不得回退到 control 自身')
} finally {
  setChatGatewayEndpointResolverForTest()
  runtimeConfig.runtimeMode = previous.runtimeMode
  runtimeConfig.performanceNodeRole = previous.performanceNodeRole
  runtimeConfig.processRole = previous.processRole
  runtimeConfig.port = previous.port
}

console.log('chat gateway dispatcher regression passed: standalone self-direct, control registry round-robin, no control fallback')
