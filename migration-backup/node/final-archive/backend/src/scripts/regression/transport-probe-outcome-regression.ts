import assert from 'node:assert/strict'
import {
  transportProbeMeetsFirstByteTarget,
  transportProbeOutcomeFromAccountTestResult
} from '../../modules/accounts/automatic-account-probe-outcome.js'

for (const status of [400, 401, 429, 500, 503]) {
  assert.deepEqual(transportProbeOutcomeFromAccountTestResult({
    success: false,
    statusCode: status,
    message: `HTTP ${status}`
  }, {
    upstreamAttempt: {
      upstreamUrl: 'https://api.openai.com/v1/responses',
      status
    }
  }), { kind: 'framing_complete', statusCode: status })
}

assert.deepEqual(transportProbeOutcomeFromAccountTestResult({
  success: false,
  errorCode: 'upstream_said_timeout',
  message: 'socket hang up after quota rejection'
}, {
  upstreamAttempt: {
    upstreamUrl: 'https://api.openai.com/v1/responses',
    status: 503
  }
}), { kind: 'framing_complete', statusCode: 503 }, '完整 framing 不得解析上游错误码或正文文案')

assert.deepEqual(transportProbeOutcomeFromAccountTestResult({
  success: false,
  message: '账户测试超时'
}, {
  upstreamAttempt: { upstreamUrl: 'https://api.openai.com/v1/responses' },
  timeout: true
}), { kind: 'transport_incomplete', failureKind: 'timeout' })

assert.deepEqual(transportProbeOutcomeFromAccountTestResult({
  success: false,
  errorCode: 'ECONNREFUSED',
  message: 'connect ECONNREFUSED'
}, {
  upstreamAttempt: {
    upstreamUrl: 'https://api.openai.com/v1/responses',
    message: 'connect ECONNREFUSED'
  }
}), { kind: 'transport_incomplete', failureKind: 'connection' })

assert.deepEqual(transportProbeOutcomeFromAccountTestResult({
  success: false,
  errorCode: 'upstream_body_interrupted',
  message: 'upstream body interrupted'
}, {
  upstreamAttempt: {
    upstreamUrl: 'https://api.openai.com/v1/responses',
    status: 200,
    transportFailureKind: 'read_incomplete'
  }
}), { kind: 'transport_incomplete', failureKind: 'read', statusCode: 200 })

for (const errorCode of ['invalid_protocol_success_response', 'upstream_body_interrupted', 'upstream_timeout']) {
  assert.deepEqual(transportProbeOutcomeFromAccountTestResult({
    success: false,
    statusCode: 200,
    errorCode,
    message: 'provider-controlled diagnostic'
  }, {
    upstreamAttempt: {
      upstreamUrl: 'https://api.openai.com/v1/responses',
      status: 200
    }
  }), { kind: 'framing_complete', statusCode: 200 }, `${errorCode} 不得伪造 transport incomplete`)
}

assert.deepEqual(transportProbeOutcomeFromAccountTestResult({
  success: false,
  message: '账户测试已取消'
}, { canceled: true }), { kind: 'unknown', failureKind: 'canceled' })

assert.deepEqual(transportProbeOutcomeFromAccountTestResult({
  success: false,
  message: '账户未绑定可用分组'
}), { kind: 'unknown', failureKind: 'task_failure' })

assert.equal(transportProbeMeetsFirstByteTarget({ success: false, firstTokenMs: 800 }, {
  kind: 'framing_complete', statusCode: 503
}, 1_000), false)
assert.equal(transportProbeMeetsFirstByteTarget({ success: true, firstTokenMs: 800 }, {
  kind: 'transport_incomplete', failureKind: 'read', statusCode: 200
}, 1_000), false)

console.log('TRANSPORT_PROBE_OUTCOME_OK')
