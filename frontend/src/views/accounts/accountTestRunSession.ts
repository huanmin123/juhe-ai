import { authState } from '@/composables/useAuth'
import type {
  AccountModelMapping,
  AccountSummary,
  AccountTestResult,
  AccountTestTask
} from '@/types/domain'
import type {
  AccountBatchTestItem,
  AccountTestEndpointMode,
  AccountTestMode
} from './accountTestFlow'

export const accountTestRunSessionStorageTtlMs = 12 * 60 * 60 * 1000

const accountTestRunSessionStoragePrefix = 'juhe-ai:account-test-run-session:'
const accountTestRunSessionStorageVersion = 2

export interface AccountTestRunSessionSnapshot {
  sessionId: string
  isManagementView: boolean
  scopeParams?: { systemAccountId: string }
  mode: AccountTestMode
  draftMode?: 'create' | 'saved'
  model: string
  testEndpointMode: AccountTestEndpointMode
  testingAccount?: AccountSummary
  batchTestingAccounts: AccountSummary[]
  activeSingleTestTask?: AccountTestTask
  batchTestItems: AccountBatchTestItem[]
  result?: AccountTestResult
  running: boolean
}

interface StoredAccountTestRunSession {
  expiresAt: number
  version: number
  snapshot: StoredAccountTestRunSessionSnapshot
}

interface StoredAccountTestRunSessionSnapshot {
  sessionId: string
  isManagementView: boolean
  scopeParams?: { systemAccountId: string }
  mode: AccountTestMode
  draftMode?: 'create' | 'saved'
  model: string
  testEndpointMode: AccountTestEndpointMode
  testingAccount?: StoredAccountSummary
  batchTestingAccounts: StoredAccountSummary[]
  activeSingleTestTask?: AccountTestTask
  batchTestItems: StoredAccountBatchTestItem[]
  result?: AccountTestResult
  running: boolean
}

interface StoredAccountBatchTestItem extends Omit<AccountBatchTestItem, 'account'> {
  account: StoredAccountSummary
}

interface StoredAccountSummary {
  id: string
  systemAccountId?: string
  providerCode: AccountSummary['providerCode']
  providerProtocolProfileId?: string
  protocolCode?: string
  protocolVersion?: string
  name: string
  type: AccountSummary['type']
  status: AccountSummary['status']
  concurrencyLimit: number
  priority: number
  superPriorityEnabled: boolean
  fallbackEnabled: boolean
  clientCompatibility: AccountSummary['clientCompatibility']
  supportedModels?: string[]
  defaultTestModel?: string
  modelMappings?: AccountModelMapping[]
  proxyProfileId?: string
  proxyProfileUnavailable?: boolean
  schedulable: boolean
  accessType?: AccountSummary['accessType']
  boundGroupId?: string
  bindingSystemAccountId?: string
  ownerSystemAccountId?: string
}

export function readAccountTestRunSession(isManagementView: boolean): AccountTestRunSessionSnapshot | undefined {
  const storage = accountTestSessionStorage()
  if (!storage) return undefined
  const key = accountTestRunSessionStorageKey(isManagementView)
  try {
    const raw = storage.getItem(key)
    if (!raw) return undefined
    const parsed = JSON.parse(raw) as Partial<StoredAccountTestRunSession>
    if (
      parsed.version !== accountTestRunSessionStorageVersion
      || !parsed.expiresAt
      || parsed.expiresAt <= Date.now()
      || !parsed.snapshot
    ) {
      storage.removeItem(key)
      return undefined
    }
    return restoreSnapshot(parsed.snapshot)
  } catch {
    storage.removeItem(key)
    return undefined
  }
}

export function writeAccountTestRunSession(snapshot: AccountTestRunSessionSnapshot): void {
  const storage = accountTestSessionStorage()
  if (!storage) return
  try {
    const payload: StoredAccountTestRunSession = {
      expiresAt: Date.now() + accountTestRunSessionStorageTtlMs,
      version: accountTestRunSessionStorageVersion,
      snapshot: storeSnapshot(snapshot)
    }
    storage.setItem(accountTestRunSessionStorageKey(snapshot.isManagementView), JSON.stringify(payload))
  } catch {
    // 本地会话存储不可用时，后端测试任务仍继续执行。
  }
}

export function clearAccountTestRunSession(isManagementView: boolean): void {
  const storage = accountTestSessionStorage()
  if (!storage) return
  try {
    storage.removeItem(accountTestRunSessionStorageKey(isManagementView))
  } catch {
    // 清理失败不影响后端任务和当前页面内存状态。
  }
}

