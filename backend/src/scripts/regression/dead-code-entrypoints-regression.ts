import { strict as assert } from 'node:assert'
import { existsSync, readdirSync, readFileSync } from 'node:fs'
import { dirname, extname, relative, resolve } from 'node:path'
import { fileURLToPath } from 'node:url'

const backendRoot = resolve(dirname(fileURLToPath(import.meta.url)), '../../..')
const repoRoot = resolve(backendRoot, '..')
const productionSourceRoot = resolve(backendRoot, 'src')
const excludedSourceDirectories = new Set([
  'src/scripts/performance',
  'src/scripts/regression'
])
const searchableExtensions = new Set(['.cjs', '.js', '.json', '.mjs', '.ps1', '.ts', '.tsx'])

const dynamicEntrypoints = [
  'src/worker.ts',
  'src/db-service.ts',
  'src/temporary-maintenance-worker.ts',
  'src/modules/audit-logs/audit-log-transport-worker.ts',
  'src/storage/sqlite-read-worker.ts',
  'src/storage/usage-record-writer-worker.ts',
  'src/storage/codex-context-state-writer-worker.ts',
  'src/modules/gateway/request/json-worker.ts',
  'src/modules/model-checks/model-checks-token-worker.ts'
] as const

const searchableFiles = [
  ...collectProductionSourceFiles(productionSourceRoot),
  resolve(backendRoot, 'package.json'),
  resolve(repoRoot, 'package.json')
]
const missingReferences = dynamicEntrypoints.filter((entrypoint) => {
  const entrypointPath = resolve(backendRoot, entrypoint)
  return !existsSync(entrypointPath) || !hasReference(entrypoint, entrypointPath, searchableFiles)
})

assert.deepEqual(
  missingReferences,
  [],
  `动态入口缺少生产源码或正式 package / maintenance / smoke 入口引用：\n${missingReferences.map((entrypoint) => `- ${entrypoint}`).join('\n')}`
)

console.log(`动态入口引用回归通过：${dynamicEntrypoints.length} 个入口均保留生产或正式入口引用`)

function collectProductionSourceFiles(directory: string): string[] {
  const files: string[] = []
  for (const entry of readdirSync(directory, { withFileTypes: true })) {
    const absolutePath = resolve(directory, entry.name)
    const relativePath = normalizePath(relative(backendRoot, absolutePath))
    if (entry.isDirectory()) {
      if (!excludedSourceDirectories.has(relativePath)) {
        files.push(...collectProductionSourceFiles(absolutePath))
      }
      continue
    }
    if (entry.isFile() && searchableExtensions.has(extname(entry.name))) {
      files.push(absolutePath)
    }
  }
  return files
}

function hasReference(entrypoint: string, entrypointPath: string, files: string[]): boolean {
  const fileName = entrypoint.split('/').at(-1)
  assert(fileName, `动态入口路径无效：${entrypoint}`)
  const entrypointStem = fileName.replace(/\.ts$/, '')
  const entrypointPattern = new RegExp(`${escapeRegExp(entrypointStem)}\\.(?:ts|js)\\b`)
  return files.some((filePath) => filePath !== entrypointPath && entrypointPattern.test(readFileSync(filePath, 'utf8')))
}

function normalizePath(filePath: string): string {
  return filePath.replaceAll('\\', '/')
}

function escapeRegExp(value: string): string {
  return value.replace(/[.*+?^${}()|[\]\\]/g, '\\$&')
}
