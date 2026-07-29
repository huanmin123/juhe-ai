import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'

import { createModelCheckDemandRequestCoordinator } from '../../views/model-checks/modelCheckDemandRequestCoordinator'

const view = readFileSync(new URL('../../views/model-checks/ModelChecksView.vue', import.meta.url), 'utf8')
const accountOptions = readFileSync(new URL('../../views/model-checks/useModelCheckAccountOptions.ts', import.meta.url), 'utf8')
const historyList = readFileSync(new URL('../../views/model-checks/ModelCheckRunHistoryList.vue', import.meta.url), 'utf8')
const api = readFileSync(new URL('../../api/domains/modelChecks.ts', import.meta.url), 'utf8')

function sourceBetween(source: string, start: string, end: string): string {
  const startIndex = source.indexOf(start)
  const endIndex = source.indexOf(end, startIndex + start.length)
  assert.notEqual(startIndex, -1, `缺少源码片段起点：${start}`)
  assert.notEqual(endIndex, -1, `缺少源码片段终点：${end}`)
  return source.slice(startIndex, endIndex)
}

const mounted = sourceBetween(view, 'onMounted(async () => {', 'onActivated(() => {')
assert.doesNotMatch(mounted, /loadQualityPolicy/, '模型检测首屏不得预加载质量策略')
assert.match(view, /@quality-policy-open="loadQualityPolicy"/, '质量策略只能在用户打开配置时加载')
assert.match(view, /let cachedModelCheckOptions:[\s\S]*async function loadOptions\(force = false\)[\s\S]*if \(!force && cachedModelCheckOptions\)/, '静态模型检测选项必须跨页面实例缓存，切换 owner 不得重复加载')
assert.match(view, /qualityPolicyPatch\(qualityPolicy\.value, input\)[\s\S]*Object\.keys\(patch\)\.length === 1/, '质量策略保存必须构造差量并拦截 no-op')
assert.match(api, /http\.patch\('\/model-checks\/quality-policy'/, '质量策略必须使用字段级 PATCH')
assert.match(api, /http\.patch\(`\/model-checks\/quality-schedules\/\$\{id\}`/, '定时计划编辑必须使用字段级 PATCH')

const saveSchedule = sourceBetween(view, 'async function saveSchedule', 'async function deleteSchedule')
assert.match(saveSchedule, /qualitySchedulePatch\(existing, input\)/, '定时计划编辑必须从列表行构造差量')
assert.match(saveSchedule, /patchQualitySchedule\(existing\.id, patch/, '定时计划编辑必须提交 PATCH')
assert.match(saveSchedule, /if \(index >= 0\) schedules\.value\.splice\(index, 1, saved\)[\s\S]*else if \(schedulesPage\.value === 1\)/, '定时计划编辑成功必须优先局部替换当前行')

for (const handler of ['handleTargetSearch', 'handleComparisonSearch', 'handleHistoryTargetSearch']) {
  const source = sourceBetween(accountOptions, `function ${handler}`, '\n  }')
  assert.match(source, /setTimeout\([\s\S]*250\)/, `${handler} 必须防抖远程搜索`)
}
assert.match(accountOptions, /targetAbortController\?\.abort\(\)/, '目标账户搜索必须中止旧请求')
assert.match(accountOptions, /comparisonAbortController\?\.abort\(\)/, '可信对比搜索必须中止旧请求')
assert.match(accountOptions, /historyAbortController\?\.abort\(\)/, '历史账户搜索必须中止旧请求')
assert.match(view, /function handleScheduleAccountSearch[\s\S]*setTimeout\([\s\S]*250\)/, '定时计划账户搜索必须防抖')
assert.match(view, /scheduleAccountOptionsAbortController\?\.abort\(\)/, '定时计划账户搜索必须中止旧请求')
assert.match(view, /purpose: 'schedule',[\s\S]*accountId: selectedId,[\s\S]*limit: 1/, '定时计划模型选项只能在展开模型下拉后按 accountId 定点加载')
assert.match(view, /scheduleAccountModelRequestCoordinator\.invalidate\(\)/, '定时计划模型选项必须中止失效上下文请求')
assert.match(accountOptions, /purpose: 'run',[\s\S]*accountId,[\s\S]*limit: 1/, '手动检测模型选项只能在展开模型下拉后按 accountId 定点加载')
assert.match(accountOptions, /targetModelRequestCoordinator\.run\(/, '手动检测模型选项必须复用 singleflight 协调器')
assert.match(historyList, /runs: ModelCheckRunListItem\[\]/, '历史列表必须只消费独立窄 DTO，不能复用详情摘要对象')

const coordinator = createModelCheckDemandRequestCoordinator()
let requestCount = 0
let capturedSignal: AbortSignal | undefined
let resolveRequest: ((value: string) => void) | undefined
const request = (signal: AbortSignal) => {
  requestCount += 1
  capturedSignal = signal
  return new Promise<string>((resolve) => { resolveRequest = resolve })
}
const first = coordinator.run('same-account', request)
const duplicate = coordinator.run('same-account', request)
assert.strictEqual(duplicate, first, '同一账户模型选项并发请求必须 singleflight 复用同一个 Promise')
assert.equal(requestCount, 1, '同一账户模型选项并发请求只能回源一次')
coordinator.invalidate()
assert.equal(capturedSignal?.aborted, true, '上下文失效必须中止在途模型选项请求')
resolveRequest?.('stale')
assert.equal(await first, undefined, '失效请求结果不得回写当前页面状态')
assert.equal(await coordinator.run('same-account', async () => 'fresh'), 'fresh', '失效后新上下文必须可以重新按需加载')

console.log('模型检测按需加载回归通过：首屏无策略预取，模型按 accountId+limit=1 定点加载且 singleflight，历史列表使用窄 DTO，策略和计划使用差量 PATCH')
