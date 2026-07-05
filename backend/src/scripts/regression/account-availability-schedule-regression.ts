import assert from 'node:assert/strict'
import { mkdirSync, rmSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join, resolve } from 'node:path'

import { runtimeConfig } from '../../config/runtime.js'
import { GPT_OPENAI_V1_PROFILE_ID } from '../../domain/provider-protocol.js'
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
const rangedCrossDaySchedule = {
  enabled: true,
  timezone: 'UTC',
  mode: 'allow_windows',
  dateRange: { startDate: '2026-06-01', endDate: '2026-06-01' },
  windows: [
    { daysOfWeek: [1], start: '22:00', end: '02:00' }
  ]
}

try {
  const group = repositories.createGroup({
    name: '账户计划回归分组',
    providerCode: 'gpt',
  }, access)
  const allowed = repositories.createAccount({
    providerCode: 'gpt',
    providerProtocolProfileId: GPT_OPENAI_V1_PROFILE_ID,
    name: '账户计划允许回归',
    type: 'api_key',
    status: 'active',
    credentials: { api_key: 'sk-account-schedule-allow', base_url: 'https://api.openai.com/v1' },
    availabilitySchedule: allDaySchedule,
    groupId: group.id
  }, access)
  const denied = repositories.createAccount({
    providerCode: 'gpt',
    providerProtocolProfileId: GPT_OPENAI_V1_PROFILE_ID,
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
  assert.equal(deniedSummary?.status, 'disabled', '时段外账户应初始化为停用状态')
  assert.equal(deniedSummary?.schedulable, true, '时段外不应改写持久参与调度开关')

  const cleared = repositories.updateAccount(denied.id, { availabilitySchedule: null }, access)
  assert.equal(cleared?.availabilitySchedule, undefined, '提交 null 应清空账户时间计划')
  assert.equal(cleared?.status, 'disabled', '清空计划不应隐式改写当前账户状态')
  const runtimeAfterClear = repositories.listOpenAIAccountsForGroup(groupId, access.systemAccountId)
  assert.equal(runtimeAfterClear.some((account) => account.id === denied.id), false, '清空计划后停用账户仍不应进入网关候选')
  const manuallyEnabledAfterClear = repositories.updateAccount(denied.id, { status: 'active' }, access)
  assert.equal(manuallyEnabledAfterClear?.status, 'active', '清空计划后可通过统一 status 手动启用')
  const runtimeAfterManualEnable = repositories.listOpenAIAccountsForGroup(groupId, access.systemAccountId)
  assert.equal(runtimeAfterManualEnable.some((account) => account.id === denied.id), true, '手动启用后账户应重新进入网关候选')

  const rangedCrossDayAccount = withMockedNow(Date.parse('2026-06-01T21:59:00.000Z'), () => repositories.createAccount({
    providerCode: 'gpt',
    providerProtocolProfileId: GPT_OPENAI_V1_PROFILE_ID,
    name: '账户跨天日期范围回归',
    type: 'api_key',
    status: 'active',
    credentials: { api_key: 'sk-account-schedule-cross-day-range', base_url: 'https://api.openai.com/v1' },
    availabilitySchedule: rangedCrossDaySchedule,
    groupId: group.id
  }, access))
  assert.equal(rangedCrossDayAccount.status, 'disabled', '跨天日期范围窗口开始前应初始化为停用状态')
  assert.equal(repositories.listOpenAIAccountsForGroup(groupId, access.systemAccountId).some((account) => account.id === rangedCrossDayAccount.id), false, '跨天日期范围开始前账户不应进入候选')

  const rangedCrossDayStartResult = repositories.syncAccountAvailabilityScheduleStatuses(new Date('2026-06-01T22:00:00.000Z'))
  assert(rangedCrossDayStartResult.changedIds.includes(rangedCrossDayAccount.id), '跨天日期范围开始边界应更新账户状态')
  assert.equal(repositories.findAccountSummary(rangedCrossDayAccount.id, access)?.status, 'active', '跨天日期范围开始边界后账户应恢复正常')
  assert.equal(repositories.listOpenAIAccountsForGroup(groupId, access.systemAccountId).some((account) => account.id === rangedCrossDayAccount.id), true, '跨天日期范围开始边界后账户应进入候选')

  const rangedCrossDayEndResult = repositories.syncAccountAvailabilityScheduleStatuses(new Date('2026-06-02T02:00:00.000Z'))
  assert(rangedCrossDayEndResult.changedIds.includes(rangedCrossDayAccount.id), '跨天日期范围次日结束边界应更新账户状态')
  assert.equal(repositories.findAccountSummary(rangedCrossDayAccount.id, access)?.status, 'disabled', '跨天日期范围次日结束边界后账户应停用')
  assert.equal(repositories.listOpenAIAccountsForGroup(groupId, access.systemAccountId).some((account) => account.id === rangedCrossDayAccount.id), false, '跨天日期范围次日结束边界后账户不应进入候选')

  const boundaryAccount = withMockedNow(Date.parse('2026-05-31T21:59:00.000Z'), () => repositories.createAccount({
    providerCode: 'gpt',
    providerProtocolProfileId: GPT_OPENAI_V1_PROFILE_ID,
    name: '账户计划边界回归',
    type: 'api_key',
    status: 'active',
    credentials: { api_key: 'sk-account-schedule-boundary', base_url: 'https://api.openai.com/v1' },
    availabilitySchedule: windowSchedule,
    groupId: group.id
  }, access))
  assert.equal(boundaryAccount.status, 'disabled', '保存计划时应按当前时间初始化账户状态')
  assert.equal(
    accountNextCheckAt(databaseModule.getBusinessDatabase(), boundaryAccount.id),
    '2026-05-31T22:00:00.000Z',
    '保存账户计划时应记录下一次边界检查时间，避免后台同步全量扫描'
  )
  assert.equal(repositories.listOpenAIAccountsForGroup(groupId, access.systemAccountId).some((account) => account.id === boundaryAccount.id), false, '允许时段外账户不应进入候选')

  const activatedResult = repositories.syncAccountAvailabilityScheduleStatuses(new Date('2026-05-31T22:00:00.000Z'))
  assert.equal(activatedResult.activated, 1, '账户计划开始边界应自动启用账户状态')
  assert.equal(repositories.findAccountSummary(boundaryAccount.id, access)?.status, 'active', '开始边界后应暴露正常账户状态')
  assert.equal(
    accountNextCheckAt(databaseModule.getBusinessDatabase(), boundaryAccount.id),
    '2026-05-31T23:55:00.000Z',
    '账户开始边界处理后应推进到下一次结束边界'
  )

  const earlyClosed = withMockedNow(Date.parse('2026-05-31T22:05:00.000Z'), () => repositories.updateAccount(boundaryAccount.id, { status: 'disabled' }, access))
  assert.equal(earlyClosed?.status, 'disabled', '账户计划内手动停用应立即改写统一状态')
  const earlyCloseNonBoundaryResult = repositories.syncAccountAvailabilityScheduleStatuses(new Date('2026-05-31T22:06:00.000Z'))
  assert.equal(earlyCloseNonBoundaryResult.activated, 0, '非边界同步不应覆盖账户计划内手动停用')
  assert.equal(repositories.findAccountSummary(boundaryAccount.id, access)?.status, 'disabled', '非边界同步后账户手动停用应保留')

  const reopened = withMockedNow(Date.parse('2026-05-31T22:07:00.000Z'), () => repositories.updateAccount(boundaryAccount.id, { status: 'active' }, access))
  assert.equal(reopened?.status, 'active', '账户计划内应支持手动停用后再次启用')
  const disabledResult = repositories.syncAccountAvailabilityScheduleStatuses(new Date('2026-05-31T23:55:00.000Z'))
  assert.equal(disabledResult.disabled, 1, '账户计划结束边界应自动停用账户状态')
  assert.equal(repositories.findAccountSummary(boundaryAccount.id, access)?.status, 'disabled', '结束边界后账户状态应停用')
  assert.equal(
    accountNextCheckAt(databaseModule.getBusinessDatabase(), boundaryAccount.id),
    '2026-06-01T22:00:00.000Z',
    '账户结束边界处理后应推进到下一次开始边界'
  )

  const earlyOpened = withMockedNow(Date.parse('2026-05-31T23:56:00.000Z'), () => repositories.updateAccount(boundaryAccount.id, { status: 'active' }, access))
  assert.equal(earlyOpened?.status, 'active', '账户计划范围外应支持手动启用')
  const earlyOpenNonBoundaryResult = repositories.syncAccountAvailabilityScheduleStatuses(new Date('2026-05-31T23:56:00.000Z'))
  assert.equal(earlyOpenNonBoundaryResult.disabled, 0, '非边界同步不应覆盖账户计划外手动启用')
  assert.equal(repositories.findAccountSummary(boundaryAccount.id, access)?.status, 'active', '非边界同步后账户手动启用应保留')
  const nextDisabledResult = repositories.syncAccountAvailabilityScheduleStatuses(new Date('2026-06-01T23:55:00.000Z'))
  assert.equal(nextDisabledResult.disabled, 1, '下一次结束边界应重新接管并停用账户状态')

  const pendingTestAccount = withMockedNow(Date.parse('2026-06-03T21:59:00.000Z'), () => repositories.createAccount({
    providerCode: 'gpt',
    providerProtocolProfileId: GPT_OPENAI_V1_PROFILE_ID,
    name: '账户计划保护状态回归',
    type: 'api_key',
    status: 'pending_test',
    credentials: { api_key: 'sk-account-schedule-pending-test', base_url: 'https://api.openai.com/v1' },
    availabilitySchedule: windowSchedule,
    groupId: group.id
  }, access))
  const protectedStatusResult = repositories.syncAccountAvailabilityScheduleStatuses(new Date('2026-06-03T22:00:00.000Z'))
  assert.equal(protectedStatusResult.changedIds.includes(pendingTestAccount.id), false, '账户计划不应自动恢复待测试等保护状态')
  assert.equal(repositories.findAccountSummary(pendingTestAccount.id, access)?.status, 'pending_test', '待测试账户到达计划开始边界后仍应保持待测试')

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

  console.log('账户时间计划回归通过：保存、列表展示、网关候选过滤、清空计划、边界同步、手动启停和保护状态符合预期')
} finally {
  try {
    databaseModule.getBusinessDatabase().close()
    databaseModule.closeStorageDatabases()
  } catch {
  }
  rmSync(tempRoot, { recursive: true, force: true })
}

function accountNextCheckAt(database: ReturnType<typeof import('../../storage/database.js').getBusinessDatabase>, id: string): string | null {
  const row = database
    .prepare('SELECT availability_schedule_next_check_at AS next_check_at FROM accounts WHERE id = ?')
    .get(id) as { next_check_at?: string | null } | undefined
  return row?.next_check_at ?? null
}

function withMockedNow<T>(nowMs: number, fn: () => T): T {
  const OriginalDate = Date
  const MockedDate = class extends OriginalDate {
    constructor(value?: string | number | Date, month?: number, date?: number, hours?: number, minutes?: number, seconds?: number, ms?: number) {
      if (arguments.length === 0) {
        super(nowMs)
        return
      }
      if (arguments.length === 1) {
        super(value as string | number | Date)
        return
      }
      super(value as number, month as number, date, hours, minutes, seconds, ms)
    }

    static now(): number {
      return nowMs
    }
  }
  Object.defineProperty(globalThis, 'Date', {
    configurable: true,
    writable: true,
    value: MockedDate
  })
  try {
    return fn()
  } finally {
    Object.defineProperty(globalThis, 'Date', {
      configurable: true,
      writable: true,
      value: OriginalDate
    })
  }
}