function accountTestRunSessionStorageKey(isManagementView: boolean): string {
  const user = authState.currentUser.value
  const userKey = user?.id || user?.username || 'anonymous'
  return `${accountTestRunSessionStoragePrefix}${userKey}:${isManagementView ? 'management' : 'self'}:v${accountTestRunSessionStorageVersion}`
}

function accountTestSessionStorage(): Storage | undefined {
  try {
    return typeof window === 'undefined' ? undefined : window.sessionStorage
  } catch {
    return undefined
  }
}

function storeSnapshot(snapshot: AccountTestRunSessionSnapshot): StoredAccountTestRunSessionSnapshot {
  return {
    sessionId: snapshot.sessionId,
    isManagementView: snapshot.isManagementView,
    scopeParams: normalizedScopeParams(snapshot.scopeParams),
    mode: snapshot.mode,
    draftMode: snapshot.draftMode,
    model: snapshot.model,
    testEndpointMode: snapshot.testEndpointMode,
    testingAccount: snapshot.testingAccount ? storeAccountSummary(snapshot.testingAccount) : undefined,
    batchTestingAccounts: snapshot.batchTestingAccounts.map(storeAccountSummary),
    activeSingleTestTask: snapshot.activeSingleTestTask,
    batchTestItems: snapshot.batchTestItems.map((item) => ({
      ...item,
      account: storeAccountSummary(item.account)
    })),
    result: snapshot.result,
    running: snapshot.running
  }
}

function restoreSnapshot(snapshot: StoredAccountTestRunSessionSnapshot): AccountTestRunSessionSnapshot | undefined {
  if (!snapshot || typeof snapshot !== 'object') return undefined
  if (typeof snapshot.sessionId !== 'string' || !snapshot.sessionId.trim()) return undefined
  if (snapshot.mode !== 'single' && snapshot.mode !== 'batch') return undefined
  if (snapshot.draftMode !== undefined && snapshot.draftMode !== 'create' && snapshot.draftMode !== 'saved') return undefined
  if (typeof snapshot.model !== 'string' || !isAccountTestEndpointMode(snapshot.testEndpointMode)) return undefined
  const testingAccount = snapshot.testingAccount ? restoreAccountSummary(snapshot.testingAccount) : undefined
  const batchTestingAccounts = Array.isArray(snapshot.batchTestingAccounts)
    ? snapshot.batchTestingAccounts.map(restoreAccountSummary).filter((account): account is AccountSummary => Boolean(account))
    : []
  const batchTestItems = Array.isArray(snapshot.batchTestItems)
    ? snapshot.batchTestItems
        .map((item) => restoreBatchTestItem(item))
        .filter((item): item is AccountBatchTestItem => Boolean(item))
    : []
  if (snapshot.mode === 'single' && !testingAccount) return undefined
  if (snapshot.mode === 'batch' && batchTestingAccounts.length === 0) return undefined
  return {
    sessionId: snapshot.sessionId.trim(),
    isManagementView: snapshot.isManagementView === true,
    scopeParams: normalizedScopeParams(snapshot.scopeParams),
    mode: snapshot.mode,
    draftMode: snapshot.draftMode,
    model: snapshot.model,
    testEndpointMode: snapshot.testEndpointMode,
    testingAccount,
    batchTestingAccounts,
    activeSingleTestTask: sanitizeTask(snapshot.activeSingleTestTask),
    batchTestItems,
    result: sanitizeResult(snapshot.result),
    running: snapshot.running === true
  }
}

function storeAccountSummary(account: AccountSummary): StoredAccountSummary {
  return {
    id: account.id,
    systemAccountId: account.systemAccountId,
    providerCode: account.providerCode,
    providerProtocolProfileId: account.providerProtocolProfileId,
    protocolCode: account.protocolCode,
    protocolVersion: account.protocolVersion,
    name: account.name,
    type: account.type,
    status: account.status,
    concurrencyLimit: account.concurrencyLimit,
    priority: account.priority,
    superPriorityEnabled: account.superPriorityEnabled,
    fallbackEnabled: account.fallbackEnabled,
    clientCompatibility: account.clientCompatibility,
    supportedModels: account.supportedModels,
    defaultTestModel: account.defaultTestModel,
    modelMappings: account.modelMappings,
    proxyProfileId: account.proxyProfileId,
    proxyProfileUnavailable: account.proxyProfileUnavailable,
    schedulable: account.schedulable,
    accessType: account.accessType,
    boundGroupId: account.boundGroupId,
    bindingSystemAccountId: account.bindingSystemAccountId,
    ownerSystemAccountId: account.ownerSystemAccountId
  }
}

