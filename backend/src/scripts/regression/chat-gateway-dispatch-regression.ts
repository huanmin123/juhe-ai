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
