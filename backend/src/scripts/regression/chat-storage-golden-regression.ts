import assert from 'node:assert/strict'
import { createHash } from 'node:crypto'
import { readFileSync } from 'node:fs'
import { dirname, resolve } from 'node:path'
import { fileURLToPath } from 'node:url'

import {
  chatAssetGeneratedMaxBytes,
  chatAssetGeneratedQuotaMaxBytes,
  chatAssetOriginalMaxBytes,
  chatAssetPreviewMaxBytes,
  chatAssetProcessedMaxBytes,
  storageKeyForChatAsset
} from '../../storage/chat-asset-storage.js'
import {
  chatAssetUserMaxBytes,
  chatAssetUserMaxCount
} from '../../storage/chat-assets.repository.js'
import { chatAssistantStorageReservationBytes } from '../../storage/chat.repository.js'
import { collectPostgresSchemaStatements } from '../../storage/postgres-schema.js'

interface GoldenTableColumn {
  name: string
  definition: string
}

interface GoldenTable {
  name: string
  definition: string
  columns: GoldenTableColumn[]
  constraints: string[]
  partitionBy?: string
}

interface GoldenIndex {
  name: string
  table: string
  unique: boolean
  definition: string
}

const projectRoot = resolve(dirname(fileURLToPath(import.meta.url)), '../../../..')
const goldenPath = resolve(projectRoot, 'testdata/ai-chat-contract/v1/storage.json')
const sourcePaths = {
  chat: 'backend/src/storage/chat.repository.ts',
  context: 'backend/src/storage/chat-context.repository.ts',
  assets: 'backend/src/storage/chat-assets.repository.ts',
  imageGenerations: 'backend/src/storage/chat-image-generations.repository.ts',
  assetStorage: 'backend/src/storage/chat-asset-storage.ts'
} as const

const sources = Object.fromEntries(
  Object.entries(sourcePaths).map(([name, path]) => [name, readProjectFile(path)])
) as Record<keyof typeof sourcePaths, string>

const chatStatements = collectPostgresSchemaStatements()
  .filter((statement) => statement.schemaName === 'juhe_chat')
  .map((statement) => normalizeWhitespace(statement.sql))

const tables = chatStatements
  .filter((sql) => /^CREATE TABLE IF NOT EXISTS\b/i.test(sql))
  .map(parseTable)
  .sort(compareByName)

const indexes = chatStatements
  .filter((sql) => /^CREATE (?:UNIQUE )?INDEX IF NOT EXISTS\b/i.test(sql))
  .map(parseIndex)
  .sort(compareByName)

const enumCheckMap = new Map<string, { table: string; column: string; values: string[] }>()
for (const table of tables) {
  for (const constraint of table.constraints) {
    for (const match of constraint.matchAll(/\b([a-z_][a-z0-9_]*) IN \(([^)]+)\)/gi)) {
      const values = [...match[2].matchAll(/'((?:''|[^'])*)'/g)].map((item) => item[1].replace(/''/g, "'"))
      if (values.length === 0) continue
      const column = match[1].toLowerCase()
      const key = `${table.name}.${column}`
      const previous = enumCheckMap.get(key)
      assert.ok(!previous || JSON.stringify(previous.values) === JSON.stringify(values), `${key} enum 约束不一致`)
      enumCheckMap.set(key, { table: table.name, column, values })
    }
  }
}
const enumChecks = [...enumCheckMap.values()]
  .sort((left, right) => `${left.table}.${left.column}`.localeCompare(`${right.table}.${right.column}`))

const idempotencyTable = requireTable(tables, 'chat_message_idempotency')
const messagesTable = requireTable(tables, 'chat_messages')
const assetsTable = requireTable(tables, 'chat_assets')
const referencesTable = requireTable(tables, 'chat_asset_references')
const lineageTable = requireTable(tables, 'chat_image_generations')

const storageKeyFixture = {
  assetId: 'chat_asset_0123456789abcdef0123456789abcdef',
  sha256: '0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef',
  mimeType: 'image/png'
}

