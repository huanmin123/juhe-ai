import assert from 'node:assert/strict'
import { mkdirSync, rmSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join, resolve } from 'node:path'

import { runtimeConfig } from '../../config/runtime.js'
import { accountAvailabilityScheduleFromRequest } from '../../storage/account-availability-schedule.js'
import { logger } from '../../shared/logger.js'

const tempRoot = resolve(tmpdir(), `juhe-ai-account-availability-schedule-${Date.now()}-${Math.random().toString(16).slice(2)}`)
runtimeConfig.databasePath = join(tempRoot, 'business.sqlite3')
runtimeConfig.datasetDatabasePath = join(tempRoot, 'dataset.sqlite3')
runtimeConfig.statsDatabasePath = join(tempRoot, 'stats.sqlite3')
runtimeConfig.secret = 'account-availability-schedule-secret'
runtimeConfig.log.consoleEnabled = false
runtimeConfig.log.fileEnabled = false
runtimeConfig.processRole = 'worker'
mkdirSync(tempRoot, { recursive: true })
logger.level = 'silent'

const [databaseModule, repositories] = await Promise.all([
  import('../../storage/database.js'),
  import('../../storage/repositories.js')
])

const access = { systemAccountId: 'sys_admin', role: 'admin' as const }
const allDaySchedule = {
  enabled: true,
  timezone: 'UTC',
  mode: 'allow_windows',
  windows: [
    { daysOfWeek: [1, 2, 3, 4, 5, 6, 7], start: '00:00', end: '23:59' },
    { daysOfWeek: [1, 2, 3, 4, 5, 6, 7], start: '23:59', end: '00:00' }
  ]
}
const futureSchedule = {
  ...allDaySchedule,
  dateRange: { startDate: '2999-01-01' }
}
const windowSchedule = {
  enabled: true,
  timezone: 'UTC',
  mode: 'allow_windows',
  windows: [
    { daysOfWeek: [1, 2, 3, 4, 5, 6, 7], start: '22:00', end: '23:55' }
  ]
}

