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
assertEqual(accountClientCompatibilityText('codex_responses'), 'Codex Responses', 'Codex 兼容模式文案应保持不变')
assertEqual(accountClientCompatibilityText('openai_standard'), 'OpenAI 标准', 'OpenAI 标准兼容模式文案应保持不变')
assertEqual(accountClientCompatibilityText(), 'OpenAI 标准', '空兼容模式应按 OpenAI 标准展示')
assertEqual(accountTypeTitle('OpenAI', 'oauth'), 'OpenAI OAuth', 'OAuth 标题应包含供应商名')
assertEqual(accountTypeTitle('OpenAI', 'api_key'), 'OpenAI API Key', 'API Key 标题应包含供应商名')
assertTrue(accountTypeDescription('gpt', 'oauth').includes('Responses / compact'), 'GPT OAuth 描述应说明网关路径限制')
assertTrue(accountTypeDescription('gpt', 'api_key').includes('Base URL'), 'GPT API Key 描述应说明 Base URL')
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

console.log('账户基础 formatter 回归通过：基础文案、授权展示、到期优先级、排序和门面导出均符合预期')

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
