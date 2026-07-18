import { strict as assert } from 'node:assert'
import { readFileSync } from 'node:fs'

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

console.log('image provider timeout and signal regression passed')