const actual = {
  contractVersion: 1,
  authority: {
    schema: 'backend/src/storage/postgres-schema.ts -> backend/src/storage/schema/chat-schema.ts',
    repositories: Object.values(sourcePaths)
  },
  normalizedFields: [
    'postgres.tables.columns',
    'postgres.tables.constraints',
    'postgres.enums',
    'postgres.indexes',
    'postgres.partitionBy',
    'repositories.exports',
    'repositories.normalizedSourceSha256',
    'semantics.cas',
    'semantics.idempotency',
    'semantics.relativeStorageKey',
    'semantics.originalAndPreview',
    'semantics.assetReferences',
    'semantics.imageLineage'
  ],
  postgres: {
    schema: 'juhe_chat',
    tableCount: tables.length,
    tables: tables.map((table) => ({
      name: table.name,
      columns: table.columns.map((column) => column.name).join(','),
      constraintCount: table.constraints.length,
      normalizedConstraintsSha256: sha256(table.constraints.join('\n')),
      normalizedDefinitionSha256: sha256(table.definition),
      ...(table.partitionBy ? { partitionBy: table.partitionBy } : {})
    })),
    enums: enumChecks,
    indexes: indexes.map((index) => ({
      name: index.name,
      table: index.table,
      unique: index.unique,
      normalizedDefinitionSha256: sha256(index.definition)
    })),
    messagePartition: {
      table: messagesTable.name,
      primaryKey: requireMatchingConstraint(messagesTable, /^PRIMARY KEY \(created_at, id\)$/),
      partitionBy: messagesTable.partitionBy,
      childNamePattern: 'chat_messages_YYYYMMDD',
      bounds: '[UTC day, next UTC day)'
    }
  },
  repositories: Object.entries(sourcePaths).map(([name, path]) => ({
    name,
    path,
    exports: exportedNames(sources[name as keyof typeof sourcePaths]).join(','),
    normalizedSourceSha256: normalizedSourceSha256(sources[name as keyof typeof sourcePaths])
  })),
  semantics: {
    idempotency: {
      table: idempotencyTable.name,
      primaryKey: requireMatchingConstraint(idempotencyTable, /^PRIMARY KEY \(conversation_id, client_message_id\)$/),
      lookupScope: requireSourcePattern(
        sources.chat,
        /WHERE conversation_id = \? AND client_message_id = \? AND system_account_id = \?/,
        'turn idempotency lookup scope'
      ),
      insertColumns: splitCommaList(requireSourcePattern(
        sources.chat,
        /INSERT INTO \$\{chatTable\(tx, 'chat_message_idempotency'\)\}\s*\(([\s\S]*?)\)\s*VALUES/,
        'turn idempotency insert columns',
        1
      )),
      duplicateResult: requireSourcePattern(sources.chat, /duplicate:\s*true/, 'turn duplicate result'),
      replacementCardinality: [
        requireSourcePattern(sources.chat, /deletedIdempotency\.changes !== 1/, 'replacement idempotency cardinality'),
        requireSourcePattern(sources.chat, /deletedMessages\.changes !== 2/, 'replacement message cardinality')
      ]
    },
    cas: {
      turn: {
        activeTurnPredicate: requireSourcePattern(
          sources.chat,
          /WHERE id = \? AND system_account_id = \? AND active_turn_id = \?/,
          'active turn CAS predicate'
        ),
        assistantStreamingPredicate: requireSourcePattern(
          sources.chat,
          /WHERE conversation_id = \? AND system_account_id = \? AND turn_id = \?\s+AND role = 'assistant' AND status = 'streaming'/,
          'assistant streaming CAS predicate'
        ),
        successfulChangeCount: 1
      },
      context: {
        states: extractStringUnion(sources.context, 'ChatContextState'),
        revisionPredicate: 'id = ? AND system_account_id = ? AND context_revision = ?',
        revisionPredicateOccurrences: countMatches(sources.context, /context_revision = \?/g),
        claimIdentityFields: [
          'context_claim_id',
          'context_claim_revision',
          'context_claim_through_sequence',
          'context_progress_sequence'
        ],
        installPredicate: requireSourcePattern(
          sources.context,
          /AND context_state = 'compacting' AND context_claim_id = \?\s+AND context_claim_revision = \? AND context_claim_through_sequence = \?/,
          'checkpoint install CAS predicate'
        ),
        successfulChangeCount: 1
      }
    },
    relativeStorageKey: {
      limits: {
        originalBytes: chatAssetOriginalMaxBytes,
        processedBytes: chatAssetProcessedMaxBytes,
        generatedBytes: chatAssetGeneratedMaxBytes,
        previewBytes: chatAssetPreviewMaxBytes,
        generatedQuotaBytes: chatAssetGeneratedQuotaMaxBytes
      },
      variants: ['original', 'preview'],
      examples: {
        original: storageKeyForChatAsset({ ...storageKeyFixture, variant: 'original' }),
        preview: storageKeyForChatAsset({ ...storageKeyFixture, variant: 'preview' })
      },
      separator: '/',
      maxLength: 512,
      rejects: [
        requireSourcePattern(sources.assetStorage, /normalized\.startsWith\('\/'\)/, 'absolute storage key guard'),
        requireSourcePattern(sources.assetStorage, /normalized\.includes\('\\0'\)/, 'NUL storage key guard'),
        requireSourcePattern(sources.assetStorage, /relativePath\.startsWith\('\.\.'\)/, 'storage root escape guard')
      ]
    },
    originalAndPreview: {
      table: assetsTable.name,
      originalColumns: columnNamesWithPrefix(assetsTable, 'original_'),
      processedColumns: [
        ...columnNamesWithPrefix(assetsTable, 'processed_'),
        'storage_key'
      ].sort(),
      previewColumns: columnNamesWithPrefix(assetsTable, 'preview_'),
      generatedObjectsMustDiffer: requireSourcePattern(
        sources.assets,
        /if \(storageKey === previewStorageKey\) throw new Error\('生成图片原图与 preview 不能共用 storage key'\)/,
        'generated original/preview key separation'
      ),
      generatedPreviewRequired: requireMatchingConstraint(assetsTable, /^CHECK \(source_kind != 'assistant_generated' OR preview_storage_key IS NOT NULL\)$/),
      sourceKinds: extractStringUnion(sources.assets, 'ChatAssetSourceKind'),
      originalMimeTypes: extractStringUnion(sources.assets, 'ChatAssetOriginalMimeType'),
      processedMimeTypes: extractStringUnion(sources.assets, 'ChatAssetProcessedMimeType'),
      processingStatuses: extractStringUnion(sources.assets, 'ChatAssetProcessingStatus'),
      observationStatuses: extractStringUnion(sources.assets, 'ChatAssetObservationStatus'),
      cleanupStatuses: extractStringUnion(sources.assets, 'ChatAssetCleanupStatus'),
      maxUserBytes: chatAssetUserMaxBytes,
      maxUserCount: chatAssetUserMaxCount
    },
    assetReferences: {
      table: referencesTable.name,
      kinds: extractStringUnion(sources.assets, 'ChatAssetReferenceKind'),
      uniqueContentSlot: requireMatchingConstraint(referencesTable, /^UNIQUE \(message_id, content_order\)$/),
      assetForeignKey: requireMatchingConstraint(
        referencesTable,
        /^FOREIGN KEY \(asset_id, conversation_id\) REFERENCES chat_assets\(id, conversation_id\) ON DELETE CASCADE$/
      ),
      insertConflictPolicy: requireSourcePattern(
        sources.assets,
        /ON CONFLICT \(message_id, content_order\) DO NOTHING/,
        'asset reference conflict policy'
      ),
      validityScope: ['system_account_id', 'conversation_id', "processing_status = 'ready'", "cleanup_status = 'active'", 'expires_at > now']
    },
    imageLineage: {
      table: lineageTable.name,
      operations: extractStringUnion(sources.imageGenerations, 'ChatImageGenerationOperation'),
      model: extractStringUnion(sources.chat, 'ChatImageModel'),
      sourceColumn: 'source_asset_ids_json',
      rootColumn: 'root_asset_id',
      maxSourceAssets: Number(requireSourcePattern(sources.imageGenerations, /const maxImageReferences = (\d+)/, 'lineage max references', 1)),
      generateRoot: requireSourcePattern(sources.imageGenerations, /let rootAssetId = assetId/, 'generate root lineage'),
      editRoot: requireSourcePattern(sources.imageGenerations, /rootAssetId = sourceRootAssetIds\[0\]!/, 'edit root lineage'),
      sourceForeignKey: requireMatchingConstraint(
        lineageTable,
        /^FOREIGN KEY \(asset_id, conversation_id\) REFERENCES chat_assets\(id, conversation_id\) ON DELETE CASCADE$/
      ),
      rootForeignKey: requireMatchingConstraint(
        lineageTable,
        /^FOREIGN KEY \(root_asset_id, conversation_id\) REFERENCES chat_assets\(id, conversation_id\) ON DELETE CASCADE$/
      ),
      sourceJsonArrayCheck: requireMatchingConstraint(
        lineageTable,
        /^CHECK \(jsonb_typeof\(source_asset_ids_json::jsonb\) = 'array'\)$/
      )
    },
    assistantStorageReservationBytes: chatAssistantStorageReservationBytes
  }
}

