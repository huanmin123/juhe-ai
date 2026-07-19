import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'
import { fileURLToPath } from 'node:url'

import { api, pageDataApi } from '@/api/client'
import { authState } from '@/composables/useAuth'
import { useRemoteAuthorizationPrincipalOptions } from '@/composables/useRemoteAuthorizationPrincipalOptions'
import { useRemoteSystemAccountOptions } from '@/composables/useRemoteSystemAccountOptions'
import type { PageDataConfirmRequest, PageDataConfirmResult } from '@/api/domains/pageData'
import type { GroupOptionSummary, SystemAccountPrincipalSummary, SystemTeamPrincipalSummary } from '@/types/domain'
import { useAccountGroupOptions } from '@/views/accounts/useAccountGroupOptions'

const systemAccountSource = readFileSync(fileURLToPath(new URL('../../composables/useRemoteSystemAccountOptions.ts', import.meta.url)), 'utf8')
const authorizationSource = readFileSync(fileURLToPath(new URL('../../composables/useRemoteAuthorizationPrincipalOptions.ts', import.meta.url)), 'utf8')
const groupSource = readFileSync(fileURLToPath(new URL('../../views/accounts/useAccountGroupOptions.ts', import.meta.url)), 'utf8')
const usageStatsAccountSource = readFileSync(fileURLToPath(new URL('../../views/usage-stats/useUsageStatsAccountOptions.ts', import.meta.url)), 'utf8')
const aiPerformanceAccountSource = readFileSync(fileURLToPath(new URL('../../views/ai-performance/useAiPerformanceAccountSelection.ts', import.meta.url)), 'utf8')
const usageRecordGroupSource = readFileSync(fileURLToPath(new URL('../../views/usage-records/useUsageRecordGroupOptions.ts', import.meta.url)), 'utf8')
const accountTagSource = readFileSync(fileURLToPath(new URL('../../views/accounts/accountTagOptionsCache.ts', import.meta.url)), 'utf8')
const authorizationUsageSource = readFileSync(fileURLToPath(new URL('../../views/authorizations/useAuthorizationUsageResourceFilters.ts', import.meta.url)), 'utf8')
const auditAccountSource = readFileSync(fileURLToPath(new URL('../../views/audit-logs/useAuditLogAccountOptions.ts', import.meta.url)), 'utf8')
const authorizationStateSource = readFileSync(fileURLToPath(new URL('../../views/authorizations/useAuthorizationOptionState.ts', import.meta.url)), 'utf8')
const authorizationResourceSource = readFileSync(fileURLToPath(new URL('../../views/authorizations/authorizationOptionResource.ts', import.meta.url)), 'utf8')
const routeStrategyResourceSource = readFileSync(fileURLToPath(new URL('../../composables/useRouteStrategyOptionsResource.ts', import.meta.url)), 'utf8')
const groupOptionsResourceSource = readFileSync(fileURLToPath(new URL('../../composables/useGroupOptionsResource.ts', import.meta.url)), 'utf8')
const apiKeysSource = readFileSync(fileURLToPath(new URL('../../views/api-keys/ApiKeysView.vue', import.meta.url)), 'utf8')
const apiKeyModalSource = readFileSync(fileURLToPath(new URL('../../views/api-keys/ApiKeyEditModal.vue', import.meta.url)), 'utf8')
for (const [name, source] of [
  ['系统账户', systemAccountSource],
  ['授权候选', authorizationSource],
  ['分组', groupSource],
  ['用量统计账户', usageStatsAccountSource],
  ['AI 性能账户', aiPerformanceAccountSource],
  ['使用记录分组', usageRecordGroupSource],
  ['账户标签', accountTagSource],
  ['授权用量资源', authorizationUsageSource],
  ['审计账户', auditAccountSource],
  ['授权资源适配器', authorizationResourceSource],
  ['策略路由资源适配器', routeStrategyResourceSource],
  ['分组资源适配器', groupOptionsResourceSource]
] as const) {
  assert.match(source, /getDefaultPageDataResourceCache/, `${name}下拉必须使用统一 IndexedDB resource cache`)
  assert.doesNotMatch(source, /createShortLivedQueryCache/, `${name}下拉不得继续维护独立 10 秒内存缓存`)
  assert.doesNotMatch(source, /readLocalSelectOptionWindow|writeLocalSelectOptionWindow/, `${name}下拉不得继续把接口响应写入 localStorage`)
}
assert.match(systemAccountSource, /domain: 'systemAccounts\.options'/)
assert.match(authorizationSource, /kind === 'team' \? 'teams\.options' : 'systemAccounts\.options'/)
assert.match(groupSource, /domain: 'groups\.static'/)
assert.match(usageStatsAccountSource, /domain: 'accounts\.options'/)
assert.match(aiPerformanceAccountSource, /domain: 'accounts\.options'/)
assert.match(usageRecordGroupSource, /domain: 'groups\.static'/)
assert.match(accountTagSource, /domain: 'accounts\.options'/)
assert.match(authorizationUsageSource, /domain:\s*filters\.resourceType === 'account' \? 'accounts\.options' : 'groups\.static'/)
assert.match(auditAccountSource, /domain: 'accounts\.options'/)
for (const domain of ['accounts.options', 'groups.static', 'systemAccounts.options', 'teams.options']) {
  assert.match(authorizationStateSource, new RegExp(`['"]${domain.replace('.', '\\.')}['"]`), `授权主表单应接入 ${domain} domain`)
}
assert.match(authorizationStateSource, /loadAuthorizationOptionResource/, '授权主表单应统一通过持久资源适配器加载')
assert.doesNotMatch(authorizationStateSource, /createShortLivedQueryCache/, '授权主表单不应继续维护独立 10 秒内存缓存')
for (const source of [apiKeysSource, apiKeyModalSource]) {
  assert.match(source, /loadRouteStrategyOptionsResource/, 'API Key 策略路由候选应复用持久资源适配器')
  assert.doesNotMatch(source, /createShortLivedQueryCache/, 'API Key 策略路由候选不应继续维护独立 10 秒内存缓存')
}

