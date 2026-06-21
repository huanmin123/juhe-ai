import { GPT_VENDOR_CODE } from '../../domain/provider-protocol.js'
import type { ExternalPublicApiField } from './external-public-api-catalog.types.js'

export function responseFieldsForPublicApiDocItem(id: string): ExternalPublicApiField[] {
  switch (id) {
    case 'source-auth-demo':
      return [
        apiDocField('data.ok', 'boolean', true, '鉴权和限频校验通过时为 true。', true),
        apiDocField('data.sourceName', 'string', true, '来源系统名称。', '内置测试来源'),
        apiDocField('data.tokenName', 'string', true, '命中的来源 token 名称。', '内置测试 token'),
        apiDocField('data.tokenPrefix', 'string', true, '命中的来源 token 前缀，用于排查和对账，不是完整 token。', 'juis_test_mo'),
        apiDocField('data.authenticatedAt', 'string', true, '鉴权完成时间，ISO 8601 字符串。', '2026-05-30T00:00:00.000Z'),
        apiDocField('data.mock', 'boolean', true, '是否为内置测试 token 调用。', true)
      ]
    case 'ip-usage':
      return [
        ...publicGeneratedFields(),
        ...publicStatsLagFields(),
        ...publicRangeFields(),
        ...publicPageFields(),
        apiDocField('data.items', 'array', true, '当前页 IP 维度用量聚合列表。'),
        ...publicClientIpUsageItemFields('data.items[]')
      ]
    case 'consumption-ranking':
      return [
        ...publicGeneratedFields(),
        ...publicStatsLagFields(),
        apiDocField('data.dimension', 'string', true, '排行维度，固定为 client_ip。', 'client_ip'),
        apiDocField('data.metric', 'string', true, '当前排行指标：totalTokens、totalCost 或 requestCount。', 'totalTokens'),
        ...publicRangeFields(),
        apiDocField('data.rangeReady', 'boolean', true, '窗口是否已由后台聚合生成；false 时 items 为空。', true),
        apiDocField('data.items', 'array', true, '按 metric 降序排列的 IP 维度 TopN 列表。'),
        apiDocField('data.items[].id', 'string', true, '排行项 ID，格式为 ip:<ip>。', 'ip:203.0.113.10'),
        apiDocField('data.items[].name', 'string', true, '排行项展示名称，当前等于 IP。', '203.0.113.10'),
        apiDocField('data.items[].metricValue', 'number', true, '当前排行指标对应的数值。', 842000),
        ...publicClientIpUsageItemFields('data.items[]')
      ]
    case 'account-usage':
      return [
        ...publicGeneratedFields(),
        ...publicStatsLagFields(),
        ...publicRangeFields(),
        ...publicPageFields(),
        apiDocField('data.items', 'array', true, '当前页账号维度用量聚合列表。'),
        ...publicAccountUsageItemFields('data.items[]')
      ]
    case 'access-info':
      return [
        ...publicGeneratedFields(),
        apiDocField('data.publicApiPrefix', 'string', true, '公开接口统一前缀。', '/__aipublic__'),
        apiDocField('data.dataDimension', 'string', true, '默认公开统计维度，当前为 client_ip。', 'client_ip'),
        apiDocField('data.supportedDimensions', 'string[]', true, '公开接口支持的统计维度。', ['client_ip', 'account']),
        apiDocField('data.authType', 'string', true, '认证方式，固定为 Bearer。', 'Bearer'),
        apiDocField('data.supportedRanges', 'string[]', true, '公开统计支持的固定窗口。', ['today', 'last7d', 'last31d']),
        apiDocField('data.supportedMetrics', 'string[]', true, '排行接口支持的指标。', ['totalTokens', 'totalCost', 'requestCount']),
        apiDocField('data.endpoints', 'array', true, '当前公开接口清单。'),
        apiDocField('data.endpoints[].method', 'string', true, '接口 HTTP 方法。', 'GET'),
        apiDocField('data.endpoints[].path', 'string', true, '接口路径。', '/__aipublic__/ip/usage'),
        apiDocField('data.endpoints[].description', 'string', true, '接口用途说明。', '读取 IP 维度用量聚合列表。'),
        apiDocField('data.boundary.provides', 'string[]', true, 'sub2api-lite 在公开接口中提供的能力边界。'),
        apiDocField('data.boundary.notProvided', 'string[]', true, '不由 sub2api-lite 提供、需要外部来源系统自行维护的能力。')
      ]
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

function publicStatsLagFields(): ExternalPublicApiField[] {
  return [
    apiDocField('data.statsLagSeconds', 'number', false, '后台聚合结果相对当前时间的滞后秒数；无法判断时可能缺省。', 60)
  ]
}

function publicRangeFields(): ExternalPublicApiField[] {
  return [
    apiDocField('data.range.preset', 'string', true, '请求使用的固定范围：today、last7d 或 last31d。', 'last7d'),
    apiDocField('data.range.label', 'string', true, '范围中文展示名。', '最近7天'),
    apiDocField('data.range.startDate', 'string', true, '窗口开始日期，格式 YYYY-MM-DD。', '2026-05-24'),
    apiDocField('data.range.endDate', 'string', true, '窗口结束日期，格式 YYYY-MM-DD。', '2026-05-30'),
    apiDocField('data.range.days', 'number', true, '当前窗口天数。', 7),
    apiDocField('data.range.maxDays', 'number', true, '公开接口当前允许的最大窗口天数。', 31),
    apiDocField('data.rangeReady', 'boolean', true, '窗口是否已由后台聚合生成；false 时 items 为空。', true)
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

function publicClientIpUsageItemFields(prefix: string): ExternalPublicApiField[] {
  return [
    apiDocField(`${prefix}.rank`, 'number', true, '当前页内按查询排序得到的排名序号。', 1),
    apiDocField(`${prefix}.dimension`, 'string', true, '数据维度，固定为 client_ip。', 'client_ip'),
    apiDocField(`${prefix}.ip`, 'string', true, '规范化 IPv4 地址。', '203.0.113.10'),
    ...publicUsageMetricFields(prefix)
  ]
}

function publicAccountUsageItemFields(prefix: string): ExternalPublicApiField[] {
  return [
    apiDocField(`${prefix}.rank`, 'number', true, '当前页内按查询排序得到的排名序号。', 1),
    apiDocField(`${prefix}.dimension`, 'string', true, '数据维度，固定为 account。', 'account'),
    apiDocField(`${prefix}.accountId`, 'string', true, '账号维度事实键，也是外部来源系统保存和映射账号的主键。', 'acc_xxx'),
    apiDocField(`${prefix}.accountName`, 'string', true, '账号名称。', '公益站-青芽主通道'),
    apiDocField(`${prefix}.providerCode`, 'string', false, '供应商编码；账号元数据缺失时可能为空。', GPT_VENDOR_CODE),
    apiDocField(`${prefix}.type`, 'string', false, '账号类型；账号元数据缺失时可能为空。', 'api_key'),
    apiDocField(`${prefix}.status`, 'string', false, '账号状态；账号元数据缺失时可能为空。', 'active'),
    ...publicUsageMetricFields(prefix)
  ]
}

function publicUsageMetricFields(prefix: string): ExternalPublicApiField[] {
  return [
    apiDocField(`${prefix}.requestCount`, 'number', true, '请求总数。', 1280),
    apiDocField(`${prefix}.successCount`, 'number', true, '成功请求数。', 1252),
    apiDocField(`${prefix}.errorCount`, 'number', true, '失败请求数。', 28),
    apiDocField(`${prefix}.errorRate`, 'number', true, '失败率，取值 0 到 1，保留 4 位小数。', 0.0219),
    apiDocField(`${prefix}.inputTokens`, 'number', true, '输入 token 数。', 516000),
    apiDocField(`${prefix}.outputTokens`, 'number', true, '输出 token 数。', 326000),
    apiDocField(`${prefix}.cacheReadTokens`, 'number', true, '缓存读取 token 数。', 126000),
    apiDocField(`${prefix}.cacheRate`, 'number', true, '缓存读取 token / 输入 token 的比例，取值 0 到 1。', 0.2442),
    apiDocField(`${prefix}.totalTokens`, 'number', true, '总 token 数，等于 inputTokens + outputTokens。', 842000),
    apiDocField(`${prefix}.totalCost`, 'number', true, '估算总成本，单位 USD。', 12.36),
    apiDocField(`${prefix}.cacheReadCost`, 'number', true, '缓存读取成本，单位 USD。', 0.42),
    apiDocField(`${prefix}.activeDays`, 'number', true, '当前窗口内有请求的活跃天数。', 7),
    apiDocField(`${prefix}.averageFirstTokenMs`, 'number', false, '平均首 token 耗时，单位毫秒；无样本时缺省。', 820),
    apiDocField(`${prefix}.averageDurationMs`, 'number', false, '平均总耗时，单位毫秒；无样本时缺省。', 3160),
    apiDocField(`${prefix}.maxDurationMs`, 'number', false, '最大总耗时，单位毫秒；无样本时缺省。', 12880),
    apiDocField(`${prefix}.lastUsedAt`, 'string', false, '最近一次请求时间，ISO 8601 字符串；无请求时缺省。', '2026-05-30T00:00:00.000Z'),
    apiDocField(`${prefix}.lastErrorAt`, 'string', false, '最近一次错误时间，ISO 8601 字符串；无错误时缺省。', '2026-05-30T00:00:00.000Z')
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

function publicApiKeyFields(prefix: string): ExternalPublicApiField[] {
  return [
    apiDocField(`${prefix}.id`, 'string', false, 'API Key ID；对象为 null 时没有该字段。', 'key_xxx'),
    apiDocField(`${prefix}.name`, 'string', false, 'API Key 名称。', '公益站访问密钥'),
    apiDocField(`${prefix}.keyPrefix`, 'string', false, 'API Key 前缀，用于展示和对账，不是完整密钥。', 'juis_xxx'),
    apiDocField(`${prefix}.key`, 'string', false, '新增 API Key 时一次性返回的完整明文密钥；列表、修改、删除响应不会返回。', 'juis_xxx_plain_once'),
    apiDocField(`${prefix}.status`, 'string', false, 'API Key 状态：active 或 disabled。', 'active'),
    apiDocField(`${prefix}.groupRouteStrategy`, 'string', false, '分组路由策略。', 'priority_failover'),
    apiDocField(`${prefix}.groupBindings`, 'array', false, 'API Key 绑定的分组路由项。'),
    apiDocField(`${prefix}.groupBindings[].id`, 'string', false, '绑定关系 ID。', 'bind_xxx'),
    apiDocField(`${prefix}.groupBindings[].groupId`, 'string', false, '绑定分组 ID。', 'grp_xxx'),
    apiDocField(`${prefix}.groupBindings[].groupName`, 'string', false, '绑定分组名称。', '福利'),
    apiDocField(`${prefix}.groupBindings[].providerCode`, 'string', false, '绑定分组供应商编码；无法补齐时可能缺省。', GPT_VENDOR_CODE),
    apiDocField(`${prefix}.groupBindings[].priority`, 'number', false, '优先级路由使用的优先级。', 1),
    apiDocField(`${prefix}.groupBindings[].weight`, 'number', false, '加权轮询使用的权重。', 1),
    apiDocField(`${prefix}.groupBindings[].status`, 'string', false, '绑定状态：active 或 disabled。', 'active'),
    apiDocField(`${prefix}.groupBindings[].groupEnabled`, 'boolean', false, '绑定分组当前是否启用。', true),
    apiDocField(`${prefix}.expiresAt`, 'string', false, 'API Key 到期时间，ISO 8601 字符串；未设置时缺省。', '2026-12-31T23:59:59.000Z'),
    apiDocField(`${prefix}.availabilitySchedule`, 'object', false, 'API Key 时间计划；未设置时缺省。'),
    apiDocField(`${prefix}.availabilityScheduleActive`, 'boolean', false, 'API Key 时间计划当前派生状态；真实可用性仍需同时满足 status、过期时间和系统账户状态，未设置计划时缺省。', true)
  ]
}

function publicAccountFields(prefix: string): ExternalPublicApiField[] {
  return [
    apiDocField(`${prefix}.id`, 'string', false, 'AI 账户 ID；对象为 null 时没有该字段。', 'acc_xxx'),
    apiDocField(`${prefix}.name`, 'string', false, 'AI 账户名称。', '公益站-青芽主通道'),
    apiDocField(`${prefix}.providerCode`, 'string', false, '供应商编码。', GPT_VENDOR_CODE),
    apiDocField(`${prefix}.type`, 'string', false, '账号类型，公开写接口当前只支持 api_key。', 'api_key'),
    apiDocField(`${prefix}.clientCompatibility`, 'string', false, '客户端兼容模式：openai_standard 或 codex_responses。GLM Coding 只有显式为 codex_responses 时启用 Codex bridge。', 'openai_standard'),
    apiDocField(`${prefix}.status`, 'string', false, '账号状态。', 'active'),
    apiDocField(`${prefix}.supportedModels`, 'string[]', false, '账号支持的模型列表；未限制或未配置时可能缺省。', ['gpt-5.5', 'gpt-5.4']),
    apiDocField(`${prefix}.boundGroupId`, 'string', false, '账号绑定分组 ID。', 'grp_xxx'),
    apiDocField(`${prefix}.boundGroupName`, 'string', false, '账号绑定分组名称。', '福利'),
    apiDocField(`${prefix}.schedulable`, 'boolean', false, '账号当前是否可调度。', true),
    apiDocField(`${prefix}.availabilitySchedule`, 'object', false, '账号时间计划；未设置时缺省。')
  ]
}
