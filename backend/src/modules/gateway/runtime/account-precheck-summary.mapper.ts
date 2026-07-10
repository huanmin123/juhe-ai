import { accountSummaryWithEffectiveAvailability } from '../../../domain/account-effective-availability.js'
import type { AccountSummary } from '../../../domain/types.js'
import {
  ANTHROPIC_PROTOCOL_CODE,
  ANTHROPIC_PROTOCOL_VERSION,
  GEMINI_PROTOCOL_CODE,
  GEMINI_PROTOCOL_VERSION,
  OPENAI_PROTOCOL_CODE,
  OPENAI_PROTOCOL_VERSION
} from '../../../domain/provider-protocol.js'
import type { OpenAIAccountSecret } from '../../../storage/repositories.js'

interface AccountPrecheckSummaryContext {
  groupId: string
  systemAccountId?: string
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
    systemAccountId: gatewayAccountSummarySystemAccountId(account, context),
    ownerSystemAccountId: account.accountOwnerSystemAccountId,
    providerCode: account.providerCode,
    providerProtocolProfileId: account.providerProtocolProfileId,
    protocolCode: gatewayAccountSummaryProtocolCode(account),
    protocolVersion: gatewayAccountSummaryProtocolVersion(account),
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
    defaultTestModel: account.defaultTestModel,
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
    bindingSystemAccountId: account.accountAccessType === 'account_authorized' ? gatewayAccountSummarySystemAccountId(account, context) : undefined,
    permissions: {
      canUse: true,
      canEdit: false,
      canDelete: false,
      canAuthorize: false,
      canViewCredentials: false
    }
  })
}

function gatewayAccountSummarySystemAccountId(account: OpenAIAccountSecret, context: AccountPrecheckSummaryContext): string {
  if (account.accountAccessType === 'account_authorized') {
    const bindingSystemAccountId = account.bindingSystemAccountId?.trim()
    if (bindingSystemAccountId) return bindingSystemAccountId
    const contextSystemAccountId = context.systemAccountId?.trim()
    if (contextSystemAccountId) return contextSystemAccountId
    throw new Error('授权账户缺少绑定系统账户，无法构造测试摘要')
  }
  const systemAccountId = account.systemAccountId?.trim()
  if (systemAccountId) return systemAccountId
  const contextSystemAccountId = context.systemAccountId?.trim()
  if (contextSystemAccountId) return contextSystemAccountId
  throw new Error('账户缺少系统账户，无法构造测试摘要')
}

function gatewayAccountSummaryBoundGroupId(account: OpenAIAccountSecret): string {
  const boundGroupId = account.boundGroupId?.trim()
  if (boundGroupId) return boundGroupId
  throw new Error('授权账户缺少绑定分组，无法构造测试摘要')
}

function gatewayAccountSummaryProtocolCode(account: OpenAIAccountSecret): string | undefined {
  const protocolCode = account.protocolCode?.trim()
  if (protocolCode) return protocolCode
  const profileId = account.providerProtocolProfileId?.toLowerCase() ?? ''
  if (profileId.includes('_openai_')) return OPENAI_PROTOCOL_CODE
  if (profileId.includes('_anthropic_')) return ANTHROPIC_PROTOCOL_CODE
  if (profileId.includes('_gemini_') || profileId.includes('_native_')) return GEMINI_PROTOCOL_CODE
  return undefined
}

function gatewayAccountSummaryProtocolVersion(account: OpenAIAccountSecret): string | undefined {
  const protocolVersion = account.protocolVersion?.trim()
  if (protocolVersion) return protocolVersion
  const profileId = account.providerProtocolProfileId?.toLowerCase() ?? ''
  if (profileId.includes('_openai_')) return OPENAI_PROTOCOL_VERSION
  if (profileId.includes('_anthropic_')) return ANTHROPIC_PROTOCOL_VERSION
  if (profileId.includes('_gemini_') || profileId.includes('_native_')) return GEMINI_PROTOCOL_VERSION
  return undefined
}
