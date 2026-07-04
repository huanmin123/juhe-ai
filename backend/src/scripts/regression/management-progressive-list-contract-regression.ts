import { strict as assert } from 'node:assert'
import { readFileSync } from 'node:fs'
import { resolve } from 'node:path'

const root = resolve('src')

const groupsRoutes = readSource('modules/groups/groups.routes.ts')
assertIncludes(groupsRoutes, 'const { accountIds, authorizationSources, ...item } = group', '分组列表 DTO 必须剥离 accountIds 和 authorizationSources')
assertIncludes(groupsRoutes, 'authorizationSourceSummary', '分组列表必须用授权来源摘要替代完整来源数组')
assertIncludes(groupsRoutes, "groupsRouter.get('/:id'", '分组编辑必须有单条详情接口支撑渐进式加载')

const systemTeamRepository = readSource('storage/system-team.repository.ts')
assertIncludes(systemTeamRepository, 'systemTeamListItemFromRow', '团队列表必须使用不含 members 的轻量映射')
assertIncludes(systemTeamRepository, 'listSystemTeamMemberCountsForTeamIds', '团队列表必须使用成员计数批量查询')
assertFunctionExcludes(systemTeamRepository, 'listSystemTeamsPage', 'listSystemTeamMembersForTeamIds', '团队分页列表不得加载完整 members')
assertFunctionExcludes(systemTeamRepository, 'listSystemTeamsPageAsync', 'listSystemTeamMembersForTeamIdsAsync', '团队异步分页列表不得加载完整 members')

const externalSourceRepository = readSource('storage/external-integration-source.repository.ts')
assertIncludes(externalSourceRepository, 'mapSourceListItem', '外部接入源列表必须使用轻量映射')
assertIncludes(externalSourceRepository, 'loadExternalIntegrationSourcePrimaryTokensBySourceIds', '外部接入源列表只能加载主 token 预览')
assertFunctionExcludes(externalSourceRepository, 'listExternalIntegrationSources', 'loadExternalIntegrationSourceTokensBySourceIds(', '外部接入源列表不得加载完整 tokens')
assertFunctionExcludes(externalSourceRepository, 'listExternalIntegrationSourcesAsync', 'loadExternalIntegrationSourceTokensBySourceIdsAsync(', '外部接入源异步列表不得加载完整 tokens')

const modelChecksRepository = readSource('storage/model-checks.repository.ts')
assertIncludes(modelChecksRepository, 'modelCheckRunListSelectColumns', '模型检查列表必须使用列表字段选择器')
assertIncludes(modelChecksRepository, 'includeSummaries: false', '模型检查列表不得返回 requestSummary/resultSummary 大摘要')

const operationLogRoutes = readSource('modules/operation-logs/operation-logs.routes.ts')
assertIncludes(operationLogRoutes, 'toOperationLogListResponse', '操作日志列表必须经过轻量 DTO 映射')
assertIncludes(operationLogRoutes, '({ changes, metadata, userAgent, ...item }) => item', '操作日志列表必须剥离 changes、metadata 和 userAgent')

const runtimeLogRepository = readSource('storage/runtime-logs.repository.ts')
assertIncludes(runtimeLogRepository, 'includeRawJson: false', '运行日志列表不得返回 rawJson')
assertIncludes(runtimeLogRepository, 'runtimeLogDetailFromRow', '运行日志详情必须保留 rawJson 单独读取')

const authorizationRoutes = readSource('modules/authorizations/authorizations.routes.ts')
assertIncludes(authorizationRoutes, 'toAuthorizationListResponse', '授权列表必须经过轻量 DTO 映射')
assertIncludes(authorizationRoutes, 'sourceSummary: summarizeAuthorizationSources(authorizationSources)', '授权列表必须用来源摘要替代完整来源数组')
for (const field of [
  'authorizationSources',
  'limits',
  'resourceAccountExpiresAt',
  'usage',
  'usageBySystemAccount',
  'usageRange'
]) {
  assertIncludes(authorizationRoutes, `'${field}'`, `授权列表轻量 DTO 必须剥离 ${field}`)
}
assertIncludes(authorizationRoutes, "authorizationsRouter.get('/:id'", '授权编辑必须有单条详情接口支撑渐进式加载')

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
