import { strict as assert } from 'node:assert'
import { readFileSync } from 'node:fs'
import { resolve } from 'node:path'

const frontendRoot = resolve(import.meta.dirname, '../..')
const repositoryRoot = resolve(frontendRoot, '../..')
const viewSource = readFileSync(resolve(frontendRoot, 'views/ai-health/AiHealthView.vue'), 'utf8')
const statusBarSource = readFileSync(resolve(frontendRoot, 'views/ai-health/AiHealthStatusBar.vue'), 'utf8')
const routerSource = readFileSync(resolve(frontendRoot, 'router/index.ts'), 'utf8')
const statsApiSource = readFileSync(resolve(frontendRoot, 'api/domains/stats.ts'), 'utf8')
const monitorRepositorySource = readFileSync(resolve(repositoryRoot, 'backend/src/storage/account-health-monitor.repository.ts'), 'utf8')

assert.match(routerSource, /path: '\/ai-health'[\s\S]+title: 'AI健康监控'/, '管理菜单必须注册 AI 健康监控')
assert.match(routerSource, /path: '\/my-ai-health'[\s\S]+viewScope: 'self'/, '用户菜单必须注册自助健康监控')
assert.match(statsApiSource, /http\.get\('\/stats\/ai-health'/, '管理视图必须调用管理健康接口')
assert.match(statsApiSource, /http\.get\('\/my-stats\/ai-health'/, '用户视图必须调用自助健康接口')
assert.match(viewSource, /ResponsiveListToolbar/, '页面必须复用现有列表工具栏')
assert.match(viewSource, /search-placeholder="搜索账户名"/, '页面必须支持按账户名搜索')
assert.match(viewSource, /<a-pagination/, '页面必须使用分页组件')
assert.doesNotMatch(viewSource, /<a-table|ResponsiveDataList/, '健康监控必须使用列表而不是表格')
assert.match(statusBarSource, /<canvas/, '小时状态条必须使用 Canvas 控制一个月视图的 DOM 数量')
assert.doesNotMatch(statusBarSource, /v-for=.*hours/, '小时点不能展开为 744 个 DOM 节点')
assert.doesNotMatch(monitorRepositorySource, /\busage_records\b/i, '页面查询不得扫描使用记录明细')
assert.match(monitorRepositorySource, /31 \* 24/, '服务端必须限制最大 31 天')
assert.match(monitorRepositorySource, /FROM account_health_hourly/, '页面查询必须读取小时预聚合')

console.log('AI 健康监控前端契约回归通过')
