import { z } from 'zod'

const scheduleTimeSchema = z.string().trim().regex(/^([01]\d|2[0-3]):([0-5]\d)$/, '时间格式应为 HH:mm')
const scheduleDateSchema = z.string().trim().regex(/^\d{4}-\d{2}-\d{2}$/, '日期格式应为 YYYY-MM-DD')
const scheduleModeSchema = z.string({
  required_error: 'API Key 自动启停计划模式必须为 allow_windows',
  invalid_type_error: 'API Key 自动启停计划模式必须为 allow_windows'
}).refine((value) => value === 'allow_windows', 'API Key 自动启停计划模式必须为 allow_windows')

const scheduleWindowSchema = z.object({
  daysOfWeek: z.array(z.number({
    invalid_type_error: '重复日期必须是数字'
  }).int('API Key 自动启停计划重复日期无效').min(1, 'API Key 自动启停计划重复日期无效').max(7, 'API Key 自动启停计划重复日期无效')).min(1, 'API Key 自动启停计划至少需要选择一个重复日期'),
  start: scheduleTimeSchema,
  end: scheduleTimeSchema
}).strict().refine((value) => value.start !== value.end, {
  message: 'API Key 自动启停计划开始时间和停止时间不能相同'
})

const scheduleExceptionWindowSchema = z.object({
  start: scheduleTimeSchema,
  end: scheduleTimeSchema
}).strict().refine((value) => value.start !== value.end, {
  message: 'API Key 自动启停计划开始时间和停止时间不能相同'
})

const scheduleExceptionSchema = z.object({
  date: scheduleDateSchema,
  action: z.enum(['allow', 'deny']),
  windows: z.array(scheduleExceptionWindowSchema).max(32).optional()
}).strict().superRefine((value, context) => {
  if (value.action === 'allow' && !value.windows?.length) {
    context.addIssue({
      code: z.ZodIssueCode.custom,
      path: ['windows'],
      message: 'API Key 自动启停计划允许例外至少需要一个允许时段'
    })
  }
  if (value.action === 'deny' && value.windows !== undefined) {
    context.addIssue({
      code: z.ZodIssueCode.custom,
      path: ['windows'],
      message: 'API Key 自动启停计划拒绝例外不能配置允许时段'
    })
  }
})

export const apiKeyAvailabilityScheduleSchema = z.object({
  enabled: z.literal(true, {
    invalid_type_error: '自动启停计划启用状态必须为 true'
  }),
  timezone: z.string().trim().min(1, 'API Key 自动启停计划时区不能为空').optional(),
  mode: scheduleModeSchema,
  windows: z.array(scheduleWindowSchema).min(1, '自动启停计划至少需要一个允许时段').max(32),
  dateRange: z.object({
    startDate: scheduleDateSchema.optional(),
    endDate: scheduleDateSchema.optional()
  }).strict().optional(),
  exceptions: z.array(scheduleExceptionSchema).max(128).optional()
}).strict()
