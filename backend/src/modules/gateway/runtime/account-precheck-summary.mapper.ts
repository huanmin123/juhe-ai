import { accountSummaryWithEffectiveAvailability } from '../../../domain/account-effective-availability.js'
import type { AccountSummary } from '../../../domain/types.js'
import type { OpenAIAccountSecret } from '../../../storage/repositories.js'

interface AccountPrecheckSummaryContext {
  groupId: string
}

export function accountSummaryFromGatewayPrecheckAccount(
  account: OpenAIAccountSecret,
  context: AccountPrecheckSummaryContext
): AccountSummary {
  const emptyUsage = {
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
  return accountSummaryWithEffectiveAvailability({
    id: account.id,
    systemAccountId: gatewayAccountSummarySystemAccountId(account),
    ownerSystemAccountId: account.accountOwnerSystemAccountId,
    providerCode: account.providerCode,
    name: account.name,
    type: account.type,
    credentials: account.credentials,
    status: account.status,
    concurrencyLimit: account.concurrencyLimit,
    currentConcurrency: account.currentConcurrency ?? 0,
    priority: account.priority,
    superPriorityEnabled: account.superPriorityEnabled,
    fallbackEnabled: account.fallbackEnabled,
    clientCompatibility: account.clientCompatibility,
    supportedModels: account.supportedModels,
    modelMappings: account.modelMappings,
    lastSuccessfulTestModel: account.lastSuccessfulTestModel,
    proxyProfileId: account.proxyProfileId,
    schedulable: true,
    cooldownUntil: account.cooldownUntil,
    lastErrorMessage: account.lastErrorMessage,
    streamFailureCount: account.streamFailureCount,
    streamFailureWindowStartedAt: account.streamFailureWindowStartedAt,
    todayUsage: emptyUsage,
    usage: emptyUsage,
    accessType: account.accountAccessType === 'account_authorized' ? 'authorized' : 'owner',
    accountAuthorizationId: account.accountAuthorizationId,
    boundGroupId: account.accountAccessType === 'account_authorized' ? gatewayAccountSummaryBoundGroupId(account) : context.groupId,
    bindingSystemAccountId: account.accountAccessType === 'account_authorized' ? gatewayAccountSummarySystemAccountId(account) : undefined,
    permissions: {
      canUse: true,
      canEdit: false,
      canDelete: false,
      canAuthorize: false,
      canViewCredentials: false
    }
  })
}

function gatewayAccountSummarySystemAccountId(account: OpenAIAccountSecret): string {
  if (account.accountAccessType === 'account_authorized') {
    const bindingSystemAccountId = account.bindingSystemAccountId?.trim()
    if (bindingSystemAccountId) return bindingSystemAccountId
    throw new Error('授权账户缺少绑定系统账户，无法构造测试摘要')
  }
  const systemAccountId = account.systemAccountId?.trim()
  if (systemAccountId) return systemAccountId
  throw new Error('账户缺少系统账户，无法构造测试摘要')
}

function gatewayAccountSummaryBoundGroupId(account: OpenAIAccountSecret): string {
  const boundGroupId = account.boundGroupId?.trim()
  if (boundGroupId) return boundGroupId
  throw new Error('授权账户缺少绑定分组，无法构造测试摘要')
}
