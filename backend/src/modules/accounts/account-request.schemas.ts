import { z } from 'zod'

const accountTestEndpointModeSchema = z.enum([
  'chat_json',
  'chat_sse',
  'responses_json',
  'responses_sse',
  'messages_json',
  'messages_sse',
  'generate_content_json',
  'generate_content_sse'
])

const accountSupportedEndpointModeSchema = z.enum([
  'chat_json',
  'chat_sse',
  'responses_json',
  'responses_sse',
  'messages_json',
  'messages_sse',
  'message_token_counting',
  'generate_content_json',
  'generate_content_sse',
  'count_tokens',
  'embed_content'
])

function batchUpdateFieldSchema<T extends z.ZodTypeAny>(valueSchema: T) {
  return z.discriminatedUnion('enabled', [
    z.object({
      enabled: z.literal(false)
    }).strict(),
    z.object({
      enabled: z.literal(true),
      value: valueSchema
    }).strict()
  ])
}

export const accountModelMappingSchema = z.object({
  sourceModel: z.string().trim().min(1),
  sourceEndpointFamily: z.enum(['chat_completions', 'responses', 'messages', 'generate_content', 'stream_generate_content']),
  upstreamModel: z.string().trim().min(1),
  upstreamEndpointFamily: z.enum(['chat_completions', 'responses', 'messages', 'generate_content']),
  enabled: z.boolean().optional()
}).strict()

export const accountCreateSchema = z.object({
  providerCode: z.string().trim().min(1),
  providerProtocolProfileId: z.string().trim().min(1),
  name: z.string().trim().min(1),
  type: z.string().trim().min(1),
  credentials: z.record(z.unknown()).optional(),
  supportedModels: z.array(z.string().trim().min(1)).min(1).max(500).optional(),
  healthCheckModel: z.string().trim().min(1).optional(),
  modelMappings: z.array(accountModelMappingSchema).max(500).optional(),
  tags: z.array(z.string().trim()).max(24).optional(),
  status: z.enum(['active', 'pending_test', 'disabled', 'error', 'rate_limited', 'temporary_unavailable']).optional(),
  concurrencyLimit: z.number().int().min(1).optional(),
  priority: z.number().int().optional(),
  superPriorityEnabled: z.boolean().optional(),
  fallbackEnabled: z.boolean().optional(),
  proxyProfileId: z.string().optional(),
  schedulable: z.boolean().optional(),
  groupId: z.string().nullable().optional(),
  accountExpiresAt: z.string().nullable().optional(),
  availabilitySchedule: z.record(z.string(), z.unknown()).nullable().optional(),
  notes: z.string().optional()
}).strict()

export const accountUpdateSchema = z.object({
  name: z.string().trim().min(1).optional(),
  credentials: z.record(z.unknown()).optional(),
  supportedModels: z.array(z.string().trim().min(1)).min(1).max(500).optional(),
  healthCheckModel: z.string().trim().min(1).optional(),
  modelMappings: z.array(accountModelMappingSchema).max(500).optional(),
  tags: z.array(z.string().trim()).max(24).optional(),
  status: z.enum(['active', 'pending_test', 'disabled', 'error', 'rate_limited', 'temporary_unavailable']).optional(),
  concurrencyLimit: z.number().int().min(1).optional(),
  priority: z.number().int().min(0).optional(),
  superPriorityEnabled: z.boolean().optional(),
  fallbackEnabled: z.boolean().optional(),
  proxyProfileId: z.string().nullable().optional(),
  schedulable: z.boolean().optional(),
  groupId: z.string().trim().min(1, '账户分组不能为空').optional(),
  accountExpiresAt: z.string().nullable().optional(),
  availabilitySchedule: z.record(z.string(), z.unknown()).nullable().optional(),
  notes: z.string().optional(),
  clearFailureState: z.boolean().optional()
}).strict()

export const accountDraftTestAccountSchema = z.object({
  providerCode: z.string().trim().min(1),
  providerProtocolProfileId: z.string().trim().min(1),
  name: z.string().trim().min(1),
  type: z.string().trim().min(1),
  credentials: z.record(z.unknown()).optional(),
  supportedModels: z.array(z.string().trim().min(1)).min(1).max(500).optional(),
  healthCheckModel: z.string().trim().min(1),
  modelMappings: z.array(accountModelMappingSchema).max(500).optional(),
  concurrencyLimit: z.number().int().min(1).optional(),
  priority: z.number().int().min(0).optional(),
  superPriorityEnabled: z.boolean().optional(),
  fallbackEnabled: z.boolean().optional(),
  proxyProfileId: z.string().nullable().optional(),
  groupId: z.string().trim().min(1),
  accountExpiresAt: z.string().nullable().optional(),
  availabilitySchedule: z.record(z.string(), z.unknown()).nullable().optional(),
  notes: z.string().optional()
}).strict()

export const accountTestSchema = z.object({
  model: z.string().trim().optional(),
  testEndpointMode: accountTestEndpointModeSchema.optional(),
  prompt: z.string().trim().optional(),
  testSessionId: z.string().trim().min(1).optional(),
  account: accountDraftTestAccountSchema.optional()
}).strict().optional()

