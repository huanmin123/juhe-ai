import type {
  SchedulingPreferenceChatMode,
  SchedulingPreferenceMutationResult,
  SchedulingPreferenceSpeedFirstConfig,
  SchedulingPreferenceSummary
} from '@/types/domain'
import type { SchedulingPreferencePatchPayload } from '@/api/domains/schedulingPreferences'

/** 速度优先九字段表单模型：表单内始终落具体数值，旧数据缺省时由 speedFirstParamsFormFromConfig 回落契约默认。 */
export interface SpeedFirstParamsForm {
  firstByteDeadlineMs: number
  slowTriggerCount: number
  slowWindowSeconds: number
  recoverySuccessCount: number
  probeIntervalSeconds: number
  degradedTtlSeconds: number
  maxFirstByteRetriesPerRequest: number
  totalTimeDeadlineSeconds: number
  compactionTotalTimeDeadlineSeconds: number
}

/** 九字段合法范围与默认值（接口契约「配置形状与速度参数契约」表）。 */
export const schedulingPreferenceSpeedFirstBounds = {
  firstByteDeadlineMs: { min: 10000, max: 60000, fallback: 30000 },
  slowTriggerCount: { min: 2, max: 10, fallback: 3 },
  slowWindowSeconds: { min: 60, max: 600, fallback: 120 },
  recoverySuccessCount: { min: 3, max: 10, fallback: 3 },
  probeIntervalSeconds: { min: 10, max: 300, fallback: 30 },
  degradedTtlSeconds: { min: 60, max: 3600, fallback: 300 },
  maxFirstByteRetriesPerRequest: { min: 1, max: 3, fallback: 2 },
  totalTimeDeadlineSeconds: { min: 60, max: 270, fallback: 120 },
  compactionTotalTimeDeadlineSeconds: { min: 300, max: 900, fallback: 300 }
} as const

export type SpeedFirstParamKey = keyof typeof schedulingPreferenceSpeedFirstBounds

/** 编辑弹窗表单模型：name/chatMode/enabled 为公共字段；speed_first 时消费 speedFirst。 */
export interface SchedulingPreferenceFormModel {
  name: string
  chatMode: SchedulingPreferenceChatMode
  enabled: boolean
  speedFirst: SpeedFirstParamsForm
}

/** 构建提交 payload 形状的完整表单值：cost_first 不携带 chatSpeedFirstConfig（契约禁止）。 */
export type SchedulingPreferenceFormPayload = Pick<SchedulingPreferenceFormModel, 'name' | 'chatMode' | 'enabled'> & {
  chatSpeedFirstConfig?: SchedulingPreferenceSpeedFirstConfig
}

export function defaultSpeedFirstParamsForm(): SpeedFirstParamsForm {
  return {
    firstByteDeadlineMs: schedulingPreferenceSpeedFirstBounds.firstByteDeadlineMs.fallback,
    slowTriggerCount: schedulingPreferenceSpeedFirstBounds.slowTriggerCount.fallback,
    slowWindowSeconds: schedulingPreferenceSpeedFirstBounds.slowWindowSeconds.fallback,
    recoverySuccessCount: schedulingPreferenceSpeedFirstBounds.recoverySuccessCount.fallback,
    probeIntervalSeconds: schedulingPreferenceSpeedFirstBounds.probeIntervalSeconds.fallback,
    degradedTtlSeconds: schedulingPreferenceSpeedFirstBounds.degradedTtlSeconds.fallback,
    maxFirstByteRetriesPerRequest: schedulingPreferenceSpeedFirstBounds.maxFirstByteRetriesPerRequest.fallback,
    totalTimeDeadlineSeconds: schedulingPreferenceSpeedFirstBounds.totalTimeDeadlineSeconds.fallback,
    compactionTotalTimeDeadlineSeconds: schedulingPreferenceSpeedFirstBounds.compactionTotalTimeDeadlineSeconds.fallback
  }
}

export function speedFirstParamsFormFromConfig(config?: SchedulingPreferenceSpeedFirstConfig): SpeedFirstParamsForm {
  const fallback = defaultSpeedFirstParamsForm()
  if (!config) return fallback
  const speedFirstConfig = config.speedFirstConfig ?? {}
  return {
    firstByteDeadlineMs: boundedSpeedFirstParam('firstByteDeadlineMs', config.firstByteDeadlineMs),
    slowTriggerCount: boundedSpeedFirstParam('slowTriggerCount', speedFirstConfig.slowTriggerCount),
    slowWindowSeconds: boundedSpeedFirstParam('slowWindowSeconds', speedFirstConfig.slowWindowSeconds),
    recoverySuccessCount: boundedSpeedFirstParam('recoverySuccessCount', speedFirstConfig.recoverySuccessCount),
    probeIntervalSeconds: boundedSpeedFirstParam('probeIntervalSeconds', speedFirstConfig.probeIntervalSeconds),
    degradedTtlSeconds: boundedSpeedFirstParam('degradedTtlSeconds', speedFirstConfig.degradedTtlSeconds),
    maxFirstByteRetriesPerRequest: boundedSpeedFirstParam('maxFirstByteRetriesPerRequest', speedFirstConfig.maxFirstByteRetriesPerRequest),
    totalTimeDeadlineSeconds: boundedSpeedFirstParam('totalTimeDeadlineSeconds', speedFirstConfig.totalTimeDeadlineSeconds),
    compactionTotalTimeDeadlineSeconds: boundedSpeedFirstParam('compactionTotalTimeDeadlineSeconds', speedFirstConfig.compactionTotalTimeDeadlineSeconds)
  }
}

