import { GPT_VENDOR_CODE } from '../../domain/provider-protocol.js'
import type { ExternalPublicApiField } from './external-public-api-catalog.types.js'

export function responseFieldsForPublicApiDocItem(id: string): ExternalPublicApiField[] {
  switch (id) {
    case 'group-list':
      return [
        ...publicGeneratedFields(),
        ...publicTargetFields('data.target'),
        ...publicPageFields(),
        apiDocField('data.items', 'array', true, '当前页分组列表。'),
        ...publicGroupFields('data.items[]')
      ]
    case 'group-add':
    case 'group-update':
    case 'group-delete':
      return [
        ...publicGeneratedFields(),
        apiDocField('data.action', 'string', true, '执行结果：created、existing、updated、deleted 或 mock；分组不存在时正式调用返回 404 错误响应。', 'created'),
        ...publicTargetFields('data.target'),
        apiDocField('data.group', 'object|null', true, '分组摘要；正式成功响应为对象，错误响应不包在 data 内。'),
        ...publicGroupFields('data.group')
      ]
    case 'route-strategy-list':
      return [
        ...publicGeneratedFields(),
        ...publicTargetFields('data.target'),
        ...publicPageFields(),
        apiDocField('data.items', 'array', true, '当前页路由策略列表。'),
        ...publicRouteStrategyFields('data.items[]')
      ]
    case 'route-strategy-add':
    case 'route-strategy-update':
    case 'route-strategy-delete':
      return [
        ...publicGeneratedFields(),
        apiDocField('data.action', 'string', true, '执行结果：created、updated、deleted 或 mock；路由策略不存在时正式调用返回 404 错误响应。', 'created'),
        ...publicTargetFields('data.target'),
        apiDocField('data.routeStrategy', 'object|null', true, '路由策略摘要；正式成功响应为对象。'),
        ...publicRouteStrategyFields('data.routeStrategy')
      ]
    case 'api-key-list':
      return [
        ...publicGeneratedFields(),
        ...publicTargetFields('data.target'),
        ...publicPageFields(),
        apiDocField('data.items', 'array', true, '当前页 API Key 摘要列表。'),
        ...publicApiKeyFields('data.items[]')
      ]
    case 'api-key-add':
    case 'api-key-update':
    case 'api-key-delete':
      return [
        ...publicGeneratedFields(),
        apiDocField('data.action', 'string', true, '执行结果：created、updated、deleted 或 mock；API Key 不存在时正式调用返回 404 错误响应。', 'created'),
        ...publicTargetFields('data.target'),
        apiDocField('data.apiKey', 'object|null', true, 'API Key 摘要；正式成功响应为对象，新增接口仅在该对象内一次性返回 key 明文。'),
        ...publicApiKeyFields('data.apiKey')
      ]
    case 'account-list':
      return [
        ...publicGeneratedFields(),
        ...publicTargetFields('data.target'),
        ...publicPageFields(),
        apiDocField('data.items', 'array', true, '当前页 AI 账户脱敏摘要列表。'),
        ...publicAccountFields('data.items[]'),
        apiDocField('data.items[].concurrencyLimit', 'number', true, '单账号并发限制。', 20),
        apiDocField('data.items[].priority', 'number', true, '账号调度优先级。', 0)
      ]
    case 'account-add':
    case 'account-update':
      return [
        ...publicGeneratedFields(),
        apiDocField('data.action', 'string', true, '执行结果：created、updated 或 mock。', 'created'),
        ...publicTargetWithGroupFields('data.target'),
        apiDocField('data.account', 'object', true, 'AI 账户脱敏摘要。'),
        ...publicAccountFields('data.account')
      ]
    case 'account-delete':
      return [
        ...publicGeneratedFields(),
        apiDocField('data.action', 'string', true, '执行结果：deleted、not_found 或 mock。', 'deleted'),
        ...publicTargetWithGroupFields('data.target'),
        apiDocField('data.account', 'object|null', true, '已删除账号的脱敏摘要；not_found 时为 null。'),
        ...publicAccountFields('data.account')
      ]
    default:
      return []
  }
}

function apiDocField(
  name: string,
  type: string,
  required: boolean,
  description: string,
  example?: unknown
): ExternalPublicApiField {
  return { name, type, required, description, example }
}

function publicGeneratedFields(): ExternalPublicApiField[] {
  return [
    apiDocField('data.source', 'string', true, '数据来源：stats 表示正式统计或控制面数据，mock 表示内置测试 token 模拟数据。', 'stats'),
    apiDocField('data.generatedAt', 'string', true, '响应生成时间，ISO 8601 字符串。', '2026-05-30T00:00:00.000Z')
  ]
}

