import { strict as assert } from 'node:assert'
import { readFileSync } from 'node:fs'

const routeSource = readFileSync(new URL('../../modules/accounts/account-traffic-migration.routes.ts', import.meta.url), 'utf8')
const repositoriesSource = readFileSync(new URL('../../storage/repositories.ts', import.meta.url), 'utf8')
const mutationRepositorySource = readFileSync(new URL('../../storage/account-runtime-mutation.repository.ts', import.meta.url), 'utf8')

assert(routeSource.includes('migrateAccountTrafficAsync'), '账户流量迁移路由必须使用 async repository')
assert(routeSource.includes('runLoggedOperationAsync'), '账户流量迁移路由必须使用 async 操作日志包裹')
assert(!routeSource.includes('runLoggedOperation('), '账户流量迁移路由不能重新引入同步操作日志包裹')
assert(routeSource.includes('migrateServerOpenAIAccountTrafficRuntime'), '账户流量迁移必须保留网关 server 运行态迁移')

assert(repositoriesSource.includes('migrateAccountTrafficAsync'), 'repositories 必须导出账户流量迁移 async 入口')
assert(mutationRepositorySource.includes('export async function migrateAccountTrafficAsync'), '账户运行态仓储必须提供流量迁移 async 入口')
assert(mutationRepositorySource.includes("runtimeConfig.databaseDriver !== 'postgres'"), '账户流量迁移 async 入口必须保留 SQLite standalone 分支')
assert(mutationRepositorySource.includes('accountRowForManageAsync'), 'PG 账户流量迁移必须使用 async 账号管理读取')
assert(mutationRepositorySource.includes('accountEnabledGroupIdForClientAsync'), 'PG 账户流量迁移必须使用 async 分组绑定读取')
assert(mutationRepositorySource.includes('migrateAuthorizedAccountBindingTrafficAsync'), 'PG 授权账户流量迁移必须使用 async 写入分支')

console.log('账户流量迁移 async query guard 通过')
