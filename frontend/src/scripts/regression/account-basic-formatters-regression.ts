import type { AccountSummary, AccountUsageSummary } from '@/types/domain'
import {
  accountClientCompatibilityText,
  accountDisplayExpiresAt,
  accountDisplayName,
  accountLastUsedAt,
  accountTypeDescription,
  accountTypeText,
  accountTypeTitle,
  asString,
  compareAccountConcurrency,
  compareAccountExpiresAt,
  compareAccountLastUsedAt,
  isAccountDisplayExpired,
  normalizeKeyword
} from '../../views/accounts/accountBasicFormatters'
import {
  accountDisplayName as facadeAccountDisplayName,
  accountTypeText as facadeAccountTypeText,
  isAuthorizedAccount
} from '../../views/accounts/accountFormatters'
import {
  accountMenuItems,
  canManageOAuthAccount
} from '../../views/accounts/accountRules'
import {
  accountClientCompatibilityCapabilities,
  canCreateOAuthAccount,
  canSelectClientCompatibility,
  defaultEndpointModesForAccount,
  profileSupportsCodexResponsesChatBridge
} from '../../views/accounts/accountProviderCapabilities'
import { defaultAccountForm } from '../../views/accounts/accountFormDefaults'
import {
  GLM_CODING_OPENAI_V1_PROFILE_ID,
  GLM_GENERAL_OPENAI_V1_PROFILE_ID,
  OPENAI_COMPATIBLE_OPENAI_V1_PROFILE_ID
} from '../../shared/providerProtocol'

const standardAccount = accountFixture({
  name: '标准账户',
  accountExpiresAt: '2026-06-17T00:00:00.000Z',
  lastUsedAt: '2026-06-15T00:00:00.000Z'
})
const authorizedAccount = accountFixture({
  accessType: 'authorized',
  name: '共享账户（授权 张三）',
  accountExpiresAt: '2026-06-20T00:00:00.000Z',
  authorizationExpiresAt: '2026-06-15T23:00:00.000Z',
  lastUsedAt: '2026-06-16T00:00:00.000Z'
})

assertEqual(accountTypeText('oauth'), 'OAuth', 'OAuth 类型文案应保持不变')
assertEqual(accountTypeText('api_key'), 'API Key', 'API Key 类型文案应保持不变')
assertEqual(accountTypeText('custom' as AccountSummary['type']), 'custom', '未知类型应透传展示')
assertEqual(accountClientCompatibilityText('codex_responses'), 'Codex Responses', 'Codex 兼容文案应保持不变')
assertEqual(accountClientCompatibilityText('openai_standard'), 'OpenAI-compatible', 'OpenAI-compatible 兼容文案应保持不变')
assertEqual(accountClientCompatibilityText(), 'OpenAI-compatible', '空客户端兼容应按 OpenAI-compatible 展示')
assertEqual(accountTypeTitle('OpenAI', 'oauth'), 'OpenAI OAuth', 'OAuth 标题应包含供应商名')
assertEqual(accountTypeTitle('OpenAI', 'api_key'), 'OpenAI API Key', 'API Key 标题应包含供应商名')
assertTrue(accountTypeDescription('gpt', 'oauth').includes('Responses / compact'), 'GPT OAuth 描述应说明网关路径限制')
assertTrue(accountTypeDescription('gpt', 'api_key').includes('Base URL'), 'GPT API Key 描述应说明 Base URL')
assertTrue(accountTypeDescription('glm', 'api_key', 'profile_glm_coding_anthropic_v1').includes('Anthropic Messages 接入'), 'GLM Coding Anthropic 描述不应把 Claude Code 当账户类型')
assertTrue(accountTypeDescription('other', 'api_key').includes('供应商定义'), '非 GPT 描述应使用通用供应商流程文案')

assertEqual(asString('hello'), 'hello', '字符串值应原样返回')
assertEqual(asString(123), '', '非字符串值应返回空字符串')
assertEqual(normalizeKeyword('  AbC  '), 'abc', '关键词应 trim 并小写')
assertEqual(normalizeKeyword(null), '', '空关键词应归一为空字符串')

