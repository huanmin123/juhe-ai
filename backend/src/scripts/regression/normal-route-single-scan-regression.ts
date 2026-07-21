import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'

const source = readFileSync(new URL('../../modules/gateway/routing/normal-model-route.service.ts', import.meta.url), 'utf8')
const selectionCalls = source.match(/selectGatewayModelTargetGroup\(/g) ?? []
assert.equal(selectionCalls.length, 1, '普通多供应商路由必须只扫描一次 group/access/accounts/filter 候选')
assert.match(
  source,
  /candidatePriority:\s*\(candidate\)[\s\S]*mappingMatchedCount[\s\S]*providerCode/,
  '普通路由单次候选扫描必须同时保留账户映射优先和目录 provider 兜底，并在扫描结束后按优先级选择'
)
assert.match(source, /const routeSource = mappingTarget\.modelFilter\.mappingMatchedCount > 0/, '路由来源必须根据同一次候选扫描结果确定')

console.log('normal route single candidate scan regression passed')
