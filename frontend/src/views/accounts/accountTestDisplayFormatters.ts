import type { AccountSummary, AccountTestResult, AccountTestTask } from '@/types/domain'
import { isAnthropicProtocolProfile } from '@/shared/providerProtocol'

import type { AccountBatchTestItem, AccountTestClientCompatibility } from './accountTestFlow'
import {
  accountClientCompatibilityText,
  accountTypeText
} from './accountBasicFormatters'
import {
  formatAccountTestDuration,
  formatErrorPolicyAction,
  formatTestTerminalResult,
  splitAccountDiagnosticMessage,
  statusText
} from './accountFormatters'

export interface AccountTestOutputLine {
  text: string
  tone?: 'muted' | 'info' | 'success' | 'warning' | 'error' | 'label' | 'divider'
}

const diagnosticAttemptTimeoutsMs = [10_000, 20_000, 30_000]
const diagnosticMaxWaitMs = diagnosticAttemptTimeoutsMs.reduce((sum, value) => sum + value, 0)

interface AccountTestBatchCounts {
  completed: number
  failed: number
  queued: number
  running: number
  stopped: number
  success: number
  total: number
}

interface SingleAccountTestOutputOptions {
  account?: AccountSummary
  activeTask?: AccountTestTask
  clientCompatibility: AccountTestClientCompatibility
  fixedOAuthCompatibilityText: string
  model: string
  providerLabel: (account: AccountSummary) => string
  result?: AccountTestResult
  running: boolean
}

interface BatchAccountTestOutputOptions {
  batchItems: AccountBatchTestItem[]
  counts: AccountTestBatchCounts
  model: string
  running: boolean
  selectedCompatibilityText: string
}

export function accountTestBatchCounts(items: AccountBatchTestItem[]): AccountTestBatchCounts {
  const success = items.filter((item) => item.status === 'success').length
  const failed = items.filter((item) => item.status === 'failed').length
  const stopped = items.filter((item) => item.status === 'stopped').length
  const queued = items.filter((item) => item.status === 'queued').length
  const running = items.filter((item) => item.status === 'running').length
  return {
    completed: success + failed + stopped,
    failed,
    queued,
    running,
    stopped,
    success,
    total: items.length
  }
}

export function accountTestBatchSelectedCompatibilityText(input: {
  clientCompatibility: AccountTestClientCompatibility
  fixedOAuthCompatibilityText: string
  showClientCompatibilityControl: boolean
}): string {
  if (!input.showClientCompatibilityControl) return input.fixedOAuthCompatibilityText
  return input.clientCompatibility === 'account_default'
    ? '跟随账户配置'
    : accountClientCompatibilityText(input.clientCompatibility)
}

export function accountTestBatchStatusColor(counts: AccountTestBatchCounts, running: boolean): string {
  if (running || counts.queued || counts.running) return 'blue'
  if (counts.failed) return 'red'
  if (counts.stopped) return 'orange'
  if (counts.success && counts.success === counts.total) return 'green'
  return 'default'
}

export function accountTestBatchStatusText(counts: AccountTestBatchCounts, running: boolean): string {
  if (running || counts.queued || counts.running) return `测试中 ${counts.completed} / ${counts.total}`
  if (!counts.completed) return '等待开始'
  if (counts.failed) return `成功 ${counts.success}，失败 ${counts.failed}`
  if (counts.stopped) return `已停止 ${counts.stopped}`
  return '全部通过'
}

export function accountTestSelectedCompatibilityText(input: {
  account: AccountSummary
  clientCompatibility: AccountTestClientCompatibility
  fixedOAuthCompatibilityText: string
}): string {
  if (isAnthropicProtocolProfile(input.account)) {
    return 'Anthropic 原生'
  }
  if (input.account.type === 'oauth') {
    return input.fixedOAuthCompatibilityText
  }
  if (input.clientCompatibility === 'account_default') {
    return `跟随账户配置（${accountClientCompatibilityText(input.account.clientCompatibility)}）`
  }
  return accountClientCompatibilityText(input.clientCompatibility)
}

