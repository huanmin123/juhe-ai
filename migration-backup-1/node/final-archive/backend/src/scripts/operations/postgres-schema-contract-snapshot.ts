import { createHash } from 'node:crypto'

import { NODE_POSTGRES_SCHEMA_CONTRACT_VERSION } from '../../storage/postgres-schema-owner-gate.js'
import { collectPostgresSchemaStatements } from '../../storage/postgres-schema.js'
import { CURRENT_RELEASE_SCHEMA_VERSION } from '../../shared/release-schema-version.js'

interface ContractStatement {
  schemaName: string
  source: string
  sqlSha256: string
}

interface SchemaContractSnapshot {
  schemaVersion: 1
  contractVersion: number
  releaseSchemaVersion: number
  schemaNames: string[]
  statementCount: number
  statements: ContractStatement[]
  digest: string
}

function stableJson(value: unknown): string {
  if (value === null || value === undefined) return 'null'
  if (Array.isArray(value)) return `[${value.map(stableJson).join(',')}]`
  if (typeof value === 'object') {
    const record = value as Record<string, unknown>
    return `{${Object.keys(record).sort().map((key) => `${JSON.stringify(key)}:${stableJson(record[key])}`).join(',')}}`
  }
  return JSON.stringify(value)
}

function sha256(value: string): string {
  return createHash('sha256').update(value).digest('hex')
}

function collectContract(): SchemaContractSnapshot {
  const statements = collectPostgresSchemaStatements().map((statement) => ({
    schemaName: statement.schemaName,
    source: statement.source,
    sqlSha256: sha256(statement.sql)
  }))
  const withoutDigest = {
    schemaVersion: 1 as const,
    contractVersion: NODE_POSTGRES_SCHEMA_CONTRACT_VERSION,
    releaseSchemaVersion: CURRENT_RELEASE_SCHEMA_VERSION,
    schemaNames: [...new Set(statements.map((statement) => statement.schemaName))],
    statementCount: statements.length,
    statements
  }
  return { ...withoutDigest, digest: sha256(stableJson(withoutDigest)) }
}

process.stdout.write(`${JSON.stringify(collectContract(), null, 2)}\n`)
