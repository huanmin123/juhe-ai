import { authState } from '@/composables/useAuth'
import type {
  AccountModelMapping,
  AccountSummary,
  AccountSupportedEndpointMode,
  AccountTestResult,
  AccountTestTask
} from '@/types/domain'
import type { AccountTestEndpointMode } from './accountTestFlow'

export const accountTestRunSessionStorageTtlMs = 12 * 60 * 60 * 1000

const accountTestRunSessionStoragePrefix = 'juhe-ai:account-test-run-session:'
const accountTestRunSessionStorageVersion = 6

export interface AccountTestRunSessionSnapshot {
  sessionId: string
  isManagementView: boolean
  scopeParams?: { systemAccountId: string }
  model: string
  modelOptions: Array<{ label: string; value: string }>
  testEndpointMode: AccountTestEndpointMode
  testEndpointModes: AccountSupportedEndpointMode[]
  testingAccount: AccountSummary
  activeTask: AccountTestTask
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
  model: string
  modelOptions: Array<{ label: string; value: string }>
  testEndpointMode: AccountTestEndpointMode
  testEndpointModes: AccountSupportedEndpointMode[]
  testingAccount: StoredAccountSummary
  activeTask: AccountTestTask
  running: boolean
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
  healthCheckModel: string
  healthCheckEndpointFamily: AccountSummary['healthCheckEndpointFamily']
  modelMappings?: AccountModelMapping[]
  proxyProfileId?: string
  proxyProfileUnavailable?: boolean
  schedulable: boolean
  accessType?: AccountSummary['accessType']
  boundGroupId?: string
  bindingSystemAccountId?: string
  ownerSystemAccountId?: string
}

export function readAccountTestRunSession(
  isManagementView: boolean,
  accountId: string
): AccountTestRunSessionSnapshot | undefined {
  const storage = accountTestSessionStorage()
  if (!storage) return undefined
  const key = accountTestRunSessionStorageKey(isManagementView, accountId)
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
    const snapshot = restoreSnapshot(parsed.snapshot)
    if (!snapshot || snapshot.testingAccount.id !== accountId) {
      storage.removeItem(key)
      return undefined
    }
    return snapshot
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
    storage.setItem(
      accountTestRunSessionStorageKey(snapshot.isManagementView, snapshot.testingAccount.id),
      JSON.stringify(payload)
    )
  } catch {
    // 本地会话存储不可用时，后端测试任务仍继续执行。
  }
}

export function clearAccountTestRunSession(isManagementView: boolean, accountId: string): void {
  const storage = accountTestSessionStorage()
  if (!storage) return
  try {
    storage.removeItem(accountTestRunSessionStorageKey(isManagementView, accountId))
  } catch {
    // 清理失败不影响后端任务和当前页面内存状态。
  }
}

function accountTestRunSessionStorageKey(isManagementView: boolean, accountId: string): string {
  const user = authState.currentUser.value
  const userKey = user?.id || user?.username || 'anonymous'
  const accountKey = encodeURIComponent(accountId)
  return `${accountTestRunSessionStoragePrefix}${userKey}:${isManagementView ? 'management' : 'self'}:${accountKey}:v${accountTestRunSessionStorageVersion}`
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
    model: snapshot.model,
    modelOptions: normalizeModelOptions(snapshot.modelOptions),
    testEndpointMode: snapshot.testEndpointMode,
    testEndpointModes: normalizeEndpointModes(snapshot.testEndpointModes),
    testingAccount: storeAccountSummary(snapshot.testingAccount),
    activeTask: storeTask(snapshot.activeTask),
    running: snapshot.running
  }
}

