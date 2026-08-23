import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'
import { resolve } from 'node:path'
import { fileURLToPath } from 'node:url'

const backendRoot = resolve(fileURLToPath(new URL('../..', import.meta.url)))
const read = (relativePath: string) => readFileSync(resolve(backendRoot, relativePath), 'utf8')

const sources = {
  types: read('modules/db-service/db-service-types.ts'),
  accessMode: read('modules/db-service/db-service-operation-access-mode.ts'),
  handlers: read('modules/db-service/db-service-handlers.ts'),
  repositories: read('storage/repositories.ts'),
  proxyRepository: read('storage/proxy.repository.ts')
}

for (const [name, source] of Object.entries(sources)) {
  assert.doesNotMatch(source, /update_proxy_test_state|updateProxyTestState|ProxyTestStateUpdateInput/, `Node J3a writer 必须从 ${name} 删除`)
}

console.log('proxy-latency-node-writer-removed-regression: PASS')
