export type ChatLongSessionStage =
  | 'foundation'
  | 'layout'
  | 'components'
  | 'responsive'
  | 'accessibility'
  | 'fixes'
  | 'refactor'
  | 'final-review'

export interface ChatLongSessionControlPlan {
  model?: 'alternate'
  reasoning?: 'alternate'
  service?: 'alternate'
}

export interface ChatLongSessionForbiddenRegression {
  id: string
  needle: string
}

export interface ChatLongSessionTurn {
  turn: number
  stage: ChatLongSessionStage
  prompt: string
  introducedFeatureId: string
  requiredFeatureIds: string[]
  exactAnchors: string[]
  forbiddenRegressions: ChatLongSessionForbiddenRegression[]
  memoryProbe: boolean
  controls: ChatLongSessionControlPlan
  responseMode: 'artifact' | 'manifest'
}

export interface ChatLongSessionResponse {
  turn: number
  assistantOutput: string
}

export interface ChatLongSessionScore {
  requirementCompletion: number
  decisionRetention: number
  anchorPrecision: number
  anchorRecall: number
  firstOmissionTurn: number | null
  firstContradictionTurn: number | null
  artifactContinuity: number
  finalRequirementCompletion: number
  manifestAccuracy: number
}

export const chatLongSessionArtifactMaxBytes = 32 * 1024

export function chatLongSessionArtifactQualityFailure(input: {
  responseMode: ChatLongSessionTurn['responseMode']
  assistantOutput: string
}): SafeChatStreamFailure | undefined {
  if (input.responseMode !== 'artifact') return undefined
  if (Buffer.byteLength(input.assistantOutput, 'utf8') <= chatLongSessionArtifactMaxBytes) return undefined
  return {
    type: 'message.failed',
    code: 'chat_long_session_artifact_too_large',
    message: 'artifact exceeds the 32 KiB UTF-8 quality limit'
  }
}

export function assertChatLongSessionScore(score: ChatLongSessionScore): void {
  const thresholds: Array<[keyof ChatLongSessionScore, number]> = [
    ['requirementCompletion', 0.85],
    ['decisionRetention', 0.9],
    ['anchorPrecision', 0.9],
    ['anchorRecall', 0.9],
    ['artifactContinuity', 0.85],
    ['finalRequirementCompletion', 0.9],
    ['manifestAccuracy', 0.95]
  ]
  for (const [key, minimum] of thresholds) {
    const value = score[key]
    if (typeof value !== 'number' || value < minimum) throw new Error(`chat_long_session_score_${key}_below_${minimum}`)
  }
  if (score.firstContradictionTurn !== null) throw new Error(`chat_long_session_score_firstContradictionTurn_${score.firstContradictionTurn}`)
  if (score.firstOmissionTurn !== null && score.firstOmissionTurn < 45) throw new Error(`chat_long_session_score_firstOmissionTurn_${score.firstOmissionTurn}`)
}

