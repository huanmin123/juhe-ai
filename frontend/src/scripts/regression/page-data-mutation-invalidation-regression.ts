import assert from 'node:assert/strict'

import { pageDataDomainsForMutation } from '../../shared/pageDataMutationInvalidation.js'

assert.deepEqual(pageDataDomainsForMutation('post', '/providers/gpt/models'), ['accounts.options', 'providers.catalog'])
assert.deepEqual(pageDataDomainsForMutation('patch', '/providers/gpt/models/model-a'), ['accounts.options', 'providers.catalog'])
assert.deepEqual(pageDataDomainsForMutation('put', '/providers/gpt/default-health-check-model'), ['accounts.options', 'providers.catalog'])
const statsDomains = ['stats.overview', 'stats.accountUsage', 'stats.aiPerformance']
assert.deepEqual(pageDataDomainsForMutation('post', '/accounts'), ['accounts.options', 'groups.static', ...statsDomains])
assert.deepEqual(pageDataDomainsForMutation('patch', '/accounts/account-a'), ['accounts.options', 'groups.static', ...statsDomains])
assert.deepEqual(pageDataDomainsForMutation('delete', '/groups/group-a'), ['groups.static', 'routeStrategies.options', ...statsDomains])
assert.deepEqual(pageDataDomainsForMutation('patch', '/route-strategies/route-a'), ['routeStrategies.options'])
assert.deepEqual(pageDataDomainsForMutation('patch', '/system-accounts/user-a'), ['accounts.options', 'systemAccounts.options', 'teams.options', ...statsDomains])
assert.deepEqual(pageDataDomainsForMutation('post', '/system-teams/team-a/members'), ['accounts.options', 'groups.static', 'systemAccounts.options', 'teams.options', ...statsDomains])
assert.deepEqual(pageDataDomainsForMutation('patch', '/authorizations/auth-a'), ['accounts.options', 'groups.static', 'systemAccounts.options', 'teams.options', ...statsDomains])

assert.deepEqual(pageDataDomainsForMutation('get', '/accounts'), [], 'GET 不应清理响应缓存')
assert.deepEqual(pageDataDomainsForMutation('post', '/accounts/account-a/test'), [], '人工测试命令不应清理静态候选缓存')
assert.deepEqual(pageDataDomainsForMutation('post', '/accounts/test-draft'), [], '草稿测试不应清理静态候选缓存')
assert.deepEqual(pageDataDomainsForMutation('post', '/accounts/test-sessions/session-a/heartbeat'), [], '测试会话心跳不应清理静态候选缓存')
assert.deepEqual(pageDataDomainsForMutation('delete', '/accounts/test-tasks/task-a'), [], '测试任务取消不应清理静态候选缓存')
assert.deepEqual(pageDataDomainsForMutation('post', '/accounts/import/preview'), [], '导入预览不应清理静态候选缓存')
assert.deepEqual(pageDataDomainsForMutation('post', '/accounts/batch-edit-context'), [], '批量编辑上下文查询不应清理静态候选缓存')
assert.deepEqual(pageDataDomainsForMutation('post', '/accounts/account-a/force-activate'), ['accounts.options', 'groups.static', ...statsDomains], '人工恢复改变运行态，必须失效账户候选缓存')
assert.deepEqual(pageDataDomainsForMutation('post', '/accounts/account-a/traffic-migration'), ['accounts.options', 'groups.static', ...statsDomains], '流量迁移改变运行态，必须失效账户候选缓存')
assert.deepEqual(pageDataDomainsForMutation('post', '/data-changes/confirm'), [], '轻量确认不应反向清理缓存')
assert.deepEqual(pageDataDomainsForMutation('post', '/operation-logs/export'), [], '日志命令不应纳入持久响应缓存失效')

console.log('页面数据写后失效映射回归通过：有限业务写清理对应 domain，命令/GET 保持不变')
