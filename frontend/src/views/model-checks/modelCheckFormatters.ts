import { formatDateTime, formatMillisecondsAsSeconds } from '@/shared/formatters'
import { providerDisplayName } from '@/shared/providerDisplay'
import type {
  ModelCheckCheckResult,
  ModelCheckLevel,
  ModelCheckOption,
  ModelCheckQuestionBankItem,
  ModelCheckQuestionBankTestVerdict,
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

// 题库可用性测试 verdict 的展示文案：颜色只作辅助语义，四态文案始终保留。
export function questionTestVerdictText(value: ModelCheckQuestionBankTestVerdict): string {
  if (value === 'pass') return '通过'
  if (value === 'fail') return '未通过'
  if (value === 'invalid') return '无法判定'
  return '请求失败'
}

// 取值方式与 quizVerdictColor 保持一致：喂 a-tag :color 的 AntD 预设/状态色字面量。
export function questionTestVerdictColor(value: ModelCheckQuestionBankTestVerdict): string {
  if (value === 'pass') return 'green'
  if (value === 'fail') return 'red'
  if (value === 'invalid') return 'default'
  return 'warning'
}

// 题库提交门禁的内容指纹：题面与参考答案按 trim 归一，评分要点按原顺序参与。
// 与可用性测试通过时的指纹一致才允许提交；标题不参与（不影响判题）。
export function questionDraftKey(questionText: string, referenceAnswer: string, keyPoints: readonly string[]): string {
  return JSON.stringify([questionText.trim(), referenceAnswer.trim(), [...keyPoints]])
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

// trustReport.reasonCodes 分组：后端 trust.go 会把「证据覆盖缺口」类码与真正的异常码
// 混在同一数组输出。缺口码形态：裸族名（该族完全未覆盖）、<family>_partial（该族证据
// 不完整）、<family>_evidence_insufficient（该族证据不足）、<family>_not_applicable
// （该族不适用），以及 model_response_evidence_unavailable（没有模型响应可取证）。
// 展示时拆成两组，避免把覆盖缺口误读成异常原因。
const trustCoverageFamilies: readonly string[] = [
  'token_integrity',
  'stability',
  'distribution',
  'usage_shape',
  'behavior_probe',
  'long_context',
  'sampling_statistics',
  'cross_model'
]

export function splitTrustReasonCodes(codes: readonly string[]): { anomalies: string[]; coverage: string[] } {
  const anomalies: string[] = []
  const coverage: string[] = []
  for (const code of codes) {
    const isCoverage = code.endsWith('_partial')
      || code.endsWith('_evidence_insufficient')
      || code.endsWith('_not_applicable')
      || code === 'model_response_evidence_unavailable'
      || trustCoverageFamilies.includes(code)
    if (isCoverage) coverage.push(code)
    else anomalies.push(code)
  }
  return { anomalies, coverage }
}

// 覆盖缺口码的中文文案：按族名 + 后缀形态归位；族名不在表内的带后缀码与未知码
// 回退「未知原因码」，不静默吞掉。
export function trustCoverageCodeText(code: string): string {
  const familyLabels: Record<string, string> = {
    token_integrity: 'Token 用量诊断',
    stability: '稳定性探针',
    distribution: '分布对照',
    usage_shape: 'Usage 字段',
    behavior_probe: '行为探针',
    long_context: '长上下文找针',
    sampling_statistics: '采样统计分析',
    cross_model: '辅助模型对照'
  }
  if (code === 'model_response_evidence_unavailable') return '模型响应证据不可用'
  const familyText = (suffix: string, text: (familyLabel: string) => string): string | undefined => {
    if (!code.endsWith(suffix)) return undefined
    const familyLabel = familyLabels[code.slice(0, code.length - suffix.length)]
    return familyLabel === undefined ? undefined : text(familyLabel)
  }
  return familyText('_evidence_insufficient', (familyLabel) => `${familyLabel}证据不足`)
    ?? familyText('_partial', (familyLabel) => `${familyLabel}未形成完整证据`)
    ?? familyText('_not_applicable', (familyLabel) => `${familyLabel}不适用`)
    ?? (familyLabels[code] !== undefined ? `${familyLabels[code]}未覆盖` : undefined)
    ?? `未知原因码：${code}`
}

// 异常原因码中文映射：自 Drawer 内联表迁出，内容保持不变（空数组的占位文案由
// 分组拼接处负责）。
export function trustReasonCodeText(code: string): string {
  return ({
    proportional_padding: '差分斜率疑似比例灌水',
    slope_warning: '差分斜率偏离待复核',
    bucket_rounding: '上游用量疑似分桶取整',
    fixed_intercept_padding: '固定输入 Token 开销超过已校准强判阈值',
    fixed_intercept_calibration_pending: '固定输入 Token 开销基线等待真实样本校准',
    reported_usage_missing: '上游未返回完整输入 Token',
    reported_usage_incompatible: '上游 usage 口径与总输入不兼容',
    configured_model_mapping: '已配置模型映射',
    undeclared_response_model_mismatch: '响应模型与请求不一致',
    protocol_check_failed: '协议探针未通过',
    tokenizer_calibration_unavailable: '尚无 Tokenizer 校准结果',
    population_baseline_unavailable: '群体基线尚未形成',
    paired_models_collapsed: '配对模型行为距离异常收缩，疑似同源',
    closer_to_lower_model_baseline: '行为特征更接近较低版本模型基线',
    identity_population_outlier: '身份特征偏离 leave-one-upstream-out 群体基线',
    population_drift_protected: '群体发生共同漂移，已暂停强身份结论',
    cross_model_mismatch: '辅助模型对照不一致',
    token_integrity_anomaly: 'Token 用量诊断异常'
  } as Record<string, string>)[code] ?? `未知原因码：${code}`
}

export function terminalLevelForCheckStatus(status: ModelCheckCheckResult['status']): ModelCheckTerminalLineLevel {
  if (status === 'passed') return 'success'
  if (status === 'warning') return 'warning'
  if (status === 'failed') return 'error'
  return 'muted'
}

// 检测项消息翻译：后端 modelcheckprobe 探针失败时写入的是英文原始消息（probe.go），
// 展示层精确匹配已知形态翻译为中文；未匹配的消息原样保留，不吞错误细节。
export function translateProbeMessage(message: string): string {
  if (message === 'J3b probe timed out') return '探针请求超时（上游未在预算内完成响应）'
  if (message === 'J3b probe canceled') return '探针已取消'
  const requestFailedPrefix = 'J3b upstream request failed: '
  if (message.startsWith(requestFailedPrefix)) return `上游请求失败：${message.slice(requestFailedPrefix.length)}`
  if (message === 'J3b upstream response read failed') return '上游响应读取失败'
  if (message === 'J3b upstream response exceeded limit') return '上游响应超过大小上限'
  if (message === 'J3b OAuth Codex response stream incomplete') return 'OAuth Codex 响应流未完成'
  const httpMatch = /^J3b upstream returned HTTP (\d+)$/.exec(message)
  if (httpMatch) return `上游返回 HTTP ${httpMatch[1]}`
  return message
}

function nonEmptyText(value: unknown): string | undefined {
  return typeof value === 'string' && value.trim() ? value.trim() : undefined
}

// 消息三级回退：evidenceSummary.message → evidenceSummary.error（custom_quiz 失败证据
// 把错误放在 error 键）→ check.errorMessage，全部为空才视为无消息；结果统一过
// translateProbeMessage，已知探针英文消息翻译为中文。
export function checkMessage(check: ModelCheckCheckResult): string | undefined {
  const message = nonEmptyText(check.evidenceSummary.message)
    ?? nonEmptyText(check.evidenceSummary.error)
    ?? nonEmptyText(check.errorMessage)
  return message === undefined ? undefined : translateProbeMessage(message)
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