const featureEvidencePatterns: Record<string, RegExp[]> = {
  'REQ-01': [/<!doctype html>/i],
  'REQ-02': [/:root\s*\{[^}]*--surface\s*:/i],
  'REQ-03': [/<nav\b/i],
  'REQ-04': [/<main\b/i],
  'REQ-05': [/<footer\b/i],
  'REQ-06': [/body\s*\{[^}]*font-family\s*:/i],
  'REQ-07': [/\.app-shell\s*\{[^}]*display\s*:\s*grid/i],
  'REQ-08': [/<section\b[^>]*class=["'][^"']*overview/i],
  'REQ-09': [/\.content\s*\{[^}]*max-width\s*:/i],
  'REQ-10': [/\.stats-grid\s*\{[^}]*grid-template-columns\s*:/i],
  'REQ-11': [/<section\b[^>]*class=["'][^"']*activity/i],
  'REQ-12': [/\.stack\s*\{[^}]*gap\s*:/i],
  'REQ-13': [/class=["'][^"']*metric-card/i],
  'REQ-14': [/class=["'][^"']*trend-chart/i],
  'REQ-15': [/<ul\b[^>]*class=["'][^"']*activity-list/i],
  'REQ-16': [/<input\b[^>]*type=["']search["']/i],
  'REQ-17': [/class=["'][^"']*status-badge/i],
  'REQ-18': [/<button\b/i],
  'REQ-19': [/class=["'][^"']*empty-state/i],
  'REQ-20': [/aria-live=["']polite["']/i],
  'REQ-21': [/@media\s*\([^)]*max-width\s*:\s*1024px/i],
  'REQ-22': [/@media\s*\([^)]*max-width\s*:\s*720px/i],
  'REQ-23': [/class=["'][^"']*mobile-nav/i],
  'REQ-24': [/@media[\s\S]*\.stats-grid\s*\{[^}]*grid-template-columns\s*:\s*1fr/i],
  'REQ-25': [/overflow-wrap\s*:\s*anywhere/i],
  'REQ-26': [/min-height\s*:\s*44px/i],
  'REQ-27': [/class=["'][^"']*skip-link/i],
  'REQ-28': [/<nav\b[^>]*aria-label=/i],
  'REQ-29': [/<input\b[^>]*aria-label=/i],
  'REQ-30': [/:focus-visible\s*\{/i],
  'REQ-31': [/@media\s*\([^)]*prefers-reduced-motion\s*:\s*reduce/i],
  'REQ-32': [/--ink\s*:\s*#[0-9a-f]{6}/i],
  'REQ-33': [/body\s*\{[^}]*overflow-x\s*:\s*hidden/i],
  'REQ-34': [/scroll-margin-top\s*:/i],
  'REQ-35': [/table-layout\s*:\s*fixed/i],
  'REQ-36': [/\.action-button\s*\{[^}]*min-width\s*:/i],
  'REQ-37': [/text-wrap\s*:\s*balance/i],
  'REQ-38': [/@media\s+print/i],
  'REQ-39': [/--color-surface\s*:/i],
  'REQ-40': [/--space-3\s*:/i],
  'REQ-41': [/class=["'][^"']*dashboard__content/i],
  'REQ-42': [/:where\s*\(/i],
  'REQ-43': [/--surface-elevated\s*:/i],
  'REQ-44': [/@media[\s\S]*\.mobile-nav/i],
  'REQ-45': [/<section\b[^>]*aria-labelledby=/i],
  'REQ-46': [/clamp\s*\(/i],
  'REQ-47': [/aria-current=["']page["']/i],
  'REQ-48': [/var\s*\(--color-surface\)/i],
  'REQ-49': [/data-review=["']passed["']/i],
  'REQ-50': [/id=["']aurora-acceptance-ready["']/i]
}

const stagePlan: Array<{ stage: ChatLongSessionStage; count: number }> = [
  { stage: 'foundation', count: 6 },
  { stage: 'layout', count: 6 },
  { stage: 'components', count: 8 },
  { stage: 'responsive', count: 6 },
  { stage: 'accessibility', count: 6 },
  { stage: 'fixes', count: 6 },
  { stage: 'refactor', count: 6 },
  { stage: 'final-review', count: 6 }
]

const stageRequests: Record<ChatLongSessionStage, string[]> = {
  foundation: ['建立语义化页面骨架', '定义颜色与间距变量', '加入顶部导航', '建立主内容区域', '加入页脚', '固定基础排版'],
  layout: ['建立侧栏与内容网格', '加入概览区', '固定内容最大宽度', '对齐统计区域', '加入活动区域', '统一垂直节奏'],
  components: ['加入指标组件', '加入趋势组件', '加入活动列表', '加入搜索控件', '加入状态标签', '加入操作按钮', '加入空状态', '统一组件状态'],
  responsive: ['增加平板断点', '增加手机断点', '折叠导航', '重排指标区域', '处理长文本', '校准触控间距'],
  accessibility: ['加入跳转链接', '补齐地标标签', '补齐控件名称', '增加键盘焦点', '支持减少动画', '核对颜色对比'],
  fixes: ['修复窄屏溢出', '修复焦点遮挡', '修复表格截断', '修复按钮跳动', '修复标题换行', '修复打印布局'],
  refactor: ['收敛颜色变量', '收敛间距变量', '整理组件类名', '删除重复规则', '保持选择器低权重', '整理媒体查询'],
  'final-review': ['核对语义结构', '核对响应式布局', '核对无障碍状态', '核对视觉一致性', '核对禁止回归项', '输出最终完整项目']
}

const anchorsByTurn = new Map<number, string>([
  [1, 'PROJECT-AURORA-FOUNDATION'],
  [5, 'DECISION-NO-JAVASCRIPT'],
  [10, 'LAYOUT-GRID-72-28'],
  [17, 'COMPONENT-ACTIVITY-TIMELINE'],
  [25, 'MOBILE-BREAKPOINT-720PX'],
  [30, 'A11Y-FOCUS-RING-3PX'],
  [38, 'FIX-NO-HORIZONTAL-OVERFLOW'],
  [43, 'REFACTOR-TOKEN-SURFACE'],
  [47, 'FINAL-ZH-CN-LANDMARKS'],
  [50, 'AURORA-ACCEPTANCE-READY']
])

const forbiddenByTurn = new Map<number, ChatLongSessionForbiddenRegression>([
  [1, { id: 'no-inline-style', needle: 'style="' }],
  [7, { id: 'no-script', needle: '<script' }],
  [21, { id: 'no-horizontal-overflow', needle: 'min-width:1200px' }],
  [29, { id: 'no-focus-removal', needle: 'outline:none' }],
  [37, { id: 'no-fixed-mobile-width', needle: 'width:390px' }],
  [45, { id: 'no-important', needle: '!important' }]
])

const artifactCheckpointTurns = new Set([1, 5, 10, 16, 17, 20, 25, 30, 31, 38, 40, 41, 43, 47, 50])

export function buildChatLongSessionFixture(): ChatLongSessionTurn[] {
  const turns: ChatLongSessionTurn[] = []
  const requiredFeatureIds: string[] = []
  const exactAnchors: string[] = []
  const forbiddenRegressions: ChatLongSessionForbiddenRegression[] = []
  let turn = 0
  for (const stageEntry of stagePlan) {
    for (const request of stageRequests[stageEntry.stage]) {
      turn += 1
      const introducedFeatureId = `REQ-${String(turn).padStart(2, '0')}`
      requiredFeatureIds.push(introducedFeatureId)
      const anchor = anchorsByTurn.get(turn)
      if (anchor) exactAnchors.push(anchor)
      const forbidden = forbiddenByTurn.get(turn)
      if (forbidden) forbiddenRegressions.push(forbidden)
      const memoryProbe = turn % 10 === 0
      const probeText = memoryProbe ? ' 同时逐字保留并核对目前所有决策锚点。' : ''
      const anchorText = exactAnchors.join('、')
      const forbiddenText = forbiddenRegressions.map((item) => item.needle).join('、')
      const requirementText = requiredFeatureIds.join(' ')
      const responseMode = artifactCheckpointTurns.has(turn) ? 'artifact' : 'manifest'
      const prompt = responseMode === 'artifact'
        ? `继续演进同一个 Aurora Dashboard 单文件 HTML+CSS 项目：${request}（${introducedFeatureId}）。这是 checkpoint：根 html 元素必须且只能有一个 data-requirements 属性，其空格分隔累计值必须为 data-requirements="${requirementText}"；在页面可见文本或语义属性中逐字保留锚点：${anchorText}，禁止用注释保存锚点。不得在项目 artifact 中出现禁止回归标记：${forbiddenText}。返回更新后的完整有效 HTML，且仅返回一个 html 代码块；UTF-8 总字节数必须 <= 32768。禁止解释、禁止 HTML/CSS 注释、禁止重复示例、禁止占位和冗余内容；HTML/CSS 保持可读但高度紧凑，不要改写为独立问答。${probeText}`
        : `继续演进同一个 Aurora Dashboard 单文件 HTML+CSS 项目：${request}（${introducedFeatureId}）。本轮只返回单个严格 JSON manifest，禁止输出 HTML、Markdown 代码块或额外文字；JSON 结构必须为 {"introducedFeatureId":"${introducedFeatureId}","changeSummary":"不超过200个中文字符的实现决策摘要","decisionAnchors":${JSON.stringify(exactAnchors)},"forbiddenConfirmed":${JSON.stringify(forbiddenRegressions.map((item) => item.id))}}。后续 checkpoint 的目标 data-requirements="${requirementText}"。${probeText}`
      turns.push({
        turn,
        stage: stageEntry.stage,
        prompt,
        introducedFeatureId,
        requiredFeatureIds: [...requiredFeatureIds],
        exactAnchors: [...exactAnchors],
        forbiddenRegressions: [...forbiddenRegressions],
        memoryProbe,
        controls: {
          ...(new Set([16, 31, 41]).has(turn) ? { model: 'alternate' as const } : {}),
          ...(new Set([12, 28, 44]).has(turn) ? { reasoning: 'alternate' as const } : {}),
          ...(new Set([18, 35]).has(turn) ? { service: 'alternate' as const } : {})
        },
        responseMode
      })
    }
  }
  return turns
}

export function extractProjectArtifact(assistantOutput: string): string | null {
  const fenced = /```(?:html)?\s*([\s\S]*?)```/i.exec(assistantOutput)?.[1]?.trim()
  if (fenced && /<(?:!doctype|html|main|div)\b/i.test(fenced)) return fenced
  const documentStart = assistantOutput.search(/<!doctype\s+html|<html\b/i)
  if (documentStart >= 0) return assistantOutput.slice(documentStart).trim()
  return null
}

export function scoreChatLongSession(fixture: readonly ChatLongSessionTurn[], responses: readonly ChatLongSessionResponse[]): ChatLongSessionScore {
  const responseByTurn = new Map(responses.map((response) => [response.turn, response.assistantOutput]))
  const allAnchors = new Set(fixture.flatMap((turn) => turn.exactAnchors))
  let requiredChecks = 0
  let completedChecks = 0
  let retainedChecks = 0
  let retainedTotal = 0
  let anchorTruePositive = 0
  let anchorFalsePositive = 0
  let anchorExpected = 0
  let continuityRetained = 0
  let continuityExpected = 0
  let firstOmissionTurn: number | null = null
  let firstContradictionTurn: number | null = null
  let manifestChecks = 0
  let accurateManifests = 0
  let previousArtifact = ''

  for (const turn of fixture) {
    const output = responseByTurn.get(turn.turn) ?? ''
    if (turn.responseMode === 'manifest') {
      manifestChecks += 1
      const accurate = isAccurateManifest(output, turn)
      if (accurate) accurateManifests += 1
      else if (firstOmissionTurn === null) firstOmissionTurn = turn.turn
      continue
    }
    const artifact = extractProjectArtifact(output)
    const text = artifact ?? ''
    const missingRequirement = turn.requiredFeatureIds.some((id) => !hasRequirement(text, id))
    const missingAnchor = turn.exactAnchors.some((anchor) => !text.includes(anchor))
    requiredChecks += turn.requiredFeatureIds.length
    completedChecks += turn.requiredFeatureIds.filter((id) => hasRequirement(text, id)).length
    const priorRequirements = turn.requiredFeatureIds.filter((id) => id !== turn.introducedFeatureId)
    retainedTotal += priorRequirements.length
    retainedChecks += priorRequirements.filter((id) => hasRequirement(text, id)).length
    const expectedAnchors = new Set(turn.exactAnchors)
    anchorExpected += expectedAnchors.size
    for (const anchor of allAnchors) {
      if (!text.includes(anchor)) continue
      if (expectedAnchors.has(anchor)) anchorTruePositive += 1
      else anchorFalsePositive += 1
    }
    const normalizedArtifact = normalizeMatchText(text)
    const contradiction = turn.forbiddenRegressions.some((item) => normalizedArtifact.includes(normalizeMatchText(item.needle)))
    if (firstOmissionTurn === null && (missingRequirement || missingAnchor || artifact === null)) firstOmissionTurn = turn.turn
    if (firstContradictionTurn === null && contradiction) firstContradictionTurn = turn.turn
    if (previousArtifact) {
      const previousSignature = artifactStructureSignature(previousArtifact)
      const currentSignature = artifactStructureSignature(text)
      continuityExpected += previousSignature.size
      continuityRetained += [...previousSignature].filter((value) => currentSignature.has(value)).length
    }
    previousArtifact = text
  }

  const finalTurn = fixture.at(-1)
  const finalArtifact = finalTurn ? extractProjectArtifact(responseByTurn.get(finalTurn.turn) ?? '') ?? '' : ''
  const finalCompleted = finalTurn?.requiredFeatureIds.filter((id) => hasRequirement(finalArtifact, id)).length ?? 0
  return {
    requirementCompletion: ratio(completedChecks, requiredChecks),
    decisionRetention: ratio(retainedChecks, retainedTotal),
    anchorPrecision: ratio(anchorTruePositive, anchorTruePositive + anchorFalsePositive),
    anchorRecall: ratio(anchorTruePositive, anchorExpected),
    firstOmissionTurn,
    firstContradictionTurn,
    artifactContinuity: ratio(continuityRetained, continuityExpected),
    finalRequirementCompletion: ratio(finalCompleted, finalTurn?.requiredFeatureIds.length ?? 0),
    manifestAccuracy: ratio(accurateManifests, manifestChecks)
  }
}

function isAccurateManifest(output: string, turn: ChatLongSessionTurn): boolean {
  if (/<(?:!doctype|html|head|body|style)\b|```/i.test(output)) return false
  let value: unknown
  try { value = JSON.parse(output.trim()) } catch { return false }
  if (!value || typeof value !== 'object' || Array.isArray(value)) return false
  const manifest = value as Record<string, unknown>
  if (Object.keys(manifest).sort().join(',') !== 'changeSummary,decisionAnchors,forbiddenConfirmed,introducedFeatureId') return false
  if (containsProhibitedManifestText(manifest)) return false
  if (manifest.introducedFeatureId !== turn.introducedFeatureId) return false
  if (typeof manifest.changeSummary !== 'string' || !manifest.changeSummary.trim() || manifest.changeSummary.length > 200 || /<\/?[a-z][^>]*>/i.test(manifest.changeSummary)) return false
  if (!sameStringArray(manifest.decisionAnchors, turn.exactAnchors)) return false
  return sameStringArray(manifest.forbiddenConfirmed, turn.forbiddenRegressions.map((item) => item.id))
}

function containsProhibitedManifestText(value: unknown): boolean {
  if (typeof value === 'string') return /`|~~~|<\/?[a-z][^>]*>/i.test(value.normalize('NFKC'))
  if (Array.isArray(value)) return value.some(containsProhibitedManifestText)
  if (!value || typeof value !== 'object') return false
  return Object.values(value as Record<string, unknown>).some(containsProhibitedManifestText)
}

function sameStringArray(value: unknown, expected: readonly string[]): boolean {
  return Array.isArray(value)
    && value.every((item) => typeof item === 'string')
    && value.length === expected.length
    && value.every((item, index) => item === expected[index])
}

export function buildSafeFixtureSummary(fixture: readonly ChatLongSessionTurn[]): {
  turnCount: number
  memoryProbeTurns: number[]
  modelSwitchTurns: number[]
  reasoningSwitchTurns: number[]
  serviceSwitchTurns: number[]
  artifactCheckpointTurns: number[]
  stageCounts: Record<ChatLongSessionStage, number>
} {
  const stageCounts = Object.fromEntries(stagePlan.map(({ stage }) => [stage, 0])) as Record<ChatLongSessionStage, number>
  for (const turn of fixture) stageCounts[turn.stage] += 1
  return {
    turnCount: fixture.length,
    memoryProbeTurns: fixture.filter((turn) => turn.memoryProbe).map((turn) => turn.turn),
    modelSwitchTurns: fixture.filter((turn) => turn.controls.model).map((turn) => turn.turn),
    reasoningSwitchTurns: fixture.filter((turn) => turn.controls.reasoning).map((turn) => turn.turn),
    serviceSwitchTurns: fixture.filter((turn) => turn.controls.service).map((turn) => turn.turn),
    artifactCheckpointTurns: fixture.filter((turn) => turn.responseMode === 'artifact').map((turn) => turn.turn),
    stageCounts
  }
}

function hasRequirement(artifact: string, id: string): boolean {
  const matches = [...artifact.matchAll(/\bdata-requirements\s*=\s*(["'])(.*?)\1/gi)]
  if (matches.length !== 1) return false
  if (!new Set(matches[0][2].trim().split(/\s+/).filter(Boolean)).has(id)) return false
  const visibleArtifact = stripArtifactComments(artifact)
  return (featureEvidencePatterns[id] ?? []).every((pattern) => pattern.test(visibleArtifact))
}

function artifactStructureSignature(artifact: string): Set<string> {
  artifact = stripArtifactComments(artifact)
  const signatures = new Set<string>()
  for (const [id, patterns] of Object.entries(featureEvidencePatterns)) {
    if (patterns.every((pattern) => pattern.test(artifact))) signatures.add(`evidence:${id}`)
  }
  for (const match of artifact.matchAll(/<([a-z][\w-]*)\b/gi)) signatures.add(`tag:${match[1].toLowerCase()}`)
  for (const match of artifact.matchAll(/\bclass\s*=\s*["']([^"']+)["']/gi)) {
    for (const className of match[1].split(/\s+/).filter(Boolean)) signatures.add(`class:${className.toLowerCase()}`)
  }
  return signatures
}

function stripArtifactComments(value: string): string {
  return value.replace(/<!--[\s\S]*?-->/g, '').replace(/\/\*[\s\S]*?\*\//g, '')
}

function normalizeMatchText(value: string): string {
  return value.toLowerCase().replace(/\s+/g, ' ').replace(/\s*([<>:=;{}])\s*/g, '$1').trim()
}

function ratio(numerator: number, denominator: number): number {
  if (denominator === 0) return 1
  return Number((numerator / denominator).toFixed(6))
}
import type { SafeChatStreamFailure } from './chat-long-session-failure.js'