function restoreAccountSummary(value: StoredAccountSummary): AccountSummary | undefined {
  if (!value || typeof value !== 'object') return undefined
  if (typeof value.id !== 'string' || typeof value.name !== 'string' || typeof value.providerCode !== 'string') return undefined
  if (value.type !== 'api_key' && value.type !== 'oauth') return undefined
  return {
    id: value.id,
    systemAccountId: optionalString(value.systemAccountId),
    providerCode: value.providerCode,
    providerProtocolProfileId: optionalString(value.providerProtocolProfileId),
    protocolCode: optionalString(value.protocolCode),
    protocolVersion: optionalString(value.protocolVersion),
    name: value.name,
    type: value.type,
    credentials: {},
    status: value.status,
    concurrencyLimit: finiteNumber(value.concurrencyLimit, 1),
    currentConcurrency: 0,
    priority: finiteNumber(value.priority, 0),
    superPriorityEnabled: value.superPriorityEnabled === true,
    fallbackEnabled: value.fallbackEnabled === true,
    clientCompatibility: value.clientCompatibility,
    supportedModels: stringList(value.supportedModels),
    defaultTestModel: optionalString(value.defaultTestModel),
    modelMappings: Array.isArray(value.modelMappings) ? value.modelMappings : undefined,
    proxyProfileId: optionalString(value.proxyProfileId),
    proxyProfileUnavailable: value.proxyProfileUnavailable === true,
    schedulable: value.schedulable !== false,
    accessType: value.accessType,
    boundGroupId: optionalString(value.boundGroupId),
    bindingSystemAccountId: optionalString(value.bindingSystemAccountId),
    ownerSystemAccountId: optionalString(value.ownerSystemAccountId),
    todayUsage: emptyUsage(),
    usage: emptyUsage()
  }
}

function restoreBatchTestItem(value: StoredAccountBatchTestItem): AccountBatchTestItem | undefined {
  if (!value || typeof value !== 'object') return undefined
  const account = restoreAccountSummary(value.account)
  if (!account || !isBatchStatus(value.status)) return undefined
  return {
    account,
    status: value.status,
    taskId: optionalString(value.taskId),
    result: sanitizeResult(value.result),
    message: optionalString(value.message),
    startedAt: optionalNumber(value.startedAt),
    finishedAt: optionalNumber(value.finishedAt)
  }
}

function sanitizeTask(value: unknown): AccountTestTask | undefined {
  if (!value || typeof value !== 'object') return undefined
  const task = value as Partial<AccountTestTask>
  return typeof task.id === 'string' && typeof task.accountId === 'string'
    ? task as AccountTestTask
    : undefined
}

function sanitizeResult(value: unknown): AccountTestResult | undefined {
  if (!value || typeof value !== 'object') return undefined
  const result = value as Partial<AccountTestResult>
  return typeof result.accountId === 'string' && typeof result.message === 'string'
    ? result as AccountTestResult
    : undefined
}

function normalizedScopeParams(value: unknown): { systemAccountId: string } | undefined {
  if (!value || typeof value !== 'object') return undefined
  const systemAccountId = optionalString((value as { systemAccountId?: unknown }).systemAccountId)
  return systemAccountId ? { systemAccountId } : undefined
}

function isAccountTestEndpointMode(value: unknown): value is AccountTestEndpointMode {
  return value === 'account_default'
    || value === 'chat_json'
    || value === 'chat_sse'
    || value === 'responses_json'
    || value === 'responses_sse'
    || value === 'messages_json'
    || value === 'messages_sse'
    || value === 'message_token_counting'
    || value === 'generate_content_json'
    || value === 'generate_content_sse'
    || value === 'count_tokens'
    || value === 'embed_content'
}

function isBatchStatus(value: unknown): value is AccountBatchTestItem['status'] {
  return value === 'pending'
    || value === 'queued'
    || value === 'running'
    || value === 'success'
    || value === 'failed'
    || value === 'stopped'
}

function optionalString(value: unknown): string | undefined {
  return typeof value === 'string' && value.trim() ? value.trim() : undefined
}

function optionalNumber(value: unknown): number | undefined {
  return typeof value === 'number' && Number.isFinite(value) ? value : undefined
}

function finiteNumber(value: unknown, fallback: number): number {
  return typeof value === 'number' && Number.isFinite(value) ? value : fallback
}

function stringList(value: unknown): string[] | undefined {
  if (!Array.isArray(value)) return undefined
  const items = value.map(optionalString).filter((item): item is string => Boolean(item))
  return items.length ? items : undefined
}

function emptyUsage() {
  return {
    requestCount: 0,
    inputTokens: 0,
    outputTokens: 0,
    cacheReadTokens: 0,
    cacheReadCost: 0,
    cacheWriteTokens: 0,
    cacheWrite1hTokens: 0,
    cacheWriteCost: 0,
    thinkingTokens: 0,
    inputImageTokens: 0,
    outputImageTokens: 0,
    totalTokens: 0,
    totalCost: 0
  }
}