export const accountHealthCheckModelSchema = z.object({
  model: z.string().trim().min(1),
  ensureSupportedModel: z.boolean().optional()
}).strict()

export const accountDraftTestSchema = z.object({
  account: accountDraftTestAccountSchema,
  testEndpointMode: accountTestEndpointModeSchema.optional(),
  prompt: z.string().trim().optional(),
  testSessionId: z.string().trim().min(1).optional()
}).strict()

export const accountGroupSchema = z.object({
  groupId: z.string().trim().min(1, '分组不能为空')
}).strict()

export const accountTagsUpdateSchema = z.object({
  tags: z.array(z.string().trim()).max(24)
}).strict()

export const accountTrafficMigrationSchema = z.object({
  targetAccountId: z.string().trim().min(1, '目标账户不能为空'),
  sourceStatus: z.enum(['temporary_unavailable', 'disabled', 'unchanged']).optional()
}).strict()

export const authorizedAccountDispatchSchema = z.object({
  status: z.enum(['active', 'disabled']).optional(),
  priority: z.number().int().min(0).optional(),
  superPriorityEnabled: z.boolean().optional(),
  fallbackEnabled: z.boolean().optional(),
  clearFailureState: z.boolean().optional()
}).strict()

export const accountBatchEditContextSchema = z.object({
  accountIds: z.array(z.string().trim().min(1)).min(2).max(100)
}).strict().superRefine((value, context) => {
  if (new Set(value.accountIds).size !== value.accountIds.length) {
    context.addIssue({
      code: z.ZodIssueCode.custom,
      path: ['accountIds'],
      message: '批量编辑账户不能重复'
    })
  }
})

export const accountBatchEditSchema = z.object({
  targets: z.array(z.object({
    accountId: z.string().trim().min(1),
    configRevision: z.number().int().min(1)
  }).strict()).min(2).max(100),
  updates: z.object({
    tags: batchUpdateFieldSchema(z.array(z.string().trim()).max(24)).optional(),
    proxyProfileId: batchUpdateFieldSchema(z.string().trim().min(1).nullable()).optional(),
    concurrencyLimit: batchUpdateFieldSchema(z.number().int().min(1)).optional(),
    priority: batchUpdateFieldSchema(z.number().int().min(0)).optional(),
    superPriorityEnabled: batchUpdateFieldSchema(z.boolean()).optional(),
    fallbackEnabled: batchUpdateFieldSchema(z.boolean()).optional(),
    accountExpiresAt: batchUpdateFieldSchema(z.string().trim().min(1).nullable()).optional(),
    availabilitySchedule: batchUpdateFieldSchema(z.record(z.string(), z.unknown()).nullable()).optional(),
    notes: batchUpdateFieldSchema(z.string()).optional(),
    errorHandlingRules: batchUpdateFieldSchema(z.array(z.unknown()).max(100)).optional(),
    responseInspectionRules: batchUpdateFieldSchema(z.array(z.unknown()).max(20)).optional(),
    supportedModels: batchUpdateFieldSchema(z.array(z.string().trim().min(1)).min(1).max(500)).optional(),
    healthCheckModel: batchUpdateFieldSchema(z.string().trim().min(1)).optional(),
    modelMappings: batchUpdateFieldSchema(z.array(accountModelMappingSchema).max(500)).optional(),
    supportedEndpointModes: batchUpdateFieldSchema(z.array(accountSupportedEndpointModeSchema).min(1).max(11)).optional(),
    serviceTierOverride: batchUpdateFieldSchema(
      z.union([z.enum(['default', 'priority', 'flex']), z.literal('')]).nullable()
    ).optional(),
    reasoningEffortOverride: batchUpdateFieldSchema(
      z.union([z.enum(['none', 'minimal', 'low', 'medium', 'high', 'xhigh', 'max']), z.literal('')]).nullable()
    ).optional()
  }).strict()
}).strict().superRefine((value, context) => {
  const accountIds = value.targets.map((target) => target.accountId)
  if (new Set(accountIds).size !== accountIds.length) {
    context.addIssue({
      code: z.ZodIssueCode.custom,
      path: ['targets'],
      message: '批量编辑账户不能重复'
    })
  }
  if (!Object.values(value.updates).some((update) => update?.enabled)) {
    context.addIssue({
      code: z.ZodIssueCode.custom,
      path: ['updates'],
      message: '请至少选择一项需要覆盖的配置'
    })
  }
})

export type AccountBatchEditRequest = z.infer<typeof accountBatchEditSchema>
export type AccountBatchEditContextRequest = z.infer<typeof accountBatchEditContextSchema>

export const accountImportRequestSchema = z.object({
  data: z.unknown(),
  options: z.object({
    createMissingGroups: z.boolean().optional(),
    createMissingProxies: z.boolean().optional(),
    skipDuplicates: z.boolean().optional()
  }).strict().optional()
}).strict()
