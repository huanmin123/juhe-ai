import { GPT_VENDOR_CODE } from '../../domain/provider-protocol.js'
import type {
  ExternalPublicApiDocItemSeed,
  ExternalPublicApiHeader
} from './external-public-api-catalog.types.js'

const authHeader: ExternalPublicApiHeader = {
  name: 'Authorization',
  required: true,
  description: '来源授权 Bearer token。每个公开接口都有独立资源 scope；使用内置测试 token 时接口只返回 mock 数据。',
  example: 'Bearer <source_token>'
}

const targetQuery = { name: 'targetUsername', type: 'string', required: true, description: '目标系统用户账号。', example: 'huanmin' }
const pageQuery = [
  { name: 'page', type: 'number', required: false, description: '分页页码，默认 1。', example: 1 },
  { name: 'pageSize', type: 'number', required: false, description: '每页数量，范围 1 到 100。', example: 20 }
]
const target = { username: 'huanmin', displayName: 'huanmin', systemAccountId: 'sysacc_xxx', created: false }
const group = { id: 'grp_xxx', name: '福利', providerCode: GPT_VENDOR_CODE, enabled: true, groupType: 'personal', isDefault: false }
const routeStrategy = {
  id: 'rts_xxx',
  name: '公益站默认路由',
  mode: 'normal',
  status: 'active',
  isDefault: false,
  groupBindings: [{ id: 'rsg_xxx', groupId: 'grp_xxx', groupName: '福利', providerCode: GPT_VENDOR_CODE, priority: 1, weight: 100, status: 'active', groupEnabled: true }],
  apiKeyCount: 1,
  createdAt: '2026-05-30T00:00:00.000Z',
  updatedAt: '2026-05-30T00:00:00.000Z'
}
const apiKey = {
  id: 'key_xxx',
  name: '公益站访问密钥',
  keyPrefix: 'juis_xxx',
  status: 'active',
  routeStrategyId: 'rts_xxx',
  routeStrategyName: '公益站默认路由',
  routeStrategyMode: 'normal',
  routeStrategyStatus: 'active'
}
const account = {
  id: 'acc_xxx',
  name: '公益站-青芽主通道',
  providerCode: GPT_VENDOR_CODE,
  providerProtocolProfileId: 'profile_gpt_openai_v1',
  protocolCode: 'openai',
  protocolVersion: 'v1',
  type: 'api_key',
  clientCompatibility: 'openai_standard',
  status: 'active',
  supportedModels: ['gpt-5.5'],
  boundGroupId: 'grp_xxx',
  boundGroupName: '福利',
  schedulable: true,
  concurrencyLimit: 20,
  priority: 0
}

