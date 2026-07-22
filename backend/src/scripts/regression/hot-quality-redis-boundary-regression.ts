import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'

const source = readFileSync(new URL('../../modules/gateway/runtime/hot-quality-redis-store.ts', import.meta.url), 'utf8')
const contractSource = readFileSync(new URL('../../modules/gateway/runtime/hot-quality-store.ts', import.meta.url), 'utf8')
const snapshotSource = readFileSync(new URL('../../modules/gateway/runtime/hot-quality-snapshot.ts', import.meta.url), 'utf8')

assert.match(contractSource, /recordTerminal\(input:\s*\{[\s\S]*scope: HotQualityScope/, '终态提交必须携带 scope 供 Redis 显式声明全部原子 KEYS')
assert.match(source, /class RedisHotQualityStore implements HotQualityStore/, 'Redis adapter 必须实现统一 HotQualityStore Port')
assert.match(source, /getRedisClient/, 'Redis adapter 必须复用统一 Redis client')
assert.match(source, /redisNamespacedKey/, '所有 Redis key 必须进入项目 namespace')
assert.match(source, /eval\(redisHotQualityMutationScript/, 'attempt 与 terminal 更新必须进入单次 mutation Lua')
assert.match(source, /operation == 'record_attempt'[\s\S]*redis\.call\('SET',[\s\S]*'PX'/, 'attempt identity 与分钟桶必须在同一 Lua 并带 PX TTL')
assert.match(source, /local terminal_owner = load_expiring\(terminal_outcome_key[\s\S]*terminal_outcome_conflict/, 'Lua 必须原子校验 terminalOutcomeId 所有者')
assert.match(source, /attempt\['terminal'\][\s\S]*terminal_conflict/, 'Lua 必须拒绝同 attempt 的第二个互斥终态')
assert.match(source, /attempt\['requestedScopeKey'\] ~= input\['requestedScopeKey'\][\s\S]*attempt_conflict/, 'Lua 必须拒绝终态跨派发 scope 串写')
assert.match(source, /local current_minute = math\.floor\(now_ms \/ 60000\)[\s\S]*buckets\[minute_key\]/, 'Lua 必须选择当前一分钟桶')
assert.match(source, /current_minute - 30[\s\S]*buckets\[bucket_key\] = nil/, 'Lua 必须清理 30 分钟窗口外桶')
assert.match(snapshotSource, /qualityAttempts = add\(add\(counters\.completedResponses, counters\.localTransportFailures\), counters\.explicitPolicyFailures\)/, '完成率分母只能使用三个互斥质量终态')
assert.match(source, /ZREMRANGEBYSCORE[\s\S]*ZCARD[\s\S]*key_capacity_exhausted/, '容量判断必须先清理到期 registry 再有界拒绝')
assert.match(source, /fallback_entry[\s\S]*degraded_to_protocol/, '新细分 key 容量满时只能退化到已有 unknown 桶')
assert.match(source, /redis\.call\('SET', terminal_outcome_key[\s\S]*'PX'/, 'terminalOutcomeId 幂等键必须至少一小时原生 TTL')
assert.doesNotMatch(source, /MemoryHotQualityStore|hot-quality-memory-store/, 'performance Redis adapter 不得导入或构造 memory fallback')
assert.doesNotMatch(source, /catch\s*\([^)]*\)\s*\{[\s\S]{0,400}(memory|fallback)/i, 'Redis 命令失败不得捕获后降级到本机状态')

console.log('hot-quality-redis-boundary-regression passed')
