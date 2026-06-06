import type { AccountClientCompatibility, AccountSummary, AccountTestResult } from '@/types/domain'
import { extractApiErrorMessage } from '@/shared/apiError'

export type AccountTestClientCompatibility = 'account_default' | AccountClientCompatibility
export type AccountTestMode = 'single' | 'batch'

export type AccountBatchTestStatus = 'pending' | 'running' | 'success' | 'failed' | 'stopped'

export type AccountTestForm = {
  model: string
  clientCompatibility: AccountTestClientCompatibility
}

export interface AccountBatchTestItem {
  account: AccountSummary
  status: AccountBatchTestStatus
  taskId?: string
  result?: AccountTestResult
  message?: string
  startedAt?: number
  finishedAt?: number
}

export function buildAccountTestPayload(form: AccountTestForm): { model?: string; clientCompatibility?: AccountClientCompatibility } {
  const payload: { model?: string; clientCompatibility?: AccountClientCompatibility } = {}
  const model = form.model.trim()
  if (model) {
    payload.model = model
  }
  if (form.clientCompatibility !== 'account_default') {
    payload.clientCompatibility = form.clientCompatibility
  }
  return payload
}

export function accountTestSuccessMessage(account: AccountSummary, result: AccountTestResult): string {
  return `${account.name}: ${result.message}${result.tokenRefreshed ? '，并已刷新 token' : ''}`
}

export function accountTestErrorMessage(account: AccountSummary, result: AccountTestResult): string {
  return `${account.name}: ${result.message}`
}

export function stoppedAccountTestMessage(account: AccountSummary): string {
  return `${account.name}: 已停止测试`
}

export function failedAccountTestResult(input: {
  account: AccountSummary
  error: unknown
  model: string
  clientCompatibility: AccountTestClientCompatibility
  startedAt: number
}): AccountTestResult {
  const fallbackMessage = extractApiErrorMessage(input.error, '测试失败')
  const testClientCompatibility = effectiveAccountTestClientCompatibility(input.account, input.clientCompatibility)
  return {
    accountId: input.account.id,
    accountName: input.account.name,
    providerCode: input.account.providerCode,
    type: input.account.type,
    clientCompatibility: effectiveAccountTestClientCompatibility(input.account, 'account_default'),
    testClientCompatibility,
    success: false,
    message: fallbackMessage,
    model: input.model,
    responseText: fallbackMessage,
    durationMs: Date.now() - input.startedAt
  }
}

function effectiveAccountTestClientCompatibility(account: AccountSummary, clientCompatibility: AccountTestClientCompatibility): AccountClientCompatibility {
  if (account.providerCode === 'openai' && account.type === 'oauth') {
    return 'codex_responses'
  }
  return clientCompatibility === 'account_default' ? account.clientCompatibility : clientCompatibility
}

export function nextTestModel(currentModel: string, modelOptions: Array<{ value: string }>, defaultModel: string): string {
  if (!modelOptions.length) return currentModel || defaultModel
  return modelOptions.some((item) => item.value === currentModel) ? currentModel : defaultModel
}

export function batchTestSummary(total: number, successCount: number): { success: boolean; message: string } {
  const failedCount = total - successCount
  if (failedCount === 0) {
    return { success: true, message: `批量测试完成，${successCount} 个账户全部通过` }
  }
  return { success: false, message: `批量测试完成，成功 ${successCount} 个，失败 ${failedCount} 个` }
}
