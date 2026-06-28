import { strict as assert } from 'node:assert'
import { readFileSync } from 'node:fs'

const routesSource = readFileSync(new URL('../../modules/external-integrations/external-integration-sources.routes.ts', import.meta.url), 'utf8')
const middlewareSource = readFileSync(new URL('../../modules/external-integrations/external-source-auth.middleware.ts', import.meta.url), 'utf8')
const repositorySource = readFileSync(new URL('../../storage/external-integration-source.repository.ts', import.meta.url), 'utf8')
const tokenRepositorySource = readFileSync(new URL('../../storage/external-integration-source-token.repository.ts', import.meta.url), 'utf8')
const authRepositorySource = readFileSync(new URL('../../storage/external-integration-source-auth.repository.ts', import.meta.url), 'utf8')

assert(routesSource.includes('listExternalIntegrationSourcesAsync'), '外部来源系统管理列表必须走 async repository')
assert(routesSource.includes('createExternalIntegrationSourceAuthorizationAsync'), '外部来源系统创建必须走 async repository')
assert(routesSource.includes('updateExternalIntegrationSourceAsync'), '外部来源系统更新必须走 async repository')
assert(routesSource.includes('deleteExternalIntegrationSourceAsync'), '外部来源系统删除必须走 async repository')
assert(routesSource.includes('createExternalIntegrationSourceTokenAsync'), '外部来源系统 token 创建必须走 async repository')
assert(routesSource.includes('findExternalIntegrationSourceTokenSecretAsync'), '外部来源系统 token 查看必须走 async repository')
assert(routesSource.includes('updateExternalIntegrationSourceTokenAsync'), '外部来源系统 token 更新必须走 async repository')
assert(routesSource.includes('recordOperationLogAsync'), '外部来源系统管理操作日志必须走 async 写入')
assert.doesNotMatch(routesSource, /import \{[^}]*\blistExternalIntegrationSources\b[^}]*\} from '..\/..\/storage\/external-integration-source\.repository\.js'/, '外部来源系统路由不能重新导入同步列表仓储')
assert.doesNotMatch(routesSource, /import \{[^}]*\brecordOperationLog\b[^}]*\} from '..\/operation-logs\/operation-log\.service\.js'/, '外部来源系统路由不能重新导入同步操作日志')

assert(middlewareSource.includes('validateExternalIntegrationSourceTokenAsync'), '外部来源系统鉴权 middleware 必须调用 async token 校验')
assert.doesNotMatch(middlewareSource, /\bvalidateExternalIntegrationSourceToken\(/, '外部来源系统鉴权 middleware 不应直接调用同步 token 校验')

assert(repositorySource.includes('createExternalIntegrationSourceTokenInClientAsync'), '外部来源系统创建授权必须在同一 PG transaction 内创建 source 和 token')
assert(tokenRepositorySource.includes('loadExternalIntegrationSourceTokensBySourceIdsAsync'), '外部来源系统 token summary 必须提供 async 批量读取')
assert(authRepositorySource.includes('touchExternalIntegrationSourceLastUsedAsync'), '外部来源系统 PG token 校验必须异步更新 last_used_at')

console.log('外部来源系统 async 边界回归通过：管理 CRUD、token 操作、鉴权和操作日志均固定 async 路径')