export function accountTestSingleOutputLines(options: SingleAccountTestOutputOptions): AccountTestOutputLine[] {
  const account = options.account
  if (!account || (!options.running && !options.result)) return []
  const selectedCompatibilityText = accountTestSelectedCompatibilityText({
    account,
    clientCompatibility: options.clientCompatibility,
    fixedOAuthCompatibilityText: options.fixedOAuthCompatibilityText
  })
  const lines: AccountTestOutputLine[] = [
    { text: `开始测试账号：${account.name}`, tone: 'info' },
    { text: `供应商：${options.providerLabel(account)}`, tone: 'muted' },
    { text: `账号类型：${accountTypeText(account.type)}`, tone: 'muted' },
    {
      text: `${isAnthropicProtocolProfile(account) ? '测试协议' : '测试兼容'}：${selectedCompatibilityText}`,
      tone: 'muted'
    }
  ]

  if (options.running) {
    lines.push(...accountTestSingleRunningOutputLines({
      account,
      activeTask: options.activeTask,
      model: options.model
    }))
    return lines
  }

  if (!options.result) {
    lines.push({ text: '点击「开始测试」后会显示完整返回结果。', tone: 'muted' })
    return lines
  }

  lines.push({
    text: options.result.statusCode && options.result.statusCode >= 200 && options.result.statusCode < 300 ? '已连接到 API' : 'API 返回错误',
    tone: options.result.success ? 'success' : 'error'
  })
  lines.push({ text: `使用模型：${options.result.model || options.model}`, tone: 'success' })
  const diagnosticParts = splitAccountDiagnosticMessage(options.result.message)
  const traceId = options.result.traceId || diagnosticParts.traceId
  if (traceId) {
    lines.push({ text: `traceId：${traceId}`, tone: 'muted' })
  }
  if (diagnosticParts.requestId) {
    lines.push({ text: `request id：${diagnosticParts.requestId}`, tone: 'muted' })
  }
  lines.push(accountTestActualProtocolLine(account, options.result))
  lines.push({ text: '响应：', tone: 'label' })
  const outputText = formatTestTerminalResult(options.result)
  if (outputText) {
    lines.push({ text: outputText, tone: options.result.success ? 'success' : 'error' })
  } else {
    lines.push({ text: diagnosticParts.message || options.result.message, tone: options.result.success ? 'success' : 'error' })
  }
  if (options.result.errorPolicyAction && options.result.errorPolicyAction !== 'none') {
    const reason = options.result.errorPolicyReason ? `，原因：${options.result.errorPolicyReason}` : ''
    lines.push({ text: `错误处理策略：${formatErrorPolicyAction(options.result.errorPolicyAction)}${reason}`, tone: 'warning' })
  }
  if (options.result.accountStatusChanged || options.result.accountStatus) {
    const status = options.result.accountStatus ? statusText(options.result.accountStatus) : '未变化'
    lines.push({ text: `账号状态：${status}`, tone: options.result.accountStatusChanged ? 'warning' : 'muted' })
  }
  lines.push({ text: '', tone: 'divider' })
  const completionText = options.result.success ? '✓ 测试完成！' : '✕ 测试失败！'
  const firstTokenText = options.result.firstTokenMs !== undefined ? `，首 token：${formatAccountTestDuration(options.result.firstTokenMs)}` : ''
  lines.push({
    text: `${completionText}  总耗时：${formatAccountTestDuration(options.result.durationMs)}${firstTokenText}`,
    tone: options.result.success ? 'success' : 'error'
  })
  return lines
}

