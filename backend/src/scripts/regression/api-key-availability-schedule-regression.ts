import assert from 'node:assert/strict'
import { mkdirSync, rmSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join, resolve } from 'node:path'

import { runtimeConfig } from '../../config/runtime.js'

import {
  dueApiKeyAvailabilityScheduleEvent,
  apiKeyAvailabilityScheduleStatus,
  evaluateApiKeyAvailabilitySchedule,
  normalizeApiKeyAvailabilitySchedule
} from '../../storage/api-key-availability-schedule.js'

const dailySchedule = normalizeApiKeyAvailabilitySchedule({
  enabled: true,
  timezone: 'Asia/Shanghai',
  mode: 'allow_windows',
  windows: [
    { daysOfWeek: [1, 2, 3, 4, 5, 6, 7], start: '22:00', end: '23:55' }
  ]
})

assert.equal(
  evaluateApiKeyAvailabilitySchedule(dailySchedule, new Date('2026-05-31T14:30:00.000Z')).allowed,
  true,
  '每天 22:00-23:55 内应允许调用'
)
assert.equal(
  apiKeyAvailabilityScheduleStatus(dailySchedule, new Date('2026-05-31T14:30:00.000Z')),
  'active',
  '允许时段内应映射为 API Key 启用状态'
)
assert.equal(
  dueApiKeyAvailabilityScheduleEvent(dailySchedule, new Date('2026-05-31T14:00:00.000Z'))?.status,
  'active',
  '开始边界分钟应触发一次启用事件'
)
assert.equal(
  dueApiKeyAvailabilityScheduleEvent(dailySchedule, new Date('2026-05-31T14:30:00.000Z')),
  undefined,
  '窗口中间不应持续触发启用事件'
)
assert.equal(
  evaluateApiKeyAvailabilitySchedule(dailySchedule, new Date('2026-05-31T15:56:00.000Z')).allowed,
  false,
  '每天 23:55 后应进入时段外'
)
assert.equal(
  apiKeyAvailabilityScheduleStatus(dailySchedule, new Date('2026-05-31T15:56:00.000Z')),
  'disabled',
  '允许时段外应映射为 API Key 停用状态'
)
assert.equal(
  dueApiKeyAvailabilityScheduleEvent(dailySchedule, new Date('2026-05-31T15:55:00.000Z'))?.status,
  'disabled',
  '结束边界分钟应触发一次停用事件'
)

const crossDaySchedule = normalizeApiKeyAvailabilitySchedule({
  enabled: true,
  timezone: 'Asia/Shanghai',
  mode: 'allow_windows',
  windows: [
    { daysOfWeek: [7], start: '22:00', end: '02:00' }
  ]
})

assert.equal(
  evaluateApiKeyAvailabilitySchedule(crossDaySchedule, new Date('2026-05-31T17:30:00.000Z')).allowed,
  true,
  '周日 22:00-次日 02:00 的次日凌晨部分应允许调用'
)
assert.equal(
  evaluateApiKeyAvailabilitySchedule(crossDaySchedule, new Date('2026-05-31T19:00:00.000Z')).allowed,
  false,
  '跨天时段结束后应进入时段外'
)

assert.throws(
  () => normalizeApiKeyAvailabilitySchedule({
    enabled: true,
    timezone: 'Asia/Shanghai',
    mode: 'allow_windows',
    windows: [
      { daysOfWeek: [1], start: '22:00', end: '22:00' }
    ]
  }),
  /开始时间和停止时间不能相同/,
  '开始时间和停止时间相同时应拒绝保存'
)

assert.throws(
  () => normalizeApiKeyAvailabilitySchedule({
    enabled: true,
    timezone: 'Asia/Shanghai',
    mode: 'allow_windows',
    windows: [
      { daysOfWeek: [1, 9], start: '22:00', end: '23:55' }
    ]
  }),
  /重复日期无效/,
  '重复日期包含非法星期时应拒绝保存'
)

assert.throws(
  () => normalizeApiKeyAvailabilitySchedule({
    enabled: true,
    timezone: 'Asia/Shanghai',
    windows: [
      { daysOfWeek: [1], start: '22:00', end: '23:55' }
    ]
  }),
  /模式必须为 allow_windows/,
  '时间计划必须显式提交当前模式'
)

assert.throws(
  () => normalizeApiKeyAvailabilitySchedule({
    enabled: true,
    timezone: '',
    mode: 'allow_windows',
    windows: [
      { daysOfWeek: [1], start: '22:00', end: '23:55' }
    ]
  }),
  /时区不能为空/,
  '时间计划显式空时区不能静默回退默认时区'
)

