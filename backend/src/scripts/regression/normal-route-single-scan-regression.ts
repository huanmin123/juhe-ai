import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'

import {
  normalGatewayModelRouteSource,
  normalGatewayModelTargetPriority
} from '../../modules/gateway/routing/normal-model-route.service.js'

const source = readFileSync(new URL('../../modules/gateway/routing/normal-model-route.service.ts', import.meta.url), 'utf8')
const selectionCalls = source.match(/selectGatewayModelTargetGroup\(/g) ?? []
assert.equal(selectionCalls.length, 1, '普通多供应商路由必须只扫描一次 group/access/accounts/filter 候选')
assert.match(
  source,
  /candidatePriority:\s*\(candidate\)\s*=>\s*normalGatewayModelTargetPriority/,
  '普通路由单次候选扫描必须复用统一候选优先级'
)
assert.equal(normalGatewayModelTargetPriority({ directMatchedCount: 1, mappingMatchedCount: 1 }, false), 2, '含真实直连的分组必须优先于映射分组')
assert.equal(normalGatewayModelTargetPriority({ directMatchedCount: 0, mappingMatchedCount: 1 }, true), 1, '纯映射分组应排在目录兜底之后的独立映射层级')
assert.equal(normalGatewayModelTargetPriority({ directMatchedCount: 0, mappingMatchedCount: 0 }, true), 0, '目录 provider 只作为无模型命中的兜底')
assert.equal(normalGatewayModelTargetPriority({ directMatchedCount: 0, mappingMatchedCount: 0 }, false), Number.NEGATIVE_INFINITY)
assert.equal(normalGatewayModelRouteSource({ directMatchedCount: 1 }), 'catalog_provider', '最终组内存在真实直连时应标记目录直连来源')
assert.equal(normalGatewayModelRouteSource({ directMatchedCount: 0 }), 'account_mapping', '最终组内只有映射时应标记账户映射来源')

console.log('normal route single candidate scan regression passed')
