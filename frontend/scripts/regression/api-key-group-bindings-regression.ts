import { strict as assert } from 'node:assert'

import type { GroupOptionSummary } from '../../src/types/domain'
import {
  apiKeyGroupOptionsForBinding,
  hiddenApiKeyGroupBindingIds,
  isApiKeyBindableGroup,
  nextAvailableApiKeyGroupForNewBinding,
  selectedApiKeyGroupBindingProviderProfileId,
  validateApiKeyGroupBindings
} from '../../src/views/api-keys/apiKeyGroupBindingRules'
import {
  createGroupBindingFormRow,
  normalizedGroupBindingPayload,
  type ApiKeyGroupBindingFormRow
} from '../../src/views/api-keys/apiKeyFormModel'
import {
  accountEndpointModeText
} from '../../src/views/accounts/accountEndpointModes'

const groups = [
  groupFixture({
    id: 'grp_gpt_primary',
    name: 'GPT 主号池',
    providerCode: 'gpt',
    providerProtocolProfileId: 'profile_gpt_openai_v1'
  }),
  groupFixture({
    id: 'grp_gpt_backup',
    name: 'GPT 备用号池',
    providerCode: 'gpt',
    providerProtocolProfileId: 'profile_gpt_openai_v1'
  }),
  groupFixture({
    id: 'grp_deepseek',
    name: 'DeepSeek 号池',
    providerCode: 'deepseek',
    providerProtocolProfileId: 'profile_deepseek_openai_v1'
  }),
  groupFixture({
    id: 'grp_disabled',
    name: '停用号池',
    providerCode: 'gpt',
    providerProtocolProfileId: 'profile_gpt_openai_v1',
    enabled: false
  }),
  groupFixture({
    id: 'grp_authorized_expired',
    name: '过期授权号池',
    providerCode: 'gpt',
    providerProtocolProfileId: 'profile_gpt_openai_v1',
    accessType: 'authorized',
    authorizationStatus: 'active',
    authorizationExpiresAt: '2024-01-01T00:00:00.000Z'
  })
]

const primaryBinding = bindingFor(groups[0])
const emptySecondBinding = createGroupBindingFormRow()
const currentBindings = [primaryBinding, emptySecondBinding]

assert.equal(isApiKeyBindableGroup(groups[0]), true, '启用自有分组应允许绑定 API Key')
assert.equal(isApiKeyBindableGroup(groups[3]), false, '停用分组不应允许作为可选 API Key 号池')
assert.equal(isApiKeyBindableGroup(groups[4]), false, '已过期授权分组不应允许绑定 API Key')

assert.equal(
  selectedApiKeyGroupBindingProviderProfileId({ bindings: currentBindings, groups }),
  'profile_gpt_openai_v1',
  '已选择号池应锁定当前 API Key 的供应商协议档案'
)
assert.deepEqual(
  apiKeyGroupOptionsForBinding({ bindings: currentBindings, groups, index: 1 }).map((group) => group.id),
  ['grp_gpt_primary', 'grp_gpt_backup'],
  '新增绑定选项应过滤到同供应商协议档案，且排除跨档案、停用和过期授权分组'
)
assert.equal(
  nextAvailableApiKeyGroupForNewBinding({ bindings: currentBindings, groups })?.id,
  'grp_gpt_backup',
  '新增绑定应选择同协议档案内尚未选择的可用分组'
)
assert.deepEqual(
  hiddenApiKeyGroupBindingIds({ bindings: currentBindings, groups, index: 1 }).sort(),
  ['grp_disabled', 'grp_gpt_primary'].sort(),
  '下拉隐藏项应包含其他行已选分组和停用分组'
)

assert.equal(
  validateMessage([bindingFor(groups[0]), bindingFor(groups[2])]),
  '同一个普通 API Key 的绑定号池必须属于同一供应商协议档案',
  '跨供应商协议档案绑定应在前端提交前拦截'
)
assert.equal(
  validateMessage([bindingFor(groups[0]), bindingFor(groups[0])]),
  '绑定分组不能重复',
  '重复分组绑定应在前端提交前拦截'
)
assert.equal(
  validateMessage([bindingFor(groups[3])]),
  '已停用分组不能作为启用号池：停用号池',
  '停用分组不能作为 active 号池提交'
)
assert.equal(
  validateMessage([bindingFor(groups[0], 'disabled')]),
  '至少需要一个启用分组',
  'API Key 至少需要一个 active 号池'
)
assert.equal(
  validateMessage([bindingFor(groups[0]), bindingFor(groups[1])]),
  undefined,
  '同协议档案多分组绑定应允许提交'
)
assert.equal(
  accountEndpointModeText(['chat_json', 'messages_sse', 'message_token_counting']),
  '对话 JSON、Messages 流式、Token 计数',
  '接口能力展示应使用中文主文案'
)

console.log('API Key 前端分组绑定回归通过：同协议档案过滤、跨档案拦截、重复/停用/无启用校验和中文接口能力文案均符合预期')

function validateMessage(bindings: ApiKeyGroupBindingFormRow[]): string | undefined {
  return validateApiKeyGroupBindings({
    groupBindings: normalizedGroupBindingPayload(bindings),
    formBindings: bindings,
    groups
  })
}

function bindingFor(group: GroupOptionSummary, status: 'active' | 'disabled' = 'active'): ApiKeyGroupBindingFormRow {
  return createGroupBindingFormRow({ id: group.id, name: group.name }, status, 1, {
    providerCode: group.providerCode,
    providerProtocolProfileId: group.providerProtocolProfileId,
    groupEnabled: group.enabled
  })
}

function groupFixture(overrides: Partial<GroupOptionSummary>): GroupOptionSummary {
  return {
    id: 'grp_fixture',
    systemAccountId: 'sys_admin',
    systemAccountName: '系统账户',
    ownerSystemAccountId: 'sys_admin',
    ownerSystemAccountName: '系统账户',
    name: '回归分组',
    providerCode: 'gpt',
    providerProtocolProfileId: 'profile_gpt_openai_v1',
    protocolCode: 'openai',
    protocolVersion: 'v1',
    enabled: true,
    isDefault: false,
    groupType: 'regular',
    schedulingPolicy: 'round_robin',
    accessType: 'owner',
    permissions: {
      canUse: true,
      canEdit: true,
      canDelete: true,
      canAuthorize: true,
      canViewCredentials: true,
      canBindToApiKey: true
    },
    ...overrides
  }
}
