import { strict as assert } from 'node:assert'
import { readFileSync } from 'node:fs'
import { resolve } from 'node:path'

const root = resolve('src')

const groupsRoutes = readSource('modules/groups/groups.routes.ts')
const groupSummaryRepository = readSource('storage/group-summary.repository.ts')
assertIncludes(groupsRoutes, 'listGroupItemsPageAsync', '分组列表必须使用专用列表 repository 投影')
assertIncludes(groupSummaryRepository, 'buildGroupListItems', '分组列表 DTO 必须由专用映射构造')
assertIncludes(groupSummaryRepository, 'authorizationSourceSummary', '分组列表必须用授权来源摘要替代完整来源数组')
assertIncludes(groupSummaryRepository, 'loadResourceAuthorizationSourcesByAuthorizationIds', '分组列表必须按授权 ID 批量读取来源，不能逐行查询')
assertIncludes(groupsRoutes, "groupsRouter.get('/:id'", '分组编辑必须有单条详情接口支撑渐进式加载')

const systemTeamRepository = readSource('storage/system-team.repository.ts')
assertIncludes(systemTeamRepository, 'systemTeamListItemFromRow', '团队列表必须使用不含 members 的轻量映射')
assertIncludes(systemTeamRepository, 'listSystemTeamMemberCountsForTeamIds', '团队列表必须使用成员计数批量查询')
assertFunctionExcludes(systemTeamRepository, 'listSystemTeamsPage', 'listSystemTeamMembersForTeamIds', '团队分页列表不得加载完整 members')
assertFunctionExcludes(systemTeamRepository, 'listSystemTeamsPageAsync', 'listSystemTeamMembersForTeamIdsAsync', '团队异步分页列表不得加载完整 members')

const externalSourceRepository = readSource('storage/external-integration-source.repository.ts')
assertIncludes(externalSourceRepository, 'mapSourceListItem', '外部接入源列表必须使用轻量映射')
assertIncludes(externalSourceRepository, 'loadExternalIntegrationSourcePrimaryTokensBySourceIds', '外部接入源列表只能加载主 token 预览')
assertFunctionExcludes(externalSourceRepository, 'listExternalIntegrationSources', 'loadExternalIntegrationSourceTokenStatsBySourceIds', '外部接入源列表不得为轻量 DTO 聚合 token 计数')
assertFunctionExcludes(externalSourceRepository, 'listExternalIntegrationSourcesAsync', 'loadExternalIntegrationSourceTokenStatsBySourceIdsAsync', '外部接入源异步列表不得为轻量 DTO 聚合 token 计数')
assertFunctionExcludes(externalSourceRepository, 'listExternalIntegrationSources', 'loadExternalIntegrationSourceTokensBySourceIds(', '外部接入源列表不得加载完整 tokens')
assertFunctionExcludes(externalSourceRepository, 'listExternalIntegrationSourcesAsync', 'loadExternalIntegrationSourceTokensBySourceIdsAsync(', '外部接入源异步列表不得加载完整 tokens')
const externalSourceMappers = readSource('storage/external-integration-source-mappers.ts')
assertFunctionExcludes(externalSourceMappers, 'mapSourceListItem', 'tokenCount', '外部接入源轻量列表 DTO 不得回流 tokenCount')
assertFunctionExcludes(externalSourceMappers, 'mapSourceListItem', 'activeTokenCount', '外部接入源轻量列表 DTO 不得回流 activeTokenCount')
const externalSourceTokenRepository = readSource('storage/external-integration-source-token.repository.ts')
assertFunctionExcludes(externalSourceTokenRepository, 'loadExternalIntegrationSourcePrimaryTokensBySourceIds', 'tokens.*', '外部接入源主 token 预览不得读取 token hash 或密文')
assertFunctionExcludes(externalSourceTokenRepository, 'loadExternalIntegrationSourcePrimaryTokensBySourceIdsAsync', 'tokens.*', '外部接入源异步主 token 预览不得读取 token hash 或密文')