/** 将任意输入收敛为该字段合法范围内的整数；非法输入回落契约默认。 */
export function boundedSpeedFirstParam(key: SpeedFirstParamKey, value: unknown): number {
  const bounds = schedulingPreferenceSpeedFirstBounds[key]
  const numeric = typeof value === 'number' ? value : Number(value)
  if (!Number.isFinite(numeric)) return bounds.fallback
  return Math.min(bounds.max, Math.max(bounds.min, Math.round(numeric)))
}

export function buildSchedulingPreferenceFormPayload(form: SchedulingPreferenceFormModel): SchedulingPreferenceFormPayload {
  const payload: SchedulingPreferenceFormPayload = {
    name: form.name.trim(),
    chatMode: form.chatMode,
    enabled: form.enabled
  }
  if (form.chatMode === 'speed_first') {
    payload.chatSpeedFirstConfig = buildSpeedFirstConfigPayload(form.speedFirst)
  }
  return payload
}

function buildSpeedFirstConfigPayload(form: SpeedFirstParamsForm): SchedulingPreferenceSpeedFirstConfig {
  return {
    firstByteDeadlineMs: boundedSpeedFirstParam('firstByteDeadlineMs', form.firstByteDeadlineMs),
    speedFirstConfig: {
      slowTriggerCount: boundedSpeedFirstParam('slowTriggerCount', form.slowTriggerCount),
      slowWindowSeconds: boundedSpeedFirstParam('slowWindowSeconds', form.slowWindowSeconds),
      recoverySuccessCount: boundedSpeedFirstParam('recoverySuccessCount', form.recoverySuccessCount),
      probeIntervalSeconds: boundedSpeedFirstParam('probeIntervalSeconds', form.probeIntervalSeconds),
      degradedTtlSeconds: boundedSpeedFirstParam('degradedTtlSeconds', form.degradedTtlSeconds),
      maxFirstByteRetriesPerRequest: boundedSpeedFirstParam('maxFirstByteRetriesPerRequest', form.maxFirstByteRetriesPerRequest),
      totalTimeDeadlineSeconds: boundedSpeedFirstParam('totalTimeDeadlineSeconds', form.totalTimeDeadlineSeconds),
      compactionTotalTimeDeadlineSeconds: boundedSpeedFirstParam('compactionTotalTimeDeadlineSeconds', form.compactionTotalTimeDeadlineSeconds)
    }
  }
}

const editableFields = [
  'name',
  'chatMode',
  'chatSpeedFirstConfig',
  'enabled'
] as const

/** 相对打开时 baseline 构建 PATCH delta；cost_first 分支不携带 chatSpeedFirstConfig，由服务端清空存储配置。 */
export function buildSchedulingPreferenceMutationPatch(
  baseline: SchedulingPreferenceFormPayload,
  current: SchedulingPreferenceFormPayload
): Omit<SchedulingPreferencePatchPayload, 'expectedUpdatedAt'> {
  const patch: Omit<SchedulingPreferencePatchPayload, 'expectedUpdatedAt'> = {}
  const writablePatch = patch as Record<string, unknown>
  for (const field of editableFields) {
    if (stableValue(baseline[field]) === stableValue(current[field])) continue
    // chatSpeedFirstConfig 缺席仅出现在切回 cost_first（伴随 chatMode 变化），契约禁止携带 config，跳过写键。
    if (current[field] === undefined) continue
    writablePatch[field] = current[field]
  }
  return patch
}

export function hasSchedulingPreferenceMutationChanges(payload: Omit<SchedulingPreferencePatchPayload, 'expectedUpdatedAt'>): boolean {
  return editableFields.some((field) => Object.prototype.hasOwnProperty.call(payload, field))
}

export function mergeSchedulingPreferenceMutationResult(
  current: SchedulingPreferenceSummary,
  result: SchedulingPreferenceMutationResult
): SchedulingPreferenceSummary {
  if (current.id !== result.id) return current
  const patch = result.rowPatch
  const next = { ...current }
  if (patch.name !== undefined) next.name = patch.name
  if (patch.chatMode !== undefined) {
    next.chatMode = patch.chatMode
    if (patch.chatMode === 'cost_first') delete next.chatSpeedFirstConfig
  }
  if (Object.prototype.hasOwnProperty.call(patch, 'chatSpeedFirstConfig')) {
    if (patch.chatSpeedFirstConfig === null || patch.chatSpeedFirstConfig === undefined) delete next.chatSpeedFirstConfig
    else next.chatSpeedFirstConfig = patch.chatSpeedFirstConfig
  }
  if (patch.enabled !== undefined) next.enabled = patch.enabled
  next.updatedAt = result.updatedAt
  return next
}

/** 删除被引用 409 时从响应体提取 referenceCount；非该场景返回 undefined。 */
export function extractSchedulingPreferenceReferenceCount(error: unknown): number | undefined {
  const data = (error as { response?: { data?: { referenceCount?: unknown } } } | undefined)?.response?.data
  const count = (data as { referenceCount?: unknown } | undefined)?.referenceCount
  return typeof count === 'number' && Number.isInteger(count) && count >= 0 ? count : undefined
}

function stableValue(value: unknown): string {
  if (value === undefined) return 'undefined'
  if (value === null || typeof value !== 'object') return JSON.stringify(value) ?? String(value)
  if (Array.isArray(value)) return `[${value.map(stableValue).join(',')}]`
  const record = value as Record<string, unknown>
  return `{${Object.keys(record).sort().map((key) => `${JSON.stringify(key)}:${stableValue(record[key])}`).join(',')}}`
}
