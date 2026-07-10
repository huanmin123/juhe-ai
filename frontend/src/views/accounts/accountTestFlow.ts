import type { AccountDraftTestAccountPayload } from '@/api/client'
import type { AccountSummary, AccountSupportedEndpointMode, AccountTestResult } from '@/types/domain'
import { extractApiErrorMessage } from '@/shared/apiError'
import {
  accountTestEndpointModesForAccount,
  defaultAccountTestEndpointModeForSelection
} from './accountEndpointModes'

export type AccountTestEndpointMode = 'account_default' | AccountSupportedEndpointMode

export type AccountTestForm = {
  model: string
  testEndpointMode: AccountTestEndpointMode
}

export function buildAccountTestPayload(
  form: AccountTestForm,
  account?: AccountSummary,
  draftAccount?: AccountDraftTestAccountPayload
): { model?: string; testEndpointMode?: AccountSupportedEndpointMode } {
  const payload: { model?: string; testEndpointMode?: AccountSupportedEndpointMode } = {}
  const model = form.model.trim()
  if (model) {
    payload.model = model
  }
  if (account) {
    const testEndpointMode = effectiveAccountTestEndpointMode(account, form.testEndpointMode, draftAccount)
    if (testEndpointMode) {
      payload.testEndpointMode = testEndpointMode
    }
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
  testEndpointMode: AccountTestEndpointMode
  startedAt: number
}): AccountTestResult {
  const fallbackMessage = extractApiErrorMessage(input.error, '测试失败')
  const testEndpointMode = effectiveAccountTestEndpointMode(input.account, input.testEndpointMode)
  return {
    accountId: input.account.id,
    accountName: input.account.name,
    providerCode: input.account.providerCode,
    type: input.account.type,
    success: false,
    message: fallbackMessage,
    model: input.model,
    testEndpointMode,
    responseText: fallbackMessage,
    durationMs: Date.now() - input.startedAt
  }
}

export function effectiveAccountTestEndpointMode(
  account: AccountSummary,
  testEndpointMode: AccountTestEndpointMode,
  draftAccount?: AccountDraftTestAccountPayload
): AccountSupportedEndpointMode | undefined {
  if (testEndpointMode !== 'account_default') return testEndpointMode
  return accountTestEndpointModesForAccount(account, draftAccount)[0]
    ?? defaultAccountTestEndpointModeForSelection(account, draftAccount)
}
