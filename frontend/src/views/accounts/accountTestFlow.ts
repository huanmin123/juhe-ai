import type { AccountDraftTestAccountPayload } from '@/api/client'
import type { AccountListItem, AccountSupportedEndpointMode, AccountTestResult } from '@/types/domain'
import { extractApiErrorMessage } from '@/shared/apiError'
import {
  accountTestEndpointModesForAccount,
  defaultAccountTestEndpointModeForSelection
} from './accountEndpointModes'
import type { AccountCapabilityDomain } from './accountCapabilityDomains'

export type AccountTestEndpointMode = 'account_default' | AccountSupportedEndpointMode

export type AccountTestForm = {
  model: string
  testEndpointMode: AccountTestEndpointMode
}

export function buildAccountTestPayload(
  form: AccountTestForm,
  account?: AccountListItem,
  draftAccount?: AccountDraftTestAccountPayload
): { model?: string; testEndpointMode?: AccountSupportedEndpointMode; kind?: 'connectivity' } {
  const domain = account?.capabilityDomain ?? draftAccount?.capabilityDomain
  if (domain && domain !== 'chat') {
    // 非对话账户只测 Key + Base URL：不选模型/协议，服务端按场景选代表探测模式。
    return { kind: 'connectivity' }
  }
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

/** 非对话连通性测试的展示用代表 mode；真实 mode 由服务端按账户决定。 */
export function connectivityEndpointModeForDomain(
  domain?: AccountCapabilityDomain
): AccountSupportedEndpointMode {
  switch (domain) {
    case 'video':
      return 'video_create'
    case 'image':
      return 'images_json'
    case 'audio':
      return 'audio_speech'
    case 'embedding':
      return 'embed_content'
    case 'rerank':
      return 'rerank_json'
    case 'moderation':
      return 'moderation_json'
    default:
      return 'chat_sse'
  }
}

export function accountTestSuccessMessage(account: AccountListItem, result: AccountTestResult): string {
  return `${account.name}: ${result.message}${result.tokenRefreshed ? '，并已刷新 token' : ''}`
}

export function accountTestErrorMessage(account: AccountListItem, result: AccountTestResult): string {
  return `${account.name}: ${result.message}`
}

export function stoppedAccountTestMessage(account: AccountListItem): string {
  return `${account.name}: 已停止测试`
}

export function failedAccountTestResult(input: {
  account: AccountListItem
  error: unknown
  model: string
  testEndpointMode: AccountTestEndpointMode
  startedAt?: number
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
    ...(input.startedAt !== undefined ? { durationMs: Math.max(0, Date.now() - input.startedAt) } : {})
  }
}

export function effectiveAccountTestEndpointMode(
  account: AccountListItem,
  testEndpointMode: AccountTestEndpointMode,
  draftAccount?: AccountDraftTestAccountPayload
): AccountSupportedEndpointMode | undefined {
  if (testEndpointMode !== 'account_default') return testEndpointMode
  return accountTestEndpointModesForAccount(account, draftAccount)[0]
    ?? defaultAccountTestEndpointModeForSelection(account, draftAccount)
}
