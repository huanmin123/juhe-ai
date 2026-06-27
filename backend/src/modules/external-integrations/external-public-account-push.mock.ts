import type {
  PublicAccountDeleteInput,
  PublicAccountDeleteResponse,
  PublicAccountListInput,
  PublicAccountListResponse,
  PublicAccountPushInput,
  PublicAccountPushResponse,
  PublicAccountUpdateInput,
  PublicApiKeyAddInput,
  PublicApiKeyDeleteInput,
  PublicApiKeyListInput,
  PublicApiKeyListResponse,
  PublicApiKeyResponse,
  PublicApiKeySummary,
  PublicApiKeyUpdateInput,
  PublicGroupAddInput,
  PublicGroupDeleteInput,
  PublicGroupListInput,
  PublicGroupListResponse,
  PublicGroupResponse,
  PublicGroupSummary,
  PublicGroupUpdateInput
} from './external-public-account-push.types.js'

export function mockPublicWelfareAccountPush(input: PublicAccountPushInput | PublicAccountUpdateInput): PublicAccountPushResponse {
  const generatedAt = new Date().toISOString()
  const username = normalizedText(input.targetUsername) || 'huanmin'
  const groupName = normalizedText(input.targetGroupName) || '福利'
  const providerCode = normalizedText(input.providerCode) || 'gpt'
  const accountName = normalizedText(input.name) || '公益站测试账号'
  return {
    source: 'mock',
    generatedAt,
    action: 'mock',
    target: {
      username,
      displayName: normalizedText(input.targetDisplayName) || username,
      systemAccountId: 'mock_system_account_huanmin',
      created: false,
      groupId: 'mock_group_welfare',
      groupName,
      groupCreated: false
    },
    account: {
      id: 'mock_account_public_welfare',
      name: accountName,
      providerCode,
      type: 'api_key',
      clientCompatibility: 'openai_standard',
      status: input.status === 'disabled' ? 'disabled' : 'active',
      supportedModels: normalizedStringList(input.supportedModels),
      boundGroupId: 'mock_group_welfare',
      boundGroupName: groupName,
      schedulable: input.status !== 'disabled'
    }
  }
}

export function mockPublicWelfareAccountList(input: PublicAccountListInput): PublicAccountListResponse {
  const username = normalizedText(input.targetUsername) || 'huanmin'
  const groupName = normalizedText(input.targetGroupName) || '福利'
  const providerCode = normalizedText(input.providerCode) || 'mock_provider'
  return {
    source: 'mock',
    generatedAt: new Date().toISOString(),
    target: {
      username,
      displayName: username,
      systemAccountId: 'mock_system_account_huanmin',
      created: false
    },
    page: input.page ?? 1,
    pageSize: input.pageSize ?? 20,
    pageUpperBound: 1,
    hasMore: false,
    items: [
      {
        id: 'mock_account_public_welfare',
        name: normalizedText(input.keyword) || '公益站测试账号',
        providerCode,
        type: 'api_key',
        clientCompatibility: 'openai_standard',
        status: 'active',
        supportedModels: ['gpt-5.5'],
        boundGroupId: normalizedText(input.groupId) || 'mock_group_welfare',
        boundGroupName: groupName,
        schedulable: true,
        concurrencyLimit: 20,
        priority: 0
      }
    ]
  }
}

export function mockPublicWelfareAccountDelete(input: PublicAccountDeleteInput): PublicAccountDeleteResponse {
  const generatedAt = new Date().toISOString()
  const username = normalizedText(input.targetUsername) || 'huanmin'
  const groupName = normalizedText(input.targetGroupName) || '福利'
  const providerCode = normalizedText(input.providerCode) || 'gpt'
  const accountId = normalizedText(input.accountId) || 'mock_account_public_welfare'
  return {
    source: 'mock',
    generatedAt,
    action: 'mock',
    target: {
      username,
      displayName: username,
      systemAccountId: 'mock_system_account_huanmin',
      created: false,
      groupId: 'mock_group_welfare',
      groupName,
      groupCreated: false
    },
    account: {
      id: accountId,
      name: accountId,
      providerCode,
      type: 'api_key',
      clientCompatibility: 'openai_standard',
      status: 'disabled',
      boundGroupId: 'mock_group_welfare',
      boundGroupName: groupName,
      schedulable: false
    }
  }
}

export function mockPublicGroupAdd(input: PublicGroupAddInput): PublicGroupResponse {
  return publicMockGroupResponse('mock', input.targetUsername, {
    id: 'mock_group_public',
    name: normalizedText(input.name) || '公开接口分组',
    providerCode: requiredProviderCode(input.providerCode),
    description: normalizedText(input.description),
    enabled: input.enabled !== false,
    groupType: input.groupType ?? 'personal',
    isDefault: false
  })
}

export function mockPublicGroupUpdate(input: PublicGroupUpdateInput): PublicGroupResponse {
  return publicMockGroupResponse('mock', input.targetUsername, {
    id: normalizedText(input.groupId) || 'mock_group_public',
    name: normalizedText(input.name) || '公开接口分组',
    providerCode: normalizedText(input.providerCode) ?? '',
    description: normalizedText(input.description),
    enabled: input.enabled !== false,
    groupType: input.groupType ?? 'personal',
    isDefault: false
  })
}

export function mockPublicGroupDelete(input: PublicGroupDeleteInput): PublicGroupResponse {
  return publicMockGroupResponse('mock', input.targetUsername, {
    id: normalizedText(input.groupId) || 'mock_group_public',
    name: '公开接口分组',
    providerCode: 'mock_provider',
    enabled: true,
    groupType: 'personal',
    isDefault: false
  })
}

