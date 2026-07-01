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

export const externalPublicApiDocItems = [
{
  id: 'source-auth-demo',
  name: '来源鉴权 Demo',
  summary: '验证来源授权 token、状态和公开接口限频是否生效。',
  status: 'mock',
  method: 'GET',
  path: '/__aipublic__/demo/source-auth',
  headers: [authHeader],
  query: [],
  responseExample: {
    data: {
      ok: true,
      sourceName: '内置测试来源',
      tokenName: '内置测试 token',
      tokenPrefix: 'juis_test_mo',
      authenticatedAt: '2026-05-30T00:00:00.000Z',
      mock: true
    }
  }
},
{
  id: 'ip-usage',
  name: 'IP 维度消费聚合',
  summary: '读取 sub2api-lite 已预聚合的 IP 维度用量事实，供公益站后端自行映射用户和生成排行榜快照。',
  status: 'mock',
  method: 'GET',
  path: '/__aipublic__/ip/usage',
  headers: [authHeader],
  query: [
    {
      name: 'range',
      type: 'string',
      required: false,
      description: '快捷范围：today、last7d、last31d。公开接口只读取后台已维护的固定窗口。',
      example: 'last7d'
    },
    {
      name: 'page',
      type: 'number',
      required: false,
      description: '分页页码，默认 1。',
      example: 1
    },
    {
      name: 'pageSize',
      type: 'number',
      required: false,
      description: '每页数量，范围 1 到 100，默认 20。',
      example: 20
    },
    {
      name: 'keyword',
      type: 'string',
      required: false,
      description: '按 IP 精确或前缀筛选，最多 120 个字符。',
      example: '203.0.113.'
    },
    {
      name: 'sortField',
      type: 'string',
      required: false,
      description: '排序字段：requestCount、successCount、errorCount、errorRate、totalTokens、totalCost、activeDays、lastUsedAt。',
      example: 'totalTokens'
    },
    {
      name: 'sortOrder',
      type: 'string',
      required: false,
      description: '排序方向：desc 或 asc，默认 desc。',
      example: 'desc'
    }
  ],
  responseExample: {
    data: {
      source: 'stats',
      generatedAt: '2026-05-30T00:00:00.000Z',
      statsLagSeconds: 60,
      range: {
        preset: 'last7d',
        label: '最近7天',
        startDate: '2026-05-24',
        endDate: '2026-05-30',
        days: 7,
        maxDays: 31
      },
      rangeReady: true,
      page: 1,
      pageSize: 20,
      pageUpperBound: 1,
      hasMore: false,
      items: [
        {
          rank: 1,
          dimension: 'client_ip',
          ip: '203.0.113.10',
          requestCount: 1280,
          successCount: 1252,
          errorCount: 28,
          errorRate: 0.0219,
          inputTokens: 516000,
          outputTokens: 326000,
          cacheReadTokens: 126000,
          cacheRate: 0.2442,
          cacheWriteTokens: 0,
          cacheWrite1hTokens: 0,
          cacheWriteCost: 0,
          thinkingTokens: 0,
          inputImageTokens: 0,
          outputImageTokens: 0,
          totalTokens: 842000,
          totalCost: 12.36,
          cacheReadCost: 0.42,
          activeDays: 7,
          averageFirstTokenMs: 820,
          averageDurationMs: 3160,
          maxDurationMs: 12880,
          lastUsedAt: '2026-05-30T00:00:00.000Z'
        }
      ]
    }
  }
},
{
  id: 'consumption-ranking',
  name: 'IP 维度消耗排行',
  summary: '按 Token、成本或请求数返回 IP 维度 TopN。它不是公益站用户排行榜，公益站需要自行把 IP 聚合事实映射到用户。',
  status: 'mock',
  method: 'GET',
  path: '/__aipublic__/consumption/ranking',
  headers: [authHeader],
  query: [
    {
      name: 'range',
      type: 'string',
      required: false,
      description: '快捷范围：today、last7d、last31d。',
      example: 'last7d'
    },
    {
      name: 'limit',
      type: 'number',
      required: false,
      description: '返回前 N 个 IP，范围 1 到 100，默认 20。',
      example: 10
    },
    {
      name: 'metric',
      type: 'string',
      required: false,
      description: '排行指标：totalTokens、totalCost、requestCount，默认 totalTokens。',
      example: 'totalTokens'
    }
  ],
  responseExample: {
    data: {
      source: 'stats',
      generatedAt: '2026-05-30T00:00:00.000Z',
      dimension: 'client_ip',
      metric: 'totalTokens',
      range: {
        preset: 'last7d',
        label: '最近7天',
        startDate: '2026-05-24',
        endDate: '2026-05-30',
        days: 7,
        maxDays: 31
      },
      rangeReady: true,
      items: [
        {
          rank: 1,
          id: 'ip:203.0.113.10',
          name: '203.0.113.10',
          dimension: 'client_ip',
          ip: '203.0.113.10',
          metricValue: 842000,
          requestCount: 1280,
          successCount: 1252,
          errorCount: 28,
          errorRate: 0.0219,
          inputTokens: 516000,
          outputTokens: 326000,
          totalTokens: 842000,
          cacheReadTokens: 126000,
          cacheRate: 0.2442,
          cacheWriteTokens: 0,
          cacheWrite1hTokens: 0,
          cacheWriteCost: 0,
          thinkingTokens: 0,
          inputImageTokens: 0,
          outputImageTokens: 0,
          totalCost: 12.36,
          cacheReadCost: 0.42,
          activeDays: 7,
          averageFirstTokenMs: 820,
          averageDurationMs: 3160,
          maxDurationMs: 12880,
          lastUsedAt: '2026-05-30T00:00:00.000Z'
        }
      ]
    }
  }
},
{
  id: 'account-usage',
  name: '账号维度实际消耗聚合',
  summary: '读取 sub2api-lite 已预聚合的账号维度实际用量事实。公益站用 accountId 映射本地登记的 sub2apiAccountId，生成贡献榜快照。',
  status: 'mock',
  method: 'GET',
  path: '/__aipublic__/account/usage',
  headers: [authHeader],
  query: [
    {
      name: 'range',
      type: 'string',
      required: false,
      description: '快捷范围：today、last7d、last31d。公开接口只读取后台已维护的固定窗口。',
      example: 'last7d'
    },
    {
      name: 'page',
      type: 'number',
      required: false,
      description: '分页页码，默认 1。',
      example: 1
    },
    {
      name: 'pageSize',
      type: 'number',
      required: false,
      description: '每页数量，范围 1 到 100，默认 20。',
      example: 20
    },
    {
      name: 'keyword',
      type: 'string',
      required: false,
      description: '按账号 ID、账号名称、供应商或账号类型做精确或前缀筛选，最多 120 个字符。',
      example: '公益站'
    },
    {
      name: 'sortField',
      type: 'string',
      required: false,
      description: '排序字段：requestCount、successCount、errorCount、errorRate、totalTokens、totalCost、activeDays、lastUsedAt。',
      example: 'totalTokens'
    },
    {
      name: 'sortOrder',
      type: 'string',
      required: false,
      description: '排序方向：desc 或 asc，默认 desc。',
      example: 'desc'
    }
  ],
  responseExample: {
    data: {
      source: 'stats',
      generatedAt: '2026-05-30T00:00:00.000Z',
      statsLagSeconds: 60,
      range: {
        preset: 'last7d',
        label: '最近7天',
        startDate: '2026-05-24',
        endDate: '2026-05-30',
        days: 7,
        maxDays: 31
      },
      rangeReady: true,
      page: 1,
      pageSize: 20,
      pageUpperBound: 1,
      hasMore: false,
      items: [
        {
          rank: 1,
          dimension: 'account',
          accountId: 'acc_xxx',
          accountName: '公益站-青芽主通道',
          providerCode: GPT_VENDOR_CODE,
          type: 'api_key',
          status: 'active',
          requestCount: 1280,
          successCount: 1252,
          errorCount: 28,
          errorRate: 0.0219,
          inputTokens: 516000,
          outputTokens: 326000,
          totalTokens: 842000,
          cacheReadTokens: 126000,
          cacheRate: 0.2442,
          cacheWriteTokens: 0,
          cacheWrite1hTokens: 0,
          cacheWriteCost: 0,
          thinkingTokens: 0,
          inputImageTokens: 0,
          outputImageTokens: 0,
          totalCost: 12.36,
          cacheReadCost: 0.42,
          activeDays: 7,
          averageFirstTokenMs: 820,
          averageDurationMs: 3160,
          maxDurationMs: 12880,
          lastUsedAt: '2026-05-30T00:00:00.000Z'
        }
      ]
    }
  }
},
{
  id: 'access-info',
  name: '公益接入信息',
  summary: '返回公开接口可用范围和边界说明，不返回普通 API Key、上游账号凭据或公益站业务配置。',
  status: 'mock',
  method: 'GET',
  path: '/__aipublic__/access/info',
  headers: [authHeader],
  query: [],
  responseExample: {
    data: {
      source: 'stats',
      generatedAt: '2026-05-30T00:00:00.000Z',
      publicApiPrefix: '/__aipublic__',
      dataDimension: 'client_ip',
      supportedDimensions: ['client_ip', 'account'],
      authType: 'Bearer',
      supportedRanges: ['today', 'last7d', 'last31d'],
      supportedMetrics: ['totalTokens', 'totalCost', 'requestCount'],
      endpoints: [
        { method: 'GET', path: '/__aipublic__/ip/usage', description: '读取 IP 维度用量聚合列表。' },
        { method: 'GET', path: '/__aipublic__/account/usage', description: '读取账号维度实际用量聚合列表。' },
        { method: 'GET', path: '/__aipublic__/consumption/ranking', description: '读取基于 IP 维度聚合的消耗排行。' },
        { method: 'GET', path: '/__aipublic__/access/info', description: '读取公开接口接入边界和可用指标。' },
        { method: 'GET', path: '/__aipublic__/account/list', description: '分页读取指定系统用户下的账号脱敏摘要。' },
        { method: 'POST', path: '/__aipublic__/account/add', description: '新增账号到指定系统用户和分组。' },
        { method: 'POST', path: '/__aipublic__/account/update', description: '按账号 ID 修改账号的指定字段。' },
        { method: 'POST', path: '/__aipublic__/account/del', description: '按账号 ID 删除账号。' },
        { method: 'GET', path: '/__aipublic__/group/list', description: '分页读取指定系统用户下的分组脱敏摘要。' },
        { method: 'POST', path: '/__aipublic__/group/add', description: '新增指定系统用户下的分组。' },
        { method: 'POST', path: '/__aipublic__/group/update', description: '按分组 ID 修改分组的指定字段。' },
        { method: 'POST', path: '/__aipublic__/group/del', description: '按分组 ID 删除分组。' },
        { method: 'GET', path: '/__aipublic__/api-key/list', description: '分页读取指定系统用户下的 API Key 脱敏摘要。' },
        { method: 'POST', path: '/__aipublic__/api-key/add', description: '新增指定系统用户下的 API Key。' },
        { method: 'POST', path: '/__aipublic__/api-key/update', description: '按 API Key ID 修改指定字段。' },
        { method: 'POST', path: '/__aipublic__/api-key/del', description: '按 API Key ID 删除 API Key。' }
      ],
      boundary: {
        provides: [
          '来源授权 Bearer token 鉴权',
          'IP 维度请求数、Token、缓存、成本、活跃天数和速度指标聚合',
          '账号维度实际请求数、Token、缓存、成本、活跃天数和速度指标聚合',
          '基于 IP 聚合表的消耗排行便利视图',
          '分组、API Key 和账号的受控脱敏列表、新增、修改与删除入口'
        ],
        notProvided: [
          '公益站用户维度排行榜快照',
          'IP 到公益站用户、账号到公益站登记人的业务归属',
          '公益站公网 IP 拦截或访问频率控制',
          '普通 API Key、上游账号凭据或内部授权关系'
        ]
      }
    }
  }
},
{
  id: 'group-list',
  name: '分组列表',
  summary: '分页读取指定系统用户名下的分组，用于外部来源系统对账和找回分组 ID。',
  status: 'mock',
  method: 'GET',
  path: '/__aipublic__/group/list',
  headers: [authHeader],
  query: [
    { name: 'targetUsername', type: 'string', required: true, description: '目标系统用户账号。', example: 'huanmin' },
    { name: 'providerCode', type: 'string', required: false, description: '供应商编码筛选。', example: GPT_VENDOR_CODE },
    { name: 'keyword', type: 'string', required: false, description: '按分组名称或供应商编码精确 / 前缀筛选。', example: '福利' },
    { name: 'page', type: 'number', required: false, description: '分页页码，默认 1。', example: 1 },
    { name: 'pageSize', type: 'number', required: false, description: '每页数量，范围 1 到 100。', example: 20 }
  ],
  responseExample: {
    data: {
      source: 'stats',
      generatedAt: '2026-05-30T00:00:00.000Z',
      target: { username: 'huanmin', displayName: 'huanmin', systemAccountId: 'sysacc_xxx', created: false },
      page: 1,
      pageSize: 20,
      pageUpperBound: 1,
      hasMore: false,
      items: [{ id: 'grp_xxx', name: '福利', providerCode: GPT_VENDOR_CODE, enabled: true, groupType: 'personal', isDefault: false }]
    }
  }
},
{
  id: 'api-key-list',
  name: 'API Key 列表',
  summary: '分页读取指定系统用户名下的 API Key 摘要和策略路由信息；不会返回 API Key 明文。',
  status: 'mock',
  method: 'GET',
  path: '/__aipublic__/api-key/list',
  headers: [authHeader],
  query: [
    { name: 'targetUsername', type: 'string', required: true, description: '目标系统用户账号。', example: 'huanmin' },
    { name: 'routeStrategyId', type: 'string', required: false, description: '按策略路由 ID 筛选。', example: 'rts_xxx' },
    { name: 'keyword', type: 'string', required: false, description: '按 API Key 名称精确 / 前缀筛选。', example: '公益站访问密钥' },
    { name: 'status', type: 'string', required: false, description: '状态筛选：active、disabled 或 all。', example: 'active' },
    { name: 'page', type: 'number', required: false, description: '分页页码，默认 1。', example: 1 },
    { name: 'pageSize', type: 'number', required: false, description: '每页数量，范围 1 到 100。', example: 20 }
  ],
  responseExample: {
    data: {
      source: 'stats',
      generatedAt: '2026-05-30T00:00:00.000Z',
      target: { username: 'huanmin', displayName: 'huanmin', systemAccountId: 'sysacc_xxx', created: false },
      page: 1,
      pageSize: 20,
      pageUpperBound: 1,
      hasMore: false,
      items: [{
        id: 'key_xxx',
        name: '公益站访问密钥',
        keyPrefix: 'juis_xxx',
        status: 'active',
        routeStrategyId: 'rts_xxx',
        routeStrategyName: '公益站默认路由',
        routeStrategyMode: 'normal',
        routeStrategyStatus: 'active'
      }]
    }
  }
},
{
  id: 'account-list',
  name: '账号列表',
  summary: '分页读取指定系统用户名下的 AI 账户脱敏摘要，支持按分组、供应商、状态和名称筛选。',
  status: 'mock',
  method: 'GET',
  path: '/__aipublic__/account/list',
  headers: [authHeader],
  query: [
    { name: 'targetUsername', type: 'string', required: true, description: '目标系统用户账号。', example: 'huanmin' },
    { name: 'targetGroupName', type: 'string', required: false, description: '目标分组名称；提供该字段时必须同时提供 providerCode。', example: '福利' },
    { name: 'providerCode', type: 'string', required: false, description: '供应商编码筛选。', example: GPT_VENDOR_CODE },
    { name: 'groupId', type: 'string', required: false, description: '目标分组 ID；优先于 targetGroupName。', example: 'grp_xxx' },
    { name: 'keyword', type: 'string', required: false, description: '按账号名称精确 / 前缀筛选。', example: '公益站' },
    { name: 'type', type: 'string', required: false, description: '账号类型筛选；公开写入当前只支持 api_key。', example: 'api_key' },
    { name: 'status', type: 'string', required: false, description: '账号状态，支持逗号分隔多个状态。', example: 'active,disabled' },
    { name: 'schedulable', type: 'string', required: false, description: '可调度状态筛选：all、enabled、disabled 或 cooling。', example: 'enabled' },
    { name: 'page', type: 'number', required: false, description: '分页页码，默认 1。', example: 1 },
    { name: 'pageSize', type: 'number', required: false, description: '每页数量，范围 1 到 100。', example: 20 }
  ],
  responseExample: {
    data: {
      source: 'stats',
      generatedAt: '2026-05-30T00:00:00.000Z',
      target: { username: 'huanmin', displayName: 'huanmin', systemAccountId: 'sysacc_xxx', created: false },
      page: 1,
      pageSize: 20,
      pageUpperBound: 1,
      hasMore: false,
      items: [{
        id: 'acc_xxx',
        name: '公益站-青芽主通道',
        providerCode: GPT_VENDOR_CODE,
        type: 'api_key',
        clientCompatibility: 'openai_standard',
        status: 'active',
        supportedModels: ['gpt-5.5'],
        boundGroupId: 'grp_xxx',
        boundGroupName: '福利',
        schedulable: true,
        concurrencyLimit: 20,
        priority: 0
      }]
    }
  }
},
{
  id: 'group-add',
  name: '分组新增',
  summary: '在指定系统用户下新增账号分组；同名分组已存在时按幂等成功返回既有分组。',
  status: 'mock',
  method: 'POST',
  path: '/__aipublic__/group/add',
  headers: [authHeader],
  query: [],
  requestBody: {
    contentType: 'application/json',
    fields: [
      { name: 'targetUsername', type: 'string', required: true, description: '目标系统用户账号。', example: 'huanmin' },
      { name: 'targetDisplayName', type: 'string', required: false, description: '自动创建目标系统用户时使用的显示名称；未填写时使用 targetUsername。', example: '欢民' },
      { name: 'name', type: 'string', required: true, description: '分组名称。', example: '福利' },
      { name: 'providerCode', type: 'string', required: true, description: '供应商编码。', example: GPT_VENDOR_CODE },
      { name: 'description', type: 'string', required: false, description: '分组说明。' },
      { name: 'enabled', type: 'boolean', required: false, description: '是否启用，默认 true。', example: true },
      { name: 'groupType', type: 'string', required: false, description: '分组类型：personal 或 high_concurrency，默认 personal。', example: 'personal' }
    ],
    example: {
      targetUsername: 'huanmin',
      targetDisplayName: '欢民',
      name: '福利',
      providerCode: GPT_VENDOR_CODE,
      description: '公益站账号分组',
      enabled: true,
      groupType: 'personal'
    }
  },
  responseExample: {
    data: {
      source: 'stats',
      generatedAt: '2026-05-30T00:00:00.000Z',
      action: 'created',
      target: { username: 'huanmin', displayName: 'huanmin', systemAccountId: 'sysacc_xxx', created: false },
      group: { id: 'grp_xxx', name: '福利', providerCode: GPT_VENDOR_CODE, enabled: true, groupType: 'personal', isDefault: false }
    }
  }
},
{
  id: 'group-update',
  name: '分组修改',
  summary: '按分组新增或列表响应返回的 ID 修改分组指定字段。',
  status: 'mock',
  method: 'POST',
  path: '/__aipublic__/group/update',
  headers: [authHeader],
  query: [],
  requestBody: {
    contentType: 'application/json',
    fields: [
      { name: 'targetUsername', type: 'string', required: false, description: '可选校验条件。提供时必须与分组归属目标用户一致。', example: 'huanmin' },
      { name: 'groupId', type: 'string', required: true, description: '分组 ID。', example: 'grp_xxx' },
      { name: 'name', type: 'string', required: false, description: '新的分组名称。', example: '福利-主池' },
      { name: 'providerCode', type: 'string', required: false, description: '新的供应商编码。', example: GPT_VENDOR_CODE },
      { name: 'description', type: 'string|null', required: false, description: '新的分组说明；传 null 表示清空。' },
      { name: 'enabled', type: 'boolean', required: false, description: '是否启用。', example: true },
      { name: 'groupType', type: 'string', required: false, description: '分组类型：personal 或 high_concurrency。', example: 'personal' }
    ],
    example: {
      groupId: 'grp_xxx',
      name: '福利-主池'
    }
  },
  responseExample: {
    data: {
      source: 'stats',
      generatedAt: '2026-05-30T00:00:00.000Z',
      action: 'updated',
      target: { username: 'huanmin', displayName: 'huanmin', systemAccountId: 'sysacc_xxx', created: false },
      group: { id: 'grp_xxx', name: '福利-主池', providerCode: GPT_VENDOR_CODE, enabled: true, groupType: 'personal', isDefault: false }
    }
  }
},
{
  id: 'group-delete',
  name: '分组删除',
  summary: '按分组新增或列表响应返回的 ID 删除分组。默认分组或仍被约束保护的分组会被拒绝删除。',
  status: 'mock',
  method: 'POST',
  path: '/__aipublic__/group/del',
  headers: [authHeader],
  query: [],
  requestBody: {
    contentType: 'application/json',
    fields: [
      { name: 'targetUsername', type: 'string', required: false, description: '可选校验条件。提供时必须与分组归属目标用户一致。', example: 'huanmin' },
      { name: 'groupId', type: 'string', required: true, description: '分组新增或列表响应返回的分组 ID。', example: 'grp_xxx' }
    ],
    example: {
      groupId: 'grp_xxx'
    }
  },
  responseExample: {
    data: {
      source: 'stats',
      generatedAt: '2026-05-30T00:00:00.000Z',
      action: 'deleted',
      target: { username: 'huanmin', displayName: 'huanmin', systemAccountId: 'sysacc_xxx', created: false },
      group: { id: 'grp_xxx', name: '福利', providerCode: GPT_VENDOR_CODE, enabled: true, groupType: 'personal', isDefault: false }
    }
  }
},
{
  id: 'api-key-add',
  name: 'API Key 新增',
  summary: '为指定系统用户新增 API Key，并绑定一个或多个分组。',
  status: 'mock',
  method: 'POST',
  path: '/__aipublic__/api-key/add',
  headers: [authHeader],
  query: [],
  requestBody: {
    contentType: 'application/json',
    fields: [
      { name: 'targetUsername', type: 'string', required: true, description: '目标系统用户账号。', example: 'huanmin' },
      { name: 'name', type: 'string', required: true, description: 'API Key 名称。', example: '公益站访问密钥' },
      { name: 'description', type: 'string|null', required: false, description: 'API Key 说明；传 null 表示清空说明。', example: '公益站后端访问' },
      { name: 'routeStrategyId', type: 'string', required: true, description: 'API Key 绑定的策略路由 ID；分组绑定和路由模式在策略路由中维护。', example: 'rts_xxx' },
      { name: 'status', type: 'string', required: false, description: '状态：active 或 disabled，默认 active；同时提交时间计划时会按当前时间初始化为计划当前状态。', example: 'active' },
      { name: 'expiresAt', type: 'string', required: false, description: 'API Key 到期时间，ISO 8601 字符串；未填写表示不过期。', example: '2026-12-31T23:59:59.000Z' },
      { name: 'quotaLimits', type: 'object|null', required: false, description: '请求成本额度限制；支持 hourly、daily、weekly、monthly、total，传 null 表示清空。', example: { daily: { enabled: true, limit: 10 } } },
      { name: 'availabilitySchedule', type: 'object|null', required: false, description: '时间计划；null 表示清空计划，未填写表示不设置计划；保存计划时按当前时间初始化 status，之后只在窗口开始分钟启用一次、窗口结束分钟停用一次。' }
    ],
    example: {
      targetUsername: 'huanmin',
      name: '公益站访问密钥',
      description: '公益站后端访问',
      routeStrategyId: 'rts_xxx',
      status: 'active',
      expiresAt: '2026-12-31T23:59:59.000Z',
      quotaLimits: { daily: { enabled: true, limit: 10 } },
      availabilitySchedule: {
        enabled: true,
        mode: 'allow_windows',
        windows: [{ daysOfWeek: [1, 2, 3, 4, 5], start: '22:00', end: '23:55' }]
      }
    }
  },
  responseExample: {
    data: {
      source: 'stats',
      generatedAt: '2026-05-30T00:00:00.000Z',
      action: 'created',
      target: { username: 'huanmin', displayName: 'huanmin', systemAccountId: 'sysacc_xxx', created: false },
      apiKey: {
        id: 'key_xxx',
        name: '公益站访问密钥',
        keyPrefix: 'juis_xxx',
        key: 'juis_xxx_plain_once',
        status: 'active',
        routeStrategyId: 'rts_xxx',
        routeStrategyName: '公益站默认路由',
        routeStrategyMode: 'normal',
        routeStrategyStatus: 'active',
        expiresAt: '2026-12-31T23:59:59.000Z',
        availabilitySchedule: { enabled: true, mode: 'allow_windows' }
      }
    }
  }
},
{
  id: 'api-key-update',
  name: 'API Key 修改',
  summary: '修改指定 API Key 的名称、状态、策略路由绑定、额度或时间计划。',
  status: 'mock',
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
      { name: 'status', type: 'string', required: false, description: '状态：active 或 disabled；提交后立即改当前状态，后续计划只在下一次开始或结束边界继续写 status。', example: 'disabled' },
      { name: 'routeStrategyId', type: 'string', required: false, description: '新的策略路由 ID；提供后 API Key 改绑定该策略路由。', example: 'rts_xxx' },
      { name: 'expiresAt', type: 'string|null', required: false, description: '新的到期时间；传 null 表示清空到期时间。', example: null },
      { name: 'quotaLimits', type: 'object|null', required: false, description: '新的请求成本额度限制；传 null 表示清空。', example: null },
      { name: 'availabilitySchedule', type: 'object|null', required: false, description: '时间计划；null 表示清空计划，未填写表示保留；保存计划时按当前时间初始化 status，之后只在窗口开始分钟启用一次、窗口结束分钟停用一次。' }
    ],
    example: {
      apiKeyId: 'key_xxx',
      status: 'disabled'
    }
  },
  responseExample: {
    data: {
      source: 'stats',
      generatedAt: '2026-05-30T00:00:00.000Z',
      action: 'updated',
      target: { username: 'huanmin', displayName: 'huanmin', systemAccountId: 'sysacc_xxx', created: false },
      apiKey: {
        id: 'key_xxx',
        name: '公益站访问密钥',
        keyPrefix: 'juis_xxx',
        status: 'disabled',
        routeStrategyId: 'rts_xxx',
        routeStrategyName: '公益站默认路由',
        routeStrategyMode: 'normal',
        routeStrategyStatus: 'active'
      }
    }
  }
},
{
  id: 'api-key-delete',
  name: 'API Key 删除',
  summary: '按 API Key 新增或列表响应返回的 ID 删除 API Key。',
  status: 'mock',
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
    example: {
      apiKeyId: 'key_xxx'
    }
  },
  responseExample: {
    data: {
      source: 'stats',
      generatedAt: '2026-05-30T00:00:00.000Z',
      action: 'deleted',
      target: { username: 'huanmin', displayName: 'huanmin', systemAccountId: 'sysacc_xxx', created: false },
      apiKey: {
        id: 'key_xxx',
        name: '公益站访问密钥',
        keyPrefix: 'juis_xxx',
        status: 'active',
        routeStrategyId: 'rts_xxx',
        routeStrategyName: '公益站默认路由',
        routeStrategyMode: 'normal',
        routeStrategyStatus: 'active'
      }
    }
  }
},
{
  id: 'account-add',
  name: '账号新增',
  summary: '新增 API Key 类型账号到指定系统用户和分组；目标用户或分组不存在时自动创建，目标用户已停用时拒绝写入，重复账号会返回冲突。新增响应返回本系统账号 ID，外部来源系统应自行保存该 ID 用于后续修改或删除。',
  status: 'mock',
  method: 'POST',
  path: '/__aipublic__/account/add',
  headers: [authHeader],
  query: [],
  requestBody: {
    contentType: 'application/json',
    fields: [
      {
        name: 'targetUsername',
        type: 'string',
        required: true,
        description: '目标系统用户账号，例如 huanmin。',
        example: 'huanmin'
      },
      {
        name: 'targetDisplayName',
        type: 'string',
        required: false,
        description: '自动创建目标系统用户时使用的显示名称；未填写时使用 targetUsername。',
        example: '欢民'
      },
      {
        name: 'targetGroupName',
        type: 'string',
        required: true,
        description: '目标账号分组名称，例如 福利。',
        example: '福利'
      },
      {
        name: 'providerCode',
        type: 'string',
        required: true,
        description: '供应商编码。',
        example: GPT_VENDOR_CODE
      },
      {
        name: 'providerProtocolProfileId',
        type: 'string',
        required: true,
        description: '供应商协议档案。',
        example: 'profile_gpt_openai_v1'
      },
      {
        name: 'name',
        type: 'string',
        required: true,
        description: '新增后的账号名称。',
        example: '公益站-青芽主通道'
      },
      {
        name: 'type',
        type: 'string',
        required: true,
        description: '账号类型；当前公开新增只支持 api_key。',
        example: 'api_key'
      },
      {
        name: 'baseUrl',
        type: 'string',
        required: true,
        description: 'OpenAI 兼容 Base URL。',
        example: 'https://api.openai.com/v1'
      },
      {
        name: 'apiKey',
        type: 'string',
        required: true,
        description: '上游 API Key；响应不会回显。',
        example: 'sk-...'
      },
      {
        name: 'supportedModels',
        type: 'string[]',
        required: false,
        description: '该账号支持的模型列表，必须属于供应商模型目录。'
      },
      {
        name: 'concurrencyLimit',
        type: 'number',
        required: false,
        description: '单账号并发限制，范围 1 到 100000；用于公益站登记并发控制同步。',
        example: 20
      },
      {
        name: 'priority',
        type: 'number',
        required: false,
        description: '账号调度优先级，范围 0 到 100000；默认 0。',
        example: 0
      },
      {
        name: 'status',
        type: 'string',
        required: false,
        description: '账号状态：active 或 disabled；新增传 active 或不传时会先落成 pending_test，需测试通过后才参与调度。',
        example: 'active'
      },
      {
        name: 'availabilitySchedule',
        type: 'object|null',
        required: false,
        description: '时间计划；null 表示清空计划，未填写表示不限制。'
      },
      {
        name: 'notes',
        type: 'string',
        required: false,
        description: '账号备注，最多 1000 个字符。'
      }
    ],
    example: {
      targetUsername: 'huanmin',
      targetDisplayName: '欢民',
      targetGroupName: '福利',
      providerCode: GPT_VENDOR_CODE,
      providerProtocolProfileId: 'profile_gpt_openai_v1',
      name: '公益站-青芽主通道',
      type: 'api_key',
      baseUrl: 'https://api.openai.com/v1',
      apiKey: 'sk-...',
      supportedModels: ['gpt-5.5', 'gpt-5.4'],
      concurrencyLimit: 20,
      priority: 0,
      status: 'active',
      availabilitySchedule: {
        enabled: true,
        mode: 'allow_windows',
        windows: [{ daysOfWeek: [1, 2, 3, 4, 5], start: '22:00', end: '23:55' }]
      },
      notes: '公益站登记账号'
    }
  },
  responseExample: {
    data: {
      source: 'stats',
      generatedAt: '2026-05-30T00:00:00.000Z',
      action: 'created',
      target: {
        username: 'huanmin',
        displayName: 'huanmin',
        systemAccountId: 'sysacc_xxx',
        created: true,
        groupId: 'grp_xxx',
        groupName: '福利',
        groupCreated: true
      },
      account: {
        id: 'acc_xxx',
        name: '公益站-青芽主通道',
        providerCode: GPT_VENDOR_CODE,
        providerProtocolProfileId: 'profile_gpt_openai_v1',
        protocolCode: 'openai',
        protocolVersion: 'v1',
        type: 'api_key',
        clientCompatibility: 'openai_standard',
        status: 'pending_test',
        supportedModels: ['gpt-5.5', 'gpt-5.4'],
        boundGroupId: 'grp_xxx',
        boundGroupName: '福利',
        schedulable: false,
        availabilitySchedule: { enabled: true, mode: 'allow_windows' }
      }
    }
  }
},
{
  id: 'account-update',
  name: '账号修改',
  summary: '按账号新增或列表响应返回的本系统账号 ID 修改指定系统用户和分组内的既有账号；找不到时返回 404，响应不回显上游凭据。',
  status: 'mock',
  method: 'POST',
  path: '/__aipublic__/account/update',
  headers: [authHeader],
  query: [],
  requestBody: {
    contentType: 'application/json',
    fields: [
      { name: 'accountId', type: 'string', required: true, description: '账号新增或列表响应返回的账号 ID。', example: 'acc_xxx' },
      { name: 'targetUsername', type: 'string', required: false, description: '可选校验条件。提供时必须与账号归属目标用户一致。', example: 'huanmin' },
      { name: 'targetGroupName', type: 'string', required: false, description: '可选校验条件。提供时账号必须在该目标分组内。', example: '福利' },
      { name: 'providerCode', type: 'string', required: false, description: '可选校验条件。提供时必须与账号供应商一致。', example: GPT_VENDOR_CODE },
      { name: 'providerProtocolProfileId', type: 'string', required: false, description: '可选校验条件。提供时必须与账号协议档案一致。', example: 'profile_gpt_openai_v1' },
      { name: 'name', type: 'string', required: false, description: '账号名称；提供时覆盖原值。', example: '公益站-青芽主通道' },
      { name: 'type', type: 'string', required: false, description: '可选校验字段；当前公开修改只支持 api_key。', example: 'api_key' },
      { name: 'baseUrl', type: 'string', required: false, description: 'OpenAI 兼容 Base URL；提供时覆盖原值，未提供时保留原值。', example: 'https://api.openai.com/v1' },
      { name: 'apiKey', type: 'string', required: false, description: '上游 API Key；提供时覆盖原值，响应不会回显。', example: 'sk-...' },
      { name: 'supportedModels', type: 'string[]', required: false, description: '该账号支持的模型列表；提供时按当前数组覆盖。', example: ['gpt-5.5', 'gpt-5.4'] },
      { name: 'concurrencyLimit', type: 'number', required: false, description: '单账号并发限制，范围 1 到 100000。', example: 20 },
      { name: 'priority', type: 'number', required: false, description: '账号调度优先级，范围 0 到 100000。', example: 0 },
      { name: 'status', type: 'string', required: false, description: '账号状态：active 或 disabled；未填写时保留原状态，待测试账号不能通过修改接口激活。', example: 'disabled' },
      { name: 'availabilitySchedule', type: 'object|null', required: false, description: '时间计划；null 表示清空计划，未填写表示保留。' },
      { name: 'notes', type: 'string', required: false, description: '账号备注，最多 1000 个字符。' }
    ],
    example: {
      accountId: 'acc_xxx',
      apiKey: 'sk-...',
      supportedModels: ['gpt-5.5', 'gpt-5.4'],
      concurrencyLimit: 20,
      priority: 0,
      status: 'disabled',
      availabilitySchedule: null,
      notes: '公益站登记账号'
    }
  },
  responseExample: {
    data: {
      source: 'stats',
      generatedAt: '2026-05-30T00:00:00.000Z',
      action: 'updated',
      target: {
        username: 'huanmin',
        displayName: 'huanmin',
        systemAccountId: 'sysacc_xxx',
        created: false,
        groupId: 'grp_xxx',
        groupName: '福利',
        groupCreated: false
      },
      account: {
        id: 'acc_xxx',
        name: '公益站-青芽主通道',
        providerCode: GPT_VENDOR_CODE,
        providerProtocolProfileId: 'profile_gpt_openai_v1',
        protocolCode: 'openai',
        protocolVersion: 'v1',
        type: 'api_key',
        clientCompatibility: 'openai_standard',
        status: 'disabled',
        supportedModels: ['gpt-5.5', 'gpt-5.4'],
        boundGroupId: 'grp_xxx',
        boundGroupName: '福利',
        schedulable: false
      }
    }
  }
},
{
  id: 'account-delete',
  name: '账号删除',
  summary: '按账号新增或列表响应返回的本系统账号 ID 删除账号；目标用户已停用时拒绝删除，找不到时幂等返回 not_found。',
  status: 'mock',
  method: 'POST',
  path: '/__aipublic__/account/del',
  headers: [authHeader],
  query: [],
  requestBody: {
    contentType: 'application/json',
    fields: [
      {
        name: 'accountId',
        type: 'string',
        required: true,
        description: '账号新增或列表响应返回的账号 ID。',
        example: 'acc_xxx'
      },
      {
        name: 'targetUsername',
        type: 'string',
        required: false,
        description: '可选校验条件。提供时必须与账号归属目标用户一致。',
        example: 'huanmin'
      },
      {
        name: 'targetGroupName',
        type: 'string',
        required: false,
        description: '可选校验条件。提供时账号必须在该目标分组内。',
        example: '福利'
      },
      {
        name: 'providerCode',
        type: 'string',
        required: false,
        description: '可选校验条件。提供时必须与账号供应商一致。',
        example: GPT_VENDOR_CODE
      },
      {
        name: 'providerProtocolProfileId',
        type: 'string',
        required: false,
        description: '可选校验条件。提供时必须与账号协议档案一致。',
        example: 'profile_gpt_openai_v1'
      }
    ],
    example: {
      accountId: 'acc_xxx'
    }
  },
  responseExample: {
    data: {
      source: 'stats',
      generatedAt: '2026-05-30T00:00:00.000Z',
      action: 'deleted',
      target: {
        username: 'huanmin',
        displayName: 'huanmin',
        systemAccountId: 'sysacc_xxx',
        created: false,
        groupId: 'grp_xxx',
        groupName: '福利',
        groupCreated: false
      },
      account: {
        id: 'acc_xxx',
        name: '公益站-青芽主通道',
        providerCode: GPT_VENDOR_CODE,
        type: 'api_key',
        status: 'active',
        boundGroupId: 'grp_xxx',
        boundGroupName: '福利',
        schedulable: true
      }
    }
  }
}
] satisfies ExternalPublicApiDocItemSeed[]