function publicPageFields(): ExternalPublicApiField[] {
  return [
    apiDocField('data.page', 'number', true, '当前页码。', 1),
    apiDocField('data.pageSize', 'number', true, '当前每页数量。', 20),
    apiDocField('data.pageUpperBound', 'number', true, '分页上界，用于前端翻页；不是精确总数。', 1),
    apiDocField('data.hasMore', 'boolean', true, '是否还有下一页。', false)
  ]
}

function publicTargetFields(prefix: string): ExternalPublicApiField[] {
  return [
    apiDocField(`${prefix}.username`, 'string', true, '目标系统用户账号。', 'huanmin'),
    apiDocField(`${prefix}.displayName`, 'string', true, '目标系统用户显示名称。', 'huanmin'),
    apiDocField(`${prefix}.systemAccountId`, 'string', true, '目标系统用户在 sub2api-lite 内的 ID。', 'sysacc_xxx'),
    apiDocField(`${prefix}.created`, 'boolean', true, '本次调用是否自动创建了目标系统用户。', false)
  ]
}

function publicTargetWithGroupFields(prefix: string): ExternalPublicApiField[] {
  return [
    ...publicTargetFields(prefix),
    apiDocField(`${prefix}.groupId`, 'string', true, '目标分组 ID。', 'grp_xxx'),
    apiDocField(`${prefix}.groupName`, 'string', true, '目标分组名称。', '福利'),
    apiDocField(`${prefix}.groupCreated`, 'boolean', true, '本次调用是否自动创建了目标分组。', false)
  ]
}

function publicGroupFields(prefix: string): ExternalPublicApiField[] {
  return [
    apiDocField(`${prefix}.id`, 'string', false, '分组 ID；对象为 null 时没有该字段。', 'grp_xxx'),
    apiDocField(`${prefix}.name`, 'string', false, '分组名称。', '福利'),
    apiDocField(`${prefix}.providerCode`, 'string', false, '供应商编码。', GPT_VENDOR_CODE),
    apiDocField(`${prefix}.description`, 'string', false, '分组说明；未填写时缺省。', '公益站账号分组'),
    apiDocField(`${prefix}.enabled`, 'boolean', false, '分组是否启用。', true),
    apiDocField(`${prefix}.groupType`, 'string', false, '分组类型：personal 或 high_concurrency。', 'personal'),
    apiDocField(`${prefix}.isDefault`, 'boolean', false, '是否默认分组。', false)
  ]
}

function publicRouteStrategyFields(prefix: string): ExternalPublicApiField[] {
  return [
    apiDocField(`${prefix}.id`, 'string', false, '路由策略 ID；对象为 null 时没有该字段。', 'rts_xxx'),
    apiDocField(`${prefix}.name`, 'string', false, '路由策略名称。', '公益站默认路由'),
    apiDocField(`${prefix}.description`, 'string', false, '路由策略说明；未填写时缺省。'),
    apiDocField(`${prefix}.mode`, 'string', false, '路由模式：normal、hybrid_smart、weighted、failover 或 round_robin。', 'normal'),
    apiDocField(`${prefix}.status`, 'string', false, '路由策略状态：active 或 disabled。', 'active'),
    apiDocField(`${prefix}.isDefault`, 'boolean', false, '是否默认路由策略。', false),
    apiDocField(`${prefix}.apiKeyCount`, 'number', false, '绑定该路由策略的 API Key 数量。', 1),
    apiDocField(`${prefix}.hybridRoutingConfig`, 'object', false, '混合智能路由配置；非 hybrid_smart 模式通常缺省。'),
    apiDocField(`${prefix}.groupBindings`, 'array', false, '路由策略绑定的分组列表。'),
    apiDocField(`${prefix}.groupBindings[].id`, 'string', false, '路由策略分组绑定 ID。', 'rsg_xxx'),
    apiDocField(`${prefix}.groupBindings[].groupId`, 'string', false, '绑定分组 ID。', 'grp_xxx'),
    apiDocField(`${prefix}.groupBindings[].groupName`, 'string', false, '绑定分组名称。', '福利'),
    apiDocField(`${prefix}.groupBindings[].providerCode`, 'string', false, '绑定分组供应商编码。', GPT_VENDOR_CODE),
    apiDocField(`${prefix}.groupBindings[].priority`, 'number', false, '故障回退或轮询等模式使用的优先级。', 1),
    apiDocField(`${prefix}.groupBindings[].weight`, 'number', false, '权重调度模式使用的权重，范围 1 到 100。', 100),
    apiDocField(`${prefix}.groupBindings[].status`, 'string', false, '绑定状态：active 或 disabled。', 'active'),
    apiDocField(`${prefix}.groupBindings[].groupEnabled`, 'boolean', false, '绑定分组当前是否启用。', true),
    apiDocField(`${prefix}.createdAt`, 'string', false, '创建时间，ISO 8601 字符串。', '2026-05-30T00:00:00.000Z'),
    apiDocField(`${prefix}.updatedAt`, 'string', false, '更新时间，ISO 8601 字符串。', '2026-05-30T00:00:00.000Z')
  ]
}

