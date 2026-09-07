import assert from 'node:assert/strict'
import { mkdirSync, rmSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join, resolve } from 'node:path'

import { createApiKeyRecordWithRouteStrategy } from '../shared/route-strategy-fixture.js'
import { runtimeConfig } from '../../config/runtime.js'
import { DEFAULT_GPT_GROUP } from '../../storage/schema-defaults.js'

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

const rangedCrossDaySchedule = normalizeApiKeyAvailabilitySchedule({
  enabled: true,
  timezone: 'UTC',
  mode: 'allow_windows',
  dateRange: { startDate: '2026-06-01', endDate: '2026-06-01' },
  windows: [
    { daysOfWeek: [1], start: '22:00', end: '02:00' }
  ]
})

assert.equal(
  evaluateApiKeyAvailabilitySchedule(rangedCrossDaySchedule, new Date('2026-06-01T21:30:00.000Z')).allowed,
  false,
  '跨天日期范围开始窗口前不应允许调用'
)
assert.equal(
  evaluateApiKeyAvailabilitySchedule(rangedCrossDaySchedule, new Date('2026-06-01T22:30:00.000Z')).allowed,
  true,
  '跨天日期范围内的开始日期夜间应允许调用'
)
assert.equal(
  evaluateApiKeyAvailabilitySchedule(rangedCrossDaySchedule, new Date('2026-06-02T01:30:00.000Z')).allowed,
  true,
  '跨天日期范围应允许最后一个开始日期延续到次日凌晨'
)
assert.equal(
  dueApiKeyAvailabilityScheduleEvent(rangedCrossDaySchedule, new Date('2026-06-02T02:00:00.000Z'))?.status,
  'disabled',
  '跨天日期范围最后一段次日结束边界应触发停用事件'
)
assert.equal(
  evaluateApiKeyAvailabilitySchedule(normalizeApiKeyAvailabilitySchedule({
    enabled: true,
    timezone: 'UTC',
    mode: 'allow_windows',
    dateRange: { startDate: '2026-06-02', endDate: '2026-06-02' },
    windows: [
      { daysOfWeek: [1], start: '22:00', end: '02:00' }
    ]
  }), new Date('2026-06-02T01:30:00.000Z')).allowed,
  false,
  '跨天日期范围开始日前一晚的尾段不应在开始日凌晨误放行'
)

