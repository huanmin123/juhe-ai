import { strict as assert } from 'node:assert'
import { readFileSync } from 'node:fs'
import { dirname, resolve } from 'node:path'
import { fileURLToPath } from 'node:url'

const backendRoot = resolve(dirname(fileURLToPath(import.meta.url)), '../../..')

const backgroundIpcSource = readSource('src/modules/background/background-ipc.ts')
const gatewayDbServiceSource = readSource('src/modules/gateway/runtime/gateway-db-service-request.ts')
const accountEffectsSource = readSource('src/modules/gateway/runtime/account-effects.ts')
const accountApiKeyEffectsSource = readSource('src/modules/gateway/runtime/account-api-key-effects.service.ts')
const accountSideEffectsSource = readSource('src/modules/gateway/runtime/account-side-effects.service.ts')

assert(
  gatewayDbServiceSource.includes('requestBackgroundWorkerDbService(operation, options)'),
  '网关 worker -> background DB service 请求必须透传完整 options，不能只传 timeoutMs'
)

assert.doesNotMatch(
  backgroundIpcSource,
  /requestDbService\(operation,\s*\{\s*(?:timeoutMs,\s*)?priority:\s*'low'\s*\}\)/,
  'background IPC 不能把所有 DB service 请求按来源身份强制降为 low'
)

assert(
  backgroundIpcSource.includes('dbServiceOperationAccessMode(operation)'),
  'background IPC 写副作用优先级必须按 DbServiceOperation access mode 派生，不能维护易漏的 operation 白名单'
)
assert.match(
  backgroundIpcSource,
  /accessMode === 'write' \|\| accessMode === 'maintenance' \? 'low' : undefined/,
  'background IPC write/maintenance 必须默认 low，read/runtime 必须保持默认优先级'
)

assert.match(
  accountEffectsSource,
  /type:\s*'mark_account_temporary_unavailable'[\s\S]*priority:\s*'low'/,
  '网关标记账号临时不可用副作用必须显式 low priority'
)
assert.match(
  accountEffectsSource,
  /type:\s*'clear_account_stream_failure_state'[\s\S]*priority:\s*'low'/,
  '网关清理账号流失败副作用必须显式 low priority'
)
assert.match(
  accountEffectsSource,
  /type:\s*'persist_openai_codex_usage_headers'[\s\S]*priority:\s*'low'/,
  'OpenAI Codex usage headers 持久化副作用必须显式 low priority'
)
assert.match(
  accountApiKeyEffectsSource,
  /type:\s*'record_account_api_key_failure'[\s\S]*priority:\s*'low'/,
  '账户内 API Key 失败副作用必须显式 low priority'
)
assert.match(
  accountApiKeyEffectsSource,
  /type:\s*'record_account_api_key_success'[\s\S]*priority:\s*'low'/,
  '账户内 API Key 成功副作用必须显式 low priority'
)
assert.match(
  accountSideEffectsSource,
  /requestGatewayDbService\(operation,\s*\{\s*priority:\s*'low'\s*\}\)/,
  '账号错误处理副作用队列落库必须显式 low priority'
)
assert.match(
  accountSideEffectsSource,
  /type:\s*'mark_account_precheck_temporary_unavailable'[\s\S]*priority:\s*'low'/,
  '账号 precheck 标记临时不可用副作用必须显式 low priority'
)
assert.match(
  accountSideEffectsSource,
  /type:\s*'find_account_for_test'[\s\S]*\{\s*timeoutMs:\s*10_000\s*\}/,
  '账号 precheck 读路径只能传 timeoutMs，不能因为 gateway/background 来源被写副作用策略拖慢'
)

console.log('网关/background 副作用优先级回归通过：读路径不强制 low，写副作用显式或兜底 low')

function readSource(relativePath: string): string {
  return readFileSync(resolve(backendRoot, relativePath), 'utf8')
}
