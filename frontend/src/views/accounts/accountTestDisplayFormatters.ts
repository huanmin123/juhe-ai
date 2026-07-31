import type { AccountListItem, AccountTestResult, AccountTestTask } from '@/types/domain'
import { isGptVendorCode } from '@/shared/providerProtocol'

import { accountDiagnosticMessageWithoutRepeatedFields } from './accountDiagnosticMessages'

import type { AccountTestEndpointMode } from './accountTestFlow'
import { accountProviderProtocolKind } from './accountProviderCapabilities'
import { accountEndpointModeLabel, accountTestEndpointModesForAccount } from './accountEndpointModes'
import { accountTypeText } from './accountBasicFormatters'
import { accountModelMappingEndpointFamilyText } from './accountModelMappingProtocolMatrix'
import {
  formatAccountTestDuration,
  formatErrorPolicyAction,
  formatTestTerminalResult,
  splitAccountDiagnosticMessage
} from './accountFormatters'

export interface AccountTestOutputLine {
  text: string
  tone?: 'muted' | 'info' | 'success' | 'warning' | 'error' | 'label' | 'divider'
}

const diagnosticAttemptTimeoutsMs = [10_000, 20_000, 30_000]
const imageDiagnosticAttemptTimeoutsMs = [120_000]

interface SingleAccountTestOutputOptions {
  account?: AccountListItem
  activeTask?: AccountTestTask
  testEndpointMode: AccountTestEndpointMode
  selectedEndpointModeText: string
  model: string
  providerLabel: (account: AccountListItem) => string
  result?: AccountTestResult
  running: boolean
}

export function accountTestSelectedEndpointModeText(input: {
  account: AccountListItem
  testEndpointMode: AccountTestEndpointMode
  selectedEndpointModeText: string
}): string {
  if (input.testEndpointMode !== 'account_default') {
    return accountEndpointModeLabel(input.testEndpointMode, input.account)
  }
  return input.selectedEndpointModeText
    || accountEndpointModeLabel(accountTestEndpointModesForAccount(input.account)[0] ?? 'chat_sse', input.account)
}

export function accountTestSingleOutputLines(options: SingleAccountTestOutputOptions): AccountTestOutputLine[] {
  const account = options.account
  if (!account || (!options.running && !options.result)) return []
  const selectedEndpointModeText = accountTestSelectedEndpointModeText({
    account,
    testEndpointMode: options.testEndpointMode,
    selectedEndpointModeText: options.selectedEndpointModeText
  })
  const imageTest = isImageTestMode(options.result?.testEndpointMode ?? options.testEndpointMode)
  const lines: AccountTestOutputLine[] = [
    { text: `开始测试账号：${account.name}`, tone: 'info' },
    { text: `供应商：${options.providerLabel(account)}`, tone: 'muted' },
    { text: `账号类型：${accountTypeText(account.type)}`, tone: 'muted' },
    {
      text: `测试请求形态：${selectedEndpointModeText}`,
      tone: 'muted'
    }
  ]

  if (options.running) {
    lines.push(...accountTestSingleRunningOutputLines({
      account,
      activeTask: options.activeTask,
      model: options.model,
      testEndpointMode: options.activeTask?.testEndpointMode ?? options.testEndpointMode
    }))
    return lines
  }

  if (!options.result) {
    lines.push({
      text: imageTest ? '点击「开始测试」后会检查图片生成是否成功。' : '点击「开始测试」后会显示完整返回结果。',
      tone: 'muted'
    })
    return lines
  }

  const statusCode = options.result.statusCode
  lines.push({
    text: typeof statusCode === 'number'
      ? statusCode >= 200 && statusCode < 300
        ? '已连接到 API'
        : `API 返回 HTTP ${statusCode}`
      : 'API 返回错误',
    tone: options.result.success ? 'success' : 'error'
  })
  lines.push(...accountTestModelMappingOutputLines(options.result, options.model))
  const diagnosticParts = splitAccountDiagnosticMessage(options.result.message)
  const traceId = options.result.traceId || diagnosticParts.traceId
  if (traceId) {
    lines.push({ text: `traceId：${traceId}`, tone: 'muted' })
  }
  if (diagnosticParts.requestId) {
    lines.push({ text: `request id：${diagnosticParts.requestId}`, tone: 'muted' })
  }
  lines.push(accountTestActualProtocolLine(account, options.result))
  lines.push(...accountTestApiKeyPoolOutputLines(options.result))
  if (imageTest) {
    lines.push({
      text: options.result.success ? '图像生成响应有效，测试通过。' : diagnosticParts.message || options.result.message,
      tone: options.result.success ? 'success' : 'error'
    })
  } else {
    lines.push({ text: '响应：', tone: 'label' })
    const outputText = formatTestTerminalResult(options.result)
    if (outputText) {
      lines.push({ text: outputText, tone: options.result.success ? 'success' : 'error' })
    } else {
      lines.push({
        text: diagnosticParts.message || options.result.message,
        tone: options.result.success ? 'success' : 'error'
      })
    }
  }
  if (options.result.errorPolicyAction && options.result.errorPolicyAction !== 'none') {
    const reason = options.result.errorPolicyReason ? `，原因：${options.result.errorPolicyReason}` : ''
    lines.push({
      text: `错误处理策略：${formatErrorPolicyAction(options.result.errorPolicyAction)}${reason}`,
      tone: 'warning'
    })
  }
  lines.push({ text: '', tone: 'divider' })
  const completionText = options.result.success ? '✓ 测试完成！' : '✕ 测试失败！'
  const firstTokenText = options.result.firstTokenMs !== undefined
    ? `，首 token：${formatAccountTestDuration(options.result.firstTokenMs)}`
    : ''
  lines.push({
    text: `${completionText}  总耗时：${formatAccountTestDuration(options.result.durationMs)}${firstTokenText}`,
    tone: options.result.success ? 'success' : 'error'
  })
  return lines
}