const originalConfirm = pageDataApi.confirm
const originalSystemAccountOptions = api.systemAccounts.options
const originalManagementAccountOptions = api.authorizationOptions.granteeAccounts
const originalManagementTeamOptions = api.authorizationOptions.granteeTeams
const originalGroupOptions = api.myGroups.options

let systemAccountCalls = 0
let principalAccountCalls = 0
let principalTeamCalls = 0
let groupCalls = 0

try {
  authState.currentUser.value = {
    id: 'cache-admin',
    username: 'cache-admin',
    displayName: '缓存管理员',
    role: 'admin',
    mustChangePassword: false
  }
  pageDataApi.confirm = confirmUnchangedOrReload
  api.systemAccounts.options = async () => {
    systemAccountCalls += 1
    return [{ id: 'sys-a', username: 'sys-a', displayName: '用户 A', role: 'user', status: 'active' }] as SystemAccountPrincipalSummary[]
  }
  api.authorizationOptions.granteeAccounts = async () => {
    principalAccountCalls += 1
    return [{ id: 'sys-b', username: 'sys-b', displayName: '用户 B', role: 'user', status: 'active' }] as SystemAccountPrincipalSummary[]
  }
  api.authorizationOptions.granteeTeams = async () => {
    principalTeamCalls += 1
    return [{ id: 'team-a', name: '团队 A', status: 'active' }] as SystemTeamPrincipalSummary[]
  }
  api.myGroups.options = async () => {
    groupCalls += 1
    return [{ id: 'group-a', name: '分组 A', providerCode: 'gpt' }] as GroupOptionSummary[]
  }

  await useRemoteSystemAccountOptions().load()
  const secondSystemAccounts = useRemoteSystemAccountOptions()
  await secondSystemAccounts.load()
  assert.equal(systemAccountCalls, 1, '重新创建系统账户下拉后必须从统一持久缓存读取')
  assert.equal(secondSystemAccounts.systemAccounts.value[0]?.id, 'sys-a')

  await useRemoteAuthorizationPrincipalOptions<SystemAccountPrincipalSummary>({
    isManagementView: () => true,
    kind: 'account'
  }).load()
  await useRemoteAuthorizationPrincipalOptions<SystemAccountPrincipalSummary>({
    isManagementView: () => true,
    kind: 'account'
  }).load()
  assert.equal(principalAccountCalls, 1, '系统账户授权候选必须跨 composable 实例复用持久缓存')

  await useRemoteAuthorizationPrincipalOptions<SystemTeamPrincipalSummary>({
    isManagementView: () => true,
    kind: 'team'
  }).load()
  await useRemoteAuthorizationPrincipalOptions<SystemTeamPrincipalSummary>({
    isManagementView: () => true,
    kind: 'team'
  }).load()
  assert.equal(principalTeamCalls, 1, '团队授权候选必须跨 composable 实例复用持久缓存')

  const groupConfig = () => ({
    isManagementView: () => false,
    scope: () => ({ providerCode: 'gpt' })
  })
  await useAccountGroupOptions(groupConfig()).load()
  const secondGroups = useAccountGroupOptions(groupConfig())
  await secondGroups.load()
  assert.equal(groupCalls, 1, '重新创建分组下拉后必须从统一持久缓存读取')
  assert.equal(secondGroups.groups.value[0]?.id, 'group-a')

  console.log('页面下拉持久缓存回归通过：系统账户、团队、授权候选和分组跨实例复用 IndexedDB resource')
} finally {
  pageDataApi.confirm = originalConfirm
  api.systemAccounts.options = originalSystemAccountOptions
  api.authorizationOptions.granteeAccounts = originalManagementAccountOptions
  api.authorizationOptions.granteeTeams = originalManagementTeamOptions
  api.myGroups.options = originalGroupOptions
  authState.currentUser.value = undefined
}

async function confirmUnchangedOrReload(request: PageDataConfirmRequest): Promise<PageDataConfirmResult> {
  const domains: PageDataConfirmResult['domains'] = {}
  for (const [domain, known] of Object.entries(request.domains)) {
    if (!domain) continue
    const token = {
      protocolVersion: 2,
      epoch: 'option-cache-epoch',
      scope: `scope:${request.viewScope}:${request.targetSystemAccountId ?? 'self'}`,
      domain,
      sequence: 1,
      resetSequence: 0
    } as NonNullable<typeof known>
    domains[domain as keyof typeof domains] = {
      action: known?.sequence === 1 ? 'unchanged' : 'reload',
      token
    }
  }
  return { serverTime: '2026-07-18T12:00:00.000Z', domains }
}
