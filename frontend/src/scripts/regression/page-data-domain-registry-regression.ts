import assert from 'node:assert/strict'

import { pageDataDomainRegistry, pageDataSpecsForRoute } from '../../shared/pageDataDomainRegistry'

const businessRoutes = [
  '/my-chat', '/my-stats', '/my-accounts', '/my-groups', '/my-api-keys', '/my-route-strategies',
  '/my-model-checks', '/my-models', '/my-authorizations', '/my-authorization-team-usage',
  '/my-authorization-user-usage', '/my-teams', '/my-usage-stats', '/my-ai-performance',
  '/my-usage-records', '/my-operation-logs', '/stats', '/providers', '/proxies', '/accounts',
  '/groups', '/authorizations', '/authorization-team-usage', '/authorization-user-usage',
  '/authorization-teams', '/api-keys', '/model-checks', '/usage-stats', '/ai-performance',
  '/usage-records', '/operation-logs', '/public-api-logs', '/audit-logs', '/runtime-logs',
  '/table-monitor', '/system-metrics-stats', '/ip-stats', '/response-inspection-policies',
  '/route-strategies', '/external-integration-sources', '/announcements', '/system-accounts', '/settings'
]

for (const route of businessRoutes) {
  assert.ok(pageDataSpecsForRoute(route).length > 0, `业务路由 ${route} 必须登记缓存或明确 no-store/specialized`)
}
assert.equal(new Set(pageDataDomainRegistry.map((entry) => entry.domain)).size, pageDataDomainRegistry.length, '数据域名称必须唯一')
for (const entry of pageDataDomainRegistry) {
  assert.ok(entry.primaryGets.length > 0, `${entry.domain} 必须登记主 GET`)
  assert.ok(entry.invalidators.length > 0, `${entry.domain} 必须登记写入失效来源`)
  if (entry.implementation === 'active') assert.notEqual(entry.persistence, 'no-store', `${entry.domain} active 域必须有真实缓存策略`)
  if (entry.sensitive && entry.persistence === 'durable') assert.equal(entry.detailPolicy, 'no-store', `${entry.domain} 敏感 durable 摘要必须明确详情 no-store`)
}

assert.equal(pageDataDomainRegistry.find((entry) => entry.domain === 'chat.specialized')?.implementation, 'specialized')
assert.equal(pageDataDomainRegistry.find((entry) => entry.domain === 'logs.audit')?.implementation, 'no-store')
assert.equal(pageDataDomainRegistry.find((entry) => entry.domain === 'accounts.static')?.implementation, 'active')
console.log('页面数据域 registry 回归通过：全部业务路由均有 active/planned/specialized/no-store 明确结论')
