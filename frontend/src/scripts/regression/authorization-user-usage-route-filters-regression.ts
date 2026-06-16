import {
  authorizationUserUsageRouteFilterValues,
  authorizationUserUsageRouteFiltersFromQuery,
  hasAuthorizationUserUsageRouteFilters,
  isAuthorizationUserUsageRoutePath
} from '@/views/authorizations/authorizationUserUsageRouteFilters'
import type { LocationQuery } from 'vue-router'

assert(isAuthorizationUserUsageRoutePath('/authorization-user-usage'), '管理端用户消耗页路径应被识别')
assert(isAuthorizationUserUsageRoutePath('/my-authorization-user-usage'), '个人端用户消耗页路径应被识别')
assert(!isAuthorizationUserUsageRoutePath('/authorization-team-usage'), '团队消耗页路径不应被用户消耗页 watcher 处理')

const routeQuery = {
  teamId: ['team_a', 'team_b'],
  granteeSystemAccountId: 'sys_grantee',
  resourceOwnerSystemAccountId: 'sys_owner',
  resourceId: ['account_a'],
  resourceType: 'account',
  startDate: '2026-06-01',
  endDate: ['2026-06-16']
}

assert(hasAuthorizationUserUsageRouteFilters(routeQuery), '包含筛选 query 时应识别为 route filters')
assertDeepEqual(
  authorizationUserUsageRouteFiltersFromQuery(routeQuery),
  {
    teamId: 'team_a',
    granteeSystemAccountId: 'sys_grantee',
    resourceOwnerSystemAccountId: 'sys_owner',
    resourceId: 'account_a',
    resourceType: 'account',
    startDate: '2026-06-01',
    endDate: '2026-06-16'
  },
  'route query 应解析为用户消耗页筛选值，并对数组 query 取首个字符串'
)

assertDeepEqual(
  authorizationUserUsageRouteFiltersFromQuery({
    resourceType: 'invalid',
    resourceId: 123,
    startDate: ['2026-06-01'],
    endDate: [false]
  } as unknown as LocationQuery),
  {
    teamId: undefined,
    granteeSystemAccountId: undefined,
    resourceOwnerSystemAccountId: undefined,
    resourceId: undefined,
    resourceType: undefined,
    startDate: '2026-06-01',
    endDate: undefined
  },
  '非法 resourceType 和非字符串 query 不应进入筛选值'
)

assert(!hasAuthorizationUserUsageRouteFilters({ resourceType: 'invalid' }), '只有非法 resourceType 时不应触发 route filters')
assert(hasAuthorizationUserUsageRouteFilters({ resourceType: 'group' }), 'group resourceType 应触发 route filters')
assert(
  authorizationUserUsageRouteFilterValues(routeQuery).includes('account'),
  'route filter values 应包含 resourceType 以便 watcher 观察 query 变化'
)

console.log('授权用户消耗 route filters 回归通过：query 解析、路径判断和非法值过滤符合预期')

function assert(condition: unknown, message: string): asserts condition {
  if (!condition) {
    throw new Error(message)
  }
}

function assertDeepEqual(actual: unknown, expected: unknown, message: string): void {
  const actualJson = JSON.stringify(actual)
  const expectedJson = JSON.stringify(expected)
  if (actualJson !== expectedJson) {
    throw new Error(`${message}，实际 ${actualJson}，期望 ${expectedJson}`)
  }
}
