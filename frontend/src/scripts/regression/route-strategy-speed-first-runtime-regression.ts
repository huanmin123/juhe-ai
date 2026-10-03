import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'
import { fileURLToPath } from 'node:url'

const viewSource = readFileSync(fileURLToPath(new URL('../../views/route-strategies/RouteStrategiesView.vue', import.meta.url)), 'utf8')
const drawerSource = readFileSync(fileURLToPath(new URL('../../views/route-strategies/SpeedFirstRuntimeDrawer.vue', import.meta.url)), 'utf8')
const apiSource = readFileSync(fileURLToPath(new URL('../../api/domains/routeStrategies.ts', import.meta.url)), 'utf8')
const scopedApiSource = readFileSync(fileURLToPath(new URL('../../composables/useScopedDomainApi.ts', import.meta.url)), 'utf8')
const typesSource = readFileSync(fileURLToPath(new URL('../../types/domain/access.ts', import.meta.url)), 'utf8')

assert.match(apiSource, /\/route-strategies\/\$\{id\}\/speed-first-runtime/, '管理作用域必须暴露速度运行态详情路径')
assert.match(apiSource, /\/my-route-strategies\/\$\{id\}\/speed-first-runtime/, '个人作用域必须暴露速度运行态详情路径')
assert.match(scopedApiSource, /speedFirstRuntime: \(id: string,[\s\S]*api\.routeStrategies\.speedFirstRuntime\(id, params, options\)[\s\S]*api\.myRouteStrategies\.speedFirstRuntime\(id, options\)/, '速度运行态详情必须沿用 scoped domain API')
assert.match(typesSource, /speedFirstLatencyRuntime\?: RouteStrategySpeedFirstLatencyRuntimeSummary/, '策略列表行必须声明速度运行态汇总')
assert.match(typesSource, /interface RouteStrategySpeedFirstLatencyRuntime[\s\S]*degradedCount: number[\s\S]*items: RouteStrategySpeedFirstLatencyRuntimeItem\[\]/, '速度运行态详情必须声明汇总和明细字段')
assert.match(
  typesSource,
  /interface RouteStrategySpeedFirstConfig[\s\S]*?totalTimeDeadlineSeconds\?: number[\s\S]*?compactionTotalTimeDeadlineSeconds\?: number/,
  '速度优先配置必须声明两个总时间截止可选字段（旧配置缺省回落默认）'
)
assert.match(
  typesSource,
  /interface RouteStrategySpeedFirstLatencyRuntimeItem[\s\S]*?dimension\?: 'first_byte' \| 'total_time'/,
  '速度运行态明细必须声明可选慢样本维度'
)

assert.match(viewSource, /速度降级 \$\{runtime\.degradedCount\} 个账号` : ''/, '列表必须显示速度降级账号数量，且正常态返回空文案')
assert.doesNotMatch(viewSource, /速度正常/, '列表正常态不得常驻展示速度正常文案')
assert.match(viewSource, /速度状态暂不可用/, '列表必须显示速度状态不可用文案')
assert.match(viewSource, /if \(isSpeedFirstRouteStrategy\(record\)\) \{[\s\S]*?key: 'speedRuntime', label: '查看速度状态'[\s\S]*?\}/, '查看速度状态入口必须位于行操作且只出现在速度优先策略')
assert.match(viewSource, /key === 'speedRuntime'[\s\S]*?openSpeedFirstRuntime\(record\)/, '行操作查看速度状态必须打开运行态详情')
assert.doesNotMatch(viewSource, /speed-first-runtime-link|speed-first-runtime-mobile-link/, '模式列与移动端卡片不得保留查看速度状态专用入口')
assert.match(viewSource, /const speedFirstRuntimeDrawerOpen = ref\(false\)/, '速度运行态详情 Drawer 默认必须关闭')
assert.match(viewSource, /function openSpeedFirstRuntime\([\s\S]*void loadSpeedFirstRuntime\(record\)/, '速度运行态详情必须由用户点击按需加载')
assert.match(viewSource, /@refresh="refreshSpeedFirstRuntime"/, '速度运行态详情必须支持显式刷新')
const mountedSource = viewSource.slice(viewSource.indexOf('onMounted(() => {'), viewSource.indexOf('async function loadRouteStrategies'))
assert.doesNotMatch(mountedSource, /speedFirstRuntime/, '策略列表初始化不得请求速度运行态详情')
const runtimeLoadSource = viewSource.slice(viewSource.indexOf('async function loadSpeedFirstRuntime'), viewSource.indexOf('function routeStrategyModeColor'))
assert.doesNotMatch(runtimeLoadSource, /setInterval|setTimeout/, '速度运行态详情不得引入自动轮询')
assert.match(viewSource, /function invalidateSpeedFirstRuntimeRequest\(\)[\s\S]*speedFirstRuntimeAbortController\?\.abort\(\)/, '关闭、作用域切换和卸载必须能中止速度运行态请求')
assert.match(viewSource, /onBeforeUnmount\(\(\)[\s\S]*invalidateSpeedFirstRuntimeRequest\(\)/, '页面卸载必须作废速度运行态请求')
assert.match(viewSource, /function resetSpeedFirstRuntimeForScopeChange\(\)[\s\S]*speedFirstRuntimeDrawerOpen\.value = false/, '切换作用域必须关闭并清空速度运行态详情')

for (const text of [
  '状态仅影响当前策略绑定分组下的候选排序',
  '账号仍可能作为故障回退或其他兜底候选被调度',
  '慢样本',
  '降级保留至',
  '下一次恢复探针',
  '真实请求恢复',
  '后台探针窗口',
  '速度优先未启用',
  '速度状态暂不可用'
]) {
  assert.match(drawerSource, new RegExp(text), `Drawer 必须展示或说明：${text}`)
}
assert.match(drawerSource, /:row-key="speedFirstRuntimeRowKey"/, 'Drawer 表格必须使用复合 row-key，避免同账号过渡期新旧运行态键重复')
assert.match(drawerSource, /function speedFirstRuntimeRowKey\([\s\S]*record\.accountId[\s\S]*record\.degradedUntil/, '复合 row-key 必须由账号身份和降级截止时间组成')
assert.match(drawerSource, /record\.recoverySuccessCount.*record\.requiredRecoverySuccessCount/, 'Drawer 必须展示真实请求恢复进度')
assert.match(drawerSource, /record\.recoveryProbeRoundSuccessCount.*record\.recoveryProbeRoundAttemptCount/, 'Drawer 必须展示后台探针窗口进度')
assert.match(drawerSource, /record\.reason/, 'Drawer 必须展示速度运行态原因')
assert.match(viewSource, /v-model:value="form\.normal\.speedFirstConfig\.totalTimeDeadlineSeconds"[\s\S]{0,200}:min="60"[\s\S]{0,200}:max="270"/, '请求总时间截止输入必须绑定表单且范围 60-270 秒')
assert.match(viewSource, /v-model:value="form\.normal\.speedFirstConfig\.compactionTotalTimeDeadlineSeconds"[\s\S]{0,200}:min="300"[\s\S]{0,200}:max="900"/, '压缩总时间截止输入必须绑定表单且范围 300-900 秒')
assert.match(viewSource, /totalTimeDeadlineSeconds: 120[\s\S]{0,200}compactionTotalTimeDeadlineSeconds: 300/, '速度优先表单默认值必须为请求 120 秒、压缩 300 秒')
assert.match(viewSource, /totalTimeDeadlineSeconds: config\?\.totalTimeDeadlineSeconds \?\? fallback\.totalTimeDeadlineSeconds/, '旧配置缺省请求总时间截止时表单必须回落默认值')
assert.match(viewSource, /compactionTotalTimeDeadlineSeconds: config\?\.compactionTotalTimeDeadlineSeconds \?\? fallback\.compactionTotalTimeDeadlineSeconds/, '旧配置缺省压缩总时间截止时表单必须回落默认值')
assert.match(viewSource, /totalTimeDeadlineSeconds: boundedInteger\(speedFirstConfig\.totalTimeDeadlineSeconds, 60, 270\)/, '提交体必须携带请求总时间截止并夹紧到 60-270 秒')
assert.match(viewSource, /compactionTotalTimeDeadlineSeconds: boundedInteger\(speedFirstConfig\.compactionTotalTimeDeadlineSeconds, 300, 900\)/, '提交体必须携带压缩总时间截止并夹紧到 300-900 秒')
assert.match(drawerSource, /record\.dimension === 'total_time'[\s\S]{0,200}总时间/, 'Drawer 必须按维度展示总时间标签')
assert.match(drawerSource, /record\.dimension === 'first_byte'[\s\S]{0,200}首字/, 'Drawer 必须按维度展示首字标签')

console.log('策略路由速度优先运行态前端回归通过：API 路径、异常态内联标记、行操作入口、按需加载、竞态作废和 Drawer 字段均已覆盖')