let golden: unknown
try {
  golden = JSON.parse(readFileSync(goldenPath, 'utf8'))
} catch (error) {
  if ((error as NodeJS.ErrnoException).code === 'ENOENT') {
    throw new Error(`AI Chat storage golden 缺失：${goldenPath}`)
  }
  throw error
}

assert.deepEqual(
  golden,
  actual,
  'AI Chat storage contract 已漂移；请先审查 Node PostgreSQL/schema/repository/asset 事实，禁止由本回归自动更新 golden'
)
assert.equal(tables.length, 10, '当前 AI Chat PostgreSQL storage contract 应固定为 10 张表')
assert.equal(actual.postgres.messagePartition.partitionBy, 'RANGE (created_at)')

console.log(`AI Chat storage golden 回归通过：${tables.length} tables, ${indexes.length} indexes`)

function readProjectFile(path: string): string {
  return readFileSync(resolve(projectRoot, path), 'utf8')
}

function normalizeWhitespace(value: string): string {
  let normalized = ''
  let quote: "'" | '"' | '`' | undefined
  let pendingSpace = false
  for (let index = 0; index < value.length; index += 1) {
    const character = value[index]
    if (quote) {
      normalized += character
      if (character === '\\' && quote !== "'" && index + 1 < value.length) {
        normalized += value[index + 1]
        index += 1
      } else if (character === quote) {
        if (quote === "'" && value[index + 1] === "'") {
          normalized += value[index + 1]
          index += 1
        } else {
          quote = undefined
        }
      }
      continue
    }
    if (character === "'" || character === '"' || character === '`') {
      if (pendingSpace && normalized) normalized += ' '
      pendingSpace = false
      quote = character
      normalized += character
      continue
    }
    if (/\s/.test(character)) {
      pendingSpace = true
      continue
    }
    if (pendingSpace && normalized) normalized += ' '
    pendingSpace = false
    normalized += character
  }
  return normalized.trim()
}