function restoreSnapshot(snapshot: StoredAccountTestRunSessionSnapshot): AccountTestRunSessionSnapshot | undefined {
  if (!snapshot || typeof snapshot !== 'object') return undefined
  if (typeof snapshot.sessionId !== 'string' || !snapshot.sessionId.trim()) return undefined
  if (typeof snapshot.model !== 'string' || !isAccountTestEndpointMode(snapshot.testEndpointMode)) return undefined
  const testingAccount = restoreAccountSummary(snapshot.testingAccount)
  const activeTask = sanitizeTask(snapshot.activeTask)
  if (!testingAccount || !activeTask || activeTask.accountId !== testingAccount.id) return undefined
  return {
    sessionId: snapshot.sessionId.trim(),
    isManagementView: snapshot.isManagementView === true,
    scopeParams: normalizedScopeParams(snapshot.scopeParams),
    model: snapshot.model,
    modelOptions: normalizeModelOptions(snapshot.modelOptions),
    testEndpointMode: snapshot.testEndpointMode,
    testEndpointModes: normalizeEndpointModes(snapshot.testEndpointModes),
    testingAccount,
    activeTask,
    result: undefined,
    running: snapshot.running === true
  }
}

function storeTask(task: AccountTestTask): AccountTestTask {
  return {
    id: task.id,
    sessionId: task.sessionId,
    accountId: task.accountId,
    accountName: task.accountName,
    providerCode: task.providerCode,
    providerProtocolProfileId: task.providerProtocolProfileId,
    protocolCode: task.protocolCode,
    protocolVersion: task.protocolVersion,
    type: task.type,
    status: task.status,
    model: task.model,
    testEndpointMode: task.testEndpointMode,
    cancelRequested: task.cancelRequested,
    createdAt: task.createdAt,
    queuedAt: task.queuedAt,
    startedAt: task.startedAt,
    finishedAt: task.finishedAt,
    updatedAt: task.updatedAt
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
    healthCheckModel: account.healthCheckModel,
    healthCheckEndpointFamily: account.healthCheckEndpointFamily,
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
  if (!['chat_completions', 'responses', 'messages', 'generate_content'].includes(value.healthCheckEndpointFamily)) return undefined
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
    healthCheckModel: typeof value.healthCheckModel === 'string' ? value.healthCheckModel : '',
    healthCheckEndpointFamily: value.healthCheckEndpointFamily,
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

function sanitizeTask(value: unknown): AccountTestTask | undefined {
  if (!value || typeof value !== 'object') return undefined
  const task = value as Partial<AccountTestTask>
  return typeof task.id === 'string' && typeof task.accountId === 'string'
    ? task as AccountTestTask
    : undefined
}

function normalizedScopeParams(value: unknown): { systemAccountId: string } | undefined {
  if (!value || typeof value !== 'object') return undefined
  const systemAccountId = optionalString((value as { systemAccountId?: unknown }).systemAccountId)
  return systemAccountId ? { systemAccountId } : undefined
}

function isAccountTestEndpointMode(value: unknown): value is AccountTestEndpointMode {
  return value === 'account_default' || isAccountSupportedEndpointMode(value)
}

function isAccountSupportedEndpointMode(value: unknown): value is AccountSupportedEndpointMode {
  return value === 'chat_json'
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

function normalizeEndpointModes(value: unknown): AccountSupportedEndpointMode[] {
  if (!Array.isArray(value)) return []
  return [...new Set(value.filter(isAccountSupportedEndpointMode))]
}

function normalizeModelOptions(value: unknown): Array<{ label: string; value: string }> {
  if (!Array.isArray(value)) return []
  const values = new Set<string>()
  const output: Array<{ label: string; value: string }> = []
  for (const item of value) {
    if (!item || typeof item !== 'object') continue
    const candidate = item as { label?: unknown; value?: unknown }
    const model = optionalString(candidate.value)
    if (!model || values.has(model)) continue
    values.add(model)
    output.push({
      label: optionalString(candidate.label) ?? model,
      value: model
    })
  }
  return output
}

function optionalString(value: unknown): string | undefined {
  return typeof value === 'string' && value.trim() ? value.trim() : undefined
}

function finiteNumber(value: unknown, fallback: number): number {
  return typeof value === 'number' && Number.isFinite(value) ? value : fallback
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
