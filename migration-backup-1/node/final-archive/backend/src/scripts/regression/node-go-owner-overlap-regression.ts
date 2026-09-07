import assert from 'node:assert/strict'
import { existsSync, readdirSync, readFileSync, statSync } from 'node:fs'
import { join, relative, resolve } from 'node:path'

type ManifestEntry = {
  originalPath?: unknown
  kind?: unknown
  sourceKind?: unknown
}

type MigrationManifest = {
  entries?: unknown
  files?: unknown
  originalFiles?: unknown
}

const repoRoot = resolve(process.cwd(), '..')
const backupRoot = join(repoRoot, 'migration-backup', 'node')
const activeBackendRoot = join(repoRoot, 'backend', 'src')
const activePackageJson = join(repoRoot, 'backend', 'package.json')

// These paths are deliberately retained because they are read-only consumers
// or one-way input/result adapters, not migrated task owners.
const retainedActivePaths = new Set([
  'backend/src/storage/table-monitor.repository.ts',
  'backend/src/modules/background/account-health-jobs-input-publisher.service.ts',
  'backend/src/storage/account-health-projection.repository.ts',
  'backend/src/modules/gateway/client-profiles/codex-turn-availability-probe.service.ts'
])

const forbiddenRuntimePatterns: Array<[string, RegExp]> = [
  ['J1 Node scheduler/health owner', /account-health-check\.service|cooldown-account-retest\.service|account-health-check\.repository|account-cooldown-retest\.repository|record_account_health_check_success|record_account_health_check_failure|record_cooldown_account_retest|list_accounts_due_for_health_check|list_accounts_due_for_cooldown_retest|background_worker_account_health_check_trigger/iu],
  ['J3a Node proxy-latency owner', /proxy-test\.service|proxy-latency-handover|proxy-latency-jobs-projector|proxy-latency-projection-cursor|runProxyLatencyTest/iu],
  ['F1 Node runtime-log owner', /startRuntimeLogFileImport|runtime-log-index-maintenance|runtime-log-index-scheduler|runtime-log-file-import\.service|runtime-log-index-retention/iu],
  ['F3/F4 Node log writer owner', /audit-log-transport-worker|audit-log-transport\.service|audit-log-queue\.service|operation-log-queue\.service|operation-log-write\.repository|operation-log-cleanup\.repository/iu]
]

function manifestEntries(manifest: MigrationManifest): ManifestEntry[] {
  const values = [manifest.entries, manifest.files, manifest.originalFiles]
    .find((value) => Array.isArray(value)) as unknown[] | undefined
  if (!values) return []
  return values.flatMap((value) => {
    if (typeof value === 'string') return [{ originalPath: value }]
    if (value && typeof value === 'object') return [value as ManifestEntry]
    return []
  })
}

function collectFiles(root: string, output: string[] = []): string[] {
  for (const name of readdirSync(root)) {
    const path = join(root, name)
    const stat = statSync(path)
    if (stat.isDirectory()) {
      if (name === 'migration-backup' || name === 'node_modules' || name === 'dist') continue
      collectFiles(path, output)
      continue
    }
    if (/\.(?:ts|tsx|js|mjs|json)$/iu.test(name)) output.push(path)
  }
  return output
}

assert(existsSync(backupRoot), `迁移归档目录不存在：${backupRoot}`)

let manifestCount = 0
let archivedPathCount = 0
const activeArchivedPaths: string[] = []
for (const feature of readdirSync(backupRoot)) {
  const featureRoot = join(backupRoot, feature)
  if (!statSync(featureRoot).isDirectory()) continue
  const manifestPath = join(featureRoot, 'manifest.json')
  if (!existsSync(manifestPath)) continue
  manifestCount += 1
  const manifest = JSON.parse(readFileSync(manifestPath, 'utf8')) as MigrationManifest
  for (const entry of manifestEntries(manifest)) {
    const rawPath = entry.originalPath
    if (typeof rawPath !== 'string' || !rawPath || rawPath.includes('*')) continue
    if (entry.kind === 'removed_source_fragment' || entry.sourceKind === 'removed_source_fragment') continue
    archivedPathCount += 1
    const normalized = rawPath.replaceAll('\\', '/')
    if (normalized.startsWith('migration-backup/')) continue
    if (!existsSync(join(repoRoot, ...normalized.split('/')))) continue
    if (retainedActivePaths.has(normalized)) {
      activeArchivedPaths.push(normalized)
      continue
    }
    assert.fail(`已归档 Node 源文件重新出现在活跃目录：${normalized}`)
  }
}

const scanFiles = [...collectFiles(activeBackendRoot), activePackageJson]
for (const file of scanFiles) {
  const relativePath = relative(repoRoot, file).replaceAll('\\', '/')
  // Regression scripts intentionally mention archived symbols in assertions;
  // only runtime/config source is considered an owner overlap here.
  if (relativePath.startsWith('backend/src/scripts/regression/')) continue
  const source = readFileSync(file, 'utf8')
  for (const [label, pattern] of forbiddenRuntimePatterns) {
    assert.doesNotMatch(source, pattern, `${label}仍出现在活跃 Node 文件：${relativePath}`)
  }
}

console.log(`node-go-owner-overlap-regression: PASS（检查 ${manifestCount} 个归档清单、${archivedPathCount} 个 Node 源文件；保留 ${activeArchivedPaths.length} 个只读/单向适配路径）`)
