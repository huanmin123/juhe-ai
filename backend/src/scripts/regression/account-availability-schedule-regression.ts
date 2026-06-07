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

try {
  const group = repositories.createGroup({
    name: '账户计划回归分组',
    providerCode: 'gpt'
  }, access)
  const allowed = repositories.createAccount({
    providerCode: 'gpt',
    name: '账户计划允许回归',
    type: 'api_key',
    credentials: { api_key: 'sk-account-schedule-allow', base_url: 'https://api.openai.com/v1' },
    availabilitySchedule: allDaySchedule,
    groupId: group.id
  }, access)
  const denied = repositories.createAccount({
    providerCode: 'gpt',
    name: '账户时段外回归',
    type: 'api_key',
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
  assert.equal(deniedSummary?.availabilitySchedule?.enabled, true, '账户详情应返回可用时段计划')
  assert.equal(deniedSummary?.schedulable, true, '时段外不应改写持久参与调度开关')

  const cleared = repositories.updateAccount(denied.id, { availabilitySchedule: null }, access)
  assert.equal(cleared?.availabilitySchedule, undefined, '提交 null 应清空账户可用时段计划')
  const runtimeAfterClear = repositories.listOpenAIAccountsForGroup(groupId, access.systemAccountId)
  assert.equal(runtimeAfterClear.some((account) => account.id === denied.id), true, '清空计划后账户应重新进入网关候选')

  assert.throws(
    () => accountAvailabilityScheduleFromRequest({
      availabilitySchedule: {
        enabled: true,
        timezone: 'UTC',
        mode: 'allow_windows',
        windows: [{ daysOfWeek: [8], start: '10:00', end: '11:00' }]
      }
    }),
    /账户可用时段计划重复日期无效/,
    '账户可用时段计划非法参数应返回账户语义错误'
  )

  console.log('账户可用时段计划回归通过：保存、列表展示、网关候选过滤和清空计划符合预期')
} finally {
  try {
    databaseModule.getBusinessDatabase().close()
    databaseModule.closeStorageDatabases()
  } catch {
  }
  rmSync(tempRoot, { recursive: true, force: true })
}


