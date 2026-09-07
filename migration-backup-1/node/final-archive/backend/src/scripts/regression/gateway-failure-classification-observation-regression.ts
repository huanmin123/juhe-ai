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
    name: '完整响应只输出有限的 5xx 诊断类，不解释账户语义',
    input: { phase: 'upstream_response', statusCode: 503 },
    expected: observation('opaque_upstream_response', 'upstream_5xx', 'opaque_upstream_response_failure')
  },
  {
    name: '已解析的额度错误码映射为有限 quota 指标标签',
    input: { phase: 'upstream_response', statusCode: 402, errorCode: 'insufficient_user_quota' },
    expected: observation('opaque_upstream_response', 'quota', 'opaque_upstream_response_failure')
  },
  {
    name: '已解析的全局额度错误码映射为有限 quota 指标标签',
    input: { phase: 'upstream_response', statusCode: 403, errorCode: 'DEFAULT_GROUP_GLOBAL_QUOTA_EXHAUSTED' },
    expected: observation('opaque_upstream_response', 'quota', 'opaque_upstream_response_failure')
  },
  {
    name: '429 状态映射为有限 rate_limit 指标标签',
    input: { phase: 'upstream_response', statusCode: 429 },
    expected: observation('opaque_upstream_response', 'rate_limit', 'opaque_upstream_response_failure')
  },
  {
    name: '传输失败只记录可观察的传输事实',
    input: { phase: 'upstream_request' },
    expected: observation('transport', 'transport', 'upstream_transport_failure')
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
  metricReasonClass: GatewayUpstreamFailureClassification['metricReasonClass'],
  classificationReason: string
): GatewayUpstreamFailureClassification {
  return {
    failureClass,
    metricReasonClass,
    classificationReason
  }
}
