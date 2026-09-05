import type { SystemTeamSummary } from '../../../../domain/types.js'
import type { AccessScope } from '../../../../storage/access-scope.js'
import * as repositories from '../../../../storage/repositories.js'
import {
  namePrefix,
  type MockSystemAccounts,
  type MockTeams
} from '../shared.js'

export function createTeams(adminAccess: AccessScope, users: MockSystemAccounts): MockTeams {
  const teamAccess: AccessScope = { systemAccountId: adminAccess.systemAccountId, role: 'admin' }
  const devTeam = repositories.createSystemTeam({
    name: `${namePrefix}研发协作团队`,
    description: 'Mockdata 研发协作团队，承接团队级分组授权',
    status: 'active'
  }, teamAccess)
  repositories.addSystemTeamMembers(devTeam.id, {
    systemAccountIds: [users.dev.id, users.tester.id, users.ops.id]
  }, teamAccess)

  const opsTeam = repositories.createSystemTeam({
    name: `${namePrefix}运维保障团队`,
    description: 'Mockdata 运维保障团队，承接备用分组授权',
    status: 'active'
  }, teamAccess)
  repositories.addSystemTeamMembers(opsTeam.id, {
    systemAccountIds: [users.ops.id, users.viewer.id]
  }, teamAccess)

  const disabledTeam = repositories.createSystemTeam({
    name: `${namePrefix}停用历史团队`,
    description: 'Mockdata 停用团队，用于状态展示',
    status: 'active'
  }, teamAccess)
  repositories.addSystemTeamMembers(disabledTeam.id, {
    systemAccountIds: [users.finance.id]
  }, teamAccess)
  repositories.updateSystemTeam(disabledTeam.id, { status: 'disabled', expectedUpdatedAt: disabledTeam.updatedAt }, teamAccess)

  return {
    devTeam: refreshTeam(devTeam.id, teamAccess),
    opsTeam: refreshTeam(opsTeam.id, teamAccess),
    disabledTeam: refreshTeam(disabledTeam.id, teamAccess)
  }
}

function refreshTeam(id: string, access: AccessScope): SystemTeamSummary {
  const team = repositories.findSystemTeamSummary(id, access)
  if (!team) throw new Error(`读取 Mockdata 团队失败：${id}`)
  return team
}
