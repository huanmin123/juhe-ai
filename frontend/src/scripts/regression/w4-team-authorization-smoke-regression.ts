import { strict as assert } from 'node:assert'
import { readFileSync } from 'node:fs'
import { dirname, resolve } from 'node:path'
import { fileURLToPath } from 'node:url'

import { api } from '@/api/client'
import { http } from '@/api/http'
import type {
  ResourceAuthorizationListResult,
  ResourceAuthorizationSummary,
  SystemTeamListResult,
  SystemTeamSummary
} from '@/types/domain'

type HttpMethod = 'get' | 'post' | 'patch' | 'delete'

interface HttpCall {
  method: HttpMethod
  url: string
  payload?: unknown
  config?: unknown
}

const currentDir = dirname(fileURLToPath(import.meta.url))
const frontendRoot = resolve(currentDir, '../../..')
const calls: HttpCall[] = []
const httpMethods = http as unknown as Record<HttpMethod, (...args: unknown[]) => Promise<unknown>>
const originalHttpMethods = {
  get: httpMethods.get,
  post: httpMethods.post,
  patch: httpMethods.patch,
  delete: httpMethods.delete
}

installHttpStubs()

try {
  await assertSystemTeamApiContract()
  await assertAuthorizationApiContract()
  assertSourceBoundaries()
} finally {
  Object.assign(httpMethods, originalHttpMethods)
}

console.log('W4 前端团队 / 统一授权 smoke 通过：团队维护、授权写入口和管理/个人作用域边界已固定')

async function assertSystemTeamApiContract(): Promise<void> {
  calls.length = 0

  await api.systemTeams.list({ systemAccountId: 'sys_owner', keyword: ' 授权团队 ', page: 2, pageSize: 20 })
  assertLastCall('get', '/system-teams', undefined, {
    params: { systemAccountId: 'sys_owner', keyword: '授权团队', page: 2, pageSize: 20 }
  }, '管理侧团队列表应保留 systemAccountId 并 trim keyword')

  await api.myTeams.list({ systemAccountId: 'sys_other', keyword: ' 我的团队 ', page: 3, pageSize: 10 } as Parameters<typeof api.myTeams.list>[0])
  assertLastCall('get', '/my-teams', undefined, {
    params: { keyword: '我的团队', page: 3, pageSize: 10 }
  }, '个人侧团队列表必须移除 systemAccountId')

  await api.systemTeams.detail('team_w4', { systemAccountId: 'sys_owner' })
  assertLastCall('get', '/system-teams/team_w4', undefined, {
    params: { systemAccountId: 'sys_owner' }
  }, '管理侧团队详情应可带 owner 作用域')

  await api.myTeams.detail('team_w4')
  assertLastCall('get', '/my-teams/team_w4', undefined, undefined, '个人侧团队详情应走 my-teams 且不带 owner 作用域')

  const createPayload = { name: '产品授权团队', description: '前端 smoke', status: 'active' as const }
  await api.systemTeams.create(createPayload)
  assertLastCall('post', '/system-teams', createPayload, undefined, '团队创建应走管理侧 system-teams POST')

  const updatePayload = { name: '产品授权团队更新', description: '更新说明', status: 'disabled' as const }
  await api.systemTeams.update('team_w4', updatePayload)
  assertLastCall('patch', '/system-teams/team_w4', updatePayload, undefined, '团队更新应走管理侧 system-teams PATCH')

  await api.systemTeams.addMembers('team_w4', { systemAccountIds: ['sys_a', 'sys_b'] })
  assertLastCall('post', '/system-teams/team_w4/members', { systemAccountIds: ['sys_a', 'sys_b'] }, undefined, '团队添加成员应只提交 systemAccountIds')

  await api.systemTeams.removeMember('team_w4', 'member_w4')
  assertLastCall('delete', '/system-teams/team_w4/members/member_w4', undefined, undefined, '团队移除成员应定位 teamId 和 memberId')
}

