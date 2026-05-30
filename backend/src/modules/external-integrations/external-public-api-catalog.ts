import {
  externalIntegrationAccountPushScope,
  externalIntegrationIpUsageReadScope,
  externalIntegrationSourceAuthDemoScope,
  externalIntegrationTestToken
} from '../../storage/external-integration-source.repository.js'

export type ExternalPublicApiMethod = 'GET' | 'POST' | 'PATCH' | 'DELETE'
export type ExternalPublicApiStatus = 'available' | 'mock'

export interface ExternalPublicApiField {
  name: string
  type: string
  required: boolean
  description: string
  example?: string | number | boolean
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
  scope: string
  headers: ExternalPublicApiHeader[]
  query: ExternalPublicApiField[]
  requestBody?: ExternalPublicApiBody
  responseExample: unknown
}

export interface ExternalPublicApiCatalog {
  basePath: string
  authType: 'Bearer'
  testTokenName: string
  testToken: string
  items: ExternalPublicApiDocItem[]
}

const authHeader: ExternalPublicApiHeader = {
  name: 'Authorization',
  required: true,
  description: '来源系统 Bearer token。使用内置测试 token 时接口只返回 mock 数据。',
  example: 'Bearer <source_token>'
}

export function getExternalPublicApiCatalog(): ExternalPublicApiCatalog {
  return {
    basePath: '/__aipublic__',
    authType: 'Bearer',
    testTokenName: '内置测试 token',
    testToken: externalIntegrationTestToken,
    items: [
      {
        id: 'source-auth-demo',
        name: '来源鉴权 Demo',
        summary: '验证来源系统 token、scope、状态和公开接口限频是否生效。',
        status: 'available',
        method: 'GET',
        path: '/__aipublic__/demo/source-auth',
        scope: externalIntegrationSourceAuthDemoScope,
        headers: [authHeader],
        query: [],
        responseExample: {
          data: {
            ok: true,
            sourceName: '内置测试来源',
            tokenName: '内置测试 token',
            tokenPrefix: 'juis_test_mo',
            scopes: [externalIntegrationSourceAuthDemoScope],
            authenticatedAt: '2026-05-30T00:00:00.000Z',
            mock: true
          }
        }
      },
      {
        id: 'mock-ranking-demo',
        name: '公益榜 Mock Demo',
        summary: '返回一份固定 mock 排行数据，用于调用方验证请求头、路径和响应解析。',
        status: 'mock',
        method: 'GET',
        path: '/__aipublic__/demo/mock-ranking',
        scope: externalIntegrationSourceAuthDemoScope,
        headers: [authHeader],
        query: [
          {
            name: 'range',
            type: 'string',
            required: false,
            description: 'mock 统计范围，示例值 last7d。',
            example: 'last7d'
          },
          {
            name: 'limit',
            type: 'number',
            required: false,
            description: '返回前 N 条 mock 排名，范围 1 到 20。',
            example: 5
          }
        ],
        responseExample: {
          data: {
            mock: true,
            range: 'last7d',
            generatedAt: '2026-05-30T00:00:00.000Z',
            items: [
              {
                rank: 1,
                name: '公益体验入口',
                provider: 'OpenAI',
                requestCount: 1280,
                totalTokens: 842000,
                totalCostUsd: 12.36
              }
            ]
          }
        }
      },
      {
        id: 'juhe-ai-ip-usage',
        name: 'IP 维度消费聚合',
        summary: '读取 sub2api-lite 已预聚合的 IP 维度用量事实，供公益站后端自行映射用户和生成排行榜快照。',
        status: 'available',
        method: 'GET',
        path: '/__aipublic__/juhe-ai/ip-usage',
        scope: externalIntegrationIpUsageReadScope,
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
        id: 'juhe-ai-consumption-ranking',
        name: 'IP 维度消耗排行',
        summary: '按 Token、成本或请求数返回 IP 维度 TopN。它不是公益站用户排行榜，公益站需要自行把 IP 聚合事实映射到用户。',
        status: 'available',
        method: 'GET',
        path: '/__aipublic__/juhe-ai/consumption-ranking',
        scope: externalIntegrationIpUsageReadScope,
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
                totalTokens: 842000,
                cacheReadTokens: 126000,
                cacheRate: 0.2442,
                averageFirstTokenMs: 820,
                averageDurationMs: 3160,
                maxDurationMs: 12880
              }
            ]
          }
        }
      },
      {
        id: 'juhe-ai-access-info',
        name: '公益接入信息',
        summary: '返回公开接口可用范围和边界说明，不返回普通 API Key、上游账号凭据或公益站业务配置。',
        status: 'available',
        method: 'GET',
        path: '/__aipublic__/juhe-ai/access-info',
        scope: externalIntegrationIpUsageReadScope,
        headers: [authHeader],
        query: [],
        responseExample: {
          data: {
            source: 'stats',
            generatedAt: '2026-05-30T00:00:00.000Z',
            publicApiPrefix: '/__aipublic__',
            dataDimension: 'client_ip',
            authType: 'Bearer',
            supportedRanges: ['today', 'last7d', 'last31d'],
            supportedMetrics: ['totalTokens', 'totalCost', 'requestCount'],
            boundary: {
              provides: ['来源系统 Bearer token 鉴权', 'IP 维度聚合事实'],
              notProvided: ['公益站用户维度排行榜快照', 'IP 到公益站用户的业务归属']
            }
          }
        }
      },
      {
        id: 'juhe-ai-account-push',
        name: '公益账号推送',
        summary: '由公益站后端把审核后的公益 AI 登记推送到指定系统用户和分组；目标用户或分组不存在时自动创建，externalId 使用结构化映射精确幂等更新。',
        status: 'available',
        method: 'POST',
        path: '/__aipublic__/juhe-ai/accounts',
        scope: externalIntegrationAccountPushScope,
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
              required: false,
              description: '供应商编码，默认 openai。',
              example: 'openai'
            },
            {
              name: 'name',
              type: 'string',
              required: true,
              description: '推送后的账号名称。',
              example: '公益站-青芽主通道'
            },
            {
              name: 'type',
              type: 'string',
              required: false,
              description: '账号类型；当前公开推送只支持 api_key。',
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
              name: 'status',
              type: 'string',
              required: false,
              description: '账号状态：active 或 disabled，默认 active。',
              example: 'active'
            },
            {
              name: 'externalId',
              type: 'string',
              required: false,
              description: '公益站登记 ID；同一目标用户、分组和供应商内按该值精确更新原账号。',
              example: 'juhe-ai-public-welfare:ai-registration:12'
            }
          ],
          example: {
            targetUsername: 'huanmin',
            targetGroupName: '福利',
            providerCode: 'openai',
            name: '公益站-青芽主通道',
            type: 'api_key',
            baseUrl: 'https://api.openai.com/v1',
            apiKey: 'sk-...',
            supportedModels: ['gpt-5.5', 'gpt-5.4'],
            status: 'active',
            externalId: 'juhe-ai-public-welfare:ai-registration:12'
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
              schedulable: true
            },
            externalId: 'juhe-ai-public-welfare:ai-registration:12'
          }
        }
      }
    ]
  }
}