export function mockPublicGroupList(input: PublicGroupListInput): PublicGroupListResponse {
  const username = normalizedText(input.targetUsername) || 'huanmin'
  return {
    source: 'mock',
    generatedAt: new Date().toISOString(),
    target: {
      username,
      displayName: username,
      systemAccountId: 'mock_system_account_huanmin',
      created: false
    },
    page: input.page ?? 1,
    pageSize: input.pageSize ?? 20,
    pageUpperBound: 1,
    hasMore: false,
    items: [
      {
        id: 'mock_group_public',
        name: normalizedText(input.keyword) || '公开接口分组',
        providerCode: normalizedText(input.providerCode) || 'mock_provider',
        enabled: true,
        groupType: 'personal',
        isDefault: false
      }
    ]
  }
}

export function mockPublicApiKeyAdd(input: PublicApiKeyAddInput): PublicApiKeyResponse {
  const groupBindings = mockPublicApiKeyGroupBindings(input.groupBindings)
  return publicMockApiKeyResponse('mock', input.targetUsername, {
    id: 'mock_api_key_public',
    name: normalizedText(input.name) || '公开接口 API Key',
    keyPrefix: 'juis_mock',
    key: 'juis_mock_public_api_key',
    status: input.status === 'disabled' ? 'disabled' : 'active',
    groupRouteStrategy: normalizedText(input.groupRouteStrategy) || 'priority_failover',
    groupBindings,
    expiresAt: normalizedText(input.expiresAt)
  })
}

export function mockPublicApiKeyList(input: PublicApiKeyListInput): PublicApiKeyListResponse {
  const username = normalizedText(input.targetUsername) || 'huanmin'
  return {
    source: 'mock',
    generatedAt: new Date().toISOString(),
    target: {
      username,
      displayName: username,
      systemAccountId: 'mock_system_account_huanmin',
      created: false
    },
    page: input.page ?? 1,
    pageSize: input.pageSize ?? 20,
    pageUpperBound: 1,
    hasMore: false,
    items: [
      {
        id: normalizedText(input.groupId) ? 'mock_api_key_public_bound' : 'mock_api_key_public',
        name: normalizedText(input.keyword) || '公开接口 API Key',
        keyPrefix: 'juis_mock',
        status: input.status === 'disabled' ? 'disabled' : 'active',
        groupRouteStrategy: 'priority_failover',
        groupBindings: mockPublicApiKeyGroupBindings(input.groupId ? [{ groupId: input.groupId }] : undefined)
      }
    ]
  }
}

export function mockPublicApiKeyUpdate(input: PublicApiKeyUpdateInput): PublicApiKeyResponse {
  const groupBindings = mockPublicApiKeyGroupBindings(input.groupBindings)
  return publicMockApiKeyResponse('mock', input.targetUsername, {
    id: normalizedText(input.apiKeyId) || 'mock_api_key_public',
    name: normalizedText(input.name) || '公开接口 API Key',
    keyPrefix: 'juis_mock',
    status: input.status === 'disabled' ? 'disabled' : 'active',
    groupRouteStrategy: normalizedText(input.groupRouteStrategy) || 'priority_failover',
    groupBindings,
    expiresAt: normalizedText(input.expiresAt)
  })
}

export function mockPublicApiKeyDelete(input: PublicApiKeyDeleteInput): PublicApiKeyResponse {
  return publicMockApiKeyResponse('mock', input.targetUsername, {
    id: normalizedText(input.apiKeyId) || 'mock_api_key_public',
    name: '公开接口 API Key',
    keyPrefix: 'juis_mock',
    status: 'disabled',
    groupRouteStrategy: 'priority_failover',
    groupBindings: mockPublicApiKeyGroupBindings()
  })
}

function mockPublicApiKeyGroupBindings(input: PublicApiKeyAddInput['groupBindings'] = []): PublicApiKeySummary['groupBindings'] {
  const bindings = input.length ? input : [{ groupId: 'mock_group_public', priority: 1, status: 'active' as const }]
  return bindings.map((binding, index) => ({
    id: `mock_api_key_group_binding_${index + 1}`,
    groupId: binding.groupId,
    groupName: binding.groupId === 'mock_group_public' ? '公开接口分组' : binding.groupId,
    priority: binding.priority ?? index + 1,
    weight: binding.weight ?? 1,
    status: binding.status ?? 'active',
    groupEnabled: true
  }))
}

function publicMockGroupResponse(action: PublicGroupResponse['action'], usernameInput: string | undefined, group: PublicGroupSummary): PublicGroupResponse {
  const username = normalizedText(usernameInput) || 'huanmin'
  return {
    source: 'mock',
    generatedAt: new Date().toISOString(),
    action,
    target: {
      username,
      displayName: username,
      systemAccountId: 'mock_system_account_huanmin',
      created: false
    },
    group
  }
}

function publicMockApiKeyResponse(action: PublicApiKeyResponse['action'], usernameInput: string | undefined, apiKey: PublicApiKeySummary): PublicApiKeyResponse {
  const username = normalizedText(usernameInput) || 'huanmin'
  return {
    source: 'mock',
    generatedAt: new Date().toISOString(),
    action,
    target: {
      username,
      displayName: username,
      systemAccountId: 'mock_system_account_huanmin',
      created: false
    },
    apiKey
  }
}

function requiredProviderCode(value: string | undefined): string {
  const providerCode = normalizedText(value)
  if (!providerCode) {
    throw new Error('供应商编码不能为空')
  }
  return providerCode
}

function normalizedText(value: unknown): string | undefined {
  return typeof value === 'string' && value.trim() ? value.trim() : undefined
}

function normalizedStringList(values: readonly string[] | undefined): string[] | undefined {
  const normalized = [...new Set((values ?? []).map((item) => item.trim()).filter(Boolean))]
  return normalized.length ? normalized : undefined
}