async function assertAuthorizationApiContract(): Promise<void> {
  calls.length = 0

  await api.authorizations.list()
  assertLastCall('get', '/authorizations', undefined, {
    params: { page: 1, pageSize: 500 }
  }, '管理侧授权轻量列表应带默认分页上限')

  await api.authorizations.listPage({
    systemAccountId: 'sys_owner',
    keyword: '授权',
    resourceType: 'account',
    direction: 'outbound',
    sourceType: 'manual',
    status: 'active',
    page: 2,
    pageSize: 50
  })
  assertLastCall('get', '/authorizations', undefined, {
    params: {
      systemAccountId: 'sys_owner',
      keyword: '授权',
      resourceType: 'account',
      direction: 'outbound',
      sourceType: 'manual',
      status: 'active',
      page: 2,
      pageSize: 50
    }
  }, '管理侧授权分页列表应保留 owner 和筛选参数')

  await api.myAuthorizations.listPage({
    systemAccountId: 'sys_other',
    keyword: '我的授权',
    direction: 'inbound',
    page: 3,
    pageSize: 25
  } as Parameters<typeof api.myAuthorizations.listPage>[0])
  assertLastCall('get', '/my-authorizations', undefined, {
    params: { keyword: '我的授权', direction: 'inbound', page: 3, pageSize: 25 }
  }, '个人侧授权分页列表必须移除 systemAccountId')

  await api.authorizations.detail('grant_w4', { systemAccountId: 'sys_owner' })
  assertLastCall('get', '/authorizations/grant_w4', undefined, {
    params: { systemAccountId: 'sys_owner' }
  }, '管理侧授权详情应按 grant ID 读取并带 owner 作用域')

  await api.myAuthorizations.detail('grant_w4')
  assertLastCall('get', '/my-authorizations/grant_w4', undefined, undefined, '个人侧授权详情应走 my-authorizations 且不带 owner 作用域')

  const createPayload = {
    resourceType: 'account' as const,
    resourceId: 'acct_w4',
    granteeType: 'system_account' as const,
    granteeId: 'sys_grantee',
    targetGroupId: 'grp_target',
    remark: '前端 smoke',
    expiresAt: '2026-08-01T00:00:00Z',
    limits: { daily: { enabled: true, limit: 10 } }
  }
  await api.authorizations.create(createPayload, { systemAccountId: 'sys_owner' })
  assertLastCall('post', '/authorizations', createPayload, {
    params: { systemAccountId: 'sys_owner' }
  }, '管理侧授权创建应带资源 owner 作用域')

  await api.myAuthorizations.create(createPayload)
  assertLastCall('post', '/my-authorizations', createPayload, undefined, '个人侧授权创建应走 my-authorizations 且不带 owner 作用域')

  const updatePayload = {
    status: 'paused' as const,
    expiresAt: '2026-08-02T00:00:00Z',
    limits: { monthly: { enabled: true, limit: 50 } }
  }
  await api.authorizations.update('grant_w4', updatePayload, { systemAccountId: 'sys_owner' })
  assertLastCall('patch', '/authorizations/grant_w4', updatePayload, {
    params: { systemAccountId: 'sys_owner' }
  }, '管理侧授权普通更新应走 PATCH grant ID 并带 owner 作用域')

  await api.myAuthorizations.update('grant_w4', { status: 'active', expiresAt: null, limits: null })
  assertLastCall('patch', '/my-authorizations/grant_w4', { status: 'active', expiresAt: null, limits: null }, undefined, '个人侧授权普通更新应保留清空 expiresAt/limits 语义')

  const expirePayload = { expiresAt: null, limits: { hourly: { enabled: true, limit: 5, hours: 24 } } }
  await api.authorizations.updateExpire('grant_w4', expirePayload, { systemAccountId: 'sys_owner' })
  assertLastCall('patch', '/authorizations/grant_w4/expire', expirePayload, {
    params: { systemAccountId: 'sys_owner' }
  }, '管理侧授权有效期更新应走专用 expire 路径')

  await api.myAuthorizations.updateExpire('grant_w4', { expiresAt: null, limits: null })
  assertLastCall('patch', '/my-authorizations/grant_w4/expire', { expiresAt: null, limits: null }, undefined, '个人侧授权有效期更新应走 my 专用 expire 路径')

  await api.authorizations.revoke('grant_w4', { systemAccountId: 'sys_owner' })
  assertLastCall('delete', '/authorizations/grant_w4', undefined, {
    params: { systemAccountId: 'sys_owner' }
  }, '管理侧授权回收应带 owner 作用域')

  await api.myAuthorizations.revoke('grant_w4')
  assertLastCall('delete', '/my-authorizations/grant_w4', undefined, undefined, '个人侧授权回收应走 my-authorizations')

  await api.authorizations.returnAuthorization('grant_w4', { systemAccountId: 'sys_owner' })
  assertLastCall('delete', '/authorizations/grant_w4/return', undefined, {
    params: { systemAccountId: 'sys_owner' }
  }, '管理侧授权归还应支持 grant ID return 路径')

  await api.myAuthorizations.returnAuthorization('grant_w4')
  assertLastCall('delete', '/my-authorizations/grant_w4/return', undefined, undefined, '个人侧授权归还应走 my return 路径')
}