export const externalPublicApiDocItems = [
{
  id: 'api-key-list',
  name: 'API Key 列表',
  summary: '分页读取指定系统用户名下的 API Key 摘要和策略路由信息；不会返回 API Key 明文。',
  status: 'available',
  method: 'GET',
  path: '/__aipublic__/api-key/list',
  headers: [authHeader],
  query: [
    targetQuery,
    { name: 'routeStrategyId', type: 'string', required: false, description: '按策略路由 ID 筛选。', example: 'rts_xxx' },
    { name: 'keyword', type: 'string', required: false, description: '按 API Key 名称精确 / 前缀筛选。', example: '公益站访问密钥' },
    { name: 'status', type: 'string', required: false, description: '状态筛选：active、disabled 或 all。', example: 'active' },
    ...pageQuery
  ],
  responseExample: { data: { source: 'stats', generatedAt: '2026-05-30T00:00:00.000Z', target, page: 1, pageSize: 20, pageUpperBound: 1, hasMore: false, items: [apiKey] } }
},
{
  id: 'api-key-add',
  name: 'API Key 新增',
  summary: '为指定系统用户新增 API Key，并绑定一条策略路由；分组绑定和路由模式由策略路由维护。',
  status: 'available',
  method: 'POST',
  path: '/__aipublic__/api-key/add',
  headers: [authHeader],
  query: [],
  requestBody: {
    contentType: 'application/json',
    fields: [
      targetQuery,
      { name: 'name', type: 'string', required: true, description: 'API Key 名称。', example: '公益站访问密钥' },
      { name: 'description', type: 'string|null', required: false, description: 'API Key 说明；传 null 表示清空说明。', example: '公益站后端访问' },
      { name: 'routeStrategyId', type: 'string', required: true, description: 'API Key 绑定的策略路由 ID。', example: 'rts_xxx' },
      { name: 'status', type: 'string', required: false, description: '状态：active 或 disabled，默认 active。', example: 'active' },
      { name: 'expiresAt', type: 'string', required: false, description: 'API Key 到期时间，ISO 8601 字符串；未填写表示不过期。' },
      { name: 'quotaLimits', type: 'object|null', required: false, description: '请求成本额度限制；传 null 表示清空。' },
      { name: 'availabilitySchedule', type: 'object|null', required: false, description: '时间计划；null 表示清空计划，未填写表示不设置计划。' }
    ],
    example: { targetUsername: 'huanmin', name: '公益站访问密钥', routeStrategyId: 'rts_xxx', status: 'active' }
  },
  responseExample: { data: { source: 'stats', generatedAt: '2026-05-30T00:00:00.000Z', action: 'created', target, apiKey: { ...apiKey, key: 'juis_xxx_plain_once' } } }
},
{
  id: 'api-key-update',
  name: 'API Key 修改',
  summary: '修改指定 API Key 的名称、状态、策略路由绑定、额度或时间计划。',
  status: 'available',
  method: 'POST',
  path: '/__aipublic__/api-key/update',
  headers: [authHeader],
  query: [],
  requestBody: {
    contentType: 'application/json',
    fields: [
      { name: 'targetUsername', type: 'string', required: false, description: '可选校验条件。提供时必须与 API Key 归属目标用户一致。', example: 'huanmin' },
      { name: 'apiKeyId', type: 'string', required: true, description: 'API Key ID。', example: 'key_xxx' },
      { name: 'name', type: 'string', required: false, description: '新的 API Key 名称。' },
      { name: 'description', type: 'string|null', required: false, description: '新的 API Key 说明；传 null 表示清空。' },
      { name: 'status', type: 'string', required: false, description: '状态：active 或 disabled。', example: 'disabled' },
      { name: 'routeStrategyId', type: 'string', required: false, description: '新的策略路由 ID。', example: 'rts_xxx' },
      { name: 'expiresAt', type: 'string|null', required: false, description: '新的到期时间；传 null 表示清空。' },
      { name: 'quotaLimits', type: 'object|null', required: false, description: '新的请求成本额度限制；传 null 表示清空。' },
      { name: 'availabilitySchedule', type: 'object|null', required: false, description: '时间计划；null 表示清空计划，未填写表示保留。' }
    ],
    example: { apiKeyId: 'key_xxx', status: 'disabled' }
  },
  responseExample: { data: { source: 'stats', generatedAt: '2026-05-30T00:00:00.000Z', action: 'updated', target, apiKey: { ...apiKey, status: 'disabled' } } }
},
{
  id: 'api-key-delete',
  name: 'API Key 删除',
  summary: '按 API Key 新增或列表响应返回的 ID 删除 API Key。',
  status: 'available',
  method: 'POST',
  path: '/__aipublic__/api-key/del',
  headers: [authHeader],
  query: [],
  requestBody: {
    contentType: 'application/json',
    fields: [
      { name: 'targetUsername', type: 'string', required: false, description: '可选校验条件。提供时必须与 API Key 归属目标用户一致。', example: 'huanmin' },
      { name: 'apiKeyId', type: 'string', required: true, description: 'API Key 新增或列表响应返回的 API Key ID。', example: 'key_xxx' }
    ],
    example: { apiKeyId: 'key_xxx' }
  },
  responseExample: { data: { source: 'stats', generatedAt: '2026-05-30T00:00:00.000Z', action: 'deleted', target, apiKey } }
},
{
  id: 'route-strategy-list',
  name: '路由策略列表',
  summary: '分页读取指定系统用户名下的路由策略摘要和分组绑定，用于 API Key 绑定和资源对账。',
  status: 'available',
  method: 'GET',
  path: '/__aipublic__/route-strategy/list',
  headers: [authHeader],
  query: [
    targetQuery,
    { name: 'keyword', type: 'string', required: false, description: '按路由策略名称精确 / 前缀筛选。', example: '公益站默认路由' },
    { name: 'mode', type: 'string', required: false, description: '路由模式筛选：normal、hybrid_smart、weighted、failover、round_robin 或 all。', example: 'normal' },
    { name: 'status', type: 'string', required: false, description: '状态筛选：active、disabled 或 all。', example: 'active' },
    ...pageQuery
  ],
  responseExample: { data: { source: 'stats', generatedAt: '2026-05-30T00:00:00.000Z', target, page: 1, pageSize: 20, pageUpperBound: 1, hasMore: false, items: [routeStrategy] } }
},
{
  id: 'route-strategy-add',
  name: '路由策略新增',
  summary: '在指定系统用户下新增路由策略，绑定一个或多个分组。',
  status: 'available',
  method: 'POST',
  path: '/__aipublic__/route-strategy/add',
  headers: [authHeader],
  query: [],
  requestBody: {
    contentType: 'application/json',
    fields: [
      targetQuery,
      { name: 'name', type: 'string', required: true, description: '路由策略名称。', example: '公益站默认路由' },
      { name: 'description', type: 'string|null', required: false, description: '路由策略说明；传 null 表示清空。' },
      { name: 'mode', type: 'string', required: false, description: '路由模式，默认 normal。', example: 'normal' },
      { name: 'status', type: 'string', required: false, description: '状态：active 或 disabled，默认 active。', example: 'active' },
      { name: 'groupBindings', type: 'array', required: true, description: '分组绑定列表，至少 1 个，最多 20 个。', example: [{ groupId: 'grp_xxx', priority: 1, weight: 100, status: 'active' }] },
      { name: 'hybridRoutingConfig', type: 'object|null', required: false, description: '混合智能路由配置；仅 hybrid_smart 模式需要。' }
    ],
    example: { targetUsername: 'huanmin', name: '公益站默认路由', mode: 'normal', groupBindings: [{ groupId: 'grp_xxx' }] }
  },
  responseExample: { data: { source: 'stats', generatedAt: '2026-05-30T00:00:00.000Z', action: 'created', target, routeStrategy } }
},
{
  id: 'route-strategy-update',
  name: '路由策略修改',
  summary: '按路由策略 ID 修改名称、状态、模式、分组绑定或混合智能路由配置。',
  status: 'available',
  method: 'POST',
  path: '/__aipublic__/route-strategy/update',
  headers: [authHeader],
  query: [],
  requestBody: {
    contentType: 'application/json',
    fields: [
      { name: 'targetUsername', type: 'string', required: false, description: '可选校验条件。提供时必须与路由策略归属目标用户一致。', example: 'huanmin' },
      { name: 'routeStrategyId', type: 'string', required: true, description: '路由策略 ID。', example: 'rts_xxx' },
      { name: 'name', type: 'string', required: false, description: '新的路由策略名称。' },
      { name: 'description', type: 'string|null', required: false, description: '新的路由策略说明；传 null 表示清空。' },
      { name: 'mode', type: 'string', required: false, description: '新的路由模式。', example: 'round_robin' },
      { name: 'status', type: 'string', required: false, description: '状态：active 或 disabled。', example: 'active' },
      { name: 'groupBindings', type: 'array', required: false, description: '新的分组绑定列表；提供时整体覆盖。' },
      { name: 'hybridRoutingConfig', type: 'object|null', required: false, description: '新的混合智能路由配置；传 null 表示清空。' }
    ],
    example: { routeStrategyId: 'rts_xxx', mode: 'round_robin', groupBindings: [{ groupId: 'grp_xxx', priority: 1 }] }
  },
  responseExample: { data: { source: 'stats', generatedAt: '2026-05-30T00:00:00.000Z', action: 'updated', target, routeStrategy: { ...routeStrategy, mode: 'round_robin' } } }
},
{
  id: 'route-strategy-delete',
  name: '路由策略删除',
  summary: '按路由策略 ID 删除策略。默认策略或仍被 API Key 使用的策略会被拒绝删除。',
  status: 'available',
  method: 'POST',
  path: '/__aipublic__/route-strategy/del',
  headers: [authHeader],
  query: [],
  requestBody: {
    contentType: 'application/json',
    fields: [
      { name: 'targetUsername', type: 'string', required: false, description: '可选校验条件。提供时必须与路由策略归属目标用户一致。', example: 'huanmin' },
      { name: 'routeStrategyId', type: 'string', required: true, description: '路由策略 ID。', example: 'rts_xxx' }
    ],
    example: { routeStrategyId: 'rts_xxx' }
  },
  responseExample: { data: { source: 'stats', generatedAt: '2026-05-30T00:00:00.000Z', action: 'deleted', target, routeStrategy } }
},
{
  id: 'group-list',
  name: '分组列表',
  summary: '分页读取指定系统用户名下的分组，用于外部来源系统对账和找回分组 ID。',
  status: 'available',
  method: 'GET',
  path: '/__aipublic__/group/list',
  headers: [authHeader],
  query: [
    targetQuery,
    { name: 'providerCode', type: 'string', required: false, description: '供应商编码筛选。', example: GPT_VENDOR_CODE },
    { name: 'keyword', type: 'string', required: false, description: '按分组名称或供应商编码精确 / 前缀筛选。', example: '福利' },
    ...pageQuery
  ],
  responseExample: { data: { source: 'stats', generatedAt: '2026-05-30T00:00:00.000Z', target, page: 1, pageSize: 20, pageUpperBound: 1, hasMore: false, items: [group] } }
},
{
  id: 'group-add',
  name: '分组新增',
  summary: '在指定系统用户下新增账号分组；目标用户不存在时自动创建，同名分组已存在时按幂等成功返回既有分组。',
  status: 'available',
  method: 'POST',
  path: '/__aipublic__/group/add',
  headers: [authHeader],
  query: [],
  requestBody: {
    contentType: 'application/json',
    fields: [
      targetQuery,
      { name: 'targetDisplayName', type: 'string', required: false, description: '自动创建目标系统用户时使用的显示名称；未填写时使用 targetUsername。', example: '欢民' },
      { name: 'name', type: 'string', required: true, description: '分组名称。', example: '福利' },
      { name: 'providerCode', type: 'string', required: true, description: '供应商编码。', example: GPT_VENDOR_CODE },
      { name: 'description', type: 'string', required: false, description: '分组说明。' },
      { name: 'enabled', type: 'boolean', required: false, description: '是否启用，默认 true。', example: true },
      { name: 'groupType', type: 'string', required: false, description: '分组类型：personal 或 high_concurrency，默认 personal。', example: 'personal' }
    ],
    example: { targetUsername: 'huanmin', name: '福利', providerCode: GPT_VENDOR_CODE }
  },
  responseExample: { data: { source: 'stats', generatedAt: '2026-05-30T00:00:00.000Z', action: 'created', target, group } }
},
{
  id: 'group-update',
  name: '分组修改',
  summary: '按分组 ID 修改名称、供应商编码、说明、启用状态或分组类型。',
  status: 'available',
  method: 'POST',
  path: '/__aipublic__/group/update',
  headers: [authHeader],
  query: [],
  requestBody: {
    contentType: 'application/json',
    fields: [
      { name: 'targetUsername', type: 'string', required: false, description: '可选校验条件。提供时必须与分组归属目标用户一致。', example: 'huanmin' },
      { name: 'groupId', type: 'string', required: true, description: '分组 ID。', example: 'grp_xxx' },
      { name: 'name', type: 'string', required: false, description: '新的分组名称。' },
      { name: 'providerCode', type: 'string', required: false, description: '新的供应商编码。' },
      { name: 'description', type: 'string|null', required: false, description: '新的分组说明；传 null 表示清空。' },
      { name: 'enabled', type: 'boolean', required: false, description: '是否启用。' },
      { name: 'groupType', type: 'string', required: false, description: '分组类型：personal 或 high_concurrency。' }
    ],
    example: { groupId: 'grp_xxx', name: '福利-主池' }
  },
  responseExample: { data: { source: 'stats', generatedAt: '2026-05-30T00:00:00.000Z', action: 'updated', target, group: { ...group, name: '福利-主池' } } }
},
{
  id: 'group-delete',
  name: '分组删除',
  summary: '按分组新增或列表响应返回的 ID 删除分组。默认分组或仍被约束保护的分组会被拒绝删除。',
  status: 'available',
  method: 'POST',
  path: '/__aipublic__/group/del',
  headers: [authHeader],
  query: [],
  requestBody: {
    contentType: 'application/json',
    fields: [
      { name: 'targetUsername', type: 'string', required: false, description: '可选校验条件。提供时必须与分组归属目标用户一致。', example: 'huanmin' },
      { name: 'groupId', type: 'string', required: true, description: '分组 ID。', example: 'grp_xxx' }
    ],
    example: { groupId: 'grp_xxx' }
  },
  responseExample: { data: { source: 'stats', generatedAt: '2026-05-30T00:00:00.000Z', action: 'deleted', target, group } }
},
{
  id: 'account-list',
  name: '账号列表',
  summary: '分页读取指定系统用户名下的 AI 账户脱敏摘要，支持按分组、供应商、状态和名称筛选。',
  status: 'available',
  method: 'GET',
  path: '/__aipublic__/account/list',
  headers: [authHeader],
  query: [
    targetQuery,
    { name: 'targetGroupName', type: 'string', required: false, description: '目标分组名称；提供该字段时必须同时提供 providerCode。', example: '福利' },
    { name: 'providerCode', type: 'string', required: false, description: '供应商编码筛选。', example: GPT_VENDOR_CODE },
    { name: 'providerProtocolProfileId', type: 'string', required: false, description: '供应商协议档案筛选。', example: 'profile_gpt_openai_v1' },
    { name: 'groupId', type: 'string', required: false, description: '目标分组 ID；优先于 targetGroupName。', example: 'grp_xxx' },
    { name: 'keyword', type: 'string', required: false, description: '按账号名称精确 / 前缀筛选。', example: '公益站' },
    { name: 'type', type: 'string', required: false, description: '账号类型筛选；公开写入当前只支持 api_key。', example: 'api_key' },
    { name: 'status', type: 'string', required: false, description: '账号状态，支持逗号分隔多个状态。', example: 'active,disabled' },
    { name: 'schedulable', type: 'string', required: false, description: '可调度状态筛选：all、enabled、disabled 或 cooling。', example: 'enabled' },
    ...pageQuery
  ],
  responseExample: { data: { source: 'stats', generatedAt: '2026-05-30T00:00:00.000Z', target, page: 1, pageSize: 20, pageUpperBound: 1, hasMore: false, items: [account] } }
},
{
  id: 'account-add',
  name: '账号新增',
  summary: '新增 API Key 类型账号到指定系统用户和分组；目标用户或分组不存在时自动创建，响应不会回显上游凭据。',
  status: 'available',
  method: 'POST',
  path: '/__aipublic__/account/add',
  headers: [authHeader],
  query: [],
  requestBody: {
    contentType: 'application/json',
    fields: [
      targetQuery,
      { name: 'targetDisplayName', type: 'string', required: false, description: '自动创建目标系统用户时使用的显示名称。', example: '欢民' },
      { name: 'targetGroupName', type: 'string', required: true, description: '目标账号分组名称。', example: '福利' },
      { name: 'providerCode', type: 'string', required: true, description: '供应商编码。', example: GPT_VENDOR_CODE },
      { name: 'providerProtocolProfileId', type: 'string', required: true, description: '供应商协议档案。', example: 'profile_gpt_openai_v1' },
      { name: 'name', type: 'string', required: true, description: '账号名称。', example: '公益站-青芽主通道' },
      { name: 'type', type: 'string', required: true, description: '账号类型；当前公开新增只支持 api_key。', example: 'api_key' },
      { name: 'baseUrl', type: 'string', required: true, description: 'OpenAI 兼容 Base URL。', example: 'https://api.openai.com/v1' },
      { name: 'apiKey', type: 'string', required: true, description: '上游 API Key；响应不会回显。', example: 'sk-...' },
      { name: 'supportedModels', type: 'string[]', required: false, description: '该账号支持的模型列表。' },
      { name: 'concurrencyLimit', type: 'number', required: false, description: '单账号并发限制，范围 1 到 100000。', example: 20 },
      { name: 'priority', type: 'number', required: false, description: '账号调度优先级，范围 0 到 100000。', example: 0 },
      { name: 'status', type: 'string', required: false, description: '账号状态：active 或 disabled。', example: 'active' },
      { name: 'availabilitySchedule', type: 'object|null', required: false, description: '时间计划；null 表示清空计划，未填写表示不限制。' },
      { name: 'notes', type: 'string', required: false, description: '账号备注，最多 1000 个字符。' }
    ],
    example: { targetUsername: 'huanmin', targetGroupName: '福利', providerCode: GPT_VENDOR_CODE, providerProtocolProfileId: 'profile_gpt_openai_v1', name: '公益站-青芽主通道', type: 'api_key', baseUrl: 'https://api.openai.com/v1', apiKey: 'sk-...' }
  },
  responseExample: { data: { source: 'stats', generatedAt: '2026-05-30T00:00:00.000Z', action: 'created', target: { ...target, groupId: 'grp_xxx', groupName: '福利', groupCreated: false }, account: { ...account, status: 'pending_test', schedulable: false } } }
},
{
  id: 'account-update',
  name: '账号修改',
  summary: '按账号 ID 修改既有 API Key 类型账号；找不到时返回 404，响应不回显上游凭据。',
  status: 'available',
  method: 'POST',
  path: '/__aipublic__/account/update',
  headers: [authHeader],
  query: [],
  requestBody: {
    contentType: 'application/json',
    fields: [
      { name: 'accountId', type: 'string', required: true, description: '账号 ID。', example: 'acc_xxx' },
      { name: 'targetUsername', type: 'string', required: false, description: '可选校验条件。提供时必须与账号归属目标用户一致。', example: 'huanmin' },
      { name: 'targetGroupName', type: 'string', required: false, description: '可选校验条件。提供时账号必须在该目标分组内。', example: '福利' },
      { name: 'providerCode', type: 'string', required: false, description: '可选校验条件。提供时必须与账号供应商一致。', example: GPT_VENDOR_CODE },
      { name: 'providerProtocolProfileId', type: 'string', required: false, description: '可选校验条件。提供时必须与账号协议档案一致。', example: 'profile_gpt_openai_v1' },
      { name: 'name', type: 'string', required: false, description: '账号名称。' },
      { name: 'type', type: 'string', required: false, description: '可选校验字段；当前公开修改只支持 api_key。', example: 'api_key' },
      { name: 'baseUrl', type: 'string', required: false, description: 'OpenAI 兼容 Base URL。' },
      { name: 'apiKey', type: 'string', required: false, description: '上游 API Key；响应不会回显。' },
      { name: 'supportedModels', type: 'string[]', required: false, description: '该账号支持的模型列表。' },
      { name: 'concurrencyLimit', type: 'number', required: false, description: '单账号并发限制。' },
      { name: 'priority', type: 'number', required: false, description: '账号调度优先级。' },
      { name: 'status', type: 'string', required: false, description: '账号状态：active 或 disabled。' },
      { name: 'availabilitySchedule', type: 'object|null', required: false, description: '时间计划；null 表示清空计划。' },
      { name: 'notes', type: 'string', required: false, description: '账号备注。' }
    ],
    example: { accountId: 'acc_xxx', apiKey: 'sk-...', status: 'disabled' }
  },
  responseExample: { data: { source: 'stats', generatedAt: '2026-05-30T00:00:00.000Z', action: 'updated', target: { ...target, groupId: 'grp_xxx', groupName: '福利', groupCreated: false }, account: { ...account, status: 'disabled', schedulable: false } } }
},
{
  id: 'account-delete',
  name: '账号删除',
  summary: '按账号 ID 删除账号；目标用户已停用时拒绝删除，找不到时幂等返回 not_found。',
  status: 'available',
  method: 'POST',
  path: '/__aipublic__/account/del',
  headers: [authHeader],
  query: [],
  requestBody: {
    contentType: 'application/json',
    fields: [
      { name: 'accountId', type: 'string', required: true, description: '账号 ID。', example: 'acc_xxx' },
      { name: 'targetUsername', type: 'string', required: false, description: '可选校验条件。提供时必须与账号归属目标用户一致。', example: 'huanmin' },
      { name: 'targetGroupName', type: 'string', required: false, description: '可选校验条件。提供时账号必须在该目标分组内。', example: '福利' },
      { name: 'providerCode', type: 'string', required: false, description: '可选校验条件。提供时必须与账号供应商一致。', example: GPT_VENDOR_CODE },
      { name: 'providerProtocolProfileId', type: 'string', required: false, description: '可选校验条件。提供时必须与账号协议档案一致。', example: 'profile_gpt_openai_v1' }
    ],
    example: { accountId: 'acc_xxx' }
  },
  responseExample: { data: { source: 'stats', generatedAt: '2026-05-30T00:00:00.000Z', action: 'deleted', target: { ...target, groupId: 'grp_xxx', groupName: '福利', groupCreated: false }, account } }
}
] satisfies ExternalPublicApiDocItemSeed[]