assertEqual(accountDisplayName(standardAccount), '标准账户', '普通账户显示名不应剥离授权后缀')
assertEqual(accountDisplayName(authorizedAccount), '共享账户', '授权账户显示名应剥离授权来源后缀')
assertEqual(accountDisplayName(accountFixture({ accessType: 'authorized', name: '（授权 张三）' })), '（授权 张三）', '授权账户显示名剥离为空时应回退原名')
assertEqual(accountDisplayExpiresAt(standardAccount), '2026-06-17T00:00:00.000Z', '普通账户应展示账户到期时间')
assertEqual(accountDisplayExpiresAt(authorizedAccount), '2026-06-15T23:00:00.000Z', '授权账户应优先展示授权到期时间')
assertEqual(accountLastUsedAt(authorizedAccount), '2026-06-16T00:00:00.000Z', '最后使用时间应直接读取账户字段')

const originalNow = Date.now
Date.now = () => Date.parse('2026-06-16T00:00:00.000Z')
try {
  assertEqual(isAccountDisplayExpired(authorizedAccount), true, '授权展示到期时间早于当前时间时应标记过期')
  assertEqual(isAccountDisplayExpired(standardAccount), false, '未来账户到期时间不应标记过期')
  assertEqual(isAccountDisplayExpired(accountFixture({ accountExpiresAt: 'bad-date' })), false, '非法到期时间不应标记过期')
} finally {
  Date.now = originalNow
}

assertTrue(compareAccountLastUsedAt(standardAccount, authorizedAccount) < 0, '最后使用时间排序应按时间戳升序')
assertTrue(compareAccountExpiresAt(authorizedAccount, standardAccount) < 0, '展示到期时间排序应按授权优先后的时间戳升序')
assertTrue(compareAccountConcurrency(
  accountFixture({ name: '乙', concurrencyLimit: 1, currentConcurrency: 1 }),
  accountFixture({ name: '甲', concurrencyLimit: 2, currentConcurrency: 0 })
) < 0, '并发排序应优先按并发上限升序')
assertTrue(compareAccountConcurrency(
  accountFixture({ name: '乙', concurrencyLimit: 1, currentConcurrency: 0 }),
  accountFixture({ name: '甲', concurrencyLimit: 1, currentConcurrency: 1 })
) < 0, '并发排序应其次按当前并发升序')

assertEqual(facadeAccountDisplayName(authorizedAccount), accountDisplayName(authorizedAccount), 'accountFormatters 门面应继续导出相同显示名行为')
assertEqual(facadeAccountTypeText('oauth'), accountTypeText('oauth'), 'accountFormatters 门面应继续导出相同类型文案行为')
assertEqual(isAuthorizedAccount(authorizedAccount), true, '授权账户谓词应继续由 accountFormatters 导出')
assertEqual(isAuthorizedAccount(standardAccount), false, '普通账户谓词应继续由 accountFormatters 导出')

const oauthAccount = accountFixture({ type: 'oauth', clientCompatibility: 'codex_responses' })
assertEqual(canManageOAuthAccount(oauthAccount), true, 'OpenAI v1 OAuth 自有账户应允许 OAuth 管理动作')
assertTrue(accountMenuItems(oauthAccount).some((item) => item.key === 'refresh-oauth-token'), 'OAuth 管理账户菜单应包含刷新令牌')
assertTrue(accountMenuItems(oauthAccount).some((item) => item.key === 'reauthorize-oauth'), 'OAuth 管理账户菜单应包含重新授权')
assertEqual(canManageOAuthAccount(accountFixture({ type: 'oauth', protocolVersion: 'v2' })), false, '非 OpenAI v1 OAuth 账户不应允许 OAuth 管理动作')
assertEqual(canManageOAuthAccount(accountFixture({ type: 'api_key' })), false, 'API Key 账户不应允许 OAuth 管理动作')
assertEqual(canCreateOAuthAccount({
  provider: providerFixture({
    code: 'openai',
    accountTypes: ['oauth', 'api_key'],
    protocolCode: 'openai',
    protocolVersion: 'v1'
  }),
  profile: providerProfileFixture({
    providerCode: 'openai',
    accountTypes: ['oauth', 'api_key'],
    protocolCode: 'openai',
    protocolVersion: 'v1'
  })
}), false, 'OpenAI-compatible 即使声明 OAuth 类型，也不应误走 GPT OAuth 创建流程')