function assertSourceBoundaries(): void {
  const packageJson = JSON.parse(readSource('package.json')) as { scripts?: Record<string, string> }
  const scopedApiSource = readSource('src/composables/useScopedDomainApi.ts')
  const systemTeamsApiSource = readSource('src/api/domains/systemTeams.ts')
  const systemTeamsViewSource = readSource('src/views/system-teams/SystemTeamsView.vue')
  const authorizationsApiSource = readSource('src/api/domains/authorizations.ts')
  const authorizationsViewSource = readSource('src/views/authorizations/AuthorizationsView.vue')
  const authorizationActionsSource = readSource('src/views/authorizations/useAuthorizationActions.ts')
  const routerSource = readSource('src/router/index.ts')

  assert.equal(
    packageJson.scripts?.['test:w4-team-authorization-smoke'],
    'pnpm --dir ../backend exec tsx --tsconfig ../frontend/tsconfig.json ../frontend/src/scripts/regression/w4-team-authorization-smoke-regression.ts',
    '前端 package script 应暴露 W4 团队 / 统一授权 smoke'
  )

  assertIncludes(scopedApiSource, 'api.systemTeams.list(params)', '授权团队页面管理侧列表必须走 systemTeams')
  assertIncludes(scopedApiSource, 'api.myTeams.list(params)', '授权团队页面个人侧列表必须走 myTeams')
  assertIncludes(scopedApiSource, 'api.systemTeams.detail(id, params)', '授权团队页面管理侧详情必须走 systemTeams detail')
  assertIncludes(scopedApiSource, 'api.myTeams.detail(id)', '授权团队页面个人侧详情必须走 myTeams detail')

  assertIncludes(systemTeamsApiSource, "http.get('/system-teams'", 'system teams API 必须暴露管理侧列表')
  assertIncludes(systemTeamsApiSource, "http.get('/my-teams'", 'system teams API 必须暴露个人侧列表')
  assertIncludes(systemTeamsApiSource, "http.post('/system-teams'", 'system teams API 必须暴露团队创建')
  assertIncludes(systemTeamsApiSource, 'http.patch(`/system-teams/${id}`', 'system teams API 必须暴露团队更新')
  assertIncludes(systemTeamsApiSource, 'http.post(`/system-teams/${id}/members`', 'system teams API 必须暴露成员新增')
  assertIncludes(systemTeamsApiSource, 'http.delete(`/system-teams/${id}/members/${memberId}`', 'system teams API 必须暴露成员移除')

  assertIncludes(systemTeamsViewSource, '<a-button v-if="isManagementView" type="primary" @click="openCreateTeam">新建授权团队</a-button>', '团队创建入口只能在管理视图展示')
  assertIncludes(systemTeamsViewSource, 'const systemTeamsApi = useScopedSystemTeamsApi(isManagementView)', '团队页面必须使用管理/个人视图作用域 API')
  assertIncludes(systemTeamsViewSource, 'await api.systemTeams.create(payload)', '团队页面创建必须调用 systemTeams.create')
  assertIncludes(systemTeamsViewSource, 'await api.systemTeams.update(editingTeamId.value, payload)', '团队页面编辑必须调用 systemTeams.update')
  assertIncludes(systemTeamsViewSource, 'await api.systemTeams.addMembers(teamId', '团队页面成员新增必须调用 addMembers')
  assertIncludes(systemTeamsViewSource, 'await api.systemTeams.removeMember(teamId, memberId)', '团队页面成员移除必须调用 removeMember')
  assertIncludes(systemTeamsViewSource, "message.warning('当前是只读视图，不能维护授权团队')", '个人团队视图必须保持只读保护')

  assertIncludes(authorizationsApiSource, "http.get('/authorizations'", 'authorizations API 必须暴露管理侧列表')
  assertIncludes(authorizationsApiSource, "http.get('/my-authorizations'", 'authorizations API 必须暴露个人侧列表')
  assertIncludes(authorizationsApiSource, 'http.patch(`/authorizations/${id}/expire`', 'authorizations API 必须暴露管理侧有效期专用路径')
  assertIncludes(authorizationsApiSource, 'http.patch(`/my-authorizations/${id}/expire`', 'authorizations API 必须暴露个人侧有效期专用路径')
  assertIncludes(authorizationsApiSource, 'http.delete(`/authorizations/${id}/return`', 'authorizations API 必须暴露管理侧归还路径')
  assertIncludes(authorizationsApiSource, 'http.delete(`/my-authorizations/${id}/return`', 'authorizations API 必须暴露个人侧归还路径')

  assertIncludes(authorizationsViewSource, '? await api.authorizations.listPage(params)', '授权页面管理侧列表必须调用 authorizations.listPage')
  assertIncludes(authorizationsViewSource, ': await api.myAuthorizations.listPage(params)', '授权页面个人侧列表必须调用 myAuthorizations.listPage')
  assertIncludes(authorizationsViewSource, 'return systemAccountId ? { systemAccountId } : undefined', '授权创建管理侧必须使用所选 owner 作用域')
  assertIncludes(authorizationsViewSource, "filters.direction !== 'inbound'", '个人授权归还入口必须只在入站视图出现')

  assertIncludes(authorizationActionsSource, 'await api.authorizations.create(payload, createAuthorizationScopeParams.value)', '管理侧授权创建必须带 owner 作用域')
  assertIncludes(authorizationActionsSource, 'await api.myAuthorizations.create(payload)', '个人侧授权创建必须走 my-authorizations')
  assertIncludes(authorizationActionsSource, 'await api.authorizations.revoke(item.id, authorizationOperationScopeParams(item))', '管理侧授权回收必须带资源 owner 作用域')
  assertIncludes(authorizationActionsSource, 'await api.myAuthorizations.returnAuthorization(item.id)', '个人侧授权归还必须走 my-authorizations return')
  assertIncludes(authorizationActionsSource, 'await api.authorizations.update(item.id, payload, authorizationOperationScopeParams(item))', '管理侧授权状态更新必须带资源 owner 作用域')
  assertIncludes(authorizationActionsSource, 'await api.authorizations.detail(item.id, authorizationOperationScopeParams(item))', '管理侧打开有效期弹窗必须按 grant ID 带 owner 读取详情')
  assertIncludes(authorizationActionsSource, 'await api.authorizations.updateExpire(authorization.id, payload, authorizationOperationScopeParams(authorization))', '管理侧有效期更新必须走专用 expire 路径并带 owner 作用域')
  assertIncludes(authorizationActionsSource, 'return { systemAccountId: item.resourceOwnerSystemAccountId }', '授权操作作用域必须来自资源归属系统账户')

  assertIncludes(routerSource, "path: '/authorization-teams'", '路由必须保留管理侧授权团队页面')
  assertIncludes(routerSource, "path: '/my-teams'", '路由必须保留个人侧我的授权团队页面')
  assertIncludes(routerSource, "path: '/authorizations'", '路由必须保留管理侧统一授权页面')
  assertIncludes(routerSource, "path: '/my-authorizations'", '路由必须保留个人侧我的统一授权页面')

  for (const forbidden of ['passwordHash', 'tokenHash', 'apiKeySecret', 'keySecret', 'credential', 'credentials']) {
    assertNotIncludes(systemTeamsViewSource, forbidden, `团队页面不应引用敏感字段：${forbidden}`)
    assertNotIncludes(authorizationsViewSource, forbidden, `授权页面不应引用敏感字段：${forbidden}`)
  }
}

