import type { Dayjs } from 'dayjs'

import { formatServerDateTimeInput } from '@/shared/formatters'
import type {
  AccountBatchEditContextField,
  AccountBatchEditContextItem,
  AccountBatchEditRequest,
  AccountGptReasoningEffortOverride,
  AccountGptServiceTierOverride,
  AccountHealthCheckEndpointMode,
  AccountModelMapping,
  AccountSupportedEndpointMode
} from '@/types/domain'
import {
  buildAccountAvailabilitySchedulePayload,
  createAccountAvailabilityScheduleForm,
  validateAccountAvailabilityScheduleForm,
  type AccountAvailabilityScheduleForm
} from './accountAvailabilitySchedule'
import {
  buildAccountErrorPolicyPayload,
  validateAccountErrorPolicyRules
} from './accountErrorPolicyPayload'
import type { AccountErrorPolicyRuleForm } from './accountErrorPolicyTypes'
import {
  buildAccountResponseInspectionPayload,
  validateAccountResponseInspectionRules
} from './accountResponseInspectionPolicyPayload'
import type { AccountResponseInspectionRuleForm } from './accountResponseInspectionPolicyTypes'
import { accountModelMappingProtocolValidationMessage } from './accountModelMappingProtocolMatrix'
import type { AccountModelMappingProviderProfile } from './accountModelMappingProtocolMatrix'
import { accountHealthCheckEndpointModeOptions } from './accountHealthCheckEndpointMode'
import {
  validateAccountModelMappings,
  type ModelMappingProtocolOption
} from './accountSavePayload'

export type AccountBatchEditFieldKey =
  | 'tags'
  | 'proxyProfileId'
  | 'concurrencyLimit'
  | 'priority'
  | 'superPriorityEnabled'
  | 'fallbackEnabled'
  | 'accountExpiresAt'
  | 'availabilitySchedule'
  | 'notes'
  | 'errorHandlingRules'
  | 'responseInspectionRules'
  | 'supportedModels'
  | 'healthCheckModel'
  | 'healthCheckEndpointMode'
  | 'modelMappings'
  | 'supportedEndpointModes'
  | 'serviceTierOverride'
  | 'reasoningEffortOverride'

export interface AccountBatchEditForm {
  enabled: Record<AccountBatchEditFieldKey, boolean>
  tags: string[]
  proxyProfileId?: string
  concurrencyLimit: number
  priority: number
  superPriorityEnabled: boolean
  fallbackEnabled: boolean
  accountExpiresAt?: Dayjs | null
  availabilitySchedule: AccountAvailabilityScheduleForm
  notes: string
  errorHandlingRules: AccountErrorPolicyRuleForm[]
  responseInspectionRules: AccountResponseInspectionRuleForm[]
  supportedModels: string[]
  healthCheckModel: string
  healthCheckEndpointMode: AccountHealthCheckEndpointMode
  modelMappings: AccountModelMapping[]
  supportedEndpointModes: AccountSupportedEndpointMode[]
  serviceTierOverride: AccountGptServiceTierOverride
  reasoningEffortOverride: AccountGptReasoningEffortOverride
}

export interface AccountBatchEditBuildResult {
  payload?: AccountBatchEditRequest
  message?: string
}

export interface AccountBatchEditModelMappingOptions {
  mappingAnthropicSourceModelOptions?: ModelMappingProtocolOption[]
  mappingCurrentProviderSourceModelOptions?: ModelMappingProtocolOption[]
  mappingGeminiSourceModelOptions?: ModelMappingProtocolOption[]
  mappingSourceModelOptions?: ModelMappingProtocolOption[]
  mappingUpstreamModelOptions?: ModelMappingProtocolOption[]
  providerCode?: string
  providerProfile?: AccountModelMappingProviderProfile
}