function normalizedSourceSha256(source: string): string {
  const normalized = source
    .replace(/\r\n?/g, '\n')
    .split('\n')
    .map((line) => line.trim())
    .filter(Boolean)
    .join('\n')
  return sha256(normalized)
}

function sha256(value: string): string {
  return createHash('sha256').update(value).digest('hex')
}

function exportedNames(source: string): string[] {
  return [...source.matchAll(/^export (?:async )?(?:class|const|function|interface|type) ([A-Za-z_$][A-Za-z0-9_$]*)/gm)]
    .map((match) => match[1])
    .sort()
}

function extractStringUnion(source: string, typeName: string): string[] {
  const match = new RegExp(`export type ${typeName} = ([^\\n]+)`).exec(source)
  assert.ok(match, `缺少 ${typeName} type union`)
  const values = [...match[1].matchAll(/'([^']+)'/g)].map((item) => item[1])
  assert.ok(values.length > 0, `${typeName} 未包含字符串成员`)
  return values
}

function parseTable(sql: string): GoldenTable {
  const nameMatch = /^CREATE TABLE IF NOT EXISTS ([a-z_][a-z0-9_]*)\s*\(/i.exec(sql)
  assert.ok(nameMatch, `无法解析 Chat table：${sql}`)
  const openIndex = sql.indexOf('(', nameMatch.index + nameMatch[0].length - 1)
  const closeIndex = matchingParenIndex(sql, openIndex)
  const body = sql.slice(openIndex + 1, closeIndex)
  const parts = splitTopLevel(body)
  const columns: GoldenTableColumn[] = []
  const constraints: string[] = []
  for (const part of parts) {
    if (/^(?:CONSTRAINT|PRIMARY KEY|FOREIGN KEY|UNIQUE|CHECK)\b/i.test(part)) {
      constraints.push(part)
      continue
    }
    const columnMatch = /^([a-z_][a-z0-9_]*)\s+(.+)$/i.exec(part)
    assert.ok(columnMatch, `无法解析 ${nameMatch[1]} column：${part}`)
    columns.push({ name: columnMatch[1].toLowerCase(), definition: part })
  }
  const suffix = sql.slice(closeIndex + 1).trim()
  const partitionMatch = /^PARTITION BY (.+)$/i.exec(suffix)
  return {
    name: nameMatch[1].toLowerCase(),
    definition: sql,
    columns,
    constraints,
    ...(partitionMatch ? { partitionBy: partitionMatch[1] } : {})
  }
}

function parseIndex(sql: string): GoldenIndex {
  const match = /^CREATE (UNIQUE )?INDEX IF NOT EXISTS ([a-z_][a-z0-9_]*) ON ([a-z_][a-z0-9_]*)/i.exec(sql)
  assert.ok(match, `无法解析 Chat index：${sql}`)
  return {
    name: match[2].toLowerCase(),
    table: match[3].toLowerCase(),
    unique: Boolean(match[1]),
    definition: sql
  }
}

function matchingParenIndex(value: string, openIndex: number): number {
  let depth = 0
  let inQuote = false
  for (let index = openIndex; index < value.length; index += 1) {
    const character = value[index]
    if (character === "'") {
      if (inQuote && value[index + 1] === "'") {
        index += 1
      } else {
        inQuote = !inQuote
      }
      continue
    }
    if (inQuote) continue
    if (character === '(') depth += 1
    if (character === ')') {
      depth -= 1
      if (depth === 0) return index
    }
  }
  throw new Error(`SQL 括号不匹配：${value}`)
}

function splitTopLevel(value: string): string[] {
  const parts: string[] = []
  let start = 0
  let depth = 0
  let inQuote = false
  for (let index = 0; index < value.length; index += 1) {
    const character = value[index]
    if (character === "'") {
      if (inQuote && value[index + 1] === "'") {
        index += 1
      } else {
        inQuote = !inQuote
      }
      continue
    }
    if (inQuote) continue
    if (character === '(') depth += 1
    if (character === ')') depth -= 1
    if (character === ',' && depth === 0) {
      parts.push(normalizeWhitespace(value.slice(start, index)))
      start = index + 1
    }
  }
  parts.push(normalizeWhitespace(value.slice(start)))
  return parts.filter(Boolean)
}

function splitCommaList(value: string): string[] {
  return value.split(',').map((item) => normalizeWhitespace(item)).filter(Boolean)
}

function requireTable(allTables: GoldenTable[], name: string): GoldenTable {
  const table = allTables.find((item) => item.name === name)
  assert.ok(table, `缺少 ${name} table`)
  return table
}

function requireMatchingConstraint(table: GoldenTable, pattern: RegExp): string {
  const constraint = table.constraints.find((item) => pattern.test(item))
  assert.ok(constraint, `${table.name} 缺少约束 ${pattern}`)
  return constraint
}

function requireSourcePattern(source: string, pattern: RegExp, label: string, group = 0): string {
  const match = pattern.exec(source)
  assert.ok(match, `缺少 ${label}`)
  assert.ok(match[group] !== undefined, `${label} capture group ${group} 缺失`)
  return normalizeWhitespace(match[group])
}

function columnNamesWithPrefix(table: GoldenTable, prefix: string): string[] {
  return table.columns.map((column) => column.name).filter((name) => name.startsWith(prefix)).sort()
}

function countMatches(source: string, pattern: RegExp): number {
  return [...source.matchAll(pattern)].length
}

function compareByName<T extends { name: string }>(left: T, right: T): number {
  return left.name.localeCompare(right.name)
}