function installHttpStubs(): void {
  httpMethods.get = async (url, config) => recordCall('get', String(url), undefined, config)
  httpMethods.post = async (url, payload, config) => recordCall('post', String(url), payload, config)
  httpMethods.patch = async (url, payload, config) => recordCall('patch', String(url), payload, config)
  httpMethods.delete = async (url, config) => recordCall('delete', String(url), undefined, config)
}

async function recordCall(method: HttpMethod, url: string, payload?: unknown, config?: unknown): Promise<unknown> {
  calls.push({ method, url, payload, config })
  return { data: { data: fixtureFor(method, url) } }
}

function fixtureFor(method: HttpMethod, url: string): unknown {
  if (method === 'get' && (url === '/system-teams' || url === '/my-teams')) {
    return systemTeamListFixture()
  }
  if (method === 'get' && (url === '/system-teams/team_w4' || url === '/my-teams/team_w4')) {
    return systemTeamFixture()
  }
  if ((method === 'post' && url === '/system-teams') || (method === 'patch' && url === '/system-teams/team_w4')) {
    return systemTeamFixture()
  }
  if ((method === 'post' && url === '/system-teams/team_w4/members') || (method === 'delete' && url === '/system-teams/team_w4/members/member_w4')) {
    return systemTeamFixture()
  }
  if (method === 'get' && (url === '/authorizations' || url === '/my-authorizations')) {
    return authorizationListFixture()
  }
  if (method === 'get' && (url === '/authorizations/grant_w4' || url === '/my-authorizations/grant_w4')) {
    return authorizationFixture()
  }
  if ((method === 'post' || method === 'patch' || method === 'delete') && (url.startsWith('/authorizations/grant_w4') || url.startsWith('/my-authorizations/grant_w4') || url === '/authorizations' || url === '/my-authorizations')) {
    return authorizationFixture()
  }
  throw new Error(`未覆盖的 HTTP stub：${method.toUpperCase()} ${url}`)
}

