import type { AccessScope } from '../../../../storage/access-scope.js'
import * as repositories from '../../../../storage/repositories.js'
import {
  dayMs,
  namePrefix,
  type MockApiKeys,
  type MockGroups,
  type MockSystemAccounts
} from '../shared.js'
import {
  activeApiKeyAvailabilitySchedule,
  inactiveApiKeyAvailabilitySchedule
} from './availability-schedules.js'
import { quotaLimits } from './quota-limits.js'

export function createApiKeys(adminAccess: AccessScope, groups: MockGroups, users: MockSystemAccounts): MockApiKeys {
  const adminMain = repositories.createApiKeyRecord({
    name: `${namePrefix}主力网关 Key`,
    description: 'Mockdata 主力本地网关 Key，绑定主力分组',
    groupBindings: [{ groupId: groups.main.id, priority: 1, status: 'active' }],
    status: 'active',
    quotaLimits: quotaLimits(35, 260, 1000),
    availabilitySchedule: activeApiKeyAvailabilitySchedule()
  }, adminAccess)
  const adminHighConcurrency = repositories.createApiKeyRecord({
    name: `${namePrefix}高并发 AI Key`,
    description: 'Mockdata 高并发本地网关 Key，绑定高并发 AI 分组，用于分组管理和调度验收',
    groupBindings: [{ groupId: groups.highConcurrency.id, priority: 1, status: 'active' }],
    status: 'active',
    quotaLimits: {
      hourly: { enabled: true, hours: 1, limit: 160 },
      daily: { enabled: true, limit: 1200 },
      monthly: { enabled: true, limit: 5200 }
    }
  }, adminAccess)
  const adminHighFrequency = repositories.createApiKeyRecord({
    name: `${namePrefix}高频限额 Key`,
    description: 'Mockdata 高频限额 Key，用于额度窗口展示',
    groupBindings: [{ groupId: groups.main.id, priority: 1, status: 'active' }],
    status: 'active',
    quotaLimits: {
      hourly: { enabled: true, hours: 3, limit: 90 },
      daily: { enabled: true, limit: 420 },
      monthly: { enabled: true, limit: 1600 }
    }
  }, adminAccess)
  const adminRoundRobin = repositories.createApiKeyRecord({
    name: `${namePrefix}轮询多分组 Key`,
    description: 'Mockdata 轮询 Key，混合启用和停用分组绑定，用于 API Key 多分组路由策略展示',
    groupRouteStrategy: 'round_robin',
    groupBindings: [
      { groupId: groups.main.id, priority: 1, weight: 1, status: 'active' },
      { groupId: groups.highConcurrency.id, priority: 2, weight: 1, status: 'active' },
      { groupId: groups.backup.id, priority: 3, weight: 1, status: 'disabled' }
    ],
    status: 'active',
    quotaLimits: {
      hourly: { enabled: true, hours: 6, limit: 220 },
      daily: { enabled: true, limit: 900 },
      weekly: { enabled: true, limit: 3200 },
      monthly: { enabled: true, limit: 9000 }
    }
  }, adminAccess)
  const adminWeighted = repositories.createApiKeyRecord({
    name: `${namePrefix}加权多分组 Key`,
    description: 'Mockdata 加权轮询 Key，用于权重、优先级和跨分组路由状态展示',
    groupRouteStrategy: 'weighted_round_robin',
    groupBindings: [
      { groupId: groups.main.id, priority: 1, weight: 6, status: 'active' },
      { groupId: groups.highConcurrency.id, priority: 2, weight: 3, status: 'active' },
      { groupId: groups.oauth.id, priority: 3, weight: 1, status: 'active' }
    ],
    status: 'active',
    quotaLimits: {
      hourly: { enabled: true, hours: 12, limit: 260 },
      daily: { enabled: true, limit: 1000 },
      weekly: { enabled: true, limit: 3600 },
      monthly: { enabled: true, limit: 12000 },
      total: { enabled: true, limit: 40000 }
    }
  }, adminAccess)
  const adminScheduled = repositories.createApiKeyRecord({
    name: `${namePrefix}时间计划 Key`,
    description: 'Mockdata 当前不在允许时段内的 API Key，用于时间计划运行态和网关拒绝状态展示',
    groupBindings: [{ groupId: groups.experiment.id, priority: 1, status: 'active' }],
    status: 'active',
    availabilitySchedule: inactiveApiKeyAvailabilitySchedule(),
    quotaLimits: quotaLimits(5, 30, 100)
  }, adminAccess)
  const adminBackup = repositories.createApiKeyRecord({
    name: `${namePrefix}备用网关 Key`,
    description: 'Mockdata 备用 Key，绑定备用分组',
    groupBindings: [{ groupId: groups.backup.id, priority: 1, status: 'active' }],
    status: 'active',
    quotaLimits: quotaLimits(20, 120, 480),
    availabilitySchedule: inactiveApiKeyAvailabilitySchedule()
  }, adminAccess)
  const adminOAuth = repositories.createApiKeyRecord({
    name: `${namePrefix}OAuth 网关 Key`,
    description: 'Mockdata OAuth Key，绑定 OAuth 分组',
    groupBindings: [{ groupId: groups.oauth.id, priority: 1, status: 'active' }],
    status: 'active',
    quotaLimits: quotaLimits(18, 140, 520)
  }, adminAccess)
  const adminAuthorizedGroups = repositories.createApiKeyRecord({
    name: `${namePrefix}超级管理员授权分组 Key`,
    description: 'Mockdata 超级管理员使用别人授权给自己的分组，验证 AI 分组管理和授权分组路由',
    groupBindings: [{ groupId: groups.adminGrantedDev.id, priority: 1, status: 'active' }],
    status: 'active',
    quotaLimits: quotaLimits(10, 80, 300)
  }, adminAccess)
  const adminDisabled = repositories.createApiKeyRecord({
    name: `${namePrefix}停用网关 Key`,
    description: 'Mockdata 停用 Key，用于状态展示',
    groupBindings: [{ groupId: groups.experiment.id, priority: 1, status: 'active' }],
    status: 'disabled',
    quotaLimits: quotaLimits(5, 30, 100)
  }, adminAccess)
  const adminExpired = repositories.createApiKeyRecord({
    name: `${namePrefix}已过期网关 Key`,
    description: 'Mockdata 已过期 Key，用于过期状态展示',
    groupBindings: [{ groupId: groups.experiment.id, priority: 1, status: 'active' }],
    status: 'active',
    expiresAt: new Date(Date.now() - 2 * dayMs).toISOString(),
    quotaLimits: quotaLimits(5, 30, 100)
  }, adminAccess)

  const managerMain = repositories.createApiKeyRecord({
    name: `${namePrefix}管理员网关 Key`,
    description: 'Mockdata 普通管理员本地网关 Key，绑定管理员自有分组',
    groupBindings: [{ groupId: groups.managerMain.id, priority: 1, status: 'active' }],
    status: 'active',
    quotaLimits: quotaLimits(16, 120, 420)
  }, { systemAccountId: users.manager.id, role: users.manager.role })

  const managerHighConcurrency = repositories.createApiKeyRecord({
    name: `${namePrefix}管理员高并发 Key`,
    description: 'Mockdata 普通管理员高并发 Key，绑定管理员高并发分组',
    groupBindings: [{ groupId: groups.managerHighConcurrency.id, priority: 1, status: 'active' }],
    status: 'active',
    quotaLimits: {
      hourly: { enabled: true, hours: 1, limit: 90 },
      daily: { enabled: true, limit: 720 },
      monthly: { enabled: true, limit: 2600 }
    }
  }, { systemAccountId: users.manager.id, role: users.manager.role })

  const devGroupAuthorized = repositories.createApiKeyRecord({
    name: `${namePrefix}研发授权调用 Key`,
    description: 'Mockdata 研发用户使用授权分组和授权账户的 Key',
    groupBindings: [
      { groupId: groups.main.id, priority: 1, status: 'active' },
      { groupId: groups.devDefault.id, priority: 2, status: 'active' }
    ],
    status: 'active',
    quotaLimits: quotaLimits(8, 50, 180)
  }, { systemAccountId: users.dev.id, role: 'user' })

  const testerTeamAuthorized = repositories.createApiKeyRecord({
    name: `${namePrefix}团队授权调用 Key`,
    description: 'Mockdata 测试用户使用团队授权分组和团队授权账户的 Key',
    groupBindings: [
      { groupId: groups.backup.id, priority: 1, status: 'active' },
      { groupId: groups.testerDefault.id, priority: 2, status: 'active' },
      { groupId: groups.experiment.id, priority: 3, status: 'active' }
    ],
    status: 'active',
    quotaLimits: quotaLimits(6, 40, 150)
  }, { systemAccountId: users.tester.id, role: 'user' })

  const opsAccountAuthorized = repositories.createApiKeyRecord({
    name: `${namePrefix}账户授权调用 Key`,
    description: 'Mockdata 运维用户使用授权分组和授权账户的 Key',
    groupBindings: [
      { groupId: groups.highConcurrency.id, priority: 1, status: 'active' },
      { groupId: groups.oauth.id, priority: 2, status: 'active' },
      { groupId: groups.opsDefault.id, priority: 3, status: 'active' }
    ],
    status: 'active',
    quotaLimits: quotaLimits(6, 36, 120)
  }, { systemAccountId: users.ops.id, role: 'user' })

  const financeAuthorized = repositories.createApiKeyRecord({
    name: `${namePrefix}财务授权调用 Key`,
    description: 'Mockdata 财务用户使用授权分组和授权账户的 Key',
    groupBindings: [
      { groupId: groups.oauth.id, priority: 1, status: 'active' },
      { groupId: groups.financeDefault.id, priority: 2, status: 'active' }
    ],
    status: 'active',
    quotaLimits: quotaLimits(5, 30, 100)
  }, { systemAccountId: users.finance.id, role: 'user' })

  const viewerAuthorized = repositories.createApiKeyRecord({
    name: `${namePrefix}观察授权调用 Key`,
    description: 'Mockdata 观察用户使用授权分组和授权账户的 Key',
    groupBindings: [
      { groupId: groups.backup.id, priority: 1, status: 'active' },
      { groupId: groups.oauth.id, priority: 2, status: 'active' },
      { groupId: groups.viewerDefault.id, priority: 3, status: 'active' }
    ],
    status: 'active',
    quotaLimits: quotaLimits(4, 24, 80)
  }, { systemAccountId: users.viewer.id, role: 'user' })

  return {
    adminMain,
    adminHighConcurrency,
    adminHighFrequency,
    adminRoundRobin,
    adminWeighted,
    adminScheduled,
    adminBackup,
    adminOAuth,
    adminAuthorizedGroups,
    adminDisabled,
    adminExpired,
    managerMain,
    managerHighConcurrency,
    devGroupAuthorized,
    testerTeamAuthorized,
    opsAccountAuthorized,
    financeAuthorized,
    viewerAuthorized
  }
}
