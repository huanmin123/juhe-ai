import type { AccountAvailabilitySchedule, ApiKeyAvailabilitySchedule } from '@/types/domain'
import { buildAccountAvailabilitySchedulePayload, createAccountAvailabilityScheduleForm, validateAccountAvailabilityScheduleForm, accountScheduleSummary, accountScheduleTagColor } from '@/views/accounts/accountAvailabilitySchedule'
import { apiKeyScheduleSummary } from '@/views/api-keys/apiKeyFormatters'
import { createApiKeyTimeScheduleForm } from '@/views/api-keys/apiKeyFormModel'
import {
  assertTimeSchedule,
  buildTimeSchedulePayload,
  createTimeScheduleForm,
  timeScheduleFormFingerprint,
  timeScheduleSummary,
  validateTimeScheduleForm
} from '@/views/shared/timeSchedule'

function assertEqual(actual: unknown, expected: unknown, message: string): void {
  if (Object.is(actual, expected)) return
  throw new Error(`${message}；实际值：${String(actual)}；预期值：${String(expected)}`)
}

function assertDeepEqual(actual: unknown, expected: unknown, message: string): void {
  const actualText = stableStringify(actual)
  const expectedText = stableStringify(expected)
  if (actualText === expectedText) return
  throw new Error(`${message}；实际值：${actualText}；预期值：${expectedText}`)
}

function assertMatch(actual: string, expected: RegExp, message: string): void {
  if (expected.test(actual)) return
  throw new Error(`${message}；实际值：${actual}`)
}

function assertThrows(run: () => unknown, expected: RegExp, message: string): void {
  try {
    run()
  } catch (error) {
    const errorText = error instanceof Error ? error.message : String(error)
    if (expected.test(errorText)) return
    throw new Error(`${message}；实际错误：${errorText}`)
  }
  throw new Error(`${message}；实际未抛出错误`)
}

function stableStringify(value: unknown): string {
  return JSON.stringify(sortValue(value))
}

function sortValue(value: unknown): unknown {
  if (Array.isArray(value)) {
    return value.map(sortValue)
  }
  if (value && typeof value === 'object') {
    return Object.fromEntries(
      Object.entries(value)
        .sort(([left], [right]) => left.localeCompare(right))
        .map(([key, item]) => [key, sortValue(item)])
    )
  }
  return value
}

const crossDaySchedule: ApiKeyAvailabilitySchedule = {
  enabled: true,
  timezone: 'UTC',
  mode: 'allow_windows',
  windows: [
    { daysOfWeek: [1, 3], start: '22:00', end: '02:00' }
  ],
  dateRange: { startDate: '2026-06-01', endDate: '2026-06-30' },
  exceptions: [
    { date: '2026-06-10', action: 'deny' },
    { date: '2026-06-11', action: 'allow', windows: [{ start: '09:00', end: '11:00' }] }
  ]
}

const emptyForm = createTimeScheduleForm()
assertEqual(emptyForm.enabled, false, '默认时间计划表单应关闭')
assertEqual(buildTimeSchedulePayload(emptyForm), null, '关闭时间计划时保存 payload 应为 null')

const form = createTimeScheduleForm(crossDaySchedule)
assertEqual(form.enabled, true, '已有计划打开编辑时应自动开启表单')
assertEqual(validateTimeScheduleForm(form), undefined, '合法跨天计划应通过前端校验')
assertDeepEqual(buildTimeSchedulePayload(form), crossDaySchedule, '前端保存 payload 应保留日期范围和例外日期')
assertEqual(timeScheduleFormFingerprint(form).includes('2026-06-10'), true, '表单指纹应包含例外日期，避免编辑态误判未变更')
assertEqual(timeScheduleSummary(crossDaySchedule).includes('22:00-次日 02:00'), true, '跨天时段摘要应显示次日')
assertEqual(apiKeyScheduleSummary(crossDaySchedule).includes('22:00-次日 02:00'), true, 'API Key 摘要应展示时间计划规则')
assertEqual(apiKeyScheduleSummary(crossDaySchedule).startsWith('计划窗口内：'), false, 'API Key 摘要不应展示派生可用前缀')
assertEqual(apiKeyScheduleSummary(crossDaySchedule).startsWith('等待窗口开启：'), false, 'API Key 摘要不应展示派生停用前缀')

const duplicateDaysForm = createTimeScheduleForm(crossDaySchedule)
duplicateDaysForm.windows[0].daysOfWeek = [5, 1, 5]
assertDeepEqual(
  buildTimeSchedulePayload(duplicateDaysForm)?.windows[0]?.daysOfWeek,
  [1, 5],
  '保存 payload 应对重复星期去重并排序'
)

const invalidDaysForm = createTimeScheduleForm(crossDaySchedule)
invalidDaysForm.windows[0].daysOfWeek = []
assertMatch(validateTimeScheduleForm(invalidDaysForm) ?? '', /请完整填写第 1 个时段/, '未选择重复日期时应提示具体时段')

const invalidTimeForm = createTimeScheduleForm(crossDaySchedule)
invalidTimeForm.windows[0].end = invalidTimeForm.windows[0].start
assertMatch(validateTimeScheduleForm(invalidTimeForm) ?? '', /请完整填写第 1 个时段/, '开始结束相同时应在前端拦截')

assertThrows(
  () => assertTimeSchedule({ ...crossDaySchedule, legacy: true } as unknown as ApiKeyAvailabilitySchedule),
  /包含未知字段：legacy/,
  '前端读取到未知计划字段时应提示数据异常'
)
assertThrows(
  () => assertTimeSchedule({ ...crossDaySchedule, dateRange: { startDate: '2026-02-31' } }),
  /开始日期无效/,
  '前端读取到非法日期范围时应提示数据异常'
)

const accountForm = createAccountAvailabilityScheduleForm(crossDaySchedule as unknown as AccountAvailabilitySchedule)
assertEqual(validateAccountAvailabilityScheduleForm(accountForm), undefined, '账户计划包装校验应复用公共计划规则')
assertDeepEqual(
  buildAccountAvailabilitySchedulePayload(accountForm),
  crossDaySchedule,
  '账户计划保存 payload 应保留公共计划字段'
)
assertEqual(accountScheduleSummary(crossDaySchedule as unknown as AccountAvailabilitySchedule).includes('次日'), true, '账户计划摘要应复用跨天展示')
assertEqual(accountScheduleTagColor(crossDaySchedule as unknown as AccountAvailabilitySchedule), 'blue', '账户计划标签应使用计划色')

const apiKeyForm = createApiKeyTimeScheduleForm(crossDaySchedule)
assertDeepEqual(
  buildTimeSchedulePayload<ApiKeyAvailabilitySchedule>(apiKeyForm),
  crossDaySchedule,
  'API Key 表单模型应保留完整计划 payload'
)

console.log('前端时间计划回归通过：表单构造、payload、跨天摘要、例外日期保留和非法输入拦截符合预期')