export const accountBatchEditFieldLabels: Record<AccountBatchEditFieldKey, string> = {
  tags: '账户标签',
  proxyProfileId: '代理',
  concurrencyLimit: '并发上限',
  priority: '优先级',
  superPriorityEnabled: '超级优先',
  fallbackEnabled: '降级备用',
  accountExpiresAt: '账户到期时间',
  availabilitySchedule: '时间计划',
  notes: '说明',
  errorHandlingRules: '错误处理策略',
  responseInspectionRules: '响应检查策略',
  supportedModels: '支持模型',
  healthCheckModel: '检查模型',
  healthCheckEndpointMode: '检查请求形态',
  modelMappings: '账号模型别名',
  supportedEndpointModes: '上游接口能力',
  serviceTierOverride: '服务等级',
  reasoningEffortOverride: '思考级别'
}

export function createAccountBatchEditForm(): AccountBatchEditForm {
  return {
    enabled: Object.fromEntries(
      Object.keys(accountBatchEditFieldLabels).map((key) => [key, false])
    ) as Record<AccountBatchEditFieldKey, boolean>,
    tags: [],
    proxyProfileId: undefined,
    concurrencyLimit: 1,
    priority: 0,
    superPriorityEnabled: false,
    fallbackEnabled: false,
    accountExpiresAt: undefined,
    availabilitySchedule: createAccountAvailabilityScheduleForm(),
    notes: '',
    errorHandlingRules: [],
    responseInspectionRules: [],
    supportedModels: [],
    healthCheckModel: '',
    healthCheckEndpointMode: 'chat_json',
    modelMappings: [],
    supportedEndpointModes: [],
    serviceTierOverride: '',
    reasoningEffortOverride: ''
  }
}

export function enabledAccountBatchEditFieldLabels(form: AccountBatchEditForm): string[] {
  return (Object.keys(form.enabled) as AccountBatchEditFieldKey[])
    .filter((key) => form.enabled[key])
    .map((key) => accountBatchEditFieldLabels[key])
}

export function accountBatchEditContextFieldsForForm(
  form: Pick<AccountBatchEditForm, 'enabled'>,
  providerCode?: string
): AccountBatchEditContextField[] {
  const fields = new Set<AccountBatchEditContextField>()
  if (!form.enabled.supportedModels && (
    form.enabled.healthCheckModel
    || form.enabled.modelMappings
    || form.enabled.serviceTierOverride
    || form.enabled.reasoningEffortOverride
  )) {
    fields.add('supportedModels')
  }
  if (!form.enabled.supportedEndpointModes && (
    form.enabled.healthCheckEndpointMode
    || form.enabled.modelMappings
    || ((form.enabled.serviceTierOverride || form.enabled.reasoningEffortOverride)
      && providerCode === 'gemini')
  )) {
    fields.add('supportedEndpointModes')
  }
  if (form.enabled.supportedEndpointModes && !form.enabled.modelMappings) {
    fields.add('modelMappings')
  }
  return [...fields]
}

