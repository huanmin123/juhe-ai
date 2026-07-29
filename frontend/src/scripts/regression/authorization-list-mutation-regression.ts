import assert from 'node:assert/strict'

import type { ResourceAuthorizationListItem } from '@/types/domain'
import {
  applyAuthorizationCreateMutation,
  applyAuthorizationPatchMutation,
  applyAuthorizationReturnMutation,
  applyAuthorizationTerminalMutation
} from '@/views/authorizations/authorizationListMutation'

const first = authorization('grant_1', '2026-07-29T02:00:00.000Z')
const second = authorization('grant_2', '2026-07-29T01:00:00.000Z')
const context = {
  filters: {
    direction: 'outbound' as const,
    sourceType: 'all' as const,
    status: 'all' as const,
    resourceType: 'all' as const,
    resourceOwnerSystemAccountId: '__all__'
  },
  keyword: '',
  isManagementView: false,
  filterResourceDisabled: false,
  allSystemAccountsValue: '__all__',
  currentSystemAccountId: 'sys_owner'
}

const created = authorization('grant_3', '2026-07-29T03:00:00.000Z')
assert.deepEqual(
  applyAuthorizationCreateMutation([first, second], { item: created, created: true }, context, 1, 2),
  { items: [created, first], totalDelta: 1 },
  '创建授权应按服务端 createdAt/id 排序插入首页并维持页上限'
)
assert.deepEqual(
  applyAuthorizationCreateMutation([first, second], { item: created, created: true }, context, 2, 2),
  { items: [first, second], totalDelta: 1 },
  '后续页不应臆测创建授权属于当前分页窗口'
)
const third = authorization('grant_0', '2026-07-29T00:00:00.000Z')
assert.deepEqual(
  applyAuthorizationCreateMutation([first, second, third], { item: created, created: true }, context, 2, 2),
  { items: [created, first, second, third], totalDelta: 1 },
  '移动端累计窗口应插入创建授权，不得套用桌面后续页规则'
)

const restored = { ...first, status: 'active' as const, updatedAt: '2026-07-29T04:00:00.000Z' }
const revoked = { ...first, status: 'revoked' as const }
assert.deepEqual(
  applyAuthorizationCreateMutation(
    [revoked, second],
    { item: restored, created: false, previousStatus: 'revoked' },
    { ...context, filters: { ...context.filters, status: 'active' } },
    1,
    50
  ).totalDelta,
  1,
  '恢复已回收授权进入当前状态筛选时必须增加 total'
)

const cleared = applyAuthorizationPatchMutation([first], {
  id: first.id,
  status: 'active',
  expiresAt: null,
  limits: null,
  updatedAt: '2026-07-29T05:00:00.000Z'
}, context)
assert.equal(cleared.items[0]?.expiresAt, undefined, 'PATCH null 回执必须清除旧到期时间')
assert.equal(cleared.items[0]?.limits, undefined, 'PATCH null 回执必须清除旧额度')

assert.deepEqual(
  applyAuthorizationTerminalMutation(
    [first, second],
    { id: first.id, status: 'revoked', updatedAt: '2026-07-29T06:00:00.000Z' },
    { ...context, filters: { ...context.filters, status: 'active' } }
  ),
  { items: [second], totalDelta: -1 },
  '回收授权后不再匹配筛选时必须本地移除并减少 total'
)
assert.deepEqual(
  applyAuthorizationReturnMutation([first, second], first.id),
  { items: [second], totalDelta: -1 },
  '归还 204 后必须本地移除当前入站行'
)

console.log('统一授权 mutation 本地列表协调回归通过')

function authorization(id: string, createdAt: string): ResourceAuthorizationListItem {
  return {
    id,
    resourceType: 'account',
    resourceId: 'acc_1',
    resourceName: 'OpenAI 主账户',
    resourceOwnerSystemAccountId: 'sys_owner',
    granteeType: 'system_account',
    granteeSystemAccountId: 'sys_grantee',
    granteeSystemAccountName: '授权用户',
    status: 'active',
    expiresAt: '2099-01-01T00:00:00.000Z',
    limits: { daily: { enabled: true, limit: 5 } },
    effectiveSourceType: 'manual',
    createdAt,
    updatedAt: createdAt,
    sourceSummary: { activeSourceCount: 1, hasManual: true, hasTeam: false, teamSources: [] },
    permissions: { canEdit: true, canAuthorize: true }
  }
}