assert.throws(
  () => normalizeApiKeyAvailabilitySchedule({
    enabled: true,
    timezone: 'Asia/Shanghai',
    mode: 'allow_windows',
    windows: [
      { daysOfWeek: [1], start: '22:00', end: '23:55' }
    ],
    dateRange: null
  }),
  /生效日期范围无效/,
  '时间计划显式 null 日期范围不能静默当作未配置'
)

assert.throws(
  () => normalizeApiKeyAvailabilitySchedule({
    enabled: true,
    timezone: 'Asia/Shanghai',
    mode: 'allow_windows',
    windows: [
      { daysOfWeek: [1], start: '22:00', end: '23:55' }
    ],
    exceptions: ''
  }),
  /例外日期无效/,
  '时间计划显式空字符串例外日期不能静默当作未配置'
)

assert.throws(
  () => normalizeApiKeyAvailabilitySchedule({
    enabled: true,
    timezone: 'Asia/Shanghai',
    mode: 'allow_windows',
    windows: [
      { daysOfWeek: [1], start: '22:00', end: '23:55' }
    ],
    exceptions: [{ date: '2026-06-01', action: 'allow' }]
  }),
  /允许例外至少需要一个允许时段/,
  '允许例外必须显式配置至少一个允许时段'
)

assert.throws(
  () => normalizeApiKeyAvailabilitySchedule({
    enabled: true,
    timezone: 'Asia/Shanghai',
    mode: 'allow_windows',
    windows: [
      { daysOfWeek: [1], start: '22:00', end: '23:55' }
    ],
    exceptions: [{ date: '2026-06-01', action: 'deny', windows: [{ start: '10:00', end: '11:00' }] }]
  }),
  /拒绝例外不能配置允许时段/,
  '拒绝例外不能携带无效允许时段'
)

const exceptionSchedule = normalizeApiKeyAvailabilitySchedule({
  enabled: true,
  timezone: 'UTC',
  mode: 'allow_windows',
  windows: [
    { daysOfWeek: [1, 2, 3, 4, 5, 6, 7], start: '22:00', end: '23:55' }
  ],
  exceptions: [{ date: '2026-06-01', action: 'allow', windows: [{ start: '10:00', end: '11:00' }] }]
})
assert.equal(
  evaluateApiKeyAvailabilitySchedule(exceptionSchedule, new Date('2026-06-01T10:30:00.000Z')).allowed,
  true,
  '合法允许例外应按例外时段放行'
)

await assertApiKeyScheduleStatusSyncAndGatewayGuard()

console.log('API Key 时间计划回归通过：日常时段、跨天时段、模式、空值、例外、非法参数、派生状态同步和热链路不解析计划符合预期')

