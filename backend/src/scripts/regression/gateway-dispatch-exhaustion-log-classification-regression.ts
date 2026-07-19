import assert from 'node:assert/strict'

import { classifyGatewayDispatchExhaustion } from '../../modules/gateway/response/dispatch-exhaustion-classifier.js'
import type { UpstreamAttempt } from '../../modules/gateway/upstream/attempt.js'

const attempt = (overrides: Partial<UpstreamAttempt>): UpstreamAttempt => ({
  accountId: 'account-a',
  accountName: '账户 A',
  upstreamUrl: 'https://upstream.example/v1/responses',
  message: '上游请求失败',
  ...overrides
})

assert.deepEqual(
  classifyGatewayDispatchExhaustion(attempt({ upstreamUrl: 'account:api_key_pool_unavailable' })),
  { failureReason: 'api_key_pool_unavailable' },
  'API Key 池耗尽必须记录独立原因，不能归为未知异常'
)
assert.deepEqual(
  classifyGatewayDispatchExhaustion(attempt({ upstreamUrl: 'account:locally_suppressed' })),
  { failureReason: 'all_accounts_locally_suppressed' },
  '本地短期屏蔽耗尽必须记录独立原因'
)
assert.deepEqual(
  classifyGatewayDispatchExhaustion(attempt({ upstreamUrl: 'concurrency:limit' })),
  { failureReason: 'account_concurrency_exhausted' },
  '并发槽耗尽必须记录独立原因'
)
assert.deepEqual(
  classifyGatewayDispatchExhaustion(attempt({ status: 400 })),
  { failureReason: 'upstream_http_error', upstreamStatus: 400 },
  '明确的上游 HTTP 响应属于受控上游失败'
)
assert.deepEqual(
  classifyGatewayDispatchExhaustion(attempt({ status: undefined })),
  { failureReason: 'upstream_transport_error' },
  '真实上游没有状态码时应区分为传输失败'
)
assert.deepEqual(
  classifyGatewayDispatchExhaustion(undefined),
  { failureReason: 'no_available_account' },
  '没有任何尝试时应明确记录无可用账户'
)

console.log('网关调度耗尽日志分类回归通过')