function accountTestModelMappingOutputLines(
  result: AccountTestResult,
  fallbackModel: string
): AccountTestOutputLine[] {
  const requestModel = result.model || fallbackModel
  const upstreamModel = result.upstreamModel || requestModel
  const lines: AccountTestOutputLine[] = [
    { text: `请求模型：${requestModel}`, tone: 'success' }
  ]
  if (result.modelMappingApplied) {
    const sourceEndpointFamily = result.sourceEndpointFamily
      ? accountModelMappingEndpointFamilyText(result.sourceEndpointFamily)
      : '当前请求'
    const upstreamEndpointFamily = result.upstreamEndpointFamily
      ? accountModelMappingEndpointFamilyText(result.upstreamEndpointFamily)
      : '上游'
    lines.push({
      text: `模型映射：${sourceEndpointFamily} / ${requestModel} -> ${upstreamEndpointFamily} / ${upstreamModel}`,
      tone: 'warning'
    })
    lines.push({ text: `实际上游模型：${upstreamModel}`, tone: 'success' })
  } else if (result.upstreamModel) {
    lines.push({ text: `实际上游模型：${result.upstreamModel}`, tone: 'success' })
  }
  return lines
}

export function accountTestSingleRunningOutputLines(input: {
  account: AccountListItem
  activeTask?: AccountTestTask
  model: string
  testEndpointMode?: AccountTestEndpointMode
}): AccountTestOutputLine[] {
  const task = input.activeTask
  const elapsedMs = accountTestTaskElapsedMs(task)
  const timeoutSchedule = diagnosticAttemptTimeoutsForMode(task?.testEndpointMode ?? input.testEndpointMode)
  const diagnosticMaxWaitMs = timeoutSchedule.reduce((sum, timeoutMs) => sum + timeoutMs, 0)
  const lines: AccountTestOutputLine[] = [
    { text: '正在走账户配置的真实请求流程...', tone: 'warning' },
    { text: `使用模型：${input.model}`, tone: 'success' },
    {
      text: `等待策略：后台接收后按 ${timeoutSchedule.map(formatAccountTestDuration).join(' + ')} 执行，运行超过 ${formatAccountTestDuration(diagnosticMaxWaitMs)} 会自动失败`,
      tone: 'muted'
    }
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
    lines.push({
      text: `运行耗时：${formatAccountTestDuration(elapsedMs)}`,
      tone: elapsedMs > diagnosticMaxWaitMs ? 'warning' : 'muted'
    })
    lines.push({
      text: `当前窗口估计：${accountTestDiagnosticAttemptWindowText(elapsedMs, task?.testEndpointMode ?? input.testEndpointMode)}`,
      tone: 'info'
    })
  }
  if (shouldDisplayManagedOAuthRefreshHint(input.account)) {
    lines.push({ text: 'OAuth Token 刷新也包含在当前等待窗口内', tone: 'muted' })
  }
  return lines
}