const deepSeekBridgeProfile = {
  id: 'profile_deepseek_openai_v1',
  providerCode: 'deepseek',
  protocolCode: 'openai',
  protocolVersion: 'v1',
  type: 'api_key' as const,
  clientCompatibility: 'codex_responses' as const
}
assertEqual(profileSupportsCodexResponsesChatBridge(deepSeekBridgeProfile), true, '前端应识别 DeepSeek 支持 Codex Responses 到 Chat SSE 桥接')
assertEqual(canSelectClientCompatibility(deepSeekBridgeProfile), true, 'DeepSeek API Key 应允许选择 Codex Responses 客户端兼容')
assertEqual(
  accountClientCompatibilityCapabilities(deepSeekBridgeProfile).join(','),
  'openai_standard,codex_responses',
  'DeepSeek bridge 账号应同时展示 OpenAI-compatible 与 Codex Responses 测试请求形态'
)
assertEqual(
  defaultEndpointModesForAccount({
    profile: deepSeekBridgeProfile,
    type: 'api_key',
    clientCompatibility: 'codex_responses'
  }).join(','),
  'chat_json,chat_sse',
  'DeepSeek Codex bridge 账号前端默认仍保存真实 Chat Completions JSON/Streaming 能力'
)
assertEqual(
  profileSupportsCodexResponsesChatBridge({
    ...deepSeekBridgeProfile,
    id: OPENAI_COMPATIBLE_OPENAI_V1_PROFILE_ID,
    providerCode: 'openai'
  }),
  true,
  '通用 OpenAI-compatible Chat-only 档案应支持显式 Responses 到 Chat bridge'
)
assertEqual(
  profileSupportsCodexResponsesChatBridge({
    ...deepSeekBridgeProfile,
    id: GLM_GENERAL_OPENAI_V1_PROFILE_ID,
    providerCode: 'glm'
  }),
  true,
  'GLM 通用 OpenAI Chat 档案应支持显式 Responses 到 Chat bridge'
)
assertEqual(
  defaultAccountForm('glm').providerProtocolProfileId,
  GLM_CODING_OPENAI_V1_PROFILE_ID,
  '前端新建 GLM 账户默认应选择 GLM Coding Plan Key'
)

console.log('账户基础 formatter 回归通过：基础文案、授权展示、OAuth 菜单能力、到期优先级、排序和门面导出均符合预期')

function accountFixture(overrides: Partial<AccountSummary> = {}): AccountSummary {
  return {
    id: 'account_basic_formatter_regression',
    providerCode: 'gpt',
    protocolCode: 'openai',
    protocolVersion: 'v1',
    name: '基础 formatter 回归账户',
    type: 'api_key',
    credentials: {},
    status: 'active',
    concurrencyLimit: 1,
    currentConcurrency: 0,
    priority: 0,
    superPriorityEnabled: false,
    fallbackEnabled: false,
    clientCompatibility: 'openai_standard',
    schedulable: true,
    todayUsage: emptyUsage(),
    usage: emptyUsage(),
    ...overrides
  }
}

function emptyUsage(): AccountUsageSummary {
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

function assertEqual<T>(actual: T, expected: T, message: string): void {
  if (actual !== expected) {
    throw new Error(`${message}，期望 ${String(expected)}，实际 ${String(actual)}`)
  }
}

function assertTrue(value: boolean, message: string): void {
  if (!value) {
    throw new Error(message)
  }
}

function providerFixture(overrides: {
  code: string
  accountTypes: string[]
  protocolCode: string
  protocolVersion: string
}) {
  return {
    id: overrides.code,
    code: overrides.code,
    name: overrides.code,
    enabled: true,
    defaultProtocolProfileId: `profile_${overrides.code}`,
    protocolCode: overrides.protocolCode,
    protocolVersion: overrides.protocolVersion,
    baseUrl: 'https://example.com/v1',
    defaultTestModel: '',
    defaultSupportedModels: ['gpt-5.5'],
    accountTypes: overrides.accountTypes,
    capabilities: [],
    protocolProfiles: []
  }
}

function providerProfileFixture(overrides: {
  providerCode: string
  accountTypes: string[]
  protocolCode: string
  protocolVersion: string
}) {
  return {
    id: `profile_${overrides.providerCode}`,
    providerCode: overrides.providerCode,
    name: overrides.providerCode,
    enabled: true,
    protocolCode: overrides.protocolCode,
    protocolVersion: overrides.protocolVersion,
    baseUrl: 'https://example.com/v1',
    defaultTestModel: '',
    accountTypes: overrides.accountTypes,
    capabilities: [],
    endpointFamilies: []
  }
}