export function buildAccountBatchEditRequest(
  accounts: AccountBatchEditContextItem[],
  form: AccountBatchEditForm,
  modelMappingOptions: AccountBatchEditModelMappingOptions = {}
): AccountBatchEditBuildResult {
  if (accounts.length < 2 || accounts.length > 100) {
    return { message: '批量编辑一次只能选择 2 到 100 个账户' }
  }
  const fields = enabledAccountBatchEditFieldLabels(form)
  if (!fields.length) return { message: '请至少勾选一项需要覆盖的配置' }
  const targets = accounts.map((account) => ({
    accountId: account.id,
    configRevision: Number(account.configRevision)
  }))
  if (targets.some((target) => !Number.isInteger(target.configRevision) || target.configRevision < 1)) {
    return { message: '账户版本信息缺失，请关闭弹窗并刷新列表后重试' }
  }
  if (form.enabled.concurrencyLimit && (!Number.isInteger(form.concurrencyLimit) || form.concurrencyLimit < 1)) {
    return { message: '并发上限必须是大于 0 的整数' }
  }
  if (form.enabled.priority && (!Number.isInteger(form.priority) || form.priority < 0)) {
    return { message: '优先级必须是大于等于 0 的整数' }
  }
  if (
    form.enabled.superPriorityEnabled
    && form.enabled.fallbackEnabled
    && form.superPriorityEnabled
    && form.fallbackEnabled
  ) {
    return { message: '超级优先和降级备用不能同时开启' }
  }
  if (form.enabled.tags && normalizedTextList(form.tags).length > 24) {
    return { message: '单个账户最多配置 24 个标签' }
  }
  if (form.enabled.availabilitySchedule) {
    const scheduleMessage = validateAccountAvailabilityScheduleForm(form.availabilitySchedule)
    if (scheduleMessage) return { message: scheduleMessage }
  }
  if (form.enabled.errorHandlingRules) {
    const validation = validateAccountErrorPolicyRules(form.errorHandlingRules)
    if (!validation.valid) return { message: validation.message ?? '错误处理策略无效' }
  }
  if (form.enabled.responseInspectionRules) {
    const validation = validateAccountResponseInspectionRules(form.responseInspectionRules)
    if (!validation.valid) return { message: validation.message ?? '响应检查策略无效' }
  }
  const supportedModels = normalizedTextList(form.supportedModels)
  if (form.enabled.supportedModels && !supportedModels.length) {
    return { message: '批量覆盖支持模型时至少选择一个模型' }
  }
  const healthCheckModel = form.healthCheckModel.trim()
  if (form.enabled.healthCheckModel && !healthCheckModel) {
    return { message: '请选择检查模型' }
  }
  if (
    form.enabled.supportedModels
    && form.enabled.healthCheckModel
    && !supportedModels.includes(healthCheckModel)
  ) {
    return { message: '检查模型必须属于本次覆盖的支持模型' }
  }
  if (form.enabled.supportedEndpointModes && !form.supportedEndpointModes.length) {
    return { message: '批量覆盖上游接口能力时至少选择一项' }
  }
  if (form.enabled.healthCheckEndpointMode) {
    const endpointModes = form.enabled.supportedEndpointModes
      ? form.supportedEndpointModes
      : intersectAccountSupportedEndpointModes(accounts)
    if (!accountHealthCheckEndpointModeOptions(endpointModes).some((option) => option.value === form.healthCheckEndpointMode)) {
      return { message: '检查请求形态必须选择全部目标账户已启用的 JSON 或流式请求能力' }
    }
  }
  const invalidMappingIndex = form.enabled.modelMappings
    ? form.modelMappings.findIndex((mapping) => (
        !mapping.sourceModel.trim()
        || !mapping.upstreamModel.trim()
        || !mapping.sourceEndpointFamily
        || !mapping.upstreamEndpointFamily
      ))
    : -1
  if (invalidMappingIndex >= 0) {
    return { message: `请完整填写第 ${invalidMappingIndex + 1} 条账号模型别名` }
  }
  const mappingValidation = validateBatchAccountModelMappings(accounts, form, modelMappingOptions)
  if (mappingValidation) return { message: mappingValidation }

  const updates: AccountBatchEditRequest['updates'] = {}
  addUpdate(updates, 'tags', form.enabled.tags, normalizedTextList(form.tags))
  addUpdate(updates, 'proxyProfileId', form.enabled.proxyProfileId, form.proxyProfileId?.trim() || null)
  addUpdate(updates, 'concurrencyLimit', form.enabled.concurrencyLimit, form.concurrencyLimit)
  addUpdate(updates, 'priority', form.enabled.priority, form.priority)
  addUpdate(updates, 'superPriorityEnabled', form.enabled.superPriorityEnabled, form.superPriorityEnabled)
  addUpdate(updates, 'fallbackEnabled', form.enabled.fallbackEnabled, form.fallbackEnabled)
  addUpdate(updates, 'accountExpiresAt', form.enabled.accountExpiresAt, formatServerDateTimeInput(form.accountExpiresAt))
  addUpdate(
    updates,
    'availabilitySchedule',
    form.enabled.availabilitySchedule,
    buildAccountAvailabilitySchedulePayload(form.availabilitySchedule)
  )
  addUpdate(updates, 'notes', form.enabled.notes, form.notes)
  addUpdate(
    updates,
    'errorHandlingRules',
    form.enabled.errorHandlingRules,
    buildAccountErrorPolicyPayload(form.errorHandlingRules)
  )
  addUpdate(
    updates,
    'responseInspectionRules',
    form.enabled.responseInspectionRules,
    buildAccountResponseInspectionPayload(form.responseInspectionRules)
  )
  addUpdate(updates, 'supportedModels', form.enabled.supportedModels, supportedModels)
  addUpdate(updates, 'healthCheckModel', form.enabled.healthCheckModel, healthCheckModel)
  addUpdate(updates, 'healthCheckEndpointMode', form.enabled.healthCheckEndpointMode, form.healthCheckEndpointMode)
  addUpdate(
    updates,
    'modelMappings',
    form.enabled.modelMappings,
    form.modelMappings.map((mapping) => ({
      ...mapping,
      sourceModel: mapping.sourceModel.trim(),
      upstreamModel: mapping.upstreamModel.trim()
    }))
  )
  addUpdate(
    updates,
    'supportedEndpointModes',
    form.enabled.supportedEndpointModes,
    [...new Set(form.supportedEndpointModes)]
  )
  addUpdate(updates, 'serviceTierOverride', form.enabled.serviceTierOverride, form.serviceTierOverride)
  addUpdate(updates, 'reasoningEffortOverride', form.enabled.reasoningEffortOverride, form.reasoningEffortOverride)

  return { payload: { targets, updates } }
}