function shouldDisplayManagedOAuthRefreshHint(account: AccountListItem): boolean {
  if (account.type !== 'oauth') return false
  return accountProviderProtocolKind(account) === 'openai_v1'
    || account.clientCompatibility === 'codex_responses'
    || isGptVendorCode(account.providerCode)
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

export function accountTestDiagnosticAttemptWindowText(elapsedMs: number, testEndpointMode?: AccountTestEndpointMode): string {
  const timeoutSchedule = diagnosticAttemptTimeoutsForMode(testEndpointMode)
  const diagnosticMaxWaitMs = timeoutSchedule.reduce((sum, timeoutMs) => sum + timeoutMs, 0)
  let cursor = 0
  for (let index = 0; index < timeoutSchedule.length; index += 1) {
    const timeoutMs = timeoutSchedule[index]
      ?? timeoutSchedule[timeoutSchedule.length - 1]
    cursor += timeoutMs
    if (elapsedMs <= cursor) {
      return `第 ${index + 1}/${timeoutSchedule.length} 次，单次最多 ${formatAccountTestDuration(timeoutMs)}`
    }
  }
  return `已超过 ${formatAccountTestDuration(diagnosticMaxWaitMs)}，将自动停止并返回运行超时错误`
}

function diagnosticAttemptTimeoutsForMode(testEndpointMode?: AccountTestEndpointMode): readonly number[] {
  return isImageTestMode(testEndpointMode)
    ? imageDiagnosticAttemptTimeoutsMs
    : diagnosticAttemptTimeoutsMs
}

function isImageTestMode(testEndpointMode?: AccountTestEndpointMode): boolean {
  return testEndpointMode === 'images_json'
}

function accountTestActualProtocolLine(
  account: AccountListItem,
  result: AccountTestResult
): AccountTestOutputLine {
  const endpointMode = result.testEndpointMode ?? accountTestEndpointModesForAccount(account)[0]
  return {
    text: `实际请求形态：${endpointMode ? accountEndpointModeLabel(endpointMode, account) : fallbackProtocolText(account)}`,
    tone: 'muted'
  }
}

function fallbackProtocolText(account: AccountListItem): string {
  const protocolKind = accountProviderProtocolKind(account)
  if (protocolKind === 'anthropic_v1') return 'Messages API'
  if (protocolKind === 'gemini_v1beta') return 'generateContent'
  return 'OpenAI API'
}

function accountTestApiKeyPoolOutputLines(result: AccountTestResult): AccountTestOutputLine[] {
  const pool = result.apiKeyPool
  if (!pool?.results.length) return []
  const untestedCount = Math.max(0, pool.total - pool.tested)
  const lines: AccountTestOutputLine[] = [
    {
      text: `API Key 池结果：可用 ${pool.successCount}/${pool.total}，已测试 ${pool.tested} 个${untestedCount > 0 ? `，未测试 ${untestedCount} 个` : ''}`,
      tone: pool.failedCount > 0 ? 'warning' : 'success'
    }
  ]
  for (const item of pool.results) {
    const statusText = item.success ? '通过' : '失败'
    const statusCodeText = typeof item.statusCode === 'number' ? `，HTTP ${item.statusCode}` : ''
    const durationText = item.durationMs !== undefined
      ? `，耗时 ${formatAccountTestDuration(item.durationMs)}`
      : ''
    const errorCodeText = item.errorCode ? `，错误码 ${item.errorCode}` : ''
    const message = accountDiagnosticMessageWithoutRepeatedFields(item.message, {
      statusCode: item.statusCode,
      errorCode: item.errorCode
    })
    const messageText = message ? `，${message}` : ''
    lines.push({
      text: `API Key ${accountTestApiKeyPreview(item)} 测试结果：${statusText}${statusCodeText}${durationText}${errorCodeText}${messageText}`,
      tone: item.success ? 'success' : 'error'
    })
  }
  return lines
}

function accountTestApiKeyPreview(
  item: NonNullable<AccountTestResult['apiKeyPool']>['results'][number]
): string {
  const prefix = previewPart(item.keyPrefix)
  const suffix = previewPart(item.keySuffix)
  if (prefix && suffix) return `${prefix}...${suffix}`
  if (prefix) return `${prefix}...`
  if (suffix) return `...${suffix}`
  return `#${item.keyIndex + 1}`
}

function previewPart(value: unknown): string {
  return typeof value === 'string' ? value.trim() : ''
}

function parseTaskTime(value?: string): number | undefined {
  if (!value) return undefined
  const timestamp = Date.parse(value)
  return Number.isFinite(timestamp) ? timestamp : undefined
}
