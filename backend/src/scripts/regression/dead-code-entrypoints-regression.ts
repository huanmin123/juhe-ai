import { strict as assert } from 'node:assert'
import { existsSync, readFileSync } from 'node:fs'
import { dirname, resolve } from 'node:path'
import { fileURLToPath } from 'node:url'
import ts from 'typescript'

interface DynamicEntrypointContract {
  entrypoint: string
  consumer: string
  resolveLiteral: string
}

const backendRoot = resolve(dirname(fileURLToPath(import.meta.url)), '../../..')
const dynamicEntrypointContracts: DynamicEntrypointContract[] = [
  {
    entrypoint: 'src/worker.ts',
    consumer: 'src/modules/background/background-worker-supervisor.ts',
    resolveLiteral: 'worker.ts'
  },
  {
    entrypoint: 'src/db-service.ts',
    consumer: 'src/modules/db-service/db-service-supervisor.ts',
    resolveLiteral: 'db-service.ts'
  },
  {
    entrypoint: 'src/temporary-maintenance-worker.ts',
    consumer: 'src/modules/record-maintenance/record-maintenance-queue.service.ts',
    resolveLiteral: 'temporary-maintenance-worker.ts'
  },
  {
    entrypoint: 'src/modules/audit-logs/audit-log-transport-worker.ts',
    consumer: 'src/modules/audit-logs/audit-log-transport.service.ts',
    resolveLiteral: 'audit-log-transport-worker.ts'
  },
  {
    entrypoint: 'src/storage/sqlite-read-worker.ts',
    consumer: 'src/storage/sqlite-read-worker-pool.ts',
    resolveLiteral: 'sqlite-read-worker.ts'
  },
  {
    entrypoint: 'src/storage/usage-record-writer-worker.ts',
    consumer: 'src/storage/usage-record-writer-pool.ts',
    resolveLiteral: 'usage-record-writer-worker.ts'
  },
  {
    entrypoint: 'src/storage/codex-context-state-writer-worker.ts',
    consumer: 'src/storage/codex-context-state-writer-pool.ts',
    resolveLiteral: 'codex-context-state-writer-worker.ts'
  },
  {
    entrypoint: 'src/modules/gateway/request/json-worker.ts',
    consumer: 'src/modules/gateway/request/json-parser.ts',
    resolveLiteral: 'json-worker.ts'
  },
  {
    entrypoint: 'src/modules/model-checks/model-checks-token-worker.ts',
    consumer: 'src/modules/model-checks/model-checks-token-worker.service.ts',
    resolveLiteral: 'model-checks-token-worker.ts'
  }
]

const retiredProductionFiles = [
  'src/modules/gateway/codex-responses/web-search-executor.ts',
  'src/shared/process-fatal.ts',
  'src/modules/stats/mock-background-runtime.ts',
  'src/modules/gateway/client-profiles/codex-switch-probe.ts',
  'src/modules/accounts/account-cleanup.service.ts',
  'src/modules/chat/chat-turn-initialization.ts',
  'src/modules/usage-semantics/types.ts',
  'src/storage/runtime/index.ts',
  'src/storage/runtime/postgres-redis-runtime.ts',
  'src/storage/runtime/sqlite-memory-runtime.ts',
  'src/storage/runtime/storage-runtime.ts'
]

const unexpectedlyPresentRetiredFiles = retiredProductionFiles.filter((filePath) => existsSync(resolve(backendRoot, filePath)))
assert.deepEqual(
  unexpectedlyPresentRetiredFiles,
  [],
  `已退役生产文件仍存在：\n${unexpectedlyPresentRetiredFiles.map((filePath) => `- ${filePath}`).join('\n')}`
)

const failures = dynamicEntrypointContracts.flatMap(validateEntrypointContract)

assert.deepEqual(
  failures,
  [],
  `动态入口生产引用契约缺失：\n${failures.map((failure) => `- ${failure}`).join('\n')}`
)

console.log(`动态入口引用回归通过：${dynamicEntrypointContracts.length} 个入口均由指定生产 consumer 的 resolve(...) AST 引用`)

function validateEntrypointContract(contract: DynamicEntrypointContract): string[] {
  const entrypointPath = resolve(backendRoot, contract.entrypoint)
  const consumerPath = resolve(backendRoot, contract.consumer)
  const contractLabel = `${contract.entrypoint} -> ${contract.consumer}`
  const failures: string[] = []

  if (!existsSync(entrypointPath)) {
    failures.push(`${contractLabel}：入口文件不存在`)
  }
  if (!existsSync(consumerPath)) {
    failures.push(`${contractLabel}：consumer 文件不存在`)
    return failures
  }
  if (!consumerResolvesLiteral(consumerPath, contract.resolveLiteral)) {
    failures.push(`${contractLabel}：未找到 resolve(..., '${contract.resolveLiteral}') AST 引用`)
  }
  return failures
}

function consumerResolvesLiteral(consumerPath: string, expectedLiteral: string): boolean {
  const source = readFileSync(consumerPath, 'utf8')
  const sourceFile = ts.createSourceFile(consumerPath, source, ts.ScriptTarget.ESNext, true, ts.ScriptKind.TS)
  let matched = false

  const visit = (node: ts.Node): void => {
    if (
      ts.isCallExpression(node)
      && ts.isIdentifier(node.expression)
      && node.expression.text === 'resolve'
      && node.arguments.some((argument) => (
        ts.isStringLiteralLike(argument)
        && normalizeResolveLiteral(argument.text) === expectedLiteral
      ))
    ) {
      matched = true
      return
    }
    ts.forEachChild(node, visit)
  }

  visit(sourceFile)
  return matched
}

function normalizeResolveLiteral(literal: string): string {
  return literal.startsWith('./') ? literal.slice(2) : literal
}
