import { strict as assert } from 'node:assert'

import {
  clearGatewayClientIpErrorCircuitForTest,
  getGatewayClientIpSecuritySnapshotForTest,
  inspectClientIpErrorCircuit,
  inspectGatewayPreAuthCircuit,
  recordClientIpErrorCircuitSample,
  recordClientIpErrorCircuitSuccess,
  recordGatewayPreAuthFailure
} from '../../modules/gateway/openai-gateway-client-ip-error-circuit.service.js'

try {
  clearGatewayClientIpErrorCircuitForTest()

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
      reason: 'upstream_error_feature',
      signature: 'request_validation_signature'
    })
    assert.equal(postAuthDecision.blocked, false, '未达到同签名阈值前不应熔断认证后来源')
  }
  postAuthDecision = recordClientIpErrorCircuitSample({
    ...scope,
    reason: 'upstream_error_feature',
    signature: 'request_validation_signature'
  })
  assert.equal(postAuthDecision.blocked, true, '同一认证后来源高频同签名请求级错误后应熔断')
  assert.equal(inspectClientIpErrorCircuit(scope).blocked, true, '认证后来源处于熔断窗口时应被识别')
  assert.equal(inspectClientIpErrorCircuit({
    ...scope,
    clientIp: '203.0.113.61'
  }).blocked, false, '不同客户端 IP 不应继承认证后错误熔断')
  assert.equal(inspectClientIpErrorCircuit({
    ...scope,
    apiKeyId: 'key_security_other'
  }).blocked, false, '不同 API Key 不应继承认证后错误熔断')

  const cleared = recordClientIpErrorCircuitSuccess(scope)
  assert.equal(cleared, true, '成功请求应清理当前认证后来源错误态')
  assert.equal(inspectClientIpErrorCircuit(scope).blocked, false, '成功恢复后当前来源不应继续熔断')

  const snapshot = getGatewayClientIpSecuritySnapshotForTest()
  assert.equal(snapshot.clientIpErrors.length, 0, '成功恢复后认证后错误运行态应清空')

  console.log('IP 级错误熔断回归通过：认证前探测保护、认证后错误风暴熔断、来源隔离和成功恢复均符合预期')
} finally {
  clearGatewayClientIpErrorCircuitForTest()
}
