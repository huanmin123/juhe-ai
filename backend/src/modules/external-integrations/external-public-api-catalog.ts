import {
  externalIntegrationAccessInfoReadScope,
  externalIntegrationAccountAddWriteScope,
  externalIntegrationAccountDeleteWriteScope,
  externalIntegrationAccountListReadScope,
  externalIntegrationAccountUpdateWriteScope,
  externalIntegrationAccountUsageReadScope,
  externalIntegrationApiKeyAddWriteScope,
  externalIntegrationApiKeyDeleteWriteScope,
  externalIntegrationApiKeyListReadScope,
  externalIntegrationApiKeyUpdateWriteScope,
  externalIntegrationConsumptionRankingReadScope,
  externalIntegrationGroupAddWriteScope,
  externalIntegrationGroupDeleteWriteScope,
  externalIntegrationGroupListReadScope,
  externalIntegrationGroupUpdateWriteScope,
  externalIntegrationIpUsageReadScope,
  externalIntegrationSourceAuthDemoScope
} from '../../storage/external-integration-source.repository.js'

export type ExternalPublicApiMethod = 'GET' | 'POST' | 'PATCH' | 'DELETE'
export type ExternalPublicApiStatus = 'available' | 'mock'

export interface ExternalPublicApiField {
  name: string
  type: string
  required: boolean
  description: string
  example?: unknown
}

export interface ExternalPublicApiHeader {
  name: string
  required: boolean
  description: string
  example: string
}

export interface ExternalPublicApiBody {
  contentType: string
  fields: ExternalPublicApiField[]
  example: unknown
}

export interface ExternalPublicApiDocItem {
  id: string
  name: string
  summary: string
  status: ExternalPublicApiStatus
  method: ExternalPublicApiMethod
  path: string
  scope?: string
  headers: ExternalPublicApiHeader[]
  query: ExternalPublicApiField[]
  requestBody?: ExternalPublicApiBody
  responseFields: ExternalPublicApiField[]
  responseExample: unknown
}

export interface ExternalPublicApiCatalog {
  basePath: string
  authType: 'Bearer'
  items: ExternalPublicApiDocItem[]
}

const authHeader: ExternalPublicApiHeader = {
  name: 'Authorization',
  required: true,
  description: '来源授权 Bearer token。每个公开接口都有独立资源 scope；使用内置测试 token 时接口只返回 mock 数据。',
  example: 'Bearer <source_token>'
}

