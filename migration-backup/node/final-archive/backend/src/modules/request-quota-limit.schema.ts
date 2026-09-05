import { z } from 'zod'

import { maxRequestQuotaAmountUsd, maxRequestQuotaHourlyWindowHours } from '../storage/request-quota-limits.js'

const quotaAmountSchema = z.number({
  invalid_type_error: '额度金额必须是数字'
}).positive('额度金额必须大于 0').max(maxRequestQuotaAmountUsd, '额度金额超出上限')

const quotaLimitSchema = z.object({
  enabled: z.literal(true, {
    invalid_type_error: '额度启用状态必须为 true'
  }),
  limit: quotaAmountSchema
}).strict()

const hourlyQuotaLimitSchema = quotaLimitSchema.extend({
  hours: z.number({
    invalid_type_error: '小时额度窗口必须是数字'
  }).int('小时额度窗口必须是整数').min(1, '小时额度窗口必须大于 0').max(maxRequestQuotaHourlyWindowHours, `小时额度窗口不能超过 ${maxRequestQuotaHourlyWindowHours}`)
}).strict()

export const requestQuotaLimitsSchema = z.object({
  hourly: hourlyQuotaLimitSchema.optional(),
  daily: quotaLimitSchema.optional(),
  weekly: quotaLimitSchema.optional(),
  monthly: quotaLimitSchema.optional(),
  total: quotaLimitSchema.optional()
}).strict()
