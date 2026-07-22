import { api, type AccountDraftTestPayload, type AccountTestPayload } from '@/api/client'
import type { AccountSummary, AccountTestSession, AccountTestTask } from '@/types/domain'
import { accountOperationScopeParams, type AccountScopeParams } from './accountOperationScope'

export type AccountTestDraftMode = 'create' | 'saved'

interface AccountTestClientScope {
  isManagementView: boolean
  scopeParams?: AccountScopeParams
}

interface AccountTestSessionClientInput extends AccountTestClientScope {
  sessionId: string
}

export function createAccountTestSession(input: AccountTestClientScope): Promise<AccountTestSession> {
  return input.isManagementView
    ? api.accounts.createTestSession(input.scopeParams)
    : api.myAccounts.createTestSession()
}

export function heartbeatAccountTestSession(input: AccountTestSessionClientInput): Promise<AccountTestSession> {
  return input.isManagementView
    ? api.accounts.heartbeatTestSession(input.sessionId, input.scopeParams)
    : api.myAccounts.heartbeatTestSession(input.sessionId)
}

export function completeAccountTestSession(input: AccountTestSessionClientInput): Promise<AccountTestSession> {
  return input.isManagementView
    ? api.accounts.completeTestSession(input.sessionId, input.scopeParams)
    : api.myAccounts.completeTestSession(input.sessionId)
}

export function cancelAccountTestSession(input: AccountTestSessionClientInput): Promise<AccountTestSession> {
  return input.isManagementView
    ? api.accounts.cancelTestSession(input.sessionId, input.scopeParams)
    : api.myAccounts.cancelTestSession(input.sessionId)
}

export function submitAccountTestTask(input: {
  account: AccountSummary
  accountScopeParams?: AccountScopeParams
  draftMode?: AccountTestDraftMode
  draftPayload?: AccountDraftTestPayload['account']
  isManagementView: boolean
  payload: AccountTestPayload
  sessionId: string
}): Promise<AccountTestTask> {
  const requestPayload: AccountTestPayload = { ...input.payload, testSessionId: input.sessionId }
  if (input.draftPayload) {
    const { model: _ignoredModel, account: _ignoredAccount, ...draftTestOptions } = requestPayload
    const draftRequestPayload: AccountDraftTestPayload = { account: input.draftPayload, ...draftTestOptions }
    if (input.draftMode === 'saved') {
      return input.isManagementView
        ? api.accounts.test(input.account.id, draftRequestPayload, accountOperationScopeParams(input.account, input.accountScopeParams))
        : api.myAccounts.test(input.account.id, draftRequestPayload)
    }
    return input.isManagementView
      ? api.accounts.testDraft(draftRequestPayload, input.accountScopeParams)
      : api.myAccounts.testDraft(draftRequestPayload)
  }
  return input.isManagementView
    ? api.accounts.test(input.account.id, requestPayload, accountOperationScopeParams(input.account, input.accountScopeParams))
    : api.myAccounts.test(input.account.id, requestPayload)
}

export function fetchAccountTestTask(input: AccountTestClientScope & {
  signal?: AbortSignal
  taskId: string
}): Promise<AccountTestTask> {
  return input.isManagementView
    ? api.accounts.testTask(input.taskId, input.scopeParams, { signal: input.signal })
    : api.myAccounts.testTask(input.taskId, { signal: input.signal })
}

export function cancelAccountTestTask(input: AccountTestClientScope & { taskId: string }): Promise<AccountTestTask> {
  return input.isManagementView
    ? api.accounts.cancelTestTask(input.taskId, input.scopeParams)
    : api.myAccounts.cancelTestTask(input.taskId)
}