export function getExternalPublicApiCatalog(): ExternalPublicApiCatalog {
  return {
    basePath: '/__aipublic__',
    authType: 'Bearer',
    items: ([
      {
        id: 'source-auth-demo',
        name: '来源鉴权 Demo',
        summary: '验证来源授权 token、状态和公开接口限频是否生效。',
        status: 'available',
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
        status: 'available',
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
        status: 'available',
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
        status: 'available',
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
                providerCode: 'openai',
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
        status: 'available',
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
              { method: 'POST', path: '/__aipublic__/account/update', description: '修改指定系统用户和分组内的账号。' },
              { method: 'POST', path: '/__aipublic__/account/del', description: '删除指定系统用户和分组内的账号。' },
              { method: 'GET', path: '/__aipublic__/group/list', description: '分页读取指定系统用户下的分组脱敏摘要。' },
              { method: 'POST', path: '/__aipublic__/group/add', description: '新增指定系统用户下的分组。' },
              { method: 'POST', path: '/__aipublic__/group/update', description: '修改指定系统用户下的分组。' },
              { method: 'POST', path: '/__aipublic__/group/del', description: '删除指定系统用户下的分组。' },
              { method: 'GET', path: '/__aipublic__/api-key/list', description: '分页读取指定系统用户下的 API Key 脱敏摘要。' },
              { method: 'POST', path: '/__aipublic__/api-key/add', description: '新增指定系统用户下的 API Key。' },
              { method: 'POST', path: '/__aipublic__/api-key/update', description: '修改指定系统用户下的 API Key。' },
              { method: 'POST', path: '/__aipublic__/api-key/del', description: '删除指定系统用户下的 API Key。' }
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
        status: 'available',
        method: 'GET',
        path: '/__aipublic__/group/list',
        headers: [authHeader],
        query: [
          { name: 'targetUsername', type: 'string', required: true, description: '目标系统用户账号。', example: 'huanmin' },
          { name: 'providerCode', type: 'string', required: false, description: '供应商编码筛选。', example: 'openai' },
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
            items: [{ id: 'grp_xxx', name: '福利', providerCode: 'openai', enabled: true, groupType: 'personal', isDefault: false }]
          }
        }
      },
      {
        id: 'api-key-list',
        name: 'API Key 列表',
        summary: '分页读取指定系统用户名下的 API Key 摘要和分组绑定；不会返回 API Key 明文。',
        status: 'available',
        method: 'GET',
        path: '/__aipublic__/api-key/list',
        headers: [authHeader],
        query: [
          { name: 'targetUsername', type: 'string', required: true, description: '目标系统用户账号。', example: 'huanmin' },
          { name: 'keyword', type: 'string', required: false, description: '按 API Key 名称精确 / 前缀筛选。', example: '公益站访问密钥' },
          { name: 'status', type: 'string', required: false, description: '状态筛选：active、disabled 或 all。', example: 'active' },
          { name: 'groupId', type: 'string', required: false, description: '按任意绑定分组筛选。', example: 'grp_xxx' },
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
              groupRouteStrategy: 'priority_failover',
              groupBindings: [{ id: 'bind_xxx', groupId: 'grp_xxx', groupName: '福利', priority: 1, weight: 1, status: 'active', groupEnabled: true }]
            }]
          }
        }
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
          { name: 'targetUsername', type: 'string', required: true, description: '目标系统用户账号。', example: 'huanmin' },
          { name: 'targetGroupName', type: 'string', required: false, description: '目标分组名称；提供该字段时必须同时提供 providerCode。', example: '福利' },
          { name: 'providerCode', type: 'string', required: false, description: '供应商编码筛选。', example: 'openai' },
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
              providerCode: 'openai',
              type: 'api_key',
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
        status: 'available',
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
            { name: 'providerCode', type: 'string', required: true, description: '供应商编码。', example: 'openai' },
            { name: 'description', type: 'string', required: false, description: '分组说明。' },
            { name: 'enabled', type: 'boolean', required: false, description: '是否启用，默认 true。', example: true },
            { name: 'groupType', type: 'string', required: false, description: '分组类型：personal 或 high_concurrency，默认 personal。', example: 'personal' }
          ],
          example: {
            targetUsername: 'huanmin',
            targetDisplayName: '欢民',
            name: '福利',
            providerCode: 'openai',
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
            group: { id: 'grp_xxx', name: '福利', providerCode: 'openai', enabled: true, groupType: 'personal', isDefault: false }
          }
        }
      },
      {
        id: 'group-update',
        name: '分组修改',
        summary: '修改指定系统用户下的分组名称、说明或启用状态。',
        status: 'available',
        method: 'POST',
        path: '/__aipublic__/group/update',
        headers: [authHeader],
        query: [],
        requestBody: {
          contentType: 'application/json',
          fields: [
            { name: 'targetUsername', type: 'string', required: true, description: '目标系统用户账号。', example: 'huanmin' },
            { name: 'groupId', type: 'string', required: true, description: '分组 ID。', example: 'grp_xxx' },
            { name: 'name', type: 'string', required: false, description: '新的分组名称。', example: '福利-主池' },
            { name: 'providerCode', type: 'string', required: false, description: '新的供应商编码。', example: 'openai' },
            { name: 'description', type: 'string|null', required: false, description: '新的分组说明；传 null 表示清空。' },
            { name: 'enabled', type: 'boolean', required: false, description: '是否启用。', example: true },
            { name: 'groupType', type: 'string', required: false, description: '分组类型：personal 或 high_concurrency。', example: 'personal' }
          ],
          example: {
            targetUsername: 'huanmin',
            groupId: 'grp_xxx',
            name: '福利-主池',
            providerCode: 'openai',
            description: null,
            enabled: true,
            groupType: 'personal'
          }
        },
        responseExample: {
          data: {
            source: 'stats',
            generatedAt: '2026-05-30T00:00:00.000Z',
            action: 'updated',
            target: { username: 'huanmin', displayName: 'huanmin', systemAccountId: 'sysacc_xxx', created: false },
            group: { id: 'grp_xxx', name: '福利-主池', providerCode: 'openai', enabled: true, groupType: 'personal', isDefault: false }
          }
        }
      },
      {
        id: 'group-delete',
        name: '分组删除',
        summary: '删除指定系统用户下的分组。默认分组或仍被约束保护的分组会被拒绝删除。',
        status: 'available',
        method: 'POST',
        path: '/__aipublic__/group/del',
        headers: [authHeader],
        query: [],
        requestBody: {
          contentType: 'application/json',
          fields: [
            { name: 'targetUsername', type: 'string', required: true, description: '目标系统用户账号。', example: 'huanmin' },
            { name: 'groupId', type: 'string', required: true, description: '分组新增或列表响应返回的分组 ID。', example: 'grp_xxx' }
          ],
          example: {
            targetUsername: 'huanmin',
            groupId: 'grp_xxx'
          }
        },
        responseExample: {
          data: {
            source: 'stats',
            generatedAt: '2026-05-30T00:00:00.000Z',
            action: 'deleted',
            target: { username: 'huanmin', displayName: 'huanmin', systemAccountId: 'sysacc_xxx', created: false },
            group: { id: 'grp_xxx', name: '福利', providerCode: 'openai', enabled: true, groupType: 'personal', isDefault: false }
          }
        }
      },
      {
        id: 'api-key-add',
        name: 'API Key 新增',
        summary: '为指定系统用户新增 API Key，并绑定一个或多个分组。',
        status: 'available',
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
            { name: 'groupBindings', type: 'array', required: true, description: 'API Key 分组绑定数组，1 到 20 项；每项包含 groupId，可选 priority、weight 和 status。', example: [{ groupId: 'grp_xxx', priority: 1, weight: 1, status: 'active' }] },
            { name: 'groupRouteStrategy', type: 'string', required: false, description: '分组路由策略：priority_failover、round_robin 或 weighted_round_robin，默认 priority_failover。', example: 'priority_failover' },
            { name: 'status', type: 'string', required: false, description: '状态：active 或 disabled，默认 active。', example: 'active' },
            { name: 'expiresAt', type: 'string', required: false, description: 'API Key 到期时间，ISO 8601 字符串；未填写表示不过期。', example: '2026-12-31T23:59:59.000Z' },
            { name: 'quotaLimits', type: 'object|null', required: false, description: '请求成本额度限制；支持 hourly、daily、weekly、monthly、total，传 null 表示清空。', example: { daily: { enabled: true, limit: 10 } } },
            { name: 'availabilitySchedule', type: 'object|null', required: false, description: '自动启停计划；null 表示清空计划，未填写表示不限制。' }
          ],
          example: {
            targetUsername: 'huanmin',
            name: '公益站访问密钥',
            description: '公益站后端访问',
            groupBindings: [{ groupId: 'grp_xxx', priority: 1, weight: 1, status: 'active' }],
            groupRouteStrategy: 'priority_failover',
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
              groupRouteStrategy: 'priority_failover',
              groupBindings: [{ id: 'bind_xxx', groupId: 'grp_xxx', groupName: '福利', priority: 1, weight: 1, status: 'active', groupEnabled: true }],
              expiresAt: '2026-12-31T23:59:59.000Z',
              availabilitySchedule: { enabled: true, mode: 'allow_windows' }
            }
          }
        }
      },
      {
        id: 'api-key-update',
        name: 'API Key 修改',
        summary: '修改指定 API Key 的名称、状态、分组绑定、额度或可用计划。',
        status: 'available',
        method: 'POST',
        path: '/__aipublic__/api-key/update',
        headers: [authHeader],
        query: [],
        requestBody: {
          contentType: 'application/json',
          fields: [
            { name: 'targetUsername', type: 'string', required: true, description: '目标系统用户账号。', example: 'huanmin' },
            { name: 'apiKeyId', type: 'string', required: true, description: 'API Key ID。', example: 'key_xxx' },
            { name: 'name', type: 'string', required: false, description: '新的 API Key 名称。' },
            { name: 'description', type: 'string|null', required: false, description: '新的 API Key 说明；传 null 表示清空。' },
            { name: 'status', type: 'string', required: false, description: '状态：active 或 disabled。', example: 'disabled' },
            { name: 'groupBindings', type: 'array', required: false, description: '新的 API Key 分组绑定数组；提供时按当前数组替换绑定关系，1 到 20 项。', example: [{ groupId: 'grp_xxx', priority: 1, weight: 1, status: 'active' }] },
            { name: 'groupRouteStrategy', type: 'string', required: false, description: '分组路由策略：priority_failover、round_robin 或 weighted_round_robin。', example: 'round_robin' },
            { name: 'expiresAt', type: 'string|null', required: false, description: '新的到期时间；传 null 表示清空到期时间。', example: null },
            { name: 'quotaLimits', type: 'object|null', required: false, description: '新的请求成本额度限制；传 null 表示清空。', example: null },
            { name: 'availabilitySchedule', type: 'object|null', required: false, description: '自动启停计划；null 表示清空计划，未填写表示保留。' }
          ],
          example: {
            targetUsername: 'huanmin',
            apiKeyId: 'key_xxx',
            description: null,
            status: 'disabled',
            groupBindings: [{ groupId: 'grp_xxx', priority: 1, weight: 1, status: 'active' }],
            groupRouteStrategy: 'round_robin',
            expiresAt: null,
            quotaLimits: null,
            availabilitySchedule: null
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
              groupRouteStrategy: 'round_robin',
              groupBindings: [{ id: 'bind_xxx', groupId: 'grp_xxx', groupName: '福利', priority: 1, weight: 1, status: 'active', groupEnabled: true }]
            }
          }
        }
      },
      {
        id: 'api-key-delete',
        name: 'API Key 删除',
        summary: '删除指定系统用户下的 API Key。',
        status: 'available',
        method: 'POST',
        path: '/__aipublic__/api-key/del',
        headers: [authHeader],
        query: [],
        requestBody: {
          contentType: 'application/json',
          fields: [
            { name: 'targetUsername', type: 'string', required: true, description: '目标系统用户账号。', example: 'huanmin' },
            { name: 'apiKeyId', type: 'string', required: true, description: 'API Key 新增或列表响应返回的 API Key ID。', example: 'key_xxx' }
          ],
          example: {
            targetUsername: 'huanmin',
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
              groupRouteStrategy: 'priority_failover',
              groupBindings: [{ id: 'bind_xxx', groupId: 'grp_xxx', groupName: '福利', priority: 1, weight: 1, status: 'active', groupEnabled: true }]
            }
          }
        }
      },
      {
        id: 'account-add',
        name: '账号新增',
        summary: '新增 API Key 类型账号到指定系统用户和分组；目标用户或分组不存在时自动创建，目标用户已停用时拒绝写入，重复账号会返回冲突。新增响应返回本系统账号 ID，外部来源系统应自行保存该 ID 用于后续修改或删除。',
        status: 'available',
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
              example: 'openai'
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
              description: '账号状态：active 或 disabled，默认 active。',
              example: 'active'
            },
            {
              name: 'availabilitySchedule',
              type: 'object|null',
              required: false,
              description: '自动启停计划；null 表示清空计划，未填写表示不限制。'
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
            providerCode: 'openai',
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
              providerCode: 'openai',
              type: 'api_key',
              status: 'active',
              supportedModels: ['gpt-5.5', 'gpt-5.4'],
              boundGroupId: 'grp_xxx',
              boundGroupName: '福利',
              schedulable: true,
              availabilitySchedule: { enabled: true, mode: 'allow_windows' }
            }
          }
        }
      },
      {
        id: 'account-update',
        name: '账号修改',
        summary: '按账号新增或列表响应返回的本系统账号 ID 修改指定系统用户和分组内的既有账号；找不到时返回 404，响应不回显上游凭据。',
        status: 'available',
        method: 'POST',
        path: '/__aipublic__/account/update',
        headers: [authHeader],
        query: [],
        requestBody: {
          contentType: 'application/json',
          fields: [
            { name: 'targetUsername', type: 'string', required: true, description: '目标系统用户账号。', example: 'huanmin' },
            { name: 'targetDisplayName', type: 'string', required: false, description: '目标系统用户显示名称；修改时不会自动创建用户，仅用于 schema 与新增保持一致。', example: '欢民' },
            { name: 'targetGroupName', type: 'string', required: true, description: '目标账号分组名称。', example: '福利' },
            { name: 'providerCode', type: 'string', required: true, description: '供应商编码。', example: 'openai' },
            { name: 'accountId', type: 'string', required: true, description: '账号新增或列表响应返回的账号 ID。', example: 'acc_xxx' },
            { name: 'name', type: 'string', required: true, description: '账号名称。', example: '公益站-青芽主通道' },
            { name: 'type', type: 'string', required: true, description: '账号类型；当前公开修改只支持 api_key。', example: 'api_key' },
            { name: 'baseUrl', type: 'string', required: true, description: 'OpenAI 兼容 Base URL。', example: 'https://api.openai.com/v1' },
            { name: 'apiKey', type: 'string', required: true, description: '上游 API Key；响应不会回显。', example: 'sk-...' },
            { name: 'supportedModels', type: 'string[]', required: false, description: '该账号支持的模型列表；提供时按当前数组覆盖。', example: ['gpt-5.5', 'gpt-5.4'] },
            { name: 'concurrencyLimit', type: 'number', required: false, description: '单账号并发限制，范围 1 到 100000。', example: 20 },
            { name: 'priority', type: 'number', required: false, description: '账号调度优先级，范围 0 到 100000。', example: 0 },
            { name: 'status', type: 'string', required: false, description: '账号状态：active 或 disabled。', example: 'disabled' },
            { name: 'availabilitySchedule', type: 'object|null', required: false, description: '自动启停计划；null 表示清空计划，未填写表示保留。' },
            { name: 'notes', type: 'string', required: false, description: '账号备注，最多 1000 个字符。' }
          ],
          example: {
            targetUsername: 'huanmin',
            targetDisplayName: '欢民',
            targetGroupName: '福利',
            providerCode: 'openai',
            accountId: 'acc_xxx',
            name: '公益站-青芽主通道',
            type: 'api_key',
            baseUrl: 'https://api.openai.com/v1',
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
              providerCode: 'openai',
              type: 'api_key',
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
        summary: '按账号新增或列表响应返回的本系统账号 ID 删除指定目标用户和分组内的账号；目标用户已停用时拒绝删除，找不到时幂等返回 not_found。',
        status: 'available',
        method: 'POST',
        path: '/__aipublic__/account/del',
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
              example: 'openai'
            },
            {
              name: 'accountId',
              type: 'string',
              required: true,
              description: '账号新增或列表响应返回的账号 ID。',
              example: 'acc_xxx'
            }
          ],
          example: {
            targetUsername: 'huanmin',
            targetGroupName: '福利',
            providerCode: 'openai',
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
              providerCode: 'openai',
              type: 'api_key',
              status: 'active',
              boundGroupId: 'grp_xxx',
              boundGroupName: '福利',
              schedulable: true
            }
          }
        }
      }
    ] as ExternalPublicApiDocItem[]).map((item) => ({
      ...item,
      responseFields: responseFieldsForPublicApiDocItem(item.id),
      scope: scopeForPublicApiDocItem(item.id)
    }))
  }
}