function systemTeamListFixture(): SystemTeamListResult {
  return {
    items: [systemTeamFixture()],
    total: 1,
    hasMore: false,
    page: 1,
    pageSize: 20
  }
}

function systemTeamFixture(): SystemTeamSummary {
  return {
    id: 'team_w4',
    name: '产品授权团队',
    description: '前端 smoke',
    status: 'active',
    createdBy: 'sys_owner',
    createdAt: '2026-07-09T09:00:00Z',
    updatedAt: '2026-07-09T09:00:00Z',
    memberCount: 1,
    activeMemberCount: 1,
    members: [{
      id: 'member_w4',
      teamId: 'team_w4',
      systemAccountId: 'sys_grantee',
      systemAccountName: '被授权用户',
      username: 'grantee',
      memberRole: 'member',
      status: 'active',
      joinedAt: '2026-07-09T09:00:00Z',
      createdAt: '2026-07-09T09:00:00Z',
      updatedAt: '2026-07-09T09:00:00Z'
    }]
  }
}

function authorizationListFixture(): ResourceAuthorizationListResult {
  return {
    items: [authorizationFixture()],
    total: 1,
    hasMore: false,
    page: 1,
    pageSize: 50
  }
}

function authorizationFixture(): ResourceAuthorizationSummary {
  return {
    id: 'grant_w4',
    resourceType: 'account',
    resourceId: 'acct_w4',
    resourceName: 'OpenAI 主账号',
    resourceOwnerSystemAccountId: 'sys_owner',
    resourceOwnerSystemAccountName: '资源归属人',
    granteeType: 'system_account',
    granteeSystemAccountId: 'sys_grantee',
    granteeSystemAccountName: '被授权用户',
    granteeUsername: 'grantee',
    status: 'active',
    scope: 'use',
    remark: '前端 smoke',
    expiresAt: '2026-08-01T00:00:00Z',
    limits: { daily: { enabled: true, limit: 10 } },
    effectiveSourceType: 'manual',
    createdAt: '2026-07-09T09:00:00Z',
    updatedAt: '2026-07-09T09:00:00Z',
    sourceSummary: {
      activeSourceCount: 1,
      hasManual: true,
      hasTeam: false,
      teamSources: []
    },
    permissions: {
      canEdit: true,
      canAuthorize: true
    }
  }
}

function assertLastCall(method: HttpMethod, url: string, payload: unknown, config: unknown, message: string): void {
  const call = calls.at(-1)
  assert(call, `${message}，未记录到 HTTP 调用`)
  assert.equal(call.method, method, message)
  assert.equal(call.url, url, message)
  assert.deepEqual(call.payload, payload, message)
  assert.deepEqual(call.config, config, message)
}

function readSource(relativePath: string): string {
  return readFileSync(resolve(frontendRoot, relativePath), 'utf8')
}

function assertIncludes(source: string, marker: string, message: string): void {
  assert(source.includes(marker), message)
}

function assertNotIncludes(source: string, marker: string, message: string): void {
  assert.equal(source.includes(marker), false, message)
}
