import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'

const counterSource = readFileSync('src/modules/gateway/runtime/user-request-limit-counter.ts', 'utf8')
const coordinatorSource = readFileSync('src/modules/gateway/runtime/user-request-limit-coordinator.ts', 'utf8')
const preAuthSource = readFileSync('src/modules/gateway/request/pre-auth.ts', 'utf8')
const businessSchemaSource = readFileSync('src/storage/schema/business-schema.ts', 'utf8')

assert.doesNotMatch(counterSource, /\basync\b|\bawait\b|\bPromise\b/, '纯内存限流器禁止 async、await 和 Promise')
assert.doesNotMatch(counterSource, /redis-client|repository|database|db-service|usage-stats-helpers|fetch\s*\(/, '纯内存限流器禁止导入远程状态或存储依赖')
assert.match(coordinatorSource, /getRedisClient/, 'Redis 只能由后台协调器访问')
assert.match(coordinatorSource, /setInterval[\s\S]*unref/, '后台协调 timer 必须 unref')
assert.match(coordinatorSource, /HINCRBY[\s\S]*__total/, 'Redis 汇总必须按实例累计值差量推进单调总数')
assert.match(coordinatorSource, /HGET[\s\S]*field[\s\S]*delta/, 'Redis 重试必须读取实例上次累计值并只应用正差量')
assert.doesNotMatch(coordinatorSource, /HVALS/, 'Redis 后台同步不得按实例字段全量求和')
assert.match(coordinatorSource, /redisCommandTimeoutMs[\s\S]*withTimeout\(client\.eval/, 'Redis 后台同步必须有命令超时')
assert.match(coordinatorSource, /consecutiveFailures[\s\S]*nextSyncAttemptAtMs/, 'Redis 后台同步失败必须退避')
assert.match(coordinatorSource, /stopUserRequestLimitCoordinator[\s\S]*dirtyEntries/, '服务退出前必须尝试排空脏计数')
assert.match(businessSchemaSource, /PRAGMA table_info\(system_accounts\)[\s\S]*ALTER TABLE system_accounts ADD COLUMN request_limits_json TEXT/, 'SQLite 旧库必须幂等补齐用户请求限制列')
assert.match(preAuthSource, /userRequestLimitCounter\.consume\s*\(/, 'pre-auth 必须同步调用用户限流器')
assert.doesNotMatch(preAuthSource, /await\s+userRequestLimitCounter\.consume/, 'pre-auth 不得等待用户限流器')
assert.match(preAuthSource, /user_request_limit_exceeded/, '超限错误码必须稳定')
assert.match(preAuthSource, /请求数已达到 \$\{decision\.limit \?\? 0\} 次，请联系管理员提升额度。/, '超限中文提示必须包含限制值、次数单位和完整句号')
assert.match(preAuthSource, /gatewayErrorPayload\(message, 'rate_limit_exceeded', 'user_request_limit_exceeded'\)/, '超限响应必须保持 OpenAI 兼容错误结构')
assert.match(preAuthSource, /recordDroppedAuditCapture\([\s\S]*reason: 'user_request_limit_exceeded'/, '超限 429 必须写入丢弃请求审计')

console.log('user request limit hot path boundary regression passed')
