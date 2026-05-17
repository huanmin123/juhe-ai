import type { AccountSummary, AccountTestResult, ProviderModelPricing } from '@/types/domain'

export type AccountTestForm = {
  model: string
  prompt: string
}

export function buildAccountTestPayload(form: AccountTestForm): { model: string; prompt: string } {
  return {
    model: form.model,
    prompt: form.prompt
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
  const fallbackMessage = input.error instanceof Error ? input.error.message : '测试失败'
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

export function nextTestModel(currentModel: string, providerModels: ProviderModelPricing[], defaultModel: string): string {
  if (!providerModels.length) return currentModel
  return providerModels.some((item) => item.model === currentModel) ? currentModel : defaultModel
}

export function batchTestSummary(total: number, successCount: number): { success: boolean; message: string } {
  const failedCount = total - successCount
  if (failedCount === 0) {
    return { success: true, message: `批量测试完成，${successCount} 个账户全部通过` }
  }
  return { success: false, message: `批量测试完成，成功 ${successCount} 个，失败 ${failedCount} 个` }
}
