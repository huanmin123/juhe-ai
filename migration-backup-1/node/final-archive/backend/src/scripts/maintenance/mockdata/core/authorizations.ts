import type {
  AccountSummary,
  GroupSummary,
  ResourceAuthorizationSummary,
  SystemAccountSummary
} from '../../../../domain/types.js'
import type { AccessScope } from '../../../../storage/access-scope.js'
import * as repositories from '../../../../storage/repositories.js'
import {
  dayMs,
  namePrefix,
  type MockAccounts,
  type MockGroups,
  type MockSystemAccounts,
  type MockTeams
} from '../shared.js'
import { quotaLimits } from './quota-limits.js'

export type DefaultGroupResolver = (systemAccountId: string) => GroupSummary

export function createAuthorizations(
  adminAccess: AccessScope,
  unscopedAdminAccess: AccessScope,
  groups: MockGroups,
  accounts: MockAccounts,
  users: MockSystemAccounts,
  teams: MockTeams,
  defaultGptGroup: DefaultGroupResolver
): ResourceAuthorizationSummary[] {
  const result: ResourceAuthorizationSummary[] = []
  result.push(repositories.createResourceAuthorization({
    resourceType: 'group',
    resourceId: groups.adminGrantedDev.id,
    granteeType: 'system_account',
    granteeId: users.admin.id,
    remark: `${namePrefix}研发分组授权给超级管理员`,
    limits: quotaLimits(12, 96, 360)
  }, unscopedAdminAccess))

  const adminPausedGroup = repositories.createResourceAuthorization({
    resourceType: 'group',
    resourceId: groups.adminGrantedOps.id,
    granteeType: 'system_account',
    granteeId: users.admin.id,
    remark: `${namePrefix}运维分组授权给超级管理员后暂停`,
    limits: quotaLimits(9, 72, 260)
  }, unscopedAdminAccess)
  repositories.updateResourceAuthorization(adminPausedGroup.id, { status: 'paused' }, unscopedAdminAccess)
  result.push(refreshAuthorization(adminPausedGroup.id, unscopedAdminAccess))

  const adminExpiredGroup = repositories.createResourceAuthorization({
    resourceType: 'group',
    resourceId: groups.adminGrantedTester.id,
    granteeType: 'system_account',
    granteeId: users.admin.id,
    remark: `${namePrefix}测试分组授权给超级管理员后过期`,
    expiresAt: new Date(Date.now() + 3 * dayMs).toISOString(),
    limits: quotaLimits(5, 30, 100)
  }, unscopedAdminAccess)
  repositories.updateResourceAuthorization(adminExpiredGroup.id, {
    status: 'expired',
    expiresAt: new Date(Date.now() - 3 * dayMs).toISOString()
  }, unscopedAdminAccess)
  result.push(refreshAuthorization(adminExpiredGroup.id, unscopedAdminAccess))

  result.push(repositories.createResourceAuthorization({
    resourceType: 'account',
    resourceId: accounts.devShared.id,
    granteeType: 'system_account',
    granteeId: users.admin.id,
    targetGroupId: defaultGptGroup(users.admin.id).id,
    remark: `${namePrefix}研发账户授权给超级管理员`,
    limits: quotaLimits(11, 88, 320)
  }, unscopedAdminAccess))

  const adminPausedAccount = repositories.createResourceAuthorization({
    resourceType: 'account',
    resourceId: accounts.opsShared.id,
    granteeType: 'system_account',
    granteeId: users.admin.id,
    targetGroupId: defaultGptGroup(users.admin.id).id,
    remark: `${namePrefix}运维账户授权给超级管理员后暂停`,
    limits: quotaLimits(7, 56, 210)
  }, unscopedAdminAccess)
  repositories.updateResourceAuthorization(adminPausedAccount.id, { status: 'paused' }, unscopedAdminAccess)
  result.push(refreshAuthorization(adminPausedAccount.id, unscopedAdminAccess))

  const adminExpiredAccount = repositories.createResourceAuthorization({
    resourceType: 'account',
    resourceId: accounts.testerShared.id,
    granteeType: 'system_account',
    granteeId: users.admin.id,
    targetGroupId: defaultGptGroup(users.admin.id).id,
    remark: `${namePrefix}测试账户授权给超级管理员后过期`,
    expiresAt: new Date(Date.now() + 4 * dayMs).toISOString(),
    limits: quotaLimits(4, 24, 96)
  }, unscopedAdminAccess)
  repositories.updateResourceAuthorization(adminExpiredAccount.id, {
    status: 'expired',
    expiresAt: new Date(Date.now() - 4 * dayMs).toISOString()
  }, unscopedAdminAccess)
  result.push(refreshAuthorization(adminExpiredAccount.id, unscopedAdminAccess))

  result.push(repositories.createResourceAuthorization({
    resourceType: 'group',
    resourceId: groups.main.id,
    granteeType: 'system_account',
    granteeId: users.dev.id,
    remark: `${namePrefix}研发用户可调用主力分组`,
    limits: quotaLimits(25, 200, 800)
  }, adminAccess))
  result.push(repositories.createResourceAuthorization({
    resourceType: 'group',
    resourceId: groups.highConcurrency.id,
    granteeType: 'system_account',
    granteeId: users.ops.id,
    remark: `${namePrefix}运维用户可调用高并发分组`,
    limits: quotaLimits(20, 160, 620)
  }, adminAccess))
  result.push(repositories.createResourceAuthorization({
    resourceType: 'group',
    resourceId: groups.backup.id,
    granteeType: 'system_account',
    granteeId: users.viewer.id,
    remark: `${namePrefix}观察用户可调用备用分组`,
    limits: quotaLimits(6, 36, 120)
  }, adminAccess))
  result.push(repositories.createResourceAuthorization({
    resourceType: 'group',
    resourceId: groups.experiment.id,
    granteeType: 'system_account',
    granteeId: users.tester.id,
    remark: `${namePrefix}测试用户可调用实验分组`,
    expiresAt: new Date(Date.now() + 14 * dayMs).toISOString(),
    limits: quotaLimits(8, 48, 160)
  }, adminAccess))
  result.push(repositories.createResourceAuthorization({
    resourceType: 'group',
    resourceId: groups.oauth.id,
    granteeType: 'system_account',
    granteeId: users.finance.id,
    remark: `${namePrefix}财务用户可调用 OAuth 分组`,
    limits: quotaLimits(7, 42, 140)
  }, adminAccess))
  result.push(repositories.createResourceAuthorization({
    resourceType: 'group',
    resourceId: groups.backup.id,
    granteeType: 'team',
    granteeId: teams.devTeam.id,
    remark: `${namePrefix}研发团队可调用备用分组`,
    limits: quotaLimits(18, 120, 500)
  }, adminAccess))
  result.push(repositories.createResourceAuthorization({
    resourceType: 'group',
    resourceId: groups.oauth.id,
    granteeType: 'team',
    granteeId: teams.opsTeam.id,
    remark: `${namePrefix}运维团队可调用 OAuth 分组`,
    limits: quotaLimits(12, 80, 300)
  }, adminAccess))
  result.push(repositories.createResourceAuthorization({
    resourceType: 'group',
    resourceId: groups.highConcurrency.id,
    granteeType: 'team',
    granteeId: teams.devTeam.id,
    remark: `${namePrefix}研发团队可调用高并发分组`,
    limits: quotaLimits(22, 180, 720)
  }, adminAccess))
  result.push(repositories.createResourceAuthorization({
    resourceType: 'account',
    resourceId: accounts.proxied.id,
    granteeType: 'system_account',
    granteeId: users.ops.id,
    targetGroupId: defaultGptGroup(users.ops.id).id,
    remark: `${namePrefix}运维用户可调用带代理账户`,
    limits: quotaLimits(8, 60, 200)
  }, adminAccess))
  result.push(repositories.createResourceAuthorization({
    resourceType: 'account',
    resourceId: accounts.burstFast.id,
    granteeType: 'system_account',
    granteeId: users.dev.id,
    targetGroupId: groups.devDefault.id,
    remark: `${namePrefix}研发用户可调用高并发快响账户`,
    limits: quotaLimits(14, 110, 420)
  }, adminAccess))
  result.push(repositories.createResourceAuthorization({
    resourceType: 'account',
    resourceId: accounts.oauth.id,
    granteeType: 'system_account',
    granteeId: users.finance.id,
    targetGroupId: groups.financeDefault.id,
    remark: `${namePrefix}财务用户可调用 OAuth 主力账户`,
    limits: quotaLimits(6, 40, 150)
  }, adminAccess))
  result.push(repositories.createResourceAuthorization({
    resourceType: 'account',
    resourceId: accounts.burstImage.id,
    granteeType: 'system_account',
    granteeId: users.viewer.id,
    targetGroupId: groups.viewerDefault.id,
    remark: `${namePrefix}观察用户可调用高并发图像账户`,
    limits: quotaLimits(5, 28, 90)
  }, adminAccess))
  result.push(repositories.createResourceAuthorization({
    resourceType: 'account',
    resourceId: accounts.primary.id,
    granteeType: 'team',
    granteeId: teams.devTeam.id,
    remark: `${namePrefix}研发团队可调用主力账户`,
    limits: quotaLimits(10, 80, 240)
  }, adminAccess))
  result.push(repositories.createResourceAuthorization({
    resourceType: 'account',
    resourceId: accounts.burstFallback.id,
    granteeType: 'team',
    granteeId: teams.opsTeam.id,
    remark: `${namePrefix}运维团队可调用高并发备用账户`,
    limits: quotaLimits(9, 66, 220)
  }, adminAccess))

  const pausedTeam = repositories.createResourceAuthorization({
    resourceType: 'group',
    resourceId: groups.experiment.id,
    granteeType: 'team',
    granteeId: teams.opsTeam.id,
    remark: `${namePrefix}运维团队暂停实验分组授权`,
    limits: quotaLimits(6, 36, 120)
  }, adminAccess)
  repositories.updateResourceAuthorization(pausedTeam.id, { status: 'paused' }, adminAccess)
  result.push(refreshAuthorization(pausedTeam.id, adminAccess))

  const revokedTeam = repositories.createResourceAuthorization({
    resourceType: 'account',
    resourceId: accounts.normal.id,
    granteeType: 'team',
    granteeId: teams.opsTeam.id,
    remark: `${namePrefix}运维团队已回收普通账户授权`,
    limits: quotaLimits(5, 30, 100)
  }, adminAccess)
  repositories.revokeResourceAuthorization(revokedTeam.id, adminAccess)
  result.push(refreshAuthorization(revokedTeam.id, adminAccess))

  const returned = repositories.createResourceAuthorization({
    resourceType: 'account',
    resourceId: accounts.oauthBackup.id,
    granteeType: 'system_account',
    granteeId: users.viewer.id,
    targetGroupId: defaultGptGroup(users.viewer.id).id,
    remark: `${namePrefix}观察用户已归还 OAuth 账户授权`,
    limits: quotaLimits(2, 10, 40)
  }, adminAccess)
  repositories.returnResourceAuthorizationForGrantee(returned.id, { systemAccountId: users.viewer.id, role: 'user' })
  result.push(refreshAuthorization(returned.id, adminAccess))

  const paused = repositories.createResourceAuthorization({
    resourceType: 'group',
    resourceId: groups.experiment.id,
    granteeType: 'system_account',
    granteeId: users.finance.id,
    remark: `${namePrefix}财务用户暂停授权`,
    limits: quotaLimits(4, 20, 80)
  }, adminAccess)
  repositories.updateResourceAuthorization(paused.id, { status: 'paused' }, adminAccess)
  result.push(refreshAuthorization(paused.id, adminAccess))

  const expiredGroup = repositories.createResourceAuthorization({
    resourceType: 'group',
    resourceId: groups.main.id,
    granteeType: 'system_account',
    granteeId: users.finance.id,
    remark: `${namePrefix}财务用户已过期主力分组授权`,
    expiresAt: new Date(Date.now() + 2 * dayMs).toISOString(),
    limits: quotaLimits(3, 18, 60)
  }, adminAccess)
  repositories.updateResourceAuthorization(expiredGroup.id, {
    status: 'expired',
    expiresAt: new Date(Date.now() - 2 * dayMs).toISOString()
  }, adminAccess)
  result.push(refreshAuthorization(expiredGroup.id, adminAccess))

  const expired = repositories.createResourceAuthorization({
    resourceType: 'account',
    resourceId: accounts.fallback.id,
    granteeType: 'system_account',
    granteeId: users.tester.id,
    targetGroupId: defaultGptGroup(users.tester.id).id,
    remark: `${namePrefix}测试用户已过期账户授权`,
    expiresAt: new Date(Date.now() + dayMs).toISOString(),
    limits: quotaLimits(3, 16, 50)
  }, adminAccess)
  repositories.updateResourceAuthorization(expired.id, {
    status: 'expired',
    expiresAt: new Date(Date.now() - dayMs).toISOString()
  }, adminAccess)
  result.push(refreshAuthorization(expired.id, adminAccess))

  const revoked = repositories.createResourceAuthorization({
    resourceType: 'account',
    resourceId: accounts.normal.id,
    granteeType: 'system_account',
    granteeId: users.viewer.id,
    targetGroupId: defaultGptGroup(users.viewer.id).id,
    remark: `${namePrefix}观察用户已回收账户授权`,
    limits: quotaLimits(2, 12, 40)
  }, adminAccess)
  repositories.revokeResourceAuthorization(revoked.id, adminAccess)
  result.push(refreshAuthorization(revoked.id, adminAccess))

  const revokedTemporary = repositories.createResourceAuthorization({
    resourceType: 'account',
    resourceId: accounts.temporary.id,
    granteeType: 'system_account',
    granteeId: users.viewer.id,
    targetGroupId: groups.viewerDefault.id,
    remark: `${namePrefix}观察用户已回收临时账户授权`,
    limits: quotaLimits(1, 8, 24)
  }, adminAccess)
  repositories.revokeResourceAuthorization(revokedTemporary.id, adminAccess)
  result.push(refreshAuthorization(revokedTemporary.id, adminAccess))
  return result
}

export function bindAuthorizedAccountToUserGroup(account: AccountSummary, group: GroupSummary, user: SystemAccountSummary): void {
  const access: AccessScope = { systemAccountId: user.id, role: 'user' }
  const updated = repositories.setAccountGroup(account.id, group.id, access)
  if (!updated) {
    throw new Error('绑定授权账户到用户默认分组失败')
  }
}

function refreshAuthorization(id: string, access: AccessScope): ResourceAuthorizationSummary {
  const authorization = repositories.findResourceAuthorization(id, access)
  if (!authorization) throw new Error(`读取 Mockdata 授权失败：${id}`)
  return authorization
}
