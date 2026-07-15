import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'

import { runtimeConfig } from '../../config/runtime.js'
import {
  clearAuthenticatedModelsRateLimitForTest,
  consumeAuthenticatedModelsRateLimit
} from '../../modules/gateway/runtime/authenticated-models-rate-limit.service.js'
import {
  clearAuthenticatedModelsResponseCache,
  getAuthenticatedModelsResponseCache,
  setAuthenticatedModelsResponseCache
} from '../../modules/gateway/response/models-response-cache.js'

runtimeConfig.cacheDriver = 'memory'
runtimeConfig.runtimeStateDriver = 'memory'

const nowMs = 2_000_000
clearAuthenticatedModelsRateLimitForTest()
for (let index = 0; index < 20; index += 1) {
  const decision = await consumeAuthenticatedModelsRateLimit({ apiKeyId: 'key-a', clientIp: '203.0.113.1', nowMs })
  assert.equal(decision.allowed, true)
}
let decision = await consumeAuthenticatedModelsRateLimit({ apiKeyId: 'key-a', clientIp: '203.0.113.1', nowMs })
assert.equal(decision.allowed, false)
assert.equal(decision.scope, 'api_key_ip')
assert.equal(decision.limit, 20)
assert.equal(decision.retryAfterSeconds, 10)
assert.equal((await consumeAuthenticatedModelsRateLimit({ apiKeyId: 'key-b', clientIp: '203.0.113.1', nowMs })).allowed, true, '不同 API Key 必须隔离')

clearAuthenticatedModelsRateLimitForTest()
for (let windowIndex = 0; windowIndex < 3; windowIndex += 1) {
  for (let index = 0; index < 20; index += 1) {
    assert.equal((await consumeAuthenticatedModelsRateLimit({
      apiKeyId: 'key-minute-ip',
      clientIp: '203.0.113.2',
      nowMs: 1_800_000 + windowIndex * 10_000
    })).allowed, true)
  }
}
decision = await consumeAuthenticatedModelsRateLimit({ apiKeyId: 'key-minute-ip', clientIp: '203.0.113.2', nowMs: 1_830_000 })
assert.equal(decision.allowed, false)
assert.equal(decision.scope, 'api_key_ip')
assert.equal(decision.limit, 60, 'API Key + IP 每分钟第 61 次必须被阻断')

clearAuthenticatedModelsRateLimitForTest()
for (let index = 0; index < 100; index += 1) {
  const globalDecision = await consumeAuthenticatedModelsRateLimit({
    apiKeyId: 'key-global',
    clientIp: `203.0.113.${Math.floor(index / 20) + 10}`,
    nowMs
  })
  assert.equal(globalDecision.allowed, true)
}
decision = await consumeAuthenticatedModelsRateLimit({ apiKeyId: 'key-global', clientIp: '203.0.113.99', nowMs })
assert.equal(decision.allowed, false)
assert.equal(decision.scope, 'api_key')
assert.equal(decision.limit, 100)

clearAuthenticatedModelsRateLimitForTest()
for (let windowIndex = 0; windowIndex < 3; windowIndex += 1) {
  for (let index = 0; index < 100; index += 1) {
    assert.equal((await consumeAuthenticatedModelsRateLimit({
      apiKeyId: 'key-minute-global',
      clientIp: `198.51.${windowIndex}.${Math.floor(index / 20)}`,
      nowMs: 1_800_000 + windowIndex * 10_000
    })).allowed, true)
  }
}
decision = await consumeAuthenticatedModelsRateLimit({ apiKeyId: 'key-minute-global', clientIp: '198.51.99.99', nowMs: 1_830_000 })
assert.equal(decision.allowed, false)
assert.equal(decision.scope, 'api_key')
assert.equal(decision.limit, 300, 'API Key 全局每分钟第 301 次必须被阻断')

runtimeConfig.runtimeStateDriver = 'redis'
runtimeConfig.redis.stateUrl = undefined
await assert.rejects(
  () => consumeAuthenticatedModelsRateLimit({ apiKeyId: 'key-redis', clientIp: '203.0.113.3', nowMs }),
  /JUHE_AI_REDIS_STATE_URL/,
  'Redis 运行态必须走 shared Redis limiter，不能回退进程内计数'
)
runtimeConfig.runtimeStateDriver = 'memory'

await clearAuthenticatedModelsResponseCache()
const cacheInput = {
  systemAccountId: 'system-a',
  providerCodes: ['openai-compatible', 'anthropic', 'openai-compatible'],
  protocol: 'openai' as const,
  variant: 'openai' as const
}
await setAuthenticatedModelsResponseCache(cacheInput, { object: 'list', data: [{ id: 'gpt-test' }] })
assert.deepEqual(
  await getAuthenticatedModelsResponseCache({ ...cacheInput, providerCodes: ['anthropic', 'openai-compatible'] }),
  { object: 'list', data: [{ id: 'gpt-test' }] },
  'provider codes 排序与去重后必须命中同一 30 秒缓存'
)
assert.equal(
  await getAuthenticatedModelsResponseCache({ ...cacheInput, systemAccountId: 'system-b' }),
  undefined,
  '不同系统账户不得串用模型响应'
)
assert.equal(
  await getAuthenticatedModelsResponseCache({ ...cacheInput, variant: 'codex' }),
  undefined,
  'Codex 与普通 OpenAI 响应不得串用'
)
assert.equal(
  await getAuthenticatedModelsResponseCache({ ...cacheInput, protocol: 'anthropic', variant: 'default' }),
  undefined,
  '不同协议不得串用'
)
await clearAuthenticatedModelsResponseCache()
assert.equal(await getAuthenticatedModelsResponseCache(cacheInput), undefined, 'runtime invalidator 必须能清空最终响应缓存')

const cacheSource = readFileSync(new URL('../../modules/gateway/response/models-response-cache.ts', import.meta.url), 'utf8')
assert.match(cacheSource, /ttlMs:\s*30_000/, '最终响应缓存 TTL 必须固定为 30 秒')
assert.match(cacheSource, /registerGatewayRuntimeCacheInvalidator/, '最终响应缓存必须注册现有 runtime invalidator')
assert.match(cacheSource, /authenticated_models_response_cache_read_failed/, '最终响应缓存读取失败必须回源，不能阻断模型列表')
assert.match(cacheSource, /authenticated_models_response_cache_write_failed/, '最终响应缓存写入失败不得阻断模型列表响应')

const preflightSource = readFileSync(new URL('../../modules/gateway/request/preflight.ts', import.meta.url), 'utf8')
assert.match(preflightSource, /consumeAuthenticatedModelsRateLimit/, '认证成功后的 models 路径必须执行双层 limiter')
assert.match(preflightSource, /Retry-After/, '认证 models 429 必须返回 Retry-After')
assert.match(preflightSource, /authenticated_models_rate_limited/, '认证 models 429 必须有独立审计错误码')
assert.match(preflightSource, /sendGatewayFailureResponse/, '认证 models 429 必须进入失败审计和失败 usage，不得写成功 usage')

const fixedResponseSource = readFileSync(new URL('../../modules/gateway/response/fixed-responses.ts', import.meta.url), 'utf8')
assert.match(fixedResponseSource, /private, max-age=30/, '认证 models 成功响应必须声明客户端私有缓存')
assert.match(fixedResponseSource, /getAuthenticatedModelsResponseCache/, '最终 payload 必须在目录构建前读取缓存')
assert.match(fixedResponseSource, /setAuthenticatedModelsResponseCache/, '目录构建后必须写入最终 payload 缓存')

console.log('认证模型列表双层限流与最终响应缓存回归通过')
