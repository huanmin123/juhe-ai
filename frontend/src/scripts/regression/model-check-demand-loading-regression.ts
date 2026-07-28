import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'

const view = readFileSync(new URL('../../views/model-checks/ModelChecksView.vue', import.meta.url), 'utf8')
const accountOptions = readFileSync(new URL('../../views/model-checks/useModelCheckAccountOptions.ts', import.meta.url), 'utf8')
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
assert.match(view, /scheduleAccountModelOptionsAbortController\?\.abort\(\)/, '定时计划模型选项请求必须中止旧请求')

console.log('模型检测按需加载回归通过：首屏无质量策略预取，搜索防抖可中止，策略和计划使用差量 PATCH，计划保存局部更新')