function publicApiKeyFields(prefix: string): ExternalPublicApiField[] {
  return [
    apiDocField(`${prefix}.id`, 'string', false, 'API Key ID；对象为 null 时没有该字段。', 'key_xxx'),
    apiDocField(`${prefix}.name`, 'string', false, 'API Key 名称。', '公益站访问密钥'),
    apiDocField(`${prefix}.keyPrefix`, 'string', false, 'API Key 前缀，用于展示和对账，不是完整密钥。', 'juis_xxx'),
    apiDocField(`${prefix}.key`, 'string', false, '新增 API Key 时一次性返回的完整明文密钥；列表、修改、删除响应不会返回。', 'juis_xxx_plain_once'),
    apiDocField(`${prefix}.status`, 'string', false, 'API Key 状态：active 或 disabled。', 'active'),
    apiDocField(`${prefix}.routeStrategyId`, 'string', false, 'API Key 绑定的策略路由 ID。', 'rts_xxx'),
    apiDocField(`${prefix}.routeStrategyName`, 'string', false, '策略路由名称；无法补齐时可能缺省。', '公益站默认路由'),
    apiDocField(`${prefix}.routeStrategyMode`, 'string', false, '策略路由模式。', 'normal'),
    apiDocField(`${prefix}.routeStrategyStatus`, 'string', false, '策略路由状态：active 或 disabled。', 'active'),
    apiDocField(`${prefix}.expiresAt`, 'string', false, 'API Key 到期时间，ISO 8601 字符串；未设置时缺省。', '2026-12-31T23:59:59.000Z'),
    apiDocField(`${prefix}.availabilitySchedule`, 'object', false, 'API Key 时间计划；未设置时缺省。计划命中开始 / 结束边界时会直接更新 API Key status。')
  ]
}

function publicAccountFields(prefix: string): ExternalPublicApiField[] {
  return [
    apiDocField(`${prefix}.id`, 'string', false, 'AI 账户 ID；对象为 null 时没有该字段。', 'acc_xxx'),
    apiDocField(`${prefix}.name`, 'string', false, 'AI 账户名称。', '公益站-青芽主通道'),
    apiDocField(`${prefix}.providerCode`, 'string', false, '供应商编码。', GPT_VENDOR_CODE),
    apiDocField(`${prefix}.providerProtocolProfileId`, 'string', false, '供应商协议档案；账号元数据缺失或旧记录未补齐时可能缺省。', 'profile_gpt_openai_v1'),
    apiDocField(`${prefix}.protocolCode`, 'string', false, '协议编码；由供应商协议档案派生。', 'openai'),
    apiDocField(`${prefix}.protocolVersion`, 'string', false, '协议版本；由供应商协议档案派生。', 'v1'),
    apiDocField(`${prefix}.type`, 'string', false, '账号类型，公开写接口当前只支持 api_key。', 'api_key'),
    apiDocField(`${prefix}.clientCompatibility`, 'string', false, '账号内部派生客户端能力摘要，只读返回；客户端画像由网关内部识别，跨协议桥接请使用混合供应商账户。', 'openai_standard'),
    apiDocField(`${prefix}.status`, 'string', false, '账号状态。', 'active'),
    apiDocField(`${prefix}.supportedModels`, 'string[]', false, '账号支持的模型列表；未限制或未配置时可能缺省。', ['gpt-5.5', 'gpt-5.4']),
    apiDocField(`${prefix}.boundGroupId`, 'string', false, '账号绑定分组 ID。', 'grp_xxx'),
    apiDocField(`${prefix}.boundGroupName`, 'string', false, '账号绑定分组名称。', '福利'),
    apiDocField(`${prefix}.schedulable`, 'boolean', false, '账号当前是否可调度。', true),
    apiDocField(`${prefix}.availabilitySchedule`, 'object', false, '账号时间计划；未设置时缺省。')
  ]
}