function scopeForPublicApiDocItem(id: string): string {
  const scopesById: Record<string, string> = {
    'source-auth-demo': externalIntegrationSourceAuthDemoScope,
    'ip-usage': externalIntegrationIpUsageReadScope,
    'consumption-ranking': externalIntegrationConsumptionRankingReadScope,
    'account-usage': externalIntegrationAccountUsageReadScope,
    'access-info': externalIntegrationAccessInfoReadScope,
    'group-list': externalIntegrationGroupListReadScope,
    'api-key-list': externalIntegrationApiKeyListReadScope,
    'account-list': externalIntegrationAccountListReadScope,
    'group-add': externalIntegrationGroupAddWriteScope,
    'group-update': externalIntegrationGroupUpdateWriteScope,
    'group-delete': externalIntegrationGroupDeleteWriteScope,
    'api-key-add': externalIntegrationApiKeyAddWriteScope,
    'api-key-update': externalIntegrationApiKeyUpdateWriteScope,
    'api-key-delete': externalIntegrationApiKeyDeleteWriteScope,
    'account-add': externalIntegrationAccountAddWriteScope,
    'account-update': externalIntegrationAccountUpdateWriteScope,
    'account-delete': externalIntegrationAccountDeleteWriteScope
  }
  return scopesById[id] ?? ''
}

