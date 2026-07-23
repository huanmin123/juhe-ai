import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'
import { fileURLToPath } from 'node:url'

const viewSource = readFileSync(fileURLToPath(new URL('../../views/route-strategies/RouteStrategiesView.vue', import.meta.url)), 'utf8')
const apiSource = readFileSync(fileURLToPath(new URL('../../api/domains/routeStrategies.ts', import.meta.url)), 'utf8')
const typesSource = readFileSync(fileURLToPath(new URL('../../types/domain/access.ts', import.meta.url)), 'utf8')

assert.doesNotMatch(viewSource, /listSnapshot|routeStrategyListSnapshot|routeStrategySnapshotStatus|暂不可用|加载中/, '策略路由列表不得保留快照补发和动态占位')
assert.doesNotMatch(apiSource, /list-snapshot|listSnapshot/, '策略路由前端 API 不得保留独立列表快照入口')
assert.match(viewSource, /routeStrategiesApi\.list\(listParams\)/, '策略路由列表必须通过列表接口获取当前页完整数据')
assert.match(viewSource, /record\.bindingCount|record\.groupBindingPreview|record\.apiKeyCount/, '策略路由列表必须消费列表响应中的计数和预览')
assert.match(typesSource, /bindingCount: number[\s\S]*apiKeyCount: number[\s\S]*groupBindingPreview/, '策略路由列表类型必须声明完整动态字段')

console.log('策略路由列表单接口完整响应回归通过：计数和绑定预览随列表返回')
