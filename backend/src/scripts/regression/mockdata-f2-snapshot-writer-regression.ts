import { readdirSync, readFileSync } from 'node:fs'
import { join, resolve } from 'node:path'

const mockdataRoot = resolve(process.cwd(), 'src/scripts/maintenance/mockdata')
const storagePath = join(mockdataRoot, 'observability/storage.ts')
const forbiddenSnapshotTables = /\b(?:database|table)_storage_snapshots\b/g

for (const filePath of listTypeScriptFiles(mockdataRoot)) {
  const source = readFileSync(filePath, 'utf8')
  const matches = source.match(forbiddenSnapshotTables)
  if (matches) {
    throw new Error(`Node mockdata 不得引用 F2 snapshot 表：${filePath}`)
  }
}

const storageSource = readFileSync(storagePath, 'utf8')
if (/getStatsDatabase|statsDatabasePath|\b(?:INSERT|UPDATE|DELETE)\b/i.test(storageSource)) {
  throw new Error(`Node storage mockdata 不得读取 stats DB 或执行写 SQL：${storagePath}`)
}

console.log('mockdata F2 snapshot writer regression passed')

function listTypeScriptFiles(directory: string): string[] {
  const files: string[] = []
  for (const entry of readdirSync(directory, { withFileTypes: true })) {
    const path = join(directory, entry.name)
    if (entry.isDirectory()) {
      files.push(...listTypeScriptFiles(path))
    } else if (entry.isFile() && path.endsWith('.ts')) {
      files.push(path)
    }
  }
  return files
}
