import { strict as assert } from 'node:assert'
import { readFileSync } from 'node:fs'
import { dirname, resolve } from 'node:path'
import { fileURLToPath } from 'node:url'

const backendRoot = resolve(dirname(fileURLToPath(import.meta.url)), '../../..')
const cooldownRetestSource = readSource('src/modules/background/account-api-key-cooldown-retest.service.ts')
const backgroundIpcSource = readSource('src/modules/background/background-ipc.ts')

assert(
  cooldownRetestSource.includes('requestBackgroundWorkerDbService'),
  '账户内 API Key 冷却复测写回必须通过 worker -> server -> DB service typed operation'
)
assert(
  !/\brecordAccountApiKeyRuntime(?:Failure|Success)\b/.test(cooldownRetestSource),
  '账户内 API Key 冷却复测不能直接调用业务库 runtime state repository 写回'
)
assert(
  backgroundIpcSource.includes('background_worker_db_service_request')
    && backgroundIpcSource.includes('background_worker_db_service_response')
    && backgroundIpcSource.includes('requestDbService(operation)'),
  'background IPC 必须保留 worker 到 DB service 的 typed operation 转发桥'
)
assert(
  backgroundIpcSource.includes('rejectedDbServiceRequestCount')
    && backgroundIpcSource.includes('timedOutDbServiceRequestCount')
    && backgroundIpcSource.includes('pendingBackgroundDbServiceRequests.size'),
  'background worker DB service pending/rejected/timeout 指标必须暴露到 getBackgroundWorkerState'
)

console.log('后台 DB service 写边界回归通过：账户内 API Key 冷却复测写回不再绕过 DB service 直写业务库')

function readSource(relativePath: string): string {
  return readFileSync(resolve(backendRoot, relativePath), 'utf8')
}
