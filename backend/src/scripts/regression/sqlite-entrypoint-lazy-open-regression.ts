import { strict as assert } from 'node:assert'
import { readFileSync } from 'node:fs'
import { dirname, resolve } from 'node:path'
import { fileURLToPath } from 'node:url'

const backendRoot = resolve(dirname(fileURLToPath(import.meta.url)), '../../..')

const entrypointFiles: Array<{ path: string; allowedCalls?: string[] }> = [
  { path: 'src/db-service.ts' },
  { path: 'src/worker.ts', allowedCalls: ['getDatasetDatabase'] },
  { path: 'src/modules/record-maintenance/temporary-maintenance-worker-runner.ts' }
]

for (const entrypoint of entrypointFiles) {
  const relativePath = entrypoint.path
  const content = readFileSync(resolve(backendRoot, relativePath), 'utf8')
  assertNoEagerDatabaseOpen(relativePath, content, new Set(entrypoint.allowedCalls ?? []))
}

console.log('SQLite 入口懒打开回归通过：DB service、后台 worker 和临时维护 worker 启动入口不再预热打开非 owner 业务库或统计库')

function assertNoEagerDatabaseOpen(relativePath: string, content: string, allowedCalls: Set<string>): void {
  const stripped = stripImports(content)
  for (const functionName of ['getBusinessDatabase', 'getDatasetDatabase', 'getStatsDatabase', 'getCodexContextStateShardDatabase']) {
    if (allowedCalls.has(functionName)) {
      continue
    }
    assert(
      !new RegExp(`\\b${functionName}\\s*\\(`).test(stripped),
      `${relativePath} 不能在启动入口直接调用 ${functionName}()；数据库连接必须由实际 job / repository 按需打开，避免多 worker 启动时抢写锁`
    )
  }
}

function stripImports(content: string): string {
  return content.replace(/^import[\s\S]*?from\s+['"][^'"]+['"]\s*$/gm, '')
}