export function intersectAccountSupportedEndpointModes(
  accounts: AccountBatchEditContextItem[]
): AccountSupportedEndpointMode[] {
  if (!accounts.length) return []
  const [first, ...rest] = accounts.map((account) => accountSupportedEndpointModes(account))
  return first.filter((mode) => rest.every((modes) => modes.includes(mode)))
}

function validateBatchAccountModelMappings(
  accounts: AccountBatchEditContextItem[],
  form: AccountBatchEditForm,
  modelMappingOptions: AccountBatchEditModelMappingOptions
): string | undefined {
  if (!form.enabled.modelMappings && !form.enabled.supportedEndpointModes) return undefined
  for (const account of accounts) {
    const mappings = form.enabled.modelMappings ? form.modelMappings : account.modelMappings ?? []
    const supportedEndpointModes = form.enabled.supportedEndpointModes
      ? form.supportedEndpointModes
      : accountSupportedEndpointModes(account)
    for (const mapping of mappings) {
      const message = accountModelMappingProtocolValidationMessage({
        sourceEndpointFamily: mapping.sourceEndpointFamily,
        upstreamEndpointFamily: mapping.upstreamEndpointFamily,
        enabled: mapping.enabled,
        context: { providerProfile: account, supportedEndpointModes }
      })
      if (message) return message
    }
  }
  if (form.enabled.modelMappings) {
    const supportedModels = form.enabled.supportedModels
      ? normalizedTextList(form.supportedModels)
      : intersectAccountSupportedModels(accounts)
    const supportedEndpointModes = form.enabled.supportedEndpointModes
      ? form.supportedEndpointModes
      : intersectAccountSupportedEndpointModes(accounts)
    return validateAccountModelMappings(
      form.modelMappings,
      supportedModels,
      supportedEndpointModes,
      modelMappingOptions.providerProfile ?? accounts[0],
      modelMappingOptions
    )
  }
  return undefined
}

function intersectAccountSupportedModels(accounts: AccountBatchEditContextItem[]): string[] {
  if (!accounts.length) return []
  const [first, ...rest] = accounts.map((account) => normalizedTextList(account.supportedModels ?? []))
  return first.filter((model) => rest.every((models) => models.includes(model)))
}

function accountSupportedEndpointModes(account: AccountBatchEditContextItem): AccountSupportedEndpointMode[] {
  return account.supportedEndpointModes ?? []
}

function addUpdate<TKey extends keyof AccountBatchEditRequest['updates']>(
  updates: AccountBatchEditRequest['updates'],
  key: TKey,
  enabled: boolean,
  value: NonNullable<AccountBatchEditRequest['updates'][TKey]>['value']
): void {
  if (!enabled) return
  updates[key] = { enabled: true, value } as AccountBatchEditRequest['updates'][TKey]
}

function normalizedTextList(values: string[]): string[] {
  const output: string[] = []
  const seen = new Set<string>()
  for (const value of values ?? []) {
    const text = value.replace(/\s+/g, ' ').trim()
    if (!text || seen.has(text)) continue
    seen.add(text)
    output.push(text)
  }
  return output
}
