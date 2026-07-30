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
    name: '完整响应不解释状态码和错误体',
    input: { phase: 'upstream_response' },
    expected: observation('opaque_upstream_response', 'opaque_upstream_response_failure')
  },
  {
    name: '传输失败只记录可观察的传输事实',
    input: { phase: 'upstream_request' },
    expected: observation('transport', 'upstream_transport_failure')
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
  classificationReason: string
): GatewayUpstreamFailureClassification {
  return {
    failureClass,
    classificationReason
  }
}
