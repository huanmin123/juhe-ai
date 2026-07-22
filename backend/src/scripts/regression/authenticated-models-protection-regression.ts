import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'

import { runtimeConfig } from '../../config/runtime.js'
import { logger } from '../../shared/logger.js'
import {
  clearAuthenticatedModelsRateLimitForTest,
  consumeAuthenticatedModelsRateLimit
} from '../../modules/gateway/runtime/authenticated-models-rate-limit.service.js'

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
decision = await consumeAuthenticatedModelsRateLimit({ apiKeyId: 'key-a', clientIp: '203.0.113.1', nowMs: nowMs + 1000 })
assert.equal(decision.allowed, false)
assert.equal(decision.retryAfterSeconds, 9, '固定窗口内重试不得把 10 秒窗口指数延长')
assert.equal(
  (await consumeAuthenticatedModelsRateLimit({ apiKeyId: 'key-a', clientIp: '203.0.113.1', nowMs: nowMs + 10_000 })).allowed,
  true,
  '10 秒窗口结束后应恢复，不得延长为 15 分钟惩罚'
)
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
for (let index = 0; index < 20; index += 1) {
  assert.equal((await consumeAuthenticatedModelsRateLimit({
    apiKeyId: 'key-global',
    clientIp: '203.0.113.99',
    nowMs: nowMs + 10_000
  })).allowed, true, '全局桶拒绝时不得提前创建或消耗 API Key + IP 桶')
}

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
const previousLogLevel = logger.level
logger.level = 'silent'
try {
  decision = await consumeAuthenticatedModelsRateLimit({ apiKeyId: 'key-redis', clientIp: '203.0.113.3', nowMs })
} finally {
  logger.level = previousLogLevel
}
assert.equal(decision.allowed, false, 'Redis limiter 异常必须 fail-closed')
assert.equal(decision.unavailable, true, 'Redis limiter 异常必须返回可区分的不可用决策')
assert.equal(decision.retryAfterSeconds, 5, 'Redis limiter 不可用应提供有界 Retry-After')
runtimeConfig.runtimeStateDriver = 'memory'