try {
  const group = repositories.createGroup({
    name: '账户计划回归分组',
    providerCode: 'gpt'
  }, access)
  const allowed = repositories.createAccount({
    providerCode: 'gpt',
    name: '账户计划允许回归',
    type: 'api_key',
    status: 'active',
    credentials: { api_key: 'sk-account-schedule-allow', base_url: 'https://api.openai.com/v1' },
    availabilitySchedule: allDaySchedule,
    groupId: group.id
  }, access)
  const denied = repositories.createAccount({
    providerCode: 'gpt',
    name: '账户时段外回归',
    type: 'api_key',
    status: 'active',
    credentials: { api_key: 'sk-account-schedule-deny', base_url: 'https://api.openai.com/v1' },
    availabilitySchedule: futureSchedule,
    groupId: group.id
  }, access)

  const groupId = allowed.boundGroupId
  assert.equal(groupId, group.id, '测试账户应加入指定分组')
  const runtimeAccounts = repositories.listOpenAIAccountsForGroup(groupId, access.systemAccountId)
  assert.equal(runtimeAccounts.some((account) => account.id === allowed.id), true, '计划允许时账户应进入网关候选')
  assert.equal(runtimeAccounts.some((account) => account.id === denied.id), false, '时段外时账户不应进入网关候选')

  const deniedSummary = repositories.findAccountSummary(denied.id, access)
  assert.equal(deniedSummary?.availabilitySchedule?.enabled, true, '账户详情应返回时间计划')
  assert.equal(deniedSummary?.schedulable, true, '时段外不应改写持久参与调度开关')

  const cleared = repositories.updateAccount(denied.id, { availabilitySchedule: null }, access)
  assert.equal(cleared?.availabilitySchedule, undefined, '提交 null 应清空账户时间计划')
  const runtimeAfterClear = repositories.listOpenAIAccountsForGroup(groupId, access.systemAccountId)
  assert.equal(runtimeAfterClear.some((account) => account.id === denied.id), true, '清空计划后账户应重新进入网关候选')

  const boundaryAccount = withMockedNow(Date.parse('2026-05-31T21:59:00.000Z'), () => repositories.createAccount({
    providerCode: 'gpt',
    name: '账户计划边界回归',
    type: 'api_key',
    status: 'active',
    credentials: { api_key: 'sk-account-schedule-boundary', base_url: 'https://api.openai.com/v1' },
    availabilitySchedule: windowSchedule,
    groupId: group.id
  }, access))
  assert.equal(boundaryAccount.availabilityScheduleActive, false, '保存计划时应按当前时间初始化账户计划派生状态')
  assert.equal(repositories.listOpenAIAccountsForGroup(groupId, access.systemAccountId).some((account) => account.id === boundaryAccount.id), false, '允许时段外账户不应进入候选')

  const activatedResult = repositories.syncAccountAvailabilityScheduleStatuses(new Date('2026-05-31T22:00:00.000Z'))
  assert.equal(activatedResult.activated, 1, '账户计划开始边界应自动启用派生状态')
  assert.equal(repositories.findAccountSummary(boundaryAccount.id, access)?.availabilityScheduleActive, true, '开始边界后应暴露账户计划派生可用')

  const earlyClosed = repositories.updateAccount(boundaryAccount.id, { availabilityScheduleActive: false }, access)
  assert.equal(earlyClosed?.status, 'active', '账户计划内提前关闭不应改写账户状态')
  assert.equal(earlyClosed?.availabilityScheduleActive, false, '账户计划内提前关闭应立即改写派生状态')
  const earlyCloseNonBoundaryResult = repositories.syncAccountAvailabilityScheduleStatuses(new Date('2026-05-31T22:06:00.000Z'))
  assert.equal(earlyCloseNonBoundaryResult.activated, 0, '非边界同步不应覆盖账户计划内提前关闭')
  assert.equal(repositories.findAccountSummary(boundaryAccount.id, access)?.availabilityScheduleActive, false, '非边界同步后账户提前关闭应保留')

  const reopened = repositories.updateAccount(boundaryAccount.id, { availabilityScheduleActive: true }, access)
  assert.equal(reopened?.availabilityScheduleActive, true, '账户计划内应支持提前关闭后再次启用')
  const disabledResult = repositories.syncAccountAvailabilityScheduleStatuses(new Date('2026-05-31T23:55:00.000Z'))
  assert.equal(disabledResult.disabled, 1, '账户计划结束边界应自动关闭派生状态')
  assert.equal(repositories.findAccountSummary(boundaryAccount.id, access)?.availabilityScheduleActive, false, '结束边界后账户计划派生状态应停用')

  const earlyOpened = repositories.updateAccount(boundaryAccount.id, { availabilityScheduleActive: true }, access)
  assert.equal(earlyOpened?.availabilityScheduleActive, true, '账户计划范围外应支持提前启用')
  const earlyOpenNonBoundaryResult = repositories.syncAccountAvailabilityScheduleStatuses(new Date('2026-05-31T23:56:00.000Z'))
  assert.equal(earlyOpenNonBoundaryResult.disabled, 0, '非边界同步不应覆盖账户计划外提前启用')
  assert.equal(repositories.findAccountSummary(boundaryAccount.id, access)?.availabilityScheduleActive, true, '非边界同步后账户提前启用应保留')
  const nextDisabledResult = repositories.syncAccountAvailabilityScheduleStatuses(new Date('2026-06-01T23:55:00.000Z'))
  assert.equal(nextDisabledResult.disabled, 1, '下一次结束边界应重新接管并关闭账户计划派生状态')

  assert.throws(
    () => accountAvailabilityScheduleFromRequest({
      availabilitySchedule: {
        enabled: true,
        timezone: 'UTC',
        mode: 'allow_windows',
        windows: [{ daysOfWeek: [8], start: '10:00', end: '11:00' }]
      }
    }),
    /账户时间计划重复日期无效/,
    '账户时间计划非法参数应返回账户语义错误'
  )

  console.log('账户时间计划回归通过：保存、列表展示、网关候选过滤、清空计划、边界同步和人工提前启用/关闭符合预期')
} finally {
  try {
    databaseModule.getBusinessDatabase().close()
    databaseModule.closeStorageDatabases()
  } catch {
  }
  rmSync(tempRoot, { recursive: true, force: true })
}

function withMockedNow<T>(nowMs: number, fn: () => T): T {
  const originalNow = Date.now
  Date.now = () => nowMs
  try {
    return fn()
  } finally {
    Date.now = originalNow
  }
}


