import assert from 'node:assert/strict'

import {
  beginPageDataMutation,
  pageDataDomainsForMutation
} from '../../shared/pageDataMutationInvalidation.js'
import { currentPageDataWriteEpoch } from '../../shared/pageDataGenerationFences.js'

assert.deepEqual(pageDataDomainsForMutation('post', '/providers/gpt/models'), ['accounts.options', 'providers.catalog'])
assert.deepEqual(pageDataDomainsForMutation('patch', '/providers/gpt/models/model-a'), ['accounts.options', 'providers.catalog'])
assert.deepEqual(pageDataDomainsForMutation('put', '/providers/gpt/default-health-check-model'), ['accounts.options', 'providers.catalog'])
const statsDomains = ['stats.overview', 'stats.accountUsage', 'stats.aiPerformance']
const accountMutationDomains = ['accounts.static', 'accounts.options', 'groups.static', ...statsDomains]
assert.deepEqual(pageDataDomainsForMutation('post', '/accounts'), accountMutationDomains)
assert.deepEqual(pageDataDomainsForMutation('patch', '/accounts/account-a'), accountMutationDomains)
assert.deepEqual(pageDataDomainsForMutation('post', '/accounts/import/confirm'), accountMutationDomains, '账户导入提交必须推进静态列表与选项域')
assert.deepEqual(pageDataDomainsForMutation('post', '/accounts/batch-update'), accountMutationDomains, '账户批量编辑必须推进静态列表与选项域')
assert.deepEqual(pageDataDomainsForMutation('post', '/openai-oauth/create-from-code'), accountMutationDomains, 'OAuth 创建账户必须推进账户列表与选项 epoch')
assert.deepEqual(pageDataDomainsForMutation('post', '/my-openai-oauth/create-from-refresh-token'), accountMutationDomains, '个人 OAuth 创建账户必须推进账户列表与选项 epoch')
assert.deepEqual(pageDataDomainsForMutation('post', '/openai-oauth/accounts/account-a/refresh-token'), accountMutationDomains, 'OAuth 刷新凭据必须推进账户列表与选项 epoch')
assert.deepEqual(pageDataDomainsForMutation('post', '/my-openai-oauth/accounts/account-a/reauthorize-from-code'), accountMutationDomains, 'OAuth 重新授权必须推进账户列表与选项 epoch')
assert.deepEqual(pageDataDomainsForMutation('post', '/openai-oauth/auth-url'), [], '仅生成 OAuth 授权地址不应推进账户 epoch')
assert.deepEqual(pageDataDomainsForMutation('delete', '/groups/group-a'), ['groups.static', 'routeStrategies.options', ...statsDomains])
assert.deepEqual(pageDataDomainsForMutation('patch', '/route-strategies/route-a'), ['routeStrategies.options'])
assert.deepEqual(pageDataDomainsForMutation('patch', '/system-accounts/user-a'), ['accounts.static', 'accounts.options', 'systemAccounts.options', 'teams.options', ...statsDomains])
assert.deepEqual(pageDataDomainsForMutation('post', '/system-teams/team-a/members'), ['accounts.static', 'accounts.options', 'groups.static', 'systemAccounts.options', 'teams.options', ...statsDomains])
assert.deepEqual(pageDataDomainsForMutation('patch', '/authorizations/auth-a'), ['accounts.static', 'accounts.options', 'groups.static', 'systemAccounts.options', 'teams.options', ...statsDomains])

assert.deepEqual(pageDataDomainsForMutation('get', '/accounts'), [], 'GET 不应清理响应缓存')
assert.deepEqual(pageDataDomainsForMutation('post', '/accounts/account-a/test'), [], '人工测试命令不应清理静态候选缓存')
assert.deepEqual(pageDataDomainsForMutation('post', '/accounts/test-draft'), [], '草稿测试不应清理静态候选缓存')
assert.deepEqual(pageDataDomainsForMutation('post', '/accounts/test-sessions/session-a/heartbeat'), [], '测试会话心跳不应清理静态候选缓存')
assert.deepEqual(pageDataDomainsForMutation('delete', '/accounts/test-tasks/task-a'), [], '测试任务取消不应清理静态候选缓存')
assert.deepEqual(pageDataDomainsForMutation('post', '/accounts/import/preview'), [], '导入预览不应清理静态候选缓存')
assert.deepEqual(pageDataDomainsForMutation('post', '/accounts/batch-edit-context'), [], '批量编辑上下文查询不应清理静态候选缓存')
assert.deepEqual(pageDataDomainsForMutation('post', '/accounts/export'), [], '导出命令不应被宽泛账户前缀误判为 mutation')
assert.deepEqual(pageDataDomainsForMutation('post', '/accounts/balance/test-draft'), [], '余额草稿测试不应推进账户缓存 epoch')
assert.deepEqual(pageDataDomainsForMutation('post', '/accounts/account-a/force-activate'), accountMutationDomains, '人工恢复改变运行态，必须失效账户候选缓存')
assert.deepEqual(pageDataDomainsForMutation('post', '/accounts/account-a/traffic-migration'), accountMutationDomains, '流量迁移改变运行态，必须失效账户候选缓存')
assert.deepEqual(pageDataDomainsForMutation('post', '/data-changes/confirm'), [], '轻量确认不应反向清理缓存')
assert.deepEqual(pageDataDomainsForMutation('post', '/operation-logs/export'), [], '日志命令不应纳入持久响应缓存失效')

const initialStaticEpoch = currentPageDataWriteEpoch('accounts.static')
const initialOptionsEpoch = currentPageDataWriteEpoch('accounts.options')
const initialProviderEpoch = currentPageDataWriteEpoch('providers.catalog')
assert.deepEqual(beginPageDataMutation('post', '/__aisys__/api/accounts/account-a'), accountMutationDomains)
assert.equal(currentPageDataWriteEpoch('accounts.static'), initialStaticEpoch + 1, 'mutation 发出时必须推进 accounts.static writeEpoch')
assert.equal(currentPageDataWriteEpoch('accounts.options'), initialOptionsEpoch + 1, '影响选项的 mutation 必须同步推进 accounts.options writeEpoch')
assert.equal(currentPageDataWriteEpoch('providers.catalog'), initialProviderEpoch, '未受影响 domain 不得推进 writeEpoch')

assert.deepEqual(beginPageDataMutation('post', '/operation-logs/export'), [])
assert.equal(currentPageDataWriteEpoch('accounts.static'), initialStaticEpoch + 1, '未映射 mutation 不得推进任意 domain writeEpoch')

const { http } = await import('../../api/http.js')
const requestStaticEpoch = currentPageDataWriteEpoch('accounts.static')
await http.request({
  method: 'post',
  url: '/accounts/account-b',
  adapter: async (config) => {
    assert.equal(
      currentPageDataWriteEpoch('accounts.static'),
      requestStaticEpoch + 1,
      'HTTP adapter 接管请求前必须已推进 mutation writeEpoch'
    )
    return { data: { data: undefined }, status: 200, statusText: 'OK', headers: {}, config }
  }
})

console.log('页面数据写后失效映射回归通过：有限 mutation map 在请求发出时推进 writeEpoch，命令/GET 保持不变')
