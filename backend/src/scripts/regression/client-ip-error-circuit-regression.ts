import { strict as assert } from 'node:assert'
import { readFileSync } from 'node:fs'

import {
  clearGatewayClientIpErrorCircuitForTest,
  getGatewayClientIpSecuritySnapshotForTest,
  inspectClientIpErrorCircuit,
  inspectGatewayPreAuthCircuit,
  recordClientIpErrorCircuitSample,
  recordClientIpErrorCircuitSuccess,
  recordGatewayPreAuthFailure
} from '../../modules/gateway/runtime/client-ip-error-circuit.service.js'

try {
  clearGatewayClientIpErrorCircuitForTest()
  const circuitSource = readFileSync(new URL('../../modules/gateway/runtime/client-ip-error-circuit.service.ts', import.meta.url), 'utf8')
  const preAuthSource = readFileSync(new URL('../../modules/gateway/request/pre-auth.ts', import.meta.url), 'utf8')
  const preflightSource = readFileSync(new URL('../../modules/gateway/request/preflight.ts', import.meta.url), 'utf8')
  const routesSource = readFileSync(new URL('../../modules/gateway/routes.ts', import.meta.url), 'utf8')
  const finalizationSource = readFileSync(new URL('../../modules/gateway/response/finalization.ts', import.meta.url), 'utf8')
  assert.equal(circuitSource.includes('[...entry.samples'), false, '认证失败样本维护不能展开复制整个 samples 数组')
  assert.equal(circuitSource.includes('[...existing[1]'), false, '认证后签名样本维护不能展开复制整个 samples 数组')
  assert.equal(circuitSource.includes('sample) => now - sample'), false, '认证失败样本维护不能在热路径 filter 扫描样本数组')
  assert.match(circuitSource, /createRuntimeStateStore\('gateway-client-ip-error-circuit'\)/, 'Redis runtime state 下 IP 错误熔断应写入共享运行态')
  assert.match(circuitSource, /runtimeConfig\.runtimeStateDriver === 'redis'/, 'IP 错误熔断应按 runtime state driver 选择 Redis 共享状态')
  assert.doesNotMatch(circuitSource, /withRuntimeEntryLock|runtimeEntryLock|acquireLock|releaseLock|运行态锁等待超时/, 'Redis IP 错误熔断采样不能在请求路径引入分布式锁等待')
  assert.match(circuitSource, /async function recordPreAuthEntryAsync[\s\S]*getRuntimeEntry[\s\S]*setRuntimeEntry/, 'Redis 认证前错误熔断应直接读写共享状态')
  assert.match(circuitSource, /recordClientIpErrorCircuitSampleAsync[\s\S]*getRuntimeEntry[\s\S]*setRuntimeEntry/, 'Redis 认证后错误熔断应直接读写共享状态')
  assert.match(preAuthSource, /await inspectGatewayPreAuthCircuitAsync/, '认证前熔断检查必须等待 Redis 共享状态')
  assert.match(preAuthSource, /await recordGatewayPreAuthFailureAsync/, '认证失败采样必须等待 Redis 共享状态')
  assert.match(preflightSource, /await inspectClientIpErrorCircuitAsync/, '认证后熔断检查必须等待 Redis 共享状态')
  assert.match(preflightSource, /await recordClientIpErrorCircuitSuccessAsync/, 'models 成功响应恢复必须删除 Redis 共享熔断状态')
  assert.match(routesSource, /await recordKnownClientIpRequestError/, '网关已知请求错误采样必须等待 Redis 共享状态')
  assert.match(routesSource, /await recordClientIpErrorCircuitSampleAsync/, '网关已知请求错误应使用 Redis-aware 采样 API')
  assert.match(finalizationSource, /await recordClientIpErrorCircuitSuccessAsync/, '成功响应最终化必须等待 Redis 共享熔断状态清理')

  assert.equal(inspectGatewayPreAuthCircuit({
    clientIp: undefined,
    authorization: undefined
  }).blocked, false, '缺少客户端 IP 时不应启用认证前熔断')

  const missingIp = '198.51.100.50'
  let missingDecision = { blocked: false }
  for (let index = 0; index < 40; index += 1) {
    missingDecision = recordGatewayPreAuthFailure({
      clientIp: missingIp,
      reason: 'missing_bearer_token'
    })
  }
  assert.equal(missingDecision.blocked, true, '同一来源高频缺少 Bearer 后应短期熔断')
  assert.equal(inspectGatewayPreAuthCircuit({
    clientIp: missingIp
  }).blocked, true, '缺少 Bearer 的同一来源后续请求应被短路')

  clearGatewayClientIpErrorCircuitForTest()

  const tokenIp = '198.51.100.51'
  const invalidAuthorization = 'Bearer sk-invalid-repeated'
  let invalidDecision = { blocked: false }
  for (let index = 0; index < 8; index += 1) {
    invalidDecision = recordGatewayPreAuthFailure({
      clientIp: tokenIp,
      authorization: invalidAuthorization,
      reason: 'invalid_api_key'
    })
  }
  assert.equal(invalidDecision.blocked, true, '同一无效 token 高频失败后应短期熔断')
  assert.equal(inspectGatewayPreAuthCircuit({
    clientIp: tokenIp,
    authorization: invalidAuthorization
  }).blocked, true, '同一无效 token 后续请求应被短路')
  assert.equal(inspectGatewayPreAuthCircuit({
    clientIp: tokenIp,
    authorization: 'Bearer sk-valid-user-token'
  }).blocked, false, '同出口 IP 的其他 Bearer token 不应被重复无效 token 熔断误伤')
  const tokenSnapshotText = JSON.stringify(getGatewayClientIpSecuritySnapshotForTest())
  assert.equal(tokenSnapshotText.includes('sk-invalid-repeated'), false, '认证前运行态 snapshot 不应保留原始无效 token')
  assert.equal(tokenSnapshotText.includes('sk-valid-user-token'), false, '认证前运行态 snapshot 不应保留其他 Bearer 原文')

  clearGatewayClientIpErrorCircuitForTest()

  const sprayIp = '198.51.100.52'
  let sprayDecision = { blocked: false }
  for (let index = 0; index < 120; index += 1) {
    sprayDecision = recordGatewayPreAuthFailure({
      clientIp: sprayIp,
      authorization: `Bearer sk-random-invalid-${index}`,
      reason: 'invalid_api_key'
    })
  }
  assert.equal(sprayDecision.blocked, true, '随机无效 token 高频探测应在验证失败后触发软熔断')
  assert.equal(inspectGatewayPreAuthCircuit({
    clientIp: sprayIp,
    authorization: 'Bearer sk-valid-after-spray'
  }).blocked, false, '随机无效 token 探测不应在认证前挡住同 IP 的有效 Bearer 请求')
  for (let index = 120; index < 5000; index += 1) {
    sprayDecision = recordGatewayPreAuthFailure({
      clientIp: sprayIp,
      authorization: `Bearer sk-random-invalid-${index}`,
      reason: 'invalid_api_key'
    })
    assert.equal(sprayDecision.blocked, true, 'spray 熔断打开后新的随机无效 token 应直接短路')
  }
  const spraySnapshot = getGatewayClientIpSecuritySnapshotForTest()
  assert(spraySnapshot.preAuth.length <= 121, 'spray 熔断打开后不应继续为每个随机无效 token 创建认证前缓存项')
  assert(
    spraySnapshot.preAuth.every((entry) => entry.failureCount <= 120),
    '认证前样本窗口必须按阈值固定上限保存'
  )

  clearGatewayClientIpErrorCircuitForTest()

  const scope = {
    systemAccountId: 'sys_security',
    apiKeyId: 'key_security',
    groupId: 'grp_security',
    clientIp: '203.0.113.60',
    endpoint: 'POST /v1/responses'
  }
  let postAuthDecision = { blocked: false }
  for (let index = 0; index < 4; index += 1) {
    postAuthDecision = recordClientIpErrorCircuitSample({
      ...scope,
      reason: 'invalid_json',
      signature: 'invalid_json'
    })
    assert.equal(postAuthDecision.blocked, false, '未达到本地高置信错误阈值前不应熔断认证后来源')
  }
  postAuthDecision = recordClientIpErrorCircuitSample({
    ...scope,
    reason: 'invalid_json',
    signature: 'invalid_json'
  })
  assert.equal(postAuthDecision.blocked, true, '同一认证后来源高频本地请求校验错误后应熔断')
  assert.equal(inspectClientIpErrorCircuit(scope).blocked, true, '认证后来源处于熔断窗口时应被识别')
  assert.equal(inspectClientIpErrorCircuit({
    ...scope,
    clientIp: '203.0.113.61'
  }).blocked, false, '不同客户端 IP 不应继承认证后错误熔断')
  assert.equal(inspectClientIpErrorCircuit({
    ...scope,
    apiKeyId: 'key_security_other'
  }).blocked, false, '不同 API Key 不应继承认证后错误熔断')
  assert.equal(inspectClientIpErrorCircuit({
    ...scope,
    groupId: 'grp_security_other'
  }).blocked, true, '同一 API Key 下不同分组应共享认证后错误熔断')
  assert.equal(inspectClientIpErrorCircuit({
    ...scope,
    systemAccountId: 'sys_security_other'
  }).blocked, false, '不同系统账户不应继承认证后错误熔断')

  const cleared = recordClientIpErrorCircuitSuccess({
    ...scope,
    groupId: 'grp_security_other'
  })
  assert.equal(cleared, true, '成功请求应清理当前认证后来源错误态')
  assert.equal(inspectClientIpErrorCircuit(scope).blocked, false, '成功恢复后当前来源不应继续熔断')

  const snapshot = getGatewayClientIpSecuritySnapshotForTest()
  assert.equal(snapshot.clientIpErrors.length, 0, '成功恢复后认证后错误运行态应清空')
  const gatewayRoutesSource = readFileSync(new URL('../../modules/gateway/routes.ts', import.meta.url), 'utf8')
  assert.equal(gatewayRoutesSource.includes('request_failure_signature'), false, '未知上游账号池失败不应作为来源错误熔断采样原因')

  console.log('IP 级错误熔断回归通过：认证前探测保护、认证后错误风暴熔断、来源隔离和成功恢复均符合预期')
} finally {
  clearGatewayClientIpErrorCircuitForTest()
}
