import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'

const source = readFileSync(new URL('../../modules/gateway/runtime/same-tier-exploration-redis-store.ts', import.meta.url), 'utf8')
const runtimeSource = readFileSync(new URL('../../modules/gateway/runtime/hot-quality-runtime.service.ts', import.meta.url), 'utf8')

assert.match(source, /class RedisSameTierExplorationStore implements SameTierExplorationStore/, 'Redis adapter 必须实现统一 Store Port')
assert.match(source, /eval\(sameTierExplorationMutationScript/, 'credit、reservation 与 settlement 必须进入单次 Lua')
assert.match(source, /ZREMRANGEBYSCORE[\s\S]*ZCARD[\s\S]*capacity_exhausted/, 'Redis pool registry 必须原子清理 TTL 并限制容量')
assert.match(source, /decoded\['expiresAtMs'\][\s\S]*now_ms[\s\S]*if not state then/, 'Redis 必须同时尊重逻辑 expiresAtMs，避免自定义时钟下复活过期 pool')
assert.match(source, /not has_value\(state\['accruedTokens'\], token\)[\s\S]*while #\(state\['accruedTokens'\] or \{\}\) >= identity_capacity[\s\S]*table\.remove\(state\['accruedTokens'\], 1\)/, 'accrual token 必须使用有界滚动去重窗口，容量满后不得冻结探索')
assert.match(source, /state\['credit'\] = math\.min\(1,[\s\S]*\+ 0\.05\)/, 'credit 必须按 1\/20 原子累积')
assert.match(source, /#state\['reservations'\] > 0[\s\S]*status = 'pool_busy'/, 'peer-pool 必须以同一 Lua 保证 reservation 单飞')
assert.match(source, /has_value\(state\['settledReservationIds'\], reservation_id\)[\s\S]*status = 'reservation_conflict'/, '已过期或已结算 reservation ID 必须 fencing，禁止复用')
assert.match(source, /cooldown_count[\s\S]*identity_capacity[\s\S]*status = 'target_cooldown'/, 'cooldown identity map 必须有界，避免高基数撑大 pool 状态')
assert.match(source, /state\['credit'\] = math\.max\(0,[\s\S]*- 1\)/, '真实派发必须原子扣除 1 credit')
assert.match(source, /input\['outcome'\] == 'dispatched'[\s\S]*state\['cursor'\][\s\S]*cooldownUntilMsByRuntimeKey/, '成功派发必须原子推进 cursor 和 60 秒冷却')
assert.doesNotMatch(source, /catch\s*\([^)]*\)\s*\{[\s\S]{0,500}MemorySameTierExplorationStore/, 'Redis 故障不得静默回退本机 memory')
assert.match(runtimeSource, /runtimeMode === 'standalone'[\s\S]*MemorySameTierExplorationStore[\s\S]*RedisSameTierExplorationStore/, 'memory 只能用于 standalone，performance 必须装配 Redis')
assert.doesNotMatch(runtimeSource, /new RedisSameTierExplorationStore[\s\S]{0,500}catch[\s\S]{0,500}MemorySameTierExplorationStore/, 'performance Redis 错误不得回退 memory')
assert.match(source, /leaseUntilMs 必须晚于 nowMs/, 'adapter 边界必须拒绝创建即过期的 lease')

console.log('same-tier exploration Redis boundary regression passed')