function responseFieldsForPublicApiDocItem(id: string): ExternalPublicApiField[] {
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
    apiDocField(`${prefix}.providerCode`, 'string', false, '供应商编码；账号元数据缺失时可能为空。', 'openai'),
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
    apiDocField(`${prefix}.providerCode`, 'string', false, '供应商编码。', 'openai'),
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
    apiDocField(`${prefix}.groupBindings[].providerCode`, 'string', false, '绑定分组供应商编码；无法补齐时可能缺省。', 'openai'),
    apiDocField(`${prefix}.groupBindings[].priority`, 'number', false, '优先级路由使用的优先级。', 1),
    apiDocField(`${prefix}.groupBindings[].weight`, 'number', false, '加权轮询使用的权重。', 1),
    apiDocField(`${prefix}.groupBindings[].status`, 'string', false, '绑定状态：active 或 disabled。', 'active'),
    apiDocField(`${prefix}.groupBindings[].groupEnabled`, 'boolean', false, '绑定分组当前是否启用。', true),
    apiDocField(`${prefix}.expiresAt`, 'string', false, 'API Key 到期时间，ISO 8601 字符串；未设置时缺省。', '2026-12-31T23:59:59.000Z'),
    apiDocField(`${prefix}.availabilitySchedule`, 'object', false, 'API Key 自动启停计划；未设置时缺省。')
  ]
}

