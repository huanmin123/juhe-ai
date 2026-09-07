import { strict as assert } from 'node:assert'
import { readFileSync } from 'node:fs'
import { fileURLToPath } from 'node:url'

const preparation = readFileSync(fileURLToPath(new URL('../../modules/gateway/dispatch/preparation.ts', import.meta.url)), 'utf8')
const upstream = readFileSync(fileURLToPath(new URL('../../modules/gateway/dispatch/upstream-dispatch.ts', import.meta.url)), 'utf8')

for (const source of [preparation, upstream]) {
  assert.match(source, /accountId: .*account\.id/, '候选观测必须包含账号 ID')
  assert.match(source, /accountName: .*account\.name/, '候选观测必须包含账号名称')
  assert.match(source, /priority: .*account\.priority/, '候选观测必须包含 priority')
  assert.match(source, /fallback: .*account\.fallbackEnabled/, '候选观测必须包含 fallback')
  assert.match(source, /super: .*account\.superPriorityEnabled/, '候选观测必须包含 super')
  assert.match(source, /modelRank/, '候选观测必须包含 modelRank')
}

assert.match(preparation, /candidateSnapshot/, '准备阶段必须记录候选快照')
assert.match(preparation, /candidateIndex/, '候选快照必须记录进入阶段时的顺序')
assert.match(preparation, /effectiveCandidateIndex/, '候选快照必须记录阶段后的有效顺序')
assert.match(preparation, /authorization_quota/, '准备阶段必须记录 quota 决策')
assert.match(preparation, /latency_degraded/, '准备阶段必须记录 latency 决策')
assert.match(preparation, /proxy_health/, '准备阶段必须记录 proxy 决策')
assert.match(preparation, /client_ip_avoidance/, '准备阶段必须记录 IP 决策')
assert.match(preparation, /client_source_avoidance/, '准备阶段必须记录 source 决策')
assert.match(preparation, /session_affinity_claim/, '准备阶段必须记录会改变最终顺序的会话粘连认领')
assert.match(upstream, /circuit_/, '上游必须记录 circuit 跳过')
assert.match(upstream, /capacity_limit/, '上游必须记录 capacity 跳过')
assert.match(upstream, /api_key_pool_unavailable/, '上游必须记录 API Key 池跳过')
assert.match(upstream, /first_byte_timeout/, '上游必须记录首字超时切换')
assert.match(upstream, /dispatch_attempt_started/, '上游必须记录实际进入派发尝试的账号')
assert.match(upstream, /decision: 'selected'/, '上游必须记录最终选中账号')

const preparationObserver = preparation.slice(
  preparation.indexOf('function gatewayCandidateLogFields'),
  preparation.indexOf('export async function prepareOpenAIGatewayDispatchAccounts')
)
const upstreamObserver = upstream.slice(
  upstream.indexOf('function logGatewayAccountDispatchDecision'),
  upstream.indexOf('export async function fetchFirstAvailableUpstream')
)
assert.ok(preparationObserver && upstreamObserver, '未找到候选观测字段构造器')
for (const block of [preparationObserver, upstreamObserver]) {
  assert.doesNotMatch(block, /credentials|apiKey|api_key|proxyUrl|proxy_url/i, '候选观测日志不得包含凭据、API key 或代理 URL')
}

console.log('gateway candidate decision observability regression passed')
