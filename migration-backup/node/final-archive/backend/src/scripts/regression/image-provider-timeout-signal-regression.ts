import { strict as assert } from 'node:assert'
import { readFileSync } from 'node:fs'
import http from 'node:http'

import { openAICompatibleImageGenerationExecutorForGatewayRequest } from '../../modules/openai-compatible-images/image-generation-executor.js'

const executorSource = readFileSync(new URL('../../modules/openai-compatible-images/image-generation-executor.ts', import.meta.url), 'utf8')
const bridgeSource = readFileSync(new URL('../../modules/providers/drivers/_shared/openai-anthropic-bridge.ts', import.meta.url), 'utf8')
const attemptSource = readFileSync(new URL('../../modules/gateway/dispatch/upstream-attempts.ts', import.meta.url), 'utf8')

assert.match(
  executorSource,
  /http:\/\/127\.0\.0\.1:\$\{runtimeConfig\.port\}\/v1\/images\/generations/,
  '跨协议图片生成必须回到本地网关 Images 路由'
)
assert.doesNotMatch(executorSource, /JUHE_AI_IMAGE_GENERATION_PROVIDER_/, '图片执行器不得使用环境变量控制功能可用性')
assert.match(
  attemptSource,
  /transformGatewayUpstreamResponseForAccount[\s\S]*signal,/,
  '上游响应转换上下文必须携带当前请求 signal'
)
assert.match(
  bridgeSource,
  /imageGeneration\.executor\.generate\(\{[\s\S]{0,180}signal:\s*input\.signal/,
  '非流式图像 provider 必须接收当前请求 signal'
)
assert.match(
  bridgeSource,
  /imageGeneration\.executor\.generateStream\?\.\(\{[\s\S]{0,180}signal/,
  '流式图像 provider 必须接收当前请求 signal'
)
assert.equal(openAICompatibleImageGenerationExecutorForGatewayRequest({ headers: {} }), undefined, '缺少当前 API Key 授权时不得构造本地图片回路')

await assertProviderTimeoutCoversResponseBody()

console.log('image provider timeout and signal regression passed')

async function assertProviderTimeoutCoversResponseBody(): Promise<void> {
  let observedAuthorization: string | undefined
  const server = http.createServer((req, res) => {
    observedAuthorization = req.headers.authorization
    const stream = req.headers.accept?.includes('text/event-stream') === true
    res.writeHead(200, { 'content-type': stream ? 'text/event-stream' : 'application/json' })
    res.write(stream ? ': provider response started\n\n' : '{"data":[')
  })
  try {
    await new Promise<void>((resolve, reject) => {
      server.once('error', reject)
      server.listen(0, '127.0.0.1', resolve)
    })
    const address = server.address()
    assert(address && typeof address === 'object', '图像 provider 回归服务未返回监听地址')
    const executor = openAICompatibleImageGenerationExecutorForGatewayRequest({
      headers: { authorization: 'Bearer sk-regression-image-route' }
    }, {
      endpoint: `http://127.0.0.1:${address.port}/v1/images/generations`,
      timeoutMs: 100,
      maxBodyBytes: 1024 * 1024
    })
    assert(executor?.generateStream, '图像 provider executor 应同时提供 JSON 与 SSE 执行入口')

    await assert.rejects(
      executor.generate({ prompt: 'json body timeout', tool: { action: 'generate' } }),
      isImageProviderTimeout,
      'JSON provider 返回响应头后正文不结束时也必须受总生命周期超时限制'
    )
    await assert.rejects(
      async () => {
        for await (const _event of executor.generateStream!({ prompt: 'sse body timeout', tool: { action: 'generate' } })) {
          // The fixture intentionally never emits a complete event.
        }
      },
      isImageProviderTimeout,
      'SSE provider 返回响应头后持续不结束时也必须受总生命周期超时限制'
    )
    assert.equal(observedAuthorization, 'Bearer sk-regression-image-route', '本地 Images 路由必须复用当前请求授权')
  } finally {
    server.closeAllConnections()
    await new Promise<void>((resolve) => server.close(() => resolve()))
  }
}

function isImageProviderTimeout(error: unknown): boolean {
  return error instanceof Error
    && 'code' in error
    && error.code === 'openai_anthropic_bridge_image_generation_provider_timeout'
    && 'statusCode' in error
    && error.statusCode === 504
}