async function assertApiKeyScheduleStatusSyncAndGatewayGuard(): Promise<void> {
  const tempRoot = resolve(tmpdir(), `juhe-ai-api-key-availability-schedule-${Date.now()}-${Math.random().toString(16).slice(2)}`)
  runtimeConfig.databasePath = join(tempRoot, 'business.sqlite3')
  runtimeConfig.datasetDatabasePath = join(tempRoot, 'dataset.sqlite3')
  runtimeConfig.statsDatabasePath = join(tempRoot, 'stats.sqlite3')
  runtimeConfig.secret = 'api-key-availability-schedule-secret'
  runtimeConfig.log.consoleEnabled = false
  runtimeConfig.log.fileEnabled = false
  runtimeConfig.processRole = 'worker'
  mkdirSync(tempRoot, { recursive: true })

  const [
    { logger },
    databaseModule,
    repositories,
    gatewayApiKeyRepository
  ] = await Promise.all([
    import('../../shared/logger.js'),
    import('../../storage/database.js'),
    import('../../storage/repositories.js'),
    import('../../storage/gateway-api-key.repository.js')
  ])
  logger.level = 'silent'

  try {
    const access = { systemAccountId: 'sys_admin', role: 'admin' as const }
    const group = repositories.createGroup({
      name: 'API Key 时间计划补偿回归分组',
      providerCode: 'gpt',
      enabled: true
    }, access)
    const apiKey = repositories.createApiKeyRecord({
      name: 'API Key 时间计划补偿回归 Key',
      status: 'active',
      groupBindings: [{ groupId: group.id, priority: 1, status: 'active' }],
      availabilitySchedule: {
        enabled: true,
        timezone: 'Asia/Shanghai',
        mode: 'allow_windows',
        windows: [
          { daysOfWeek: [1, 2, 3, 4, 5, 6, 7], start: '22:00', end: '23:55' }
        ]
      }
    }, access)

    const allowedAt = Date.parse('2026-05-31T14:30:00.000Z')
    const missedEndBoundaryAt = Date.parse('2026-05-31T15:56:00.000Z')
    assert.equal(
      await withMockedNow(missedEndBoundaryAt, () => gatewayApiKeyRepository.validateGatewayApiKey(apiKey.key)?.id),
      apiKey.id,
      '后台尚未同步计划派生状态前，网关热链路不解析时间计划，允许存在一个同步周期内的状态延迟'
    )

    const disabledResult = repositories.syncApiKeyAvailabilityScheduleStatuses(new Date(missedEndBoundaryAt))
    gatewayApiKeyRepository.clearGatewayApiKeyValidationCache()
    assert.equal(disabledResult.disabled, 1, '时段外后下一轮同步应把 API Key 计划派生状态标记为不可用')
    const inactiveSummary = repositories.findApiKeySummary(apiKey.id, access)
    assert.equal(inactiveSummary?.status, 'active', '时间计划外不应改写 API Key 手动启停状态')
    assert.equal(inactiveSummary?.availabilityScheduleActive, false, '时间计划外应通过 availabilityScheduleActive 暴露')
    assert(repositories.listApiKeysPage(access, { status: 'disabled', page: 1, pageSize: 20 }).items.some((item) => item.id === apiKey.id), '时间计划外 API Key 应归入停用父筛选')
    assert(!repositories.listApiKeysPage(access, { status: 'active', page: 1, pageSize: 20 }).items.some((item) => item.id === apiKey.id), '时间计划外 API Key 不应归入启用父筛选')
    assert.equal(
      await withMockedNow(missedEndBoundaryAt, () => gatewayApiKeyRepository.validateGatewayApiKey(apiKey.key)),
      undefined,
      '后台同步计划派生状态并清缓存后，网关应拒绝时段外 API Key'
    )

    const activatedResult = repositories.syncApiKeyAvailabilityScheduleStatuses(new Date(allowedAt))
    gatewayApiKeyRepository.clearGatewayApiKeyValidationCache()
    assert.equal(activatedResult.activated, 1, '进入允许时段后下一轮同步应把 API Key 计划派生状态标记为可用')
    const activeSummary = repositories.findApiKeySummary(apiKey.id, access)
    assert.equal(activeSummary?.status, 'active', '计划恢复可用后仍不改写 API Key 手动启停状态')
    assert.equal(activeSummary?.availabilityScheduleActive, true, '计划恢复可用后应通过 availabilityScheduleActive 暴露')
    assert(repositories.listApiKeysPage(access, { status: 'active', page: 1, pageSize: 20 }).items.some((item) => item.id === apiKey.id), '计划命中且手动启用的 API Key 应归入启用父筛选')
    assert.equal(
      await withMockedNow(allowedAt, () => gatewayApiKeyRepository.validateGatewayApiKey(apiKey.key)?.id),
      apiKey.id,
      'API Key 允许时段内应通过网关校验'
    )

    databaseModule.getBusinessDatabase().prepare("UPDATE api_keys SET status = 'disabled' WHERE id = ?").run(apiKey.id)
    gatewayApiKeyRepository.clearGatewayApiKeyValidationCache()
    const manualDisabledAt = allowedAt + 10 * 60_000
    const manualDisabledResult = repositories.syncApiKeyAvailabilityScheduleStatuses(new Date(manualDisabledAt))
    assert.equal(manualDisabledResult.activated, 0, '计划允许窗口内人工停用不应被同步任务再次启用')
    assert.equal(repositories.findApiKeySummary(apiKey.id, access)?.status, 'disabled', '窗口中间人工停用应保持停用状态')
    assert.equal(
      await withMockedNow(manualDisabledAt, () => gatewayApiKeyRepository.validateGatewayApiKey(apiKey.key)),
      undefined,
      '窗口中间人工停用后，即使计划允许也不应通过网关校验'
    )
  } finally {
    try {
      databaseModule.getBusinessDatabase().close()
      databaseModule.closeStorageDatabases()
    } catch {
    }
    rmSync(tempRoot, { recursive: true, force: true })
  }
}

async function withMockedNow<T>(nowMs: number, operation: () => Promise<T> | T): Promise<T> {
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
    return await operation()
  } finally {
    Object.defineProperty(globalThis, 'Date', {
      configurable: true,
      writable: true,
      value: OriginalDate
    })
  }
}


