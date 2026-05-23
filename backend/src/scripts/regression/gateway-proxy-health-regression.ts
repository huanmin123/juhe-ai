import { strict as assert } from 'node:assert'

import { logger } from '../../shared/logger.js'
import type { OpenAIAccountSecret } from '../../storage/repositories.js'
import {
  clearGatewayProxyHealthForTest,
  orderOpenAIAccountsByGatewayProxyHealth,
  recordGatewayProxyFailure,
  recordGatewayProxySuccess
} from '../../modules/gateway/openai-gateway-proxy-health.service.js'

logger.level = 'silent'
clearGatewayProxyHealthForTest()

const first = account('account-proxy-1', 'proxy-shared')
const second = account('account-proxy-2', 'proxy-shared')
const third = account('account-proxy-3', 'proxy-other')

let order = orderOpenAIAccountsByGatewayProxyHealth([first, second, third])
assert.equal(order.applied, false, '初始状态不应应用代理避让')
assert.deepEqual(order.accounts.map((item) => item.id), [first.id, second.id, third.id], '初始排序应保持原样')

let decision = recordGatewayProxyFailure(first, 'ECONNRESET')
assert.equal(decision.recorded, true, '绑定代理的账号失败应记录代理桶')
assert.equal(decision.suspected, false, '单账号代理失败不能直接判定代理问题')
order = orderOpenAIAccountsByGatewayProxyHealth([first, second, third])
assert.equal(order.applied, false, '单账号代理失败不应避让整个代理')

decision = recordGatewayProxyFailure(second, 'ECONNRESET')
assert.equal(decision.suspected, true, '同代理两个不同账号短窗失败应判定代理可疑')
order = orderOpenAIAccountsByGatewayProxyHealth([first, second, third])
assert.equal(order.applied, true, '存在其他代理可用账号时应应用代理避让')
assert.deepEqual(order.accounts.map((item) => item.id), [third.id, first.id, second.id], '可疑代理绑定账号应被排到后面')
assert.deepEqual(order.avoidedAccountIds.sort(), [first.id, second.id].sort(), '应避让共享可疑代理的账号')

assert.equal(recordGatewayProxySuccess(first), true, '任一绑定账号成功应清理代理运行态失败桶')
order = orderOpenAIAccountsByGatewayProxyHealth([first, second, third])
assert.equal(order.applied, false, '代理成功后不应继续避让')

recordGatewayProxyFailure(first, 'ECONNRESET')
recordGatewayProxyFailure(second, 'ECONNRESET')
order = orderOpenAIAccountsByGatewayProxyHealth([first, second])
assert.equal(order.applied, false, '所有候选账号都被代理桶命中时不应清空候选')
assert.equal(order.bypassedAllAvoided, true, '所有候选都被避让时应旁路以保证可用性')
assert.deepEqual(order.accounts.map((item) => item.id), [first.id, second.id], '旁路时应保持原候选顺序')

clearGatewayProxyHealthForTest()

console.log('代理运行态失败桶回归通过：同代理多账号失败才避让，成功恢复，全部命中时旁路保证可用性')

function account(id: string, proxyProfileId: string): OpenAIAccountSecret {
  return {
    id,
    systemAccountId: 'sys_admin',
    name: id,
    providerCode: 'openai',
    type: 'api_key',
    status: 'active',
    credentials: {},
    apiKey: 'sk-test',
    baseUrl: 'https://example.invalid/v1',
    concurrencyLimit: 20,
    currentConcurrency: 0,
    priority: 0,
    superPriorityEnabled: false,
    fallbackEnabled: false,
    schedulable: true,
    passthroughEnabled: false,
    proxyProfileId,
    accountOwnerSystemAccountId: 'sys_admin',
    groupOwnerSystemAccountId: 'sys_admin',
    accountAccessType: 'owner',
    groupAccessType: 'owner',
    streamFailureCount: 0
  } as OpenAIAccountSecret
}
