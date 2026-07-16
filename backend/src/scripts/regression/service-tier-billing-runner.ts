import { mkdtempSync, rmSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join } from 'node:path'

const dataRoot = mkdtempSync(join(tmpdir(), 'juhe-ai-service-tier-billing-'))
process.env.JUHE_AI_DATA_DIR = dataRoot
process.env.JUHE_AI_DATABASE_PATH = join(dataRoot, 'business.sqlite3')

try {
  await import('./service-tier-billing-regression.js')
} finally {
  const { closeStorageDatabases } = await import('../../storage/database.js')
  closeStorageDatabases()
  rmSync(dataRoot, { recursive: true, force: true })
}
