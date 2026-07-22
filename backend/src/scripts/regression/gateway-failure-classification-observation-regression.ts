import assert from 'node:assert/strict'

import {
  classifyGatewayUpstreamFailure,
  type GatewayUpstreamFailureClassification,
  type GatewayUpstreamFailureClassificationInput
} from '../../modules/gateway/response/upstream-failure-classifier.js'

const cases: Array<{
  name: string
  input: GatewayUpstreamFailureClassificationInput
  expected: GatewayUpstreamFailureClassification
}> = [
  {
    name: '客户端生命周期中断不归因给上游',
    input: { phase: 'client_lifecycle' },
    expected: observation('client_lifecycle', 'client_lifecycle_failure')
  },
  {
    name: '明确的无效请求不建议避让任何上游资源',
    input: { phase: 'upstream_response', statusCode: 400, errorCode: 'invalid_request_error', errorType: 'invalid_request_error' },
    expected: observation('request_semantic', 'explicit_request_error')
  },
  {
    name: '上下文超限属于请求语义错误',
    input: { phase: 'upstream_response', statusCode: 400, errorCode: 'context_length_exceeded' },
    expected: observation('request_semantic', 'explicit_request_error')
  },
  {
    name: '多 Key 账户的凭据错误只建议避让当前 Key',
    input: { phase: 'upstream_response', statusCode: 401, errorCode: 'invalid_api_key', hasAlternativeApiKeys: true },
    expected: observation('credential', 'credential_error_with_alternative_key', { wouldAvoidApiKey: true })
  },
  {
    name: '单 Key 账户的凭据错误建议避让账户并探活',
    input: { phase: 'upstream_response', statusCode: 401, errorCode: 'invalid_api_key' },
    expected: observation('credential', 'credential_error_without_alternative_key', {
      wouldAvoidAccount: true
    })
  },
  {
    name: '限流只建议账户级短暂避让',
    input: { phase: 'upstream_response', statusCode: 429, errorCode: 'rate_limit_exceeded' },
    expected: observation('rate_limit', 'upstream_rate_limit', {
      wouldAvoidAccount: true
    })
  },
  {
    name: '上游 5xx 建议账户和上游桶避让',
    input: { phase: 'upstream_response', statusCode: 503 },
    expected: observation('upstream_service', 'upstream_server_error', {
      wouldAvoidAccount: true,
      wouldAvoidUpstreamBucket: true
    })
  },
  {
    name: '传输失败建议账户和上游桶避让',
    input: { phase: 'upstream_request' },
    expected: observation('transport', 'upstream_transport_failure', {
      wouldAvoidAccount: true,
      wouldAvoidUpstreamBucket: true
    })
  },
  {
    name: '未知非成功响应保持未知且不推断副作用',
    input: { phase: 'upstream_response', statusCode: 418, errorCode: 'unexpected_teapot' },
    expected: observation('unknown', 'unclassified_upstream_response')
  }
]

for (const testCase of cases) {
  assert.deepEqual(
    classifyGatewayUpstreamFailure(testCase.input),
    testCase.expected,
    testCase.name
  )
}

console.log(`gateway failure classification observation regression passed (${cases.length} cases)`)

function observation(
  failureClass: GatewayUpstreamFailureClassification['failureClass'],
  classificationReason: string,
  overrides: Partial<Omit<GatewayUpstreamFailureClassification, 'failureClass' | 'classificationReason'>> = {}
): GatewayUpstreamFailureClassification {
  return {
    failureClass,
    classificationReason,
    wouldAvoidApiKey: false,
    wouldAvoidAccount: false,
    wouldAvoidUpstreamBucket: false,
    ...overrides
  }
}
