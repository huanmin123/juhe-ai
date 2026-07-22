import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'

const source = readFileSync(new URL('../../modules/gateway/runtime/account-circuit-redis-store.ts', import.meta.url), 'utf8')
const contractSource = readFileSync(new URL('../../modules/gateway/runtime/account-circuit-store.ts', import.meta.url), 'utf8')

assert.match(contractSource, /listDue\(nowMs: number, limit: number\): Promise<AccountCircuitState\[\]>/, 'Store Port 必须暴露共享 due 索引')
assert.match(contractSource, /size\(\): Promise<number>/, '跨进程 store size 必须是异步事实')
assert.match(source, /class RedisAccountCircuitStore implements AccountCircuitStore/, 'Redis adapter 必须实现统一 Store Port')
assert.match(source, /eval\(redisAccountCircuitTransitionScript/, '所有状态转换必须进入单次 Redis Lua')
assert.match(source, /local function validate_identity[\s\S]*generation[\s\S]*dispatchRevision/, 'Lua 必须原子校验 generation 与 dispatchRevision')
assert.match(source, /local function replayed[\s\S]*transitionId/, 'Lua 必须原子识别 transitionId 重放')
assert.match(source, /lease\['leaseId'\][\s\S]*lease_mismatch/, 'Lua 必须校验 matching leaseId')
assert.match(source, /leaseUntilMs[\s\S]*halfOpenOrigin[\s\S]*retryAtMs/, '租约过期必须保守恢复原活动 phase')
assert.match(source, /local function reserve_capacity[\s\S]*ZRANGE[\s\S]*closed_key[\s\S]*capacity_exhausted/, '容量回收只能选择 CLOSED 索引，活动电路满载时必须拒绝')
assert.match(source, /state\['phase'\] == 'CLOSED'[\s\S]*closedExpiresAtMs/, '只有 CLOSED tombstone 可以按 retention 过期')
assert.match(source, /ZADD[\s\S]*due_key/, '状态与 due 索引必须在同一 Lua 内维护')
assert.doesNotMatch(source, /catch\s*\([^)]*\)\s*\{[\s\S]{0,300}closedAccountCircuitState/, 'Redis 故障不得捕获后伪装 CLOSED')
assert.doesNotMatch(source, /runtimeStateDriver[\s\S]*MemoryAccountCircuitStore/, 'Redis adapter 不得静默回退本机 memory')

console.log('account-circuit-redis-boundary-regression passed')