const overlappingSchedule = normalizeApiKeyAvailabilitySchedule({
  enabled: true,
  timezone: 'UTC',
  mode: 'allow_windows',
  windows: [
    { daysOfWeek: [1], start: '09:00', end: '12:00' },
    { daysOfWeek: [1], start: '10:00', end: '14:00' }
  ]
})
assert.equal(
  dueApiKeyAvailabilityScheduleEvent(overlappingSchedule, new Date('2026-06-01T12:00:00.000Z'))?.status,
  'active',
  '重叠时段中较短窗口结束时仍处于另一个允许窗口内，状态不应被错误关闭'
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

const crossDayExceptionSchedule = normalizeApiKeyAvailabilitySchedule({
  enabled: true,
  timezone: 'UTC',
  mode: 'allow_windows',
  windows: [
    { daysOfWeek: [1, 2, 3, 4, 5, 6, 7], start: '10:00', end: '11:00' }
  ],
  exceptions: [{ date: '2026-06-01', action: 'allow', windows: [{ start: '22:00', end: '02:00' }] }]
})
assert.equal(
  evaluateApiKeyAvailabilitySchedule(crossDayExceptionSchedule, new Date('2026-06-02T01:30:00.000Z')).allowed,
  true,
  '允许例外的跨天时段应延续到次日凌晨'
)
assert.equal(
  dueApiKeyAvailabilityScheduleEvent(crossDayExceptionSchedule, new Date('2026-06-02T02:00:00.000Z'))?.status,
  'disabled',
  '允许例外的跨天时段应在次日结束边界停用'
)

await assertApiKeyScheduleStatusSyncAndGatewayGuard()

console.log('API Key 时间计划回归通过：日常时段、跨天时段、模式、空值、例外、非法参数、边界同步、人工启用/停用和热链路不解析计划符合预期')

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
      providerCode: DEFAULT_GPT_GROUP.providerCode,
      enabled: true
    }, access)
    const rangedCrossDayKey = await withMockedNow(Date.parse('2026-06-01T21:59:00.000Z'), () => createApiKeyRecordWithRouteStrategy(repositories, {
      name: 'API Key 跨天日期范围回归 Key',
      status: 'active',
      groupBindings: [{ groupId: group.id, priority: 1, status: 'active' }],
      availabilitySchedule: {
        enabled: true,
        timezone: 'UTC',
        mode: 'allow_windows',
        dateRange: { startDate: '2026-06-01', endDate: '2026-06-01' },
        windows: [
          { daysOfWeek: [1], start: '22:00', end: '02:00' }
        ]
      }
    }, access))
    assert.equal(repositories.findApiKeySummary(rangedCrossDayKey.id, access)?.status, 'disabled', '跨天日期范围开始前 API Key 应初始化为停用')
    assert.equal(
      await withMockedNow(Date.parse('2026-06-01T21:59:00.000Z'), () => gatewayApiKeyRepository.validateGatewayApiKey(rangedCrossDayKey.key)),
      undefined,
      '跨天日期范围开始前 API Key 应被网关拒绝'
    )
    const rangedCrossDayStartResult = repositories.syncApiKeyAvailabilityScheduleStatuses(new Date('2026-06-01T22:00:00.000Z'))
    gatewayApiKeyRepository.clearGatewayApiKeyValidationCache()
    assert(rangedCrossDayStartResult.changedIds.includes(rangedCrossDayKey.id), '跨天日期范围开始边界应更新 API Key 状态')
    assert.equal(repositories.findApiKeySummary(rangedCrossDayKey.id, access)?.status, 'active', '跨天日期范围开始边界后 API Key 应启用')
    assert.equal(
      await withMockedNow(Date.parse('2026-06-02T01:30:00.000Z'), () => gatewayApiKeyRepository.validateGatewayApiKey(rangedCrossDayKey.key)?.id),
      rangedCrossDayKey.id,
      '跨天日期范围延续到次日凌晨时 API Key 应通过网关校验'
    )
    const rangedCrossDayEndResult = repositories.syncApiKeyAvailabilityScheduleStatuses(new Date('2026-06-02T02:00:00.000Z'))
    gatewayApiKeyRepository.clearGatewayApiKeyValidationCache()
    assert(rangedCrossDayEndResult.changedIds.includes(rangedCrossDayKey.id), '跨天日期范围次日结束边界应更新 API Key 状态')
    assert.equal(repositories.findApiKeySummary(rangedCrossDayKey.id, access)?.status, 'disabled', '跨天日期范围次日结束边界后 API Key 应停用')
    assert.equal(
      await withMockedNow(Date.parse('2026-06-02T02:01:00.000Z'), () => gatewayApiKeyRepository.validateGatewayApiKey(rangedCrossDayKey.key)),
      undefined,
      '跨天日期范围次日结束边界后 API Key 应被网关拒绝'
    )

    const beforeStartAt = Date.parse('2026-05-31T13:59:00.000Z')
    const startBoundaryAt = Date.parse('2026-05-31T14:00:00.000Z')
    const allowedAt = Date.parse('2026-05-31T14:30:00.000Z')
    const endBoundaryAt = Date.parse('2026-05-31T15:55:00.000Z')
    const afterEndAt = Date.parse('2026-05-31T15:56:00.000Z')
    const nextEndBoundaryAt = Date.parse('2026-06-01T15:55:00.000Z')
    const apiKey = await withMockedNow(beforeStartAt, () => createApiKeyRecordWithRouteStrategy(repositories, {
      name: 'API Key 时间计划边界回归 Key',
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
    }, access))

    const initialSummary = repositories.findApiKeySummary(apiKey.id, access)
    assert.equal(initialSummary?.status, 'disabled', '保存计划时应按当前时间初始化 API Key 状态')
    assert.equal(
      apiKeyNextCheckAt(databaseModule.getBusinessDatabase(), apiKey.id),
      new Date(startBoundaryAt).toISOString(),
      '保存计划时应记录下一次边界检查时间，避免后台同步全量扫描'
    )
    assert.equal(
      await withMockedNow(beforeStartAt, () => gatewayApiKeyRepository.validateGatewayApiKey(apiKey.key)),
      undefined,
      '保存计划后处于允许时段外的 API Key 应被网关拒绝'
    )

    const activatedResult = repositories.syncApiKeyAvailabilityScheduleStatuses(new Date(startBoundaryAt))
    gatewayApiKeyRepository.clearGatewayApiKeyValidationCache()
    assert.equal(activatedResult.activated, 1, '开始边界应把 API Key 状态标记为启用')
    const activeSummary = repositories.findApiKeySummary(apiKey.id, access)
    assert.equal(activeSummary?.status, 'active', '计划开始边界后应通过 status 暴露启用')
    assert.equal(
      apiKeyNextCheckAt(databaseModule.getBusinessDatabase(), apiKey.id),
      new Date(endBoundaryAt).toISOString(),
      '开始边界处理后应推进到下一次结束边界'
    )
    assert(repositories.listApiKeysPage(access, { status: 'active', page: 1, pageSize: 20 }).items.some((item) => item.id === apiKey.id), '计划命中且手动启用的 API Key 应归入启用父筛选')
    assert.equal(
      await withMockedNow(allowedAt, () => gatewayApiKeyRepository.validateGatewayApiKey(apiKey.key)?.id),
      apiKey.id,
      'API Key 允许时段内应通过网关校验'
    )

    const earlyClosedAt = allowedAt + 5 * 60_000
    const earlyCloseSyncAt = allowedAt + 6 * 60_000
    const earlyReopenedAt = allowedAt + 7 * 60_000
    const manuallyDeactivated = await withMockedNow(earlyClosedAt, () => repositories.updateApiKey(apiKey.id, { status: 'disabled' }, access))
    assert.equal(manuallyDeactivated?.status, 'disabled', '计划内人工停用应立即改写 API Key 状态')
    assert.equal(
      await withMockedNow(earlyClosedAt, () => gatewayApiKeyRepository.validateGatewayApiKey(apiKey.key)),
      undefined,
      '计划内人工停用后应立即被网关拒绝'
    )

    const earlyCloseNonBoundaryResult = repositories.syncApiKeyAvailabilityScheduleStatuses(new Date(earlyCloseSyncAt))
    gatewayApiKeyRepository.clearGatewayApiKeyValidationCache()
    assert.equal(earlyCloseNonBoundaryResult.activated, 0, '非边界同步不应把人工停用再次打开')
    assert.equal(repositories.findApiKeySummary(apiKey.id, access)?.status, 'disabled', '非边界同步后人工停用状态应保留')
    assert.equal(
      await withMockedNow(earlyCloseSyncAt, () => gatewayApiKeyRepository.validateGatewayApiKey(apiKey.key)),
      undefined,
      '非边界同步后人工停用仍应被网关拒绝'
    )

    const manuallyReopened = await withMockedNow(earlyReopenedAt, () => repositories.updateApiKey(apiKey.id, { status: 'active' }, access))
    assert.equal(manuallyReopened?.status, 'active', '计划内人工停用后应支持再次启用')
    assert.equal(
      await withMockedNow(earlyReopenedAt, () => gatewayApiKeyRepository.validateGatewayApiKey(apiKey.key)?.id),
      apiKey.id,
      '计划内再次启用后应通过网关校验'
    )

    const disabledResult = repositories.syncApiKeyAvailabilityScheduleStatuses(new Date(endBoundaryAt))
    gatewayApiKeyRepository.clearGatewayApiKeyValidationCache()
    assert.equal(disabledResult.disabled, 1, '结束边界应把 API Key 状态标记为停用')
    const inactiveSummary = repositories.findApiKeySummary(apiKey.id, access)
    assert.equal(inactiveSummary?.status, 'disabled', '时间计划结束边界后应通过 status 暴露停用')
    assert.equal(
      apiKeyNextCheckAt(databaseModule.getBusinessDatabase(), apiKey.id),
      new Date(Date.parse('2026-06-01T14:00:00.000Z')).toISOString(),
      '结束边界处理后应推进到下一次开始边界'
    )
    assert(repositories.listApiKeysPage(access, { status: 'disabled', page: 1, pageSize: 20 }).items.some((item) => item.id === apiKey.id), '时间计划外 API Key 应归入停用父筛选')
    assert(!repositories.listApiKeysPage(access, { status: 'active', page: 1, pageSize: 20 }).items.some((item) => item.id === apiKey.id), '时间计划外 API Key 不应归入启用父筛选')
    assert.equal(
      await withMockedNow(afterEndAt, () => gatewayApiKeyRepository.validateGatewayApiKey(apiKey.key)),
      undefined,
      '结束边界后网关应拒绝时段外 API Key'
    )

    const manuallyActivated = await withMockedNow(afterEndAt, () => repositories.updateApiKey(apiKey.id, { status: 'active' }, access))
    assert.equal(manuallyActivated?.status, 'active', '时间计划外人工启用应立即改写状态')
    assert.equal(
      await withMockedNow(afterEndAt, () => gatewayApiKeyRepository.validateGatewayApiKey(apiKey.key)?.id),
      apiKey.id,
      '时间计划外人工启用后应立即通过网关校验'
    )

    const nonBoundaryResult = repositories.syncApiKeyAvailabilityScheduleStatuses(new Date(afterEndAt))
    gatewayApiKeyRepository.clearGatewayApiKeyValidationCache()
    assert.equal(nonBoundaryResult.disabled, 0, '非边界同步不应把人工启用再次关闭')
    assert.equal(repositories.findApiKeySummary(apiKey.id, access)?.status, 'active', '非边界同步后人工启用状态应保留')
    assert.equal(
      await withMockedNow(afterEndAt, () => gatewayApiKeyRepository.validateGatewayApiKey(apiKey.key)?.id),
      apiKey.id,
      '非边界同步后人工启用仍应通过网关校验'
    )

    const nextDisabledResult = repositories.syncApiKeyAvailabilityScheduleStatuses(new Date(nextEndBoundaryAt))
    gatewayApiKeyRepository.clearGatewayApiKeyValidationCache()
    assert.equal(nextDisabledResult.disabled, 1, '下一次结束边界应再次关闭人工启用的 API Key')
    assert.equal(
      await withMockedNow(nextEndBoundaryAt + 60_000, () => gatewayApiKeyRepository.validateGatewayApiKey(apiKey.key)),
      undefined,
      '下一次结束边界后人工启用的 API Key 应重新被网关拒绝'
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

function apiKeyNextCheckAt(database: ReturnType<typeof import('../../storage/database.js').getBusinessDatabase>, id: string): string | null {
  const row = database
    .prepare('SELECT availability_schedule_next_check_at AS next_check_at FROM api_keys WHERE id = ?')
    .get(id) as { next_check_at?: string | null } | undefined
  return row?.next_check_at ?? null
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
