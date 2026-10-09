import { formatDateTime, formatMillisecondsAsSeconds } from '@/shared/formatters'
import { providerDisplayName } from '@/shared/providerDisplay'
import type {
  ModelCheckCheckResult,
  ModelCheckLevel,
  ModelCheckOption,
  ModelCheckQuestionBankItem,
  ModelCheckQuestionStatus,
  ModelCheckQuizItem,
  ModelCheckQuizItemVerdict,
  ModelCheckQuizSummary,
  ModelCheckRunSummary,
  ModelCheckProfile,
  ModelCheckStatus,
  ModelCheckTriggerKind,
  ModelQualityScheduleExecutionState
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

export function profileText(profile?: ModelCheckProfile): string {
  return profile === 'full' ? '深度检测' : '快速检测'
}

export function profileColor(profile?: ModelCheckProfile): string {
  return profile === 'full' ? 'purple' : 'cyan'
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

export function triggerKindText(value: ModelCheckTriggerKind): string {
  if (value === 'scheduled') return '定时检查'
  if (value === 'quality_recovery') return '质量恢复'
  if (value === 'schedule_now') return '计划立即执行'
  return '手动检查'
}

export function triggerKindColor(value: ModelCheckTriggerKind): string {
  if (value === 'scheduled') return 'purple'
  if (value === 'quality_recovery') return 'orange'
  if (value === 'schedule_now') return 'cyan'
  return 'blue'
}

export function executionStateText(value: ModelQualityScheduleExecutionState): string {
  if (value === 'paused') return '已暂停'
  if (value === 'running') return '执行中'
  if (value === 'blocked_account') return '账户阻塞'
  if (value === 'queued') return '排队中'
  return '已启用'
}

export function executionStateColor(value: ModelQualityScheduleExecutionState): string {
  if (value === 'paused') return 'default'
  if (value === 'running') return 'processing'
  if (value === 'blocked_account') return 'warning'
  if (value === 'queued') return 'gold'
  return 'green'
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
  const summary = recordValue(run.resultSummary?.evidenceCompleteness)
  const score = numberValue(summary?.evidenceCompletenessScore)
  const scored = numberValue(summary?.scoredEvidenceProbeCount)
  const total = numberValue(summary?.evidenceProbeCount)
  if (score === undefined || scored === undefined || total === undefined || total <= 0) return '-'
  return `${scored} / ${total}（${score}%）`
}

export const modelCheckQuestionStatusOptions: Array<{ label: string; value: ModelCheckQuestionStatus }> = [
  { label: '待审核', value: 'pending' },
  { label: '已通过', value: 'approved' },
  { label: '已驳回', value: 'rejected' }
]

export function questionStatusText(value: ModelCheckQuestionStatus): string {
  return modelCheckQuestionStatusOptions.find((item) => item.value === value)?.label ?? value
}

export function questionStatusColor(value: ModelCheckQuestionStatus): string {
  if (value === 'approved') return 'green'
  if (value === 'rejected') return 'red'
  return 'gold'
}

// 题库场景的时间展示去掉毫秒段：formatDateTime 保留全站统一的毫秒精度，
// 但题目卡片与详情的提交/审核时间只需秒级，毫秒只有噪音。
export function questionBankDateTimeText(value?: string): string {
  const formatted = formatDateTime(value)
  return formatted.replace(/\.(\d{1,6})(\s|$)/, '$2')
}

// 题库提交人展示文本：管理面透出 createdByName；缺省（自助面或名称解析
// 失败的存量行）回退显示创建者 id，保证字段不会静默消失。
export function questionCreatorText(item: Pick<ModelCheckQuestionBankItem, 'createdByName' | 'createdBy'>): string {
  return item.createdByName?.trim() || item.createdBy
}

export function quizVerdictText(value: ModelCheckQuizItemVerdict): string {
  if (value === 'passed') return '通过'
  if (value === 'failed') return '未通过'
  return '不可判定'
}

export function quizVerdictColor(value: ModelCheckQuizItemVerdict): string {
  if (value === 'passed') return 'green'
  if (value === 'failed') return 'red'
  return 'default'
}

export function modelCheckQuizSummary(resultSummary: Record<string, unknown> | undefined): ModelCheckQuizSummary | undefined {
  const value = recordValue(resultSummary?.customQuiz)
  if (!value) return undefined
  const items = Array.isArray(value.items)
    ? value.items.flatMap((raw): ModelCheckQuizItem[] => {
        const item = recordValue(raw)
        const verdict = item?.verdict
        if (!item || (verdict !== 'passed' && verdict !== 'failed' && verdict !== 'unavailable')) return []
        return [{
          questionId: typeof item.questionId === 'string' ? item.questionId : '',
          title: typeof item.title === 'string' ? item.title : '',
          verdict,
          reason: typeof item.reason === 'string' ? item.reason : ''
        }]
      }).filter((item) => item.questionId)
    : []
  return {
    enabled: value.enabled === true,
    executed: modelCheckQuizExecuted(value, items),
    score: numberValue(value.score) ?? 0,
    maxScore: numberValue(value.maxScore) ?? 31,
    deduction: numberValue(value.deduction) ?? 0,
    items
  }
}

// 题库未执行判定：新数据以后端 executed 字段为准（false 即未执行，score=0）；
// 历史数据无 executed 字段时以判定结果兜底——只有拿到 passed/failed 判定的题
// 才算执行过；「全请求失败转 skipped（verdict=unavailable）」与「题目不可用」
// 的历史 run 同样视为未执行，避免被展示成 31/31 满分（与后端 executed 语义
// 对齐：拿到判定才算执行）。
function modelCheckQuizExecuted(value: Record<string, unknown>, items: ModelCheckQuizItem[]): boolean {
  if (value.executed === true) return true
  if (value.executed === false) return false
  return items.some((item) => item.verdict === 'passed' || item.verdict === 'failed')
}

export function checkTitle(check: ModelCheckCheckResult): string {
  const base = checkTitleByType(check.itemType, check.itemKey)
  const questionTitle = typeof check.evidenceSummary?.questionTitle === 'string' ? check.evidenceSummary.questionTitle.trim() : ''
  return questionTitle ? `${base} · ${questionTitle}` : base
}

export function visibleModelCheckChecks(checks: ModelCheckCheckResult[]): ModelCheckCheckResult[] {
  return checks.filter((check) => {
    if (check.itemType !== 'responses_basic' && check.itemType !== 'protocol_basic') return true
    return check.evidenceSummary.success !== true || check.evidenceSummary.modelMismatch === true
  })
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
    token_integrity: 'Token 用量诊断',
    behavior_probe: '行为探针',
    long_context: '长上下文找针',
    stability: '稳定性探针',
    cross_model: '辅助模型对照',
    distribution_similarity: '分布相似度对照',
    trusted_comparison: '可信对比',
    custom_quiz: '题库测试',
    astra_constants: 'Astra 专项探针',
    identity_selfreport: '身份自报一致性',
    identity_extraction: '指令完整性复读',
    identity_anchor: '推理锚点题',
    sampling_statistics: '采样统计分析'
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
