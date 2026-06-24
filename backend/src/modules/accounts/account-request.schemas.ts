import { z } from 'zod'

export const accountModelMappingSchema = z.object({
  sourceModel: z.string().trim().min(1),
  sourceEndpointFamily: z.enum(['chat_completions', 'responses']),
  upstreamModel: z.string().trim().min(1),
  upstreamEndpointFamily: z.enum(['chat_completions', 'responses', 'messages']),
  enabled: z.boolean().optional()
}).strict()

export const accountCreateSchema = z.object({
  providerCode: z.string().trim().min(1),
  providerProtocolProfileId: z.string().trim().min(1).optional(),
  connectionType: z.string().trim().min(1).optional(),
  name: z.string().trim().min(1),
  type: z.string().trim().min(1),
  credentials: z.record(z.unknown()).optional(),
  supportedModels: z.array(z.string().trim().min(1)).max(500).optional(),
  modelMappings: z.array(accountModelMappingSchema).max(500).optional(),
  tags: z.array(z.string().trim()).max(24).optional(),
  status: z.enum(['active', 'pending_test', 'disabled', 'error', 'rate_limited', 'temporary_unavailable']).optional(),
  activationTestTaskId: z.string().trim().min(1).optional(),
  clientCompatibility: z.enum(['openai_standard', 'codex_responses']).optional(),
  concurrencyLimit: z.number().int().min(1).optional(),
  priority: z.number().int().optional(),
  superPriorityEnabled: z.boolean().optional(),
  fallbackEnabled: z.boolean().optional(),
  proxyProfileId: z.string().optional(),
  schedulable: z.boolean().optional(),
  groupId: z.string().nullable().optional(),
  accountExpiresAt: z.string().nullable().optional(),
  availabilitySchedule: z.record(z.string(), z.unknown()).nullable().optional(),
  availabilityScheduleActive: z.boolean().optional(),
  notes: z.string().optional()
}).strict()

export const accountUpdateSchema = z.object({
  name: z.string().trim().min(1).optional(),
  credentials: z.record(z.unknown()).optional(),
  supportedModels: z.array(z.string().trim().min(1)).max(500).optional(),
  modelMappings: z.array(accountModelMappingSchema).max(500).optional(),
  tags: z.array(z.string().trim()).max(24).optional(),
  status: z.enum(['active', 'pending_test', 'disabled', 'error', 'rate_limited', 'temporary_unavailable']).optional(),
  clientCompatibility: z.enum(['openai_standard', 'codex_responses']).optional(),
  concurrencyLimit: z.number().int().min(1).optional(),
  priority: z.number().int().min(0).optional(),
  superPriorityEnabled: z.boolean().optional(),
  fallbackEnabled: z.boolean().optional(),
  proxyProfileId: z.string().nullable().optional(),
  schedulable: z.boolean().optional(),
  groupId: z.string().trim().min(1, '账户分组不能为空').optional(),
  accountExpiresAt: z.string().nullable().optional(),
  availabilitySchedule: z.record(z.string(), z.unknown()).nullable().optional(),
  availabilityScheduleActive: z.boolean().optional(),
  notes: z.string().optional(),
  clearFailureState: z.boolean().optional()
}).strict()

export const accountDraftTestAccountSchema = z.object({
  providerCode: z.string().trim().min(1),
  providerProtocolProfileId: z.string().trim().min(1).optional(),
  connectionType: z.string().trim().min(1).optional(),
  name: z.string().trim().min(1),
  type: z.string().trim().min(1),
  credentials: z.record(z.unknown()).optional(),
  clientCompatibility: z.enum(['openai_standard', 'codex_responses']).optional(),
  supportedModels: z.array(z.string().trim().min(1)).max(500).optional(),
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
  prompt: z.string().trim().optional(),
  clientCompatibility: z.enum(['openai_standard', 'codex_responses']).optional(),
  testSessionId: z.string().trim().min(1).optional(),
  account: accountDraftTestAccountSchema.optional()
}).strict().optional()

export const accountDraftTestSchema = z.object({
  account: accountDraftTestAccountSchema,
  model: z.string().trim().optional(),
  prompt: z.string().trim().optional(),
  testSessionId: z.string().trim().min(1).optional(),
  clientCompatibility: z.enum(['openai_standard', 'codex_responses']).optional()
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

export const accountImportRequestSchema = z.object({
  data: z.unknown(),
  options: z.object({
    createMissingGroups: z.boolean().optional(),
    createMissingProxies: z.boolean().optional(),
    skipDuplicates: z.boolean().optional()
  }).strict().optional()
}).strict()