function publicAccountFields(prefix: string): ExternalPublicApiField[] {
  return [
    apiDocField(`${prefix}.id`, 'string', false, 'AI 账户 ID；对象为 null 时没有该字段。', 'acc_xxx'),
    apiDocField(`${prefix}.name`, 'string', false, 'AI 账户名称。', '公益站-青芽主通道'),
    apiDocField(`${prefix}.providerCode`, 'string', false, '供应商编码。', 'openai'),
    apiDocField(`${prefix}.type`, 'string', false, '账号类型，公开写接口当前只支持 api_key。', 'api_key'),
    apiDocField(`${prefix}.status`, 'string', false, '账号状态。', 'active'),
    apiDocField(`${prefix}.supportedModels`, 'string[]', false, '账号支持的模型列表；未限制或未配置时可能缺省。', ['gpt-5.5', 'gpt-5.4']),
    apiDocField(`${prefix}.boundGroupId`, 'string', false, '账号绑定分组 ID。', 'grp_xxx'),
    apiDocField(`${prefix}.boundGroupName`, 'string', false, '账号绑定分组名称。', '福利'),
    apiDocField(`${prefix}.schedulable`, 'boolean', false, '账号当前是否可调度。', true),
    apiDocField(`${prefix}.availabilitySchedule`, 'object', false, '账号自动启停计划；未设置时缺省。')
  ]
}
