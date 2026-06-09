import { strict as assert } from 'node:assert'

import type { AccountSummary, AccountUsageSummary } from '../../domain/types.js'
import { accountSummaryWithEffectiveAvailability } from '../../domain/account-effective-availability.js'
import { testOpenAIAccount } from '../../modules/accounts/account-test.service.js'

const emptyUsage: AccountUsageSummary = {
  requestCount: 0,
  inputTokens: 0,
  outputTokens: 0,
  cacheReadTokens: 0,
  cacheReadCost: 0,
  totalTokens: 0,
  totalCost: 0
}

const account: AccountSummary = accountSummaryWithEffectiveAvailability({
  id: 'acct_limited_diagnostics',
  providerCode: 'gpt',
  name: '授权账户脱敏回归',
  type: 'api_key',
  credentials: {},
  status: 'active',
  concurrencyLimit: 1,
  currentConcurrency: 0,
  priority: 0,
  superPriorityEnabled: false,
  fallbackEnabled: false,
  clientCompatibility: 'openai_standard',
  schedulable: true,
  todayUsage: emptyUsage,
  usage: emptyUsage,
  accessType: 'authorized',
  bindingSystemAccountId: 'sys_limited_viewer',
  ownerSystemAccountId: 'sys_owner_only_secret'
})

const fullResult = await testOpenAIAccount(account, { diagnostics: 'full' })
assert.equal(fullResult.success, false)
assert(fullResult.traceId, '完整诊断应返回本次账户测试 traceId，便于按日志追踪')
assert.match(fullResult.message, /账户未绑定可用分组/, '完整诊断应保留内部失败原因，便于所有者或管理员排查')
assert.equal(fullResult.responseText, fullResult.message, '完整诊断失败响应可携带原始失败文本')

const limitedResult = await testOpenAIAccount(account, { diagnostics: 'limited' })
assert.equal(limitedResult.success, false)
assert(limitedResult.traceId, 'limited 诊断也应返回本次账户测试 traceId，便于授权用户反馈给所有者排查')
assert.equal(limitedResult.statusCode, undefined, '本用例覆盖无 HTTP 状态码的异常路径')
assert.equal(limitedResult.message, '账户测试未通过；请联系授权人或管理员查看完整诊断')
assert.equal(limitedResult.responseText, limitedResult.message)
assert(!JSON.stringify(limitedResult).includes('账户未绑定可用分组'), '授权用户 limited 诊断不应回吐内部异常文本')
assert.equal(limitedResult.requestUrl, undefined, '授权用户 limited 诊断不应暴露请求 URL')
assert.equal(limitedResult.requestBody, undefined, '授权用户 limited 诊断不应暴露请求体')
assert.equal(limitedResult.modelsUrl, undefined, '授权用户 limited 诊断不应暴露模型 URL')
assert.equal(limitedResult.proxyUrl, undefined, '授权用户 limited 诊断不应暴露代理标记')

console.log('账户测试 limited 诊断脱敏回归通过：无状态码异常不会泄露内部失败文本')