const preflightSource = readFileSync(new URL('../../modules/gateway/request/preflight.ts', import.meta.url), 'utf8')
assert.match(preflightSource, /consumeAuthenticatedModelsRateLimit/, '认证成功后的 models 路径必须执行双层 limiter')
assert.match(preflightSource, /resolveGatewayApiKeyForModelsAsync/, '认证 models 必须使用轻量 API Key 校验，不能加载完整网关运行时')
assert.match(preflightSource, /sendAuthenticatedModelsGatewayResponse/, '认证 models 必须在轻量认证后直接返回发布快照')
assert.match(preflightSource, /Retry-After/, '认证 models 429 必须返回 Retry-After')
assert.match(preflightSource, /authenticated_models_rate_limited/, '认证 models 429 必须有独立审计错误码')
assert.match(preflightSource, /authenticated_models_rate_limit_unavailable/, 'Redis limiter 异常必须返回独立 audited 503')
assert.match(preflightSource, /statusCode = limiterUnavailable \? 503 : 429/, 'Redis limiter 异常必须明确返回 503，普通超限保持 429')
assert.match(preflightSource, /sendGatewayFailureResponse/, 'limiter 拒绝必须写失败 usage，不能落入成功 usage')
assert.match(preflightSource, /sendGatewayFailureResponse/, '认证 models 429 必须进入失败审计和失败 usage，不得写成功 usage')
const earlyHandlerStart = preflightSource.indexOf('async function handleGatewayModelsRequestBeforeRequiredAuth')
const earlyHandlerEnd = preflightSource.indexOf('function gatewayModelsProviderCodes', earlyHandlerStart)
const earlyHandlerSource = preflightSource.slice(earlyHandlerStart, earlyHandlerEnd)
const earlyUsageContextStart = earlyHandlerSource.indexOf('const usageContext: OpenAIModelsResponseUsageContext')
const earlyUsageContextEnd = earlyHandlerSource.indexOf('input.auditCapture.bindContext', earlyUsageContextStart)
const earlyUsageContextSource = earlyHandlerSource.slice(earlyUsageContextStart, earlyUsageContextEnd)
assert.doesNotMatch(earlyUsageContextSource, /groupId:/, '固定模型目录 usage 不得写入未解析归属快照的分组维度')
assert.doesNotMatch(earlyHandlerSource, /resolveGatewayRuntimeAsync/, '认证 models 快路径不得加载账户、分组和响应检查策略')
assert.doesNotMatch(earlyHandlerSource, /rejectGatewayApiKeyQuotaIfExceeded|rejectGatewayAuthorizationQuotaIfExceeded/, '固定模型目录不得依赖额度统计链路')
assert.match(earlyHandlerSource, /resolveGatewayApiKeyForModelsAsync\([\s\S]*inspectClientIpPolicyAfterRuntime: false/, '认证 models 已在认证前检查 IP 策略，不得重复读取同一策略')
assert.doesNotMatch(earlyHandlerSource, /recordClientIpErrorCircuitSuccessAsync/, 'GET models 不产生请求体错误，不得读写请求体错误熔断状态')

const authenticatedModelsLimiterSource = readFileSync(new URL('../../modules/gateway/runtime/authenticated-models-rate-limit.service.ts', import.meta.url), 'utf8')
assert.match(
  authenticatedModelsLimiterSource,
  /consumePenaltyWindowRateLimitGroupsAsync\(\{[\s\S]*groups:\s*\[[\s\S]*scope:\s*'api_key'[\s\S]*scope:\s*'api_key_ip'/,
  '认证模型列表必须按 API Key 全局桶、API Key + IP 桶顺序用一次分组原子入口消费'
)
assert.doesNotMatch(
  authenticatedModelsLimiterSource,
  /consumePenaltyWindowRateLimitAsync/,
  '认证模型列表不得继续通过两次顺序调用产生两次 Redis EVAL'
)

const penaltyLimiterSource = readFileSync(new URL('../../modules/rate-limit/penalty-window-rate-limit.ts', import.meta.url), 'utf8')
assert.match(
  penaltyLimiterSource,
  /consumePenaltyWindowRateLimitGroupsAsync[\s\S]*consumeRedisPenaltyWindowRateLimitGroups/,
  'Penalty limiter 必须提供跨 scope 的单次 Redis 分组消费入口'
)
assert.match(
  penaltyLimiterSource,
  /const redisPenaltyWindowRateLimitGroupsScript = `[\s\S]*group_count[\s\S]*return \{0, blocked_retry_ms, blocked_rule_index, blocked_group_index\}/,
  '分组 Redis Lua 必须返回首个阻断 group，保持全局 API Key 桶优先'
)

const fixedResponseSource = readFileSync(new URL('../../modules/gateway/response/fixed-responses.ts', import.meta.url), 'utf8')
assert.match(fixedResponseSource, /private, no-cache/, '认证 models 成功响应不得允许客户端 30 秒内直接复用跨凭据响应')
for (const varyHeader of [
  'Authorization',
  'X-API-Key',
  'X-Goog-API-Key',
  'X-Juhe-Client-Profile',
  'Anthropic-Version',
  'Anthropic-Beta',
  'X-Claude-Code-Session-Id',
  'X-Claude-Code-Agent-Id',
  'Originator',
  'User-Agent',
  'X-Codex-Client'
]) {
  assert(fixedResponseSource.includes(varyHeader), `认证 models Vary 缺少实际判别头：${varyHeader}`)
}
assert.match(fixedResponseSource, /listClientModelCatalogAsync/, '认证 models 必须动态聚合 API Key 绑定供应商目录')
assert.match(fixedResponseSource, /providerCodes: input\.providerCodes/, '认证 models 必须把 API Key 绑定供应商集合传给客户端目录服务')
assert.doesNotMatch(fixedResponseSource, /readPublishedModelCatalogResponseAsync/, '认证 models 不得继续读取 default\/codex 发布快照')
assert.doesNotMatch(preflightSource, /fallbackProviderCode:/, '认证 models 目录不得回退为当前选中分组单供应商，必须只使用 API Key 全部 active binding')
const usageRecordsSource = readFileSync(new URL('../../modules/gateway/usage/records.ts', import.meta.url), 'utf8')
assert.match(usageRecordsSource, /hasResolvedGroupUsageMetadata/, '网关失败 usage 缺少分组归属快照时必须去掉分组维度，不能写入毒化记录')

console.log('认证模型列表双层限流与最终响应缓存回归通过')
