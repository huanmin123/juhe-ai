import { existsSync, mkdirSync, readFileSync, writeFileSync } from 'node:fs'
import { dirname, resolve } from 'node:path'
import { fileURLToPath, pathToFileURL } from 'node:url'

import { getExternalPublicApiCatalog } from '../../backend/src/modules/external-integrations/external-public-api-catalog.js'

const scriptDirectory = dirname(fileURLToPath(import.meta.url))

export const EXTERNAL_PUBLIC_API_CATALOG_SNAPSHOT_PATH = resolve(
  scriptDirectory,
  '../internal/modules/publicapi/external_public_api_catalog.generated.json'
)

export function serializeExternalPublicApiCatalog(): string {
  return `${JSON.stringify(getExternalPublicApiCatalog(), null, 2)}\n`
}

if (isDirectExecution()) {
  try {
    run(process.argv.slice(2))
  } catch (error) {
    console.error(error instanceof Error ? error.message : String(error))
    process.exitCode = 1
  }
}

function run(args: string[]): void {
  const mode = parseMode(args)
  const snapshot = serializeExternalPublicApiCatalog()

  if (mode === 'check') {
    if (!existsSync(EXTERNAL_PUBLIC_API_CATALOG_SNAPSHOT_PATH)) {
      console.error(`external public API catalog snapshot is missing: ${EXTERNAL_PUBLIC_API_CATALOG_SNAPSHOT_PATH}`)
      process.exitCode = 1
      return
    }

    const committedSnapshot = readFileSync(EXTERNAL_PUBLIC_API_CATALOG_SNAPSHOT_PATH, 'utf8')
    if (committedSnapshot !== snapshot) {
      console.error(`external public API catalog snapshot drifted: ${EXTERNAL_PUBLIC_API_CATALOG_SNAPSHOT_PATH}`)
      process.exitCode = 1
      return
    }

    console.log(`external public API catalog snapshot is current: ${EXTERNAL_PUBLIC_API_CATALOG_SNAPSHOT_PATH}`)
    return
  }

  mkdirSync(dirname(EXTERNAL_PUBLIC_API_CATALOG_SNAPSHOT_PATH), { recursive: true })
  writeFileSync(EXTERNAL_PUBLIC_API_CATALOG_SNAPSHOT_PATH, snapshot, 'utf8')
  console.log(`wrote external public API catalog snapshot: ${EXTERNAL_PUBLIC_API_CATALOG_SNAPSHOT_PATH}`)
}

function parseMode(args: string[]): 'write' | 'check' {
  if (args.length !== 1 || (args[0] !== '--write' && args[0] !== '--check')) {
    throw new Error('usage: generate-external-public-api-catalog.ts (--write | --check)')
  }
  return args[0] === '--write' ? 'write' : 'check'
}

function isDirectExecution(): boolean {
  const entryPath = process.argv[1]
  return Boolean(entryPath) && import.meta.url === pathToFileURL(resolve(entryPath)).href
}
