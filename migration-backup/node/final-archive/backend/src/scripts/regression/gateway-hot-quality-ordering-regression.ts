import assert from 'node:assert/strict'

process.env.JUHE_AI_RUNTIME_MODE = 'standalone'
process.env.JUHE_AI_RUNTIME_STATE_DRIVER = 'memory'

const {
  getGatewayHotQualityRuntime,
  gatewayHotQualityRouteScopeKey,
  orderGatewayAccountsByHotQualityAsync,
  resetGatewayHotQualityRuntimeForTest,
  sameTierExplorationPoolKey
} = await import('../../modules/gateway/runtime/hot-quality-runtime.service.js')
const { gatewayAccountConfigurationTierKey } = await import('../../modules/gateway/routing/hot-quality-candidate-selection.js')

resetGatewayHotQualityRuntimeForTest()
const accounts = [account('account-a'), account('account-b'), account('account-c')]
const modelPriority = { rankByAccountId: new Map(accounts.map((item) => [item.id, 0])) }
const baseInput = {
  accounts,
  modelPriority,
  mode: 'cost_first' as const,
  systemAccountId: 'system-a',
  routeStrategyId: 'route-a',
  groupId: 'group-a',
  requestLane: 'text' as const,
  model: 'gpt-runtime-ordering',
  eligibleFirstPrimaryDispatch: true
}

for (let index = 1; index < 20; index += 1) {
  const ordered = await orderGatewayAccountsByHotQualityAsync({ ...baseInput, requestId: `request-${index}` })
  assert.equal(ordered.dispatchIntent, 'primary_service')
  assert.equal(ordered.selectedAccountId, 'account-a')
}
const twentieth = await orderGatewayAccountsByHotQualityAsync({ ...baseInput, requestId: 'request-20' })
assert.equal(twentieth.dispatchIntent, 'same_tier_exploration', '第 20 个合格真实请求应获得一个探索 assignment')
assert.equal(twentieth.selectedAccountId, 'account-b')
assert.ok(twentieth.explorationReservation)
await twentieth.settleExplorationAfterDispatch?.('not_dispatched')

const restored = await orderGatewayAccountsByHotQualityAsync({ ...baseInput, requestId: 'request-21' })
assert.equal(restored.dispatchIntent, 'same_tier_exploration', '未完成真实派发时 credit 应可立即重新 reservation')
assert.equal(restored.selectedAccountId, 'account-b')
await restored.settleExplorationAfterDispatch?.('dispatched')

const routeScopeKey = gatewayHotQualityRouteScopeKey({
  systemAccountId: 'system-a',
  routeStrategyId: 'route-a',
  groupId: 'group-a',
  protocolProfile: 'profile-openai-responses',
  requestLane: 'text'
})
const poolKey = sameTierExplorationPoolKey(routeScopeKey, gatewayAccountConfigurationTierKey({
  modelMatchRank: 0,
  fallbackEnabled: false,
  superPriorityEnabled: false,
  priority: 0
}))
const afterDispatch = await getGatewayHotQualityRuntime().explorationStore.get({ poolKey })
assert.equal(afterDispatch.credit, 0)
assert.equal(afterDispatch.cursor, 1)
assert.ok((afterDispatch.cooldownUntilMsByRuntimeKey['account-b'] ?? 0) > Date.now())

let nextExploration
for (let index = 22; index <= 41; index += 1) {
  nextExploration = await orderGatewayAccountsByHotQualityAsync({ ...baseInput, requestId: `request-${index}` })
}
assert.equal(nextExploration?.dispatchIntent, 'same_tier_exploration')
assert.equal(nextExploration?.selectedAccountId, 'account-c', 'cursor 和目标冷却必须把下一次探索公平推进到另一个 peer')
await nextExploration?.settleExplorationAfterDispatch?.('not_dispatched')

console.log('gateway hot quality ordering regression passed')

function account(id: string) {
  return {
    id,
    providerProtocolProfileId: 'profile-openai-responses',
    protocolCode: 'openai_v1',
    protocolVersion: 'responses',
    priority: 0,
    fallbackEnabled: false,
    superPriorityEnabled: false
  } as Parameters<typeof orderGatewayAccountsByHotQualityAsync>[0]['accounts'][number]
}
