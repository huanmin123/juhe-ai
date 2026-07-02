import { formatMillisecondsAsSeconds } from '@/shared/formatters'
import { providerDisplayName } from '@/shared/providerDisplay'
import type {
  ModelCheckCheckResult,
  ModelCheckLevel,
  ModelCheckOption,
  ModelCheckRunSummary,
  ModelCheckStatus
} from '@/types/domain'

export type ModelCheckTerminalLineLevel = 'info' | 'success' | 'warning' | 'error' | 'muted'

export const modelCheckStatusOptions: Array<{ label: string; value: ModelCheckStatus }> = [
  { label: '检测中', value: 'running' },
  { label: '已完成', value: 'completed' },
  { label: '失败', value: 'failed' },
  { label: '已取消', value: 'canceled' }
]

export const modelCheckLevelOptions: Array<{ label: string; value: ModelCheckLevel }> = [
  { label: '高可信', value: 'high_confidence' },
  { label: '较可信', value: 'likely' },
  { label: '不确定', value: 'uncertain' },
  { label: '疑似不符', value: 'suspicious' },
  { label: '不可检测', value: 'unavailable' }
]

export function targetTypeText(value: ModelCheckRunSummary['targetType']): string {
  if (value === 'account') return 'AI 账户'
  return value
}

export function providerText(value: ModelCheckRunSummary['providerCode']): string {
  return providerDisplayName(value)
}

export function runTrustedComparison(run: Pick<ModelCheckRunSummary, 'trustedComparison'>): boolean {
  return run.trustedComparison
}

export function statusText(value: ModelCheckStatus): string {
  return modelCheckStatusOptions.find((item) => item.value === value)?.label ?? value
}

export function statusColor(value: ModelCheckStatus): string {
  if (value === 'completed') return 'green'
  if (value === 'failed') return 'red'
  if (value === 'running') return 'blue'
  return 'default'
}

export function levelText(value: ModelCheckLevel): string {
  return modelCheckLevelOptions.find((item) => item.value === value)?.label ?? value
}

export function levelColor(value: ModelCheckLevel): string {
  if (value === 'high_confidence') return 'green'
  if (value === 'likely') return 'blue'
  if (value === 'uncertain') return 'orange'
  if (value === 'suspicious') return 'red'
  return 'default'
}

export function checkStatusText(value: NonNullable<ModelCheckCheckResult['status']>): string {
  if (value === 'passed') return '通过'
  if (value === 'warning') return '需关注'
  if (value === 'failed') return '失败'
  if (value === 'skipped') return '未计分'
  return value
}

export function checkStatusColor(value: NonNullable<ModelCheckCheckResult['status']>): string {
  if (value === 'passed') return 'green'
  if (value === 'warning') return 'orange'
  if (value === 'failed') return 'red'
  if (value === 'skipped') return 'default'
  return 'default'
}

export function modelCheckModelText(value: string, supportedModels: ModelCheckOption[]): string {
  return supportedModels.find((item) => item.value === value)?.label ?? value
}

export function formatModelCheckDuration(value?: number): string {
  return formatMillisecondsAsSeconds(value)
}

export function evidenceCompletenessText(run: Pick<ModelCheckRunSummary, 'resultSummary'>): string {
  const summary = recordValue(run.resultSummary.evidenceCompleteness)
  const score = numberValue(summary?.evidenceCompletenessScore)
  const scored = numberValue(summary?.scoredEvidenceProbeCount)
  const total = numberValue(summary?.evidenceProbeCount)
  if (score === undefined || scored === undefined || total === undefined || total <= 0) return '-'
  return `${scored} / ${total}（${score}%）`
}

export function checkTitle(check: ModelCheckCheckResult): string {
  return checkTitleByType(check.itemType, check.itemKey)
}

export function checkTitleByType(itemType: string, itemKey: string): string {
  const labels: Record<string, string> = {
    responses_basic: 'Responses 非流式',
    responses_stream: 'Responses 流式',
    protocol_basic: '协议非流式',
    protocol_stream: '协议流式',
    structured_output: '结构化输出',
    tool_calling: '工具调用',
    usage_shape: 'Usage 字段',
    behavior_probe: '行为探针',
    long_context: '长上下文找针',
    stability: '稳定性探针',
    cross_model: '辅助模型对照',
    distribution_similarity: '分布相似度对照',
    trusted_comparison: '可信对比'
  }
  return labels[itemType] ?? itemKey
}

export function progressItemTitle(itemKey: string, itemType?: string): string {
  if (itemKey.includes('.distribution.')) return '分布相似度采样'
  return checkTitleByType(itemType ?? itemKey.split('.').pop() ?? itemKey, itemKey)
}

export function terminalLevelForCheckStatus(status: ModelCheckCheckResult['status']): ModelCheckTerminalLineLevel {
  if (status === 'passed') return 'success'
  if (status === 'warning') return 'warning'
  if (status === 'failed') return 'error'
  return 'muted'
}

export function checkMessage(check: ModelCheckCheckResult): string | undefined {
  const message = check.evidenceSummary.message
  return typeof message === 'string' && message.trim() ? message.trim() : check.errorMessage
}

export function hasCheckExtra(check: ModelCheckCheckResult): boolean {
  return Object.keys(check.evidenceSummary).length > 0 || Boolean(check.traceId)
}

export function checkExtra(check: ModelCheckCheckResult): Record<string, unknown> {
  return {
    traceId: check.traceId,
    evidence: check.evidenceSummary,
    errorCode: check.errorCode,
    errorMessage: check.errorMessage
  }
}

export function formatModelCheckJson(value: unknown): string {
  return JSON.stringify(value, null, 2)
}

export function formatClockTime(value: Date): string {
  return [value.getHours(), value.getMinutes(), value.getSeconds()]
    .map((item) => String(item).padStart(2, '0'))
    .join(':')
}

function recordValue(value: unknown): Record<string, unknown> | undefined {
  return typeof value === 'object' && value !== null && !Array.isArray(value) ? value as Record<string, unknown> : undefined
}

function numberValue(value: unknown): number | undefined {
  return typeof value === 'number' && Number.isFinite(value) ? value : undefined
}
