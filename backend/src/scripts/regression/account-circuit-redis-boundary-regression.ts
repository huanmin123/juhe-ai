import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'

const source = readFileSync(new URL('../../modules/gateway/runtime/account-circuit-redis-store.ts', import.meta.url), 'utf8')
const contractSource = readFileSync(new URL('../../modules/gateway/runtime/account-circuit-store.ts', import.meta.url), 'utf8')
const transitionSource = source.slice(
  source.indexOf('export const redisAccountCircuitTransitionScript'),
  source.indexOf('export const redisAccountCircuitEscalationScript')
)
const escalationSource = source.slice(
  source.indexOf('export const redisAccountCircuitEscalationScript'),
  source.indexOf('export const redisAccountCircuitClearEscalationScript')
)
const sizeSource = source.slice(
  source.indexOf('export const redisAccountCircuitSizeScript'),
  source.indexOf('export const redisAccountCircuitRestoreScript')
)
const restoreSource = source.slice(
  source.indexOf('export const redisAccountCircuitRestoreScript'),
  source.indexOf('export const redisAccountCircuitAccountRevisionScript')
)
const revisionSource = source.slice(
  source.indexOf('export const redisAccountCircuitAccountRevisionScript'),
  source.indexOf('function validateOperationPayload')
)

assert.match(contractSource, /listDue\(nowMs: number, limit: number\): Promise<AccountCircuitState\[\]>/, 'Store Port 必须暴露共享 due 索引')
assert.match(contractSource, /size\(\): Promise<number>/, '跨进程 store size 必须是异步事实')
assert.match(source, /class RedisAccountCircuitStore implements AccountCircuitStore/, 'Redis adapter 必须实现统一 Store Port')
assert.match(source, /eval\(redisAccountCircuitTransitionScript/, '所有状态转换必须进入单次 Redis Lua')
assert.match(source, /local function validate_identity[\s\S]*generation[\s\S]*dispatchRevision/, 'Lua 必须原子校验 generation 与 dispatchRevision')
assert.match(source, /local function replayed[\s\S]*transitionId/, 'Lua 必须原子识别 transitionId 重放')
assert.match(contractSource, /relatedStates\?: AccountCircuitState\[\]/, '多 scope 原子变更必须向持久化 bridge 返回实际关联状态')
assert.match(transitionSource, /hierarchy_transition_id[\s\S]*hierarchy:' \.\. action[\s\S]*remember\(child_entry, relationship_transition_id\)/, '父关闭解除 shadow 必须为每个子状态生成并记住独立 hierarchy transitionId')
assert.match(escalationSource, /hierarchy_transition_id[\s\S]*hierarchy:' \.\. action[\s\S]*remember\(entry, relationship_transition_id\)/, '父升级建立 shadow 必须为每个子状态生成并记住独立 hierarchy transitionId')
assert.match(escalationSource, /incident_ids\[index\] == current_child_incident_id[\s\S]*shadowedByIncidentId'\] == nil/, '父升级只能 shadow 当前 incident 且关系缺失的子状态')
assert.match(escalationSource, /merged_changed[\s\S]*transitionId'\] = input\['accountTransitionId'\][\s\S]*remember\(account_entry, input\['accountTransitionId'\]\)/, 'already_active 扩展 child 集必须推进父 transitionId 与 replay fence')
assert.match(restoreSource, /project_parent_relationship[\s\S]*dispatchRevision'\] == parent_state\['dispatchRevision'\][\s\S]*child_incident_ids\[index\] == current_child_incident_id/, '父关系重建必须按子 revision 与 incidentId fence')
assert.match(restoreSource, /shadowedByIncidentId'\] == nil[\s\S]*shadowedByIncidentId'\] = parent_state\['incidentId'\]/, '父关系重建只能填补缺失 shadow，不得覆盖更新关系')
assert.match(restoreSource, /hierarchy_transition_id[\s\S]*remember\(child_entry, relationship_transition_id\)[\s\S]*relatedStates = related_states/, '父关系重建修复必须生成可重放 transition 并返回 bridge 持久化')
assert.match(restoreSource, /child_state\['updatedAtMs'\][\s\S]*<= tonumber\(parent_state\['updatedAtMs'\]/, '旧父记录不得覆盖更新时间更新的独立子状态')
assert.match(source, /lease\['leaseId'\][\s\S]*lease_mismatch/, 'Lua 必须校验 matching leaseId')
assert.match(source, /leaseUntilMs[\s\S]*halfOpenOrigin[\s\S]*retryAtMs/, '租约过期必须保守恢复原活动 phase')
assert.match(source, /local function reserve_capacity[\s\S]*ZRANGE[\s\S]*closed_key[\s\S]*capacity_exhausted/, '容量回收只能选择 CLOSED 索引，活动电路满载时必须拒绝')
assert.match(source, /state\['phase'\] == 'CLOSED'[\s\S]*closedExpiresAtMs/, '只有 CLOSED tombstone 可以按 retention 过期')
assert.equal(
  (source.match(/ZRANGEBYSCORE[^\n]*LIMIT[^\n]*math\.min\(capacity, 256\)/g) ?? []).length,
  3,
  'transition、升级和 restore 的 tombstone 清理都必须限制为单次 256 条'
)
assert.match(sizeSource, /cleanup_limit[\s\S]*cleanup_limit > 256[\s\S]*ZRANGEBYSCORE[\s\S]*cleanup_limit/, 'size 的每次 Lua 清理也必须硬限制为 256 条')
assert.match(source, /const cleanupLimit = Math\.min\(this\.capacity, 256\)[\s\S]*maxPages[\s\S]*result\.processed! < cleanupLimit/, 'size 必须由客户端分多次小 Lua 精确清理，不能退化为单个长 Lua')
assert.doesNotMatch(transitionSource, /local function reserve_capacity\(\)[\s\S]{0,120}cleanup_closed\(\)/, 'transition 不得在入口和容量预留中重复执行 256 条清理')
assert.match(transitionSource, /local cleanup_count = cleanup_closed\(\)[\s\S]*cleanup_count >= math\.min\(capacity, 256\)[\s\S]*capacity_exhausted/, '清满单个窗口后必须保守返回容量不足，由后续调用继续推进')
assert.match(sizeSource, /state\['phase'\] == 'CLOSED'[\s\S]*ZADD[\s\S]*else[\s\S]*ZREM/, 'tombstone 清理必须校验真实 phase，陈旧 CLOSED 索引不得删除 active scope')
assert.match(source, /ZADD[\s\S]*due_key/, '状态与 due 索引必须在同一 Lua 内维护')
assert.match(source, /confirmationFailuresRequired[\s\S]*confirmationFailureCount[\s\S]*failureEvidenceKeys/, 'Redis 状态必须原子保存确认阈值、计数和 evidence')
assert.match(source, /if count >= required then return open\(entry\) end[\s\S]*return apply\(entry\)/, '未达确认阈值必须释放租约保持 SUSPECT，达到阈值才 OPEN')
assert.match(source, /operation == 'complete_confirmation'[\s\S]*local attempt = tonumber\(state\['backoffAttempt'\] or 0\) \+ 1[\s\S]*jittered_backoff\(backoffs\[index\]/, 'SUSPECT unknown 必须共享长退避阶梯，不能固定每 3 秒探测')
assert.match(source, /outcome'\] == 'transport_failure'[\s\S]*state\['backoffAttempt'\] = 0[\s\S]*if count >= required then return open\(entry\)/, '真实 confirmation 证据必须清除内部 unknown 退避轮次')
assert.match(source, /state\['phase'\] == 'SUSPECT' or state\['phase'\] == 'OPEN' or state\['phase'\] == 'RECOVERING'[\s\S]*retryAtMs/, '无租约 SUSPECT 必须按 retryAtMs 进入共享 due 索引')
assert.match(source, /redisAccountCircuitRestoreScript[\s\S]*state\['phase'\] == 'SUSPECT' or state\['phase'\] == 'OPEN' or state\['phase'\] == 'RECOVERING'[\s\S]*ZADD'[\s\S]*due_key/, 'control-plane 重建 SUSPECT 时必须同步恢复 Redis due 索引')
assert.match(source, /lease\['kind'\] == 'confirmation'[\s\S]*state\['lease'\] = nil[\s\S]*state\['retryAtMs'\] = now_ms/, '过期 confirmation lease 必须原子释放并立即允许后台接管')
assert.match(source, /operation == 'complete_confirmation'[\s\S]*framingCompleteDisposition'[\s\S]*close\(entry\)/, '后台 framing confirmation 必须支持原子清除 SUSPECT')
assert.match(source, /operation == 'acquire_confirmation'[\s\S]*expectedFailureEvidenceKey'[\s\S]*evidence\[#evidence\] ~= expected_evidence[\s\S]*state_mismatch/, '同请求多 Key 成功回收 SUSPECT 必须在 Redis Lua 租约 CAS 内校验最新 failure evidence')
assert.match(source, /redisAccountCircuitListDueScript[\s\S]*ZRANGEBYSCORE[\s\S]*HGET[\s\S]*ZREM[\s\S]*ZADD/, '旧 due 索引的校验与修复必须在同一 Lua 中完成')
assert.match(source, /scanChunkSize = Math\.min\(512[\s\S]*while \(scopeKeys\.length < normalizedLimit && scanned < this\.capacity\)/, 'Redis due 必须由客户端发起多个硬上限分页，单次 Lua 不得扫描全部容量')
assert.match(source, /local batch_size = math\.min\(scan_limit, 128\)[\s\S]*nextOffset = retained_offset/, '单次 due Lua 必须限制 128 条子批并返回续扫 offset')
assert.match(source, /capacitySaturated[\s\S]*capacityState[\s\S]*capacity_exhausted/, '容量不足必须留下共享哨兵并返回非 CLOSED 状态')
assert.match(source, /redisAccountCircuitRestoreScript[\s\S]*capacity = tonumber\(ARGV\[4\]\)[\s\S]*capacity_exhausted/, 'control-plane restore 也必须执行容量上限')
assert.match(source, /async restore\(rawState[\s\S]*assertAccountCircuitStateScopeKey\(state\)[\s\S]*redisAccountCircuitRestoreScript/, 'Redis restore 必须在写入 Lua 前拒绝 scope tuple 与 key 不一致')
assert.match(source, /while #evidence > required \+ 1|while #normalized_evidence > required \+ 1/, 'Redis evidence 必须有 N+1 上限')
assert.match(contractSource, /accountCircuitEscalationDistinctScopeThresholdDefault = 3/, '父级升级默认必须至少要求三个独立 protocol/model scope')
assert.match(source, /#scopes < tonumber\(input\['distinctScopeThreshold'\]\)/, 'Redis 父级升级必须只按显式独立 scope 阈值判断')
assert.doesNotMatch(source, /#scopes < 2 or failure_total < 3/, 'Redis 不得继续允许两个高累计失败 scope 升级父级')
assert.match(source, /local function close\(entry\)[\s\S]*state\['scope'\]\['kind'\] == 'account'[\s\S]*HDEL[\s\S]*escalation_key[\s\S]*accountRuntimeKey[\s\S]*return apply\(entry\)/, '父 account 关闭与旧升级证据清理必须在同一 Lua 内完成')
assert.match(source, /redisAccountCircuitClearEscalationScript[\s\S]*dispatch_revision[\s\S]*evidence\['dispatchRevision'\] ~= dispatch_revision then return 0 end[\s\S]*HDEL/, '普通 framing 清理必须在同一 Lua 内按 dispatch revision fence 升级证据')
assert.match(source, /family_prefix[\s\S]*matches_runtime_key/, '裸账户 ID revision 投影必须覆盖授权实例 runtime-key family')
assert.match(revisionSource, /HSCAN', states_key, states_cursor, 'COUNT', 128/, 'revision state 扫描必须使用固定窗口 HSCAN')
assert.doesNotMatch(revisionSource, /HSCAN', states_key, states_cursor, 'MATCH'/, 'state hash field 是编码 scope key，不得用 runtime key MATCH 过滤而漏掉 active scope')
assert.match(revisionSource, /states_cursor ~= 'done'[\s\S]*evidence_cursor ~= 'done'/, '两个 HSCAN cursor 必须独立完成，已完成的 hash 不得从 cursor 0 重新扫描')
assert.match(source, /stateCount[\s\S]*evidenceCount[\s\S]*seenCursorPairs[\s\S]*cursor 未前进/, 'revision 客户端分页必须按真实 hash 规模设保护并拒绝重复 cursor')
assert.match(source, /is_older_revision[\s\S]*current_number > incoming_number/, '迟到旧 numeric revision 不得回退较新运行态')
assert.match(source, /operation == 'replace_revision'[\s\S]*is_older_numeric_revision[\s\S]*stale_dispatch_revision/, '单 scope revision 替换也必须拒绝迟到旧版本')
assert.match(source, /operation == 'replace_revision'[\s\S]*dispatchRevision'\] == input\['dispatchRevision'[\s\S]*idempotent/, '同 revision 重复投影不得关闭活动电路')
assert.match(source, /redisAccountCircuitRestoreScript[\s\S]*existing_revision > incoming_revision[\s\S]*stale_dispatch_revision/, '重建/对账不得用旧 revision 覆盖较新运行态')
assert.doesNotMatch(source, /catch\s*\([^)]*\)\s*\{[\s\S]{0,300}closedAccountCircuitState/, 'Redis 故障不得捕获后伪装 CLOSED')
assert.doesNotMatch(source, /runtimeStateDriver[\s\S]*MemoryAccountCircuitStore/, 'Redis adapter 不得静默回退本机 memory')

console.log('account-circuit-redis-boundary-regression passed')