const modelChecksRepository = readSource('storage/model-checks.repository.ts')
assertIncludes(modelChecksRepository, 'modelCheckRunListSelectColumns', '模型检查列表必须使用列表字段选择器')
assertIncludes(modelChecksRepository, 'includeSummaries: false', '模型检查列表不得返回 requestSummary/resultSummary 大摘要')

const operationLogRoutes = readSource('modules/operation-logs/operation-logs.routes.ts')
assertIncludes(operationLogRoutes, 'listOperationLogsAsync', '操作日志列表必须调用专用轻量 repository')
assertIncludes(operationLogRoutes, 'res.json(ok(result))', '操作日志列表必须直接返回 repository 构造的轻量 DTO')
const operationLogRepository = readSource('storage/operation-log-read.repository.ts')
assertFunctionExcludes(operationLogRepository, 'listOperationLogsWithFilters', 'changes_json', '操作日志列表查询不得读取 changes payload')
assertFunctionExcludes(operationLogRepository, 'listOperationLogsWithFilters', 'metadata_json', '操作日志列表查询不得读取 metadata payload')

const runtimeLogRepository = readSource('storage/runtime-log-query.repository.ts')
assertIncludes(runtimeLogRepository, 'includeRawJson: false', '运行日志列表不得返回 rawJson')
assertIncludes(runtimeLogRepository, 'runtimeLogDetailFromRow', '运行日志详情必须保留 rawJson 单独读取')

const authorizationRoutes = readSource('modules/authorizations/authorizations.routes.ts')
assertIncludes(authorizationRoutes, 'res.json(ok(result))', '授权路由必须直接返回 repository 构造的轻量列表 DTO')
assertIncludes(authorizationRoutes, "authorizationsRouter.get('/:id'", '授权编辑必须有单条详情接口支撑渐进式加载')

const authorizationRepository = readSource('storage/resource-authorization-read.repository.ts')
assertIncludes(authorizationRepository, 'resourceAuthorizationListSelectColumns', '授权分页列表必须使用窄字段投影')
assertIncludes(authorizationRepository, 'resourceAuthorizationListItemFromRow', '授权分页列表必须直接构造列表 DTO')
assertFunctionExcludes(authorizationRepository, 'resourceAuthorizationListItemFromRow', 'parseRequestQuotaLimitsJson', '授权列表 DTO 不得解析额度详情')
assertFunctionExcludes(authorizationRepository, 'resourceAuthorizationListItemFromRow', 'authorizationSources', '授权列表 DTO 不得先构造完整来源数组')

const authorizationActions = readSource('../frontend/src/views/authorizations/useAuthorizationActions.ts', false)
assertIncludes(authorizationActions, 'api.authorizations.detail', '授权编辑弹窗必须先加载管理侧单条详情')
assertIncludes(authorizationActions, 'api.myAuthorizations.detail', '授权编辑弹窗必须先加载用户侧单条详情')

console.log('管理接口渐进式列表契约回归通过：P1/P2 列表不回流详情字段，编辑/详情改为按需读取')

function readSource(path: string, backendPath = true): string {
  return readFileSync(backendPath ? resolve(root, path) : resolve(path), 'utf8')
}

function assertIncludes(source: string, expected: string, message: string): void {
  assert.ok(source.includes(expected), message)
}

function assertFunctionExcludes(source: string, functionName: string, unexpected: string, message: string): void {
  const body = functionBody(source, functionName)
  assert.ok(!body.includes(unexpected), message)
}

function functionBody(source: string, functionName: string): string {
  const start = source.indexOf(`function ${functionName}`)
  assert.notEqual(start, -1, `未找到函数 ${functionName}`)
  const bodyStart = source.indexOf('{', start)
  assert.notEqual(bodyStart, -1, `未找到函数 ${functionName} 的函数体`)
  let depth = 0
  for (let index = bodyStart; index < source.length; index += 1) {
    const char = source[index]
    if (char === '{') depth += 1
    if (char === '}') {
      depth -= 1
      if (depth === 0) {
        return source.slice(bodyStart, index + 1)
      }
    }
  }
  throw new Error(`未能截取函数 ${functionName}`)
}
