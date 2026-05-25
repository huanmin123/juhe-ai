import type { AccountSummary, AccountTestResult } from '@/types/domain'
import { extractApiErrorMessage } from '@/shared/apiError'

export type AccountTestForm = {
  model: string
}

export function buildAccountTestPayload(form: AccountTestForm): { model: string } {
  return {
    model: form.model
  }
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
  startedAt: number
}): AccountTestResult {
  const fallbackMessage = extractApiErrorMessage(input.error, '测试失败')
  return {
    accountId: input.account.id,
    accountName: input.account.name,
    providerCode: input.account.providerCode,
    type: input.account.type,
    success: false,
    message: fallbackMessage,
    model: input.model,
    responseText: fallbackMessage,
    durationMs: Date.now() - input.startedAt
  }
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
