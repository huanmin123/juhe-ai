import { strict as assert } from 'node:assert'
import { readFileSync } from 'node:fs'
import http from 'node:http'

import { runtimeConfig } from '../../config/runtime.js'
import { openAICompatibleImageGenerationExecutorForGatewayRequest } from '../../modules/openai-compatible-images/image-generation-executor.js'

const runtimeSource = readFileSync(new URL('../../config/runtime.ts', import.meta.url), 'utf8')
const bridgeSource = readFileSync(new URL('../../modules/providers/drivers/_shared/openai-anthropic-bridge.ts', import.meta.url), 'utf8')
const attemptSource = readFileSync(new URL('../../modules/gateway/dispatch/upstream-attempts.ts', import.meta.url), 'utf8')

assert.match(
  runtimeSource,
  /JUHE_AI_IMAGE_GENERATION_PROVIDER_TIMEOUT_MS',\s*600000,\s*1000,\s*900000/,
  '图像 provider 默认超时必须为 600 秒且最大允许 900 秒'
)
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

await assertProviderTimeoutCoversResponseBody()

console.log('image provider timeout and signal regression passed')

async function assertProviderTimeoutCoversResponseBody(): Promise<void> {
  const previous = { ...runtimeConfig.imageGenerationProvider }
  const server = http.createServer((req, res) => {
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
    Object.assign(runtimeConfig.imageGenerationProvider, {
      endpoint: `http://127.0.0.1:${address.port}/v1/images/generations`,
      api: 'images',
      timeoutMs: 100,
      maxBodyBytes: 1024 * 1024
    })
    const executor = openAICompatibleImageGenerationExecutorForGatewayRequest()
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
  } finally {
    Object.assign(runtimeConfig.imageGenerationProvider, previous)
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