export function accountTestSingleRunningOutputLines(input: {
  account: AccountSummary
  activeTask?: AccountTestTask
  model: string
}): AccountTestOutputLine[] {
  const task = input.activeTask
  const elapsedMs = accountTestTaskElapsedMs(task)
  const lines: AccountTestOutputLine[] = [
    { text: '正在走账户配置的真实请求流程...', tone: 'warning' },
    { text: `使用模型：${input.model}`, tone: 'success' },
    { text: `等待策略：后台接收后按 10s + 20s + 30s 执行，运行超过 ${formatAccountTestDuration(diagnosticMaxWaitMs)} 会自动失败`, tone: 'muted' }
  ]
  if (task?.id) {
    lines.push({ text: `后台任务：${task.id}（${accountTestTaskStatusText(task.status)}）`, tone: 'muted' })
  } else {
    lines.push({ text: '后台任务：提交中', tone: 'muted' })
  }
  if (task?.message) {
    lines.push({ text: task.message, tone: task.status === 'queued' ? 'muted' : 'info' })
  }
  if (task?.status === 'queued') {
    lines.push({ text: '等待后台 worker 接收，尚未开始计时', tone: 'muted' })
  }
  if (elapsedMs !== undefined) {
    lines.push({ text: `运行耗时：${formatAccountTestDuration(elapsedMs)}`, tone: elapsedMs > diagnosticMaxWaitMs ? 'warning' : 'muted' })
    lines.push({ text: `当前窗口估计：${accountTestDiagnosticAttemptWindowText(elapsedMs)}`, tone: 'info' })
  }
  if (input.account.type === 'oauth') {
    lines.push({ text: 'OAuth Token 刷新也包含在当前等待窗口内', tone: 'muted' })
  }
  return lines
}

export function accountTestBatchOutputLines(options: BatchAccountTestOutputOptions): AccountTestOutputLine[] {
  if (!options.counts.total) return []
  const lines: AccountTestOutputLine[] = [
    { text: `批量测试账号：${options.counts.total} 个`, tone: 'info' },
    { text: '提交策略：每批最多 10 个账户，本批全部结束后再提交下一批', tone: 'muted' },
    { text: `优先测试模型：${options.model}`, tone: 'muted' },
    { text: `测试兼容：${options.selectedCompatibilityText}`, tone: 'muted' },
    { text: `单个任务运行上限：${formatAccountTestDuration(diagnosticMaxWaitMs)}，后台未接收前不计时`, tone: 'muted' }
  ]
  if (options.running || options.counts.queued || options.counts.running) {
    const activeNames = options.batchItems
      .filter((item) => item.status === 'queued' || item.status === 'running')
      .map((item) => item.account.name)
      .slice(0, 3)
      .join('、')
    lines.push({ text: `正在执行：已完成 ${options.counts.completed} / ${options.counts.total}`, tone: 'warning' })
    if (activeNames) lines.push({ text: `当前账户：${activeNames}`, tone: 'success' })
    const activeItems = options.batchItems.filter((item) => item.status === 'queued' || item.status === 'running').slice(0, 3)
    for (const item of activeItems) {
      const elapsedMs = item.startedAt ? Date.now() - item.startedAt : undefined
      const elapsedText = elapsedMs !== undefined ? `，运行 ${formatAccountTestDuration(elapsedMs)}` : '，未开始计时'
      lines.push({ text: `${item.account.name}: ${accountTestBatchItemMessage(item)}${elapsedText}`, tone: item.status === 'queued' ? 'muted' : 'info' })
    }
    return lines
  }
  if (!options.counts.completed) {
    lines.push({ text: '点击「开始批量测试」后会逐个显示结果。', tone: 'muted' })
    return lines
  }
  lines.push({
    text: `测试完成：成功 ${options.counts.success} 个，失败 ${options.counts.failed} 个，已停止 ${options.counts.stopped} 个`,
    tone: options.counts.failed ? 'warning' : 'success'
  })
  const failedItems = options.batchItems.filter((item) => item.status === 'failed').slice(0, 5)
  if (failedItems.length) {
    lines.push({ text: '失败摘要：', tone: 'label' })
    for (const item of failedItems) {
      lines.push({ text: `${item.account.name}: ${accountTestBatchItemMessage(item)}`, tone: 'error' })
    }
  }
  return lines
}

export function accountTestBatchItemStatusColor(item: AccountBatchTestItem): string {
  if (item.status === 'success') return 'green'
  if (item.status === 'failed') return 'red'
  if (item.status === 'running') return 'blue'
  if (item.status === 'queued') return 'processing'
  if (item.status === 'stopped') return 'orange'
  return 'default'
}

export function accountTestBatchItemStatusText(item: AccountBatchTestItem): string {
  if (item.status === 'success') return '通过'
  if (item.status === 'failed') return '失败'
  if (item.status === 'running') return '测试中'
  if (item.status === 'queued') return '等待接收'
  if (item.status === 'stopped') return '已停止'
  return '等待'
}

