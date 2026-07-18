export type PageDataCachePriority = 'P0' | 'P1' | 'P2' | 'P3'
export type PageDataCachePersistence = 'durable' | 'session' | 'memory' | 'no-store' | 'specialized'
export type PageDataCacheScope = 'self' | 'admin-target' | 'admin-global' | 'public-global'
export type PageDataDomainImplementation = 'active' | 'planned' | 'specialized' | 'no-store'

export interface PageDataDomainSpec {
  domain: string
  priority: PageDataCachePriority
  routes: string[]
  primaryGets: string[]
  scopes: PageDataCacheScope[]
  persistence: PageDataCachePersistence
  maxStaleMs: number
  sensitive: boolean
  implementation: PageDataDomainImplementation
  invalidators: string[]
  detailPolicy?: PageDataCachePersistence
}

export const pageDataDomainRegistry = [
  spec('accounts.static', 'P0', ['/my-accounts', '/accounts'], ['/my-accounts', '/accounts'], ['self', 'admin-target'], 'durable', 30_000, false, 'active', ['account-write', 'account-import', 'account-authorization']),
  spec('accounts.runtime', 'P0', ['/my-accounts', '/accounts'], ['/my-accounts/status-snapshot', '/accounts/status-snapshot'], ['self', 'admin-target'], 'memory', 30_000, false, 'active', ['account-probe', 'account-cooldown-retest', 'account-policy']),
  spec('usage.records', 'P0', ['/my-usage-records', '/usage-records'], ['/my-usage-records', '/usage-records'], ['self', 'admin-target'], 'durable', 15_000, false, 'active', ['usage-ingest', 'usage-retention'], 'memory'),
  spec('announcements.public', 'P0', ['/my-chat', '/my-stats', '/my-accounts', '/stats', '/accounts'], ['/announcements/public'], ['self', 'public-global'], 'durable', 30_000, false, 'active', ['announcement-publish', 'announcement-unpublish', 'announcement-delete']),
  spec('stats.overview', 'P0', ['/my-stats', '/stats'], ['/my-stats/usage-overview', '/stats/usage-overview'], ['self', 'admin-target', 'admin-global'], 'durable', 60_000, false, 'planned', ['stats-window-commit', 'stats-rebuild', 'usage-timezone']),
  spec('groups.static', 'P1', ['/my-groups', '/groups'], ['/my-groups', '/groups'], ['self', 'admin-target'], 'durable', 30_000, false, 'planned', ['group-write', 'group-account-binding', 'group-authorization']),
  spec('apiKeys.static', 'P1', ['/my-api-keys', '/api-keys'], ['/my-api-keys', '/api-keys'], ['self', 'admin-target'], 'durable', 30_000, false, 'planned', ['api-key-write', 'route-strategy-write'], 'no-store'),
  spec('routeStrategies.static', 'P1', ['/my-route-strategies', '/route-strategies'], ['/my-route-strategies', '/route-strategies'], ['self', 'admin-target'], 'durable', 30_000, false, 'planned', ['route-strategy-write', 'group-write']),
  spec('providers.catalog', 'P1', ['/my-models', '/providers'], ['/providers/options', '/providers/models/options'], ['self', 'admin-target', 'admin-global'], 'durable', 300_000, false, 'planned', ['provider-write', 'provider-model-write', 'model-pricing-write']),
  spec('authorizations.static', 'P1', ['/my-authorizations', '/authorizations'], ['/my-authorizations', '/authorizations'], ['self', 'admin-target', 'admin-global'], 'durable', 30_000, false, 'planned', ['authorization-write', 'authorization-expiry', 'resource-write']),
  spec('authorizations.usage', 'P1', ['/my-authorization-team-usage', '/my-authorization-user-usage', '/authorization-team-usage', '/authorization-user-usage'], ['/authorization-team-usage', '/authorization-user-usage'], ['self', 'admin-target', 'admin-global'], 'durable', 60_000, false, 'planned', ['stats-window-commit', 'authorization-write', 'team-member-write']),
  spec('teams.static', 'P1', ['/my-teams', '/authorization-teams'], ['/my-teams', '/system-teams'], ['self', 'admin-global'], 'durable', 30_000, false, 'planned', ['team-write', 'team-member-write', 'authorization-write']),
  spec('stats.accountUsage', 'P1', ['/my-usage-stats', '/usage-stats'], ['/my-stats/account-usage', '/stats/account-usage'], ['self', 'admin-target'], 'durable', 60_000, false, 'planned', ['stats-window-commit', 'stats-rebuild']),
  spec('stats.aiPerformance', 'P1', ['/my-ai-performance', '/ai-performance'], ['/my-stats/ai-performance', '/stats/ai-performance'], ['self', 'admin-target'], 'durable', 60_000, false, 'planned', ['stats-window-commit', 'account-write']),
  spec('modelChecks.history', 'P2', ['/my-model-checks', '/model-checks'], ['/my-model-checks', '/model-checks'], ['self', 'admin-target'], 'durable', 30_000, false, 'planned', ['model-check-run', 'model-check-complete'], 'memory'),
  spec('proxies.static', 'P2', ['/proxies'], ['/proxies', '/proxies/options'], ['admin-global'], 'session', 30_000, true, 'planned', ['proxy-write', 'proxy-test'], 'no-store'),
  spec('responsePolicies.static', 'P2', ['/response-inspection-policies'], ['/response-inspection-policies'], ['admin-global'], 'durable', 60_000, false, 'planned', ['response-policy-write']),
  spec('integrations.sources', 'P2', ['/external-integration-sources'], ['/external-integration-sources'], ['admin-global'], 'session', 30_000, true, 'planned', ['integration-source-write', 'integration-token-write'], 'no-store'),
  spec('announcements.admin', 'P2', ['/announcements'], ['/announcements'], ['admin-global'], 'durable', 30_000, false, 'planned', ['announcement-write']),
  spec('systemAccounts.static', 'P2', ['/system-accounts'], ['/system-accounts'], ['admin-global'], 'session', 30_000, true, 'planned', ['system-account-write', 'profile-write']),
  spec('settings.admin', 'P2', ['/settings'], ['/settings', '/settings/global'], ['admin-global'], 'session', 30_000, true, 'planned', ['settings-write']),
  spec('chat.specialized', 'P3', ['/my-chat'], ['/my-chat/conversations', '/my-chat/messages'], ['self'], 'specialized', 0, true, 'specialized', ['chat-sync']),
  spec('logs.operation', 'P3', ['/my-operation-logs', '/operation-logs'], ['/my-operation-logs', '/operation-logs'], ['self', 'admin-global'], 'memory', 10_000, true, 'planned', ['operation-log-append', 'log-retention']),
  spec('logs.publicApi', 'P3', ['/public-api-logs'], ['/public-api-logs'], ['admin-global'], 'memory', 10_000, true, 'planned', ['public-api-log-append', 'log-retention'], 'no-store'),
  spec('logs.audit', 'P3', ['/audit-logs'], ['/audit-logs'], ['admin-global'], 'no-store', 0, true, 'no-store', ['audit-log-append', 'log-retention']),
  spec('logs.runtime', 'P3', ['/runtime-logs'], ['/runtime-logs'], ['admin-global'], 'no-store', 0, true, 'no-store', ['runtime-log-append', 'runtime-log-rotation']),
  spec('system.storage', 'P3', ['/table-monitor'], ['/table-monitor'], ['admin-global'], 'memory', 60_000, true, 'planned', ['table-monitor-snapshot']),
  spec('system.metrics', 'P3', ['/system-metrics-stats'], ['/stats/system-metrics'], ['admin-global'], 'memory', 15_000, true, 'planned', ['system-metrics-sample']),
  spec('ip.stats', 'P3', ['/ip-stats'], ['/ip-stats'], ['admin-global'], 'session', 15_000, true, 'planned', ['ip-stats-aggregate', 'ip-policy-write']),
] as const satisfies readonly PageDataDomainSpec[]

export function pageDataSpecsForRoute(route: string): readonly PageDataDomainSpec[] {
  return pageDataDomainRegistry.filter((entry) => entry.routes.includes(route))
}

function spec(
  domain: string,
  priority: PageDataCachePriority,
  routes: string[],
  primaryGets: string[],
  scopes: PageDataCacheScope[],
  persistence: PageDataCachePersistence,
  maxStaleMs: number,
  sensitive: boolean,
  implementation: PageDataDomainImplementation,
  invalidators: string[],
  detailPolicy?: PageDataCachePersistence
): PageDataDomainSpec {
  return { domain, priority, routes, primaryGets, scopes, persistence, maxStaleMs, sensitive, implementation, invalidators, ...(detailPolicy ? { detailPolicy } : {}) }
}
