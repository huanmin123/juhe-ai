import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'

const recoverySource = readFileSync(new URL('../../modules/background/account-circuit-recovery.service.ts', import.meta.url), 'utf8')
const jobsSource = readFileSync(new URL('../../modules/background/background-jobs.ts', import.meta.url), 'utf8')
const redisSource = readFileSync(new URL('../../modules/gateway/runtime/account-circuit-redis-store.ts', import.meta.url), 'utf8')

assert.match(recoverySource, /store\.listDue\(/, '恢复 worker 必须消费 Store 的共享 due 索引')
assert.match(recoverySource, /store\.acquireCanaryLease\(/, '恢复 worker 必须先原子取得跨节点 canary lease')
assert.match(recoverySource, /store\.completeCanary\(/, '探针结果必须通过 Store fencing 原子提交')
assert.match(recoverySource, /target\.dispatchRevision !== dueState\.dispatchRevision[\s\S]*replaceDispatchRevision/, '探针前必须校验当前配置 revision')
assert.match(recoverySource, /outcome\.kind === 'framing_complete'[\s\S]*'framing_complete'/, '任意 framing 完整结果必须推进恢复')
assert.match(recoverySource, /outcome\.kind === 'transport_incomplete'[\s\S]*'transport_failure'/, 'transport 不完整必须重新打开电路')
assert.match(recoverySource, /return 'unknown'/, '取消或任务未知必须保守释放且不计失败')
assert.match(recoverySource, /AggregateError/, 'Store/Redis 或探针异常必须显式上报 scheduler')
assert.doesNotMatch(recoverySource, /MemoryAccountCircuitStore|catch[\s\S]{0,200}CLOSED/, '后台恢复不得静默回退本机状态或伪装 CLOSED')
assert.match(jobsSource, /backgroundScheduledJobName\('account-circuit-recovery'\)[\s\S]*runScheduledAccountCircuitRecovery/, 'ops worker 必须挂载账户电路恢复任务')
assert.match(redisSource, /operation == 'acquire_canary'[\s\S]*phase.*OPEN[\s\S]*RECOVERING/, 'Redis Lua 必须为 OPEN/RECOVERING 共用原子 lease')
assert.match(redisSource, /operation == 'complete_canary'[\s\S]*success_count >= 3[\s\S]*close\(entry\)/, 'Redis Lua 必须连续三次 canary 完整后关闭')

console.log('account-circuit-recovery-boundary-regression passed')