export function accountTestTaskStatusText(status: AccountTestTask['status']): string {
  if (status === 'queued') return '等待接收'
  if (status === 'running') return '测试中'
  if (status === 'success') return '已通过'
  if (status === 'failed') return '失败'
  return '已停止'
}

export function accountTestTaskElapsedMs(task?: AccountTestTask): number | undefined {
  const startedAt = parseTaskTime(task?.startedAt)
  if (startedAt === undefined) return undefined
  return Math.max(0, Date.now() - startedAt)
}

export function accountTestDiagnosticAttemptWindowText(elapsedMs: number): string {
  let cursor = 0
  for (let index = 0; index < diagnosticAttemptTimeoutsMs.length; index += 1) {
    const timeoutMs = diagnosticAttemptTimeoutsMs[index] ?? diagnosticAttemptTimeoutsMs[diagnosticAttemptTimeoutsMs.length - 1]
    cursor += timeoutMs
    if (elapsedMs <= cursor) {
      return `第 ${index + 1}/${diagnosticAttemptTimeoutsMs.length} 次，单次最多 ${formatAccountTestDuration(timeoutMs)}`
    }
  }
  return `已超过 ${formatAccountTestDuration(diagnosticMaxWaitMs)}，将自动停止并返回运行超时错误`
}

export function accountTestBatchItemModelText(item: AccountBatchTestItem, model: string): string {
  if (item.result?.model) return item.result.model
  return item.status === 'pending' ? `优先 ${model}` : model
}

export function accountTestBatchItemDurationText(item: AccountBatchTestItem): string {
  if (item.result?.durationMs !== undefined) return formatAccountTestDuration(item.result.durationMs)
  if (item.startedAt && item.finishedAt) return formatAccountTestDuration(item.finishedAt - item.startedAt)
  return ''
}

export function accountTestBatchItemMessage(item: AccountBatchTestItem): string {
  if (item.message) return item.message
  if (item.result?.message) return item.result.message
  if (item.status === 'queued') return '等待后台 worker 接收'
  if (item.status === 'running') return isAnthropicProtocolProfile(item.account) ? '正在连接 Anthropic API' : '正在连接 OpenAI API'
  if (item.status === 'stopped') return '已停止测试'
  return '等待开始测试'
}

function accountTestActualProtocolLine(account: AccountSummary, result: AccountTestResult): AccountTestOutputLine {
  if (isAnthropicProtocolProfile(account)) {
    return { text: '实际协议：Anthropic 原生', tone: 'muted' }
  }
  return {
    text: `实际兼容：${accountClientCompatibilityText(result.testClientCompatibility ?? result.clientCompatibility ?? account.clientCompatibility)}`,
    tone: 'muted'
  }
}

export function accountTestBatchItemJson(item: AccountBatchTestItem): string {
  return JSON.stringify(accountTestBatchItemSnapshot(item), null, 2)
}

export function accountTestBatchResultSnapshot(input: {
  batchItems: AccountBatchTestItem[]
  clientCompatibility: AccountTestClientCompatibility
  model: string
}) {
  const counts = accountTestBatchCounts(input.batchItems)
  return {
    model: input.model,
    clientCompatibility: input.clientCompatibility,
    summary: {
      total: counts.total,
      completed: counts.completed,
      success: counts.success,
      failed: counts.failed,
      stopped: counts.stopped
    },
    results: input.batchItems.map(accountTestBatchItemSnapshot)
  }
}

export function accountTestBatchItemSnapshot(item: AccountBatchTestItem) {
  return {
    accountId: item.account.id,
    accountName: item.account.name,
    providerCode: item.account.providerCode,
    type: item.account.type,
    status: item.status,
    message: accountTestBatchItemMessage(item),
    startedAt: item.startedAt,
    finishedAt: item.finishedAt,
    result: item.result
  }
}

function parseTaskTime(value?: string): number | undefined {
  if (!value) return undefined
  const timestamp = Date.parse(value)
  return Number.isFinite(timestamp) ? timestamp : undefined
}
