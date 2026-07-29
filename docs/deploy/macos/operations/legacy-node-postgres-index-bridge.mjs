#!/usr/bin/env node

import { createHash } from 'node:crypto'
import { readFile } from 'node:fs/promises'
import { createRequire } from 'node:module'
import { dirname, resolve } from 'node:path'
import { fileURLToPath } from 'node:url'

const scriptDirectory = dirname(fileURLToPath(import.meta.url))
const catalogPath = resolve(scriptDirectory, 'legacy-node-postgres-index-bridge.catalog.json')
const backendRequire = createRequire(resolve(scriptDirectory, '../../../../backend/package.json'))
const catalogFingerprint = '725c665427bdf55cad9e83398c323ba3bb77ed1b29f3b9467cfe9776bc67fcec'
const advisoryLockKey = 'juhe-ai:legacy-node-postgres-index-bridge:v1'
const actions = new Set(['inspect', 'apply', 'verify', 'cleanup-invalid'])

class BridgeError extends Error {
  constructor(code) {
    super(code)
    this.code = code
  }
}

let options = { action: 'inspect' }

try {
  options = parseOptions(process.argv.slice(2))
  const catalog = await loadCatalog()
  if ((options.action === 'apply' || options.action === 'cleanup-invalid') && options.catalogFingerprint !== catalogFingerprint) {
    throw new BridgeError('catalog_confirmation_required')
  }
  const connectionString = process.env[options.postgresUrlEnv]
  if (!connectionString) throw new BridgeError('missing_connection')

  const { Client } = backendRequire('pg')
  const client = new Client({ connectionString, application_name: 'juhe-ai-legacy-node-index-bridge' })
  await client.connect()
  try {
    await configureSession(client)
    const identity = await verifyIdentity(client, options.database)
    await assertNoGooseLedger(client)
    let report
    if (options.action === 'inspect' || options.action === 'verify') {
      report = await inspectCatalog(client, catalog, identity.currentUser)
      if (options.action === 'verify' && report.indexes.some((index) => !isVerifiedState(index.state))) {
        throw new BridgeError('verification_failed')
      }
    } else {
      await acquireAdvisoryLock(client)
      try {
        report = options.action === 'apply'
          ? await applyCatalog(client, catalog, identity.currentUser)
          : await cleanupInvalidIndex(client, catalog, identity.currentUser, options.index)
      } finally {
        await releaseAdvisoryLock(client)
      }
    }
    print({ ok: true, action: options.action, database: identity.database, indexes: report.indexes })
  } finally {
    await client.end()
  }
} catch (error) {
  const errorCode = error && typeof error === 'object' && typeof error.code === 'string'
    ? error.code
    : 'operation_failed'
  print({ ok: false, action: options.action, error: errorCode })
  process.exitCode = 1
}

function parseOptions(args) {
  const options = { action: 'inspect', postgresUrlEnv: 'JUHE_AI_POSTGRES_URL', database: undefined, index: undefined, catalogFingerprint: undefined }
  for (let index = 0; index < args.length; index += 1) {
    const argument = args[index]
    if (argument === '--action') options.action = requiredOptionValue(args, ++index, argument)
    else if (argument === '--postgres-url-env') options.postgresUrlEnv = requiredOptionValue(args, ++index, argument)
    else if (argument === '--database') options.database = requiredOptionValue(args, ++index, argument)
    else if (argument === '--index') options.index = requiredOptionValue(args, ++index, argument)
    else if (argument === '--catalog-fingerprint') options.catalogFingerprint = requiredOptionValue(args, ++index, argument)
    else throw new BridgeError('invalid_arguments')
  }
  if (!actions.has(options.action) || !identifier(options.postgresUrlEnv) || !identifier(options.database ?? '')) {
    throw new BridgeError('invalid_arguments')
  }
  if (options.action === 'cleanup-invalid' && !identifier(options.index ?? '')) throw new BridgeError('invalid_arguments')
  if (options.action !== 'cleanup-invalid' && options.index !== undefined) throw new BridgeError('invalid_arguments')
  if (options.catalogFingerprint !== undefined && !/^[a-f0-9]{64}$/u.test(options.catalogFingerprint)) throw new BridgeError('invalid_arguments')
  return options
}

function requiredOptionValue(args, index, option) {
  const value = args[index]
  if (!value || value.startsWith('--')) throw new BridgeError('invalid_arguments')
  return value
}

async function loadCatalog() {
  const raw = await readFile(catalogPath, 'utf8')
  const catalog = JSON.parse(raw)
  const actualFingerprint = createHash('sha256').update(JSON.stringify(catalog)).digest('hex')
  if (actualFingerprint !== catalogFingerprint) throw new BridgeError('catalog_fingerprint_mismatch')
  if (catalog?.catalogVersion !== 1 || catalog?.owner !== 'node-postgres-legacy' || !Array.isArray(catalog.indexes) || catalog.indexes.length !== 3) {
    throw new BridgeError('invalid_catalog')
  }
  for (const index of catalog.indexes) validateCatalogIndex(index)
  return catalog
}

function validateCatalogIndex(index) {
  if (!identifier(index?.name) || !identifier(index?.schema) || !identifier(index?.table) || !index?.createSql?.startsWith('CREATE INDEX CONCURRENTLY ')) {
    throw new BridgeError('invalid_catalog')
  }
  if (index.accessMethod !== 'btree') throw new BridgeError('invalid_catalog')
  if (index.skipWhenRequiredColumnsMissing !== undefined && index.skipWhenRequiredColumnsMissing !== true) throw new BridgeError('invalid_catalog')
  if (/\bIF\s+NOT\s+EXISTS\b|\bDROP\s+INDEX\b|;|--|\/\*|\*\//i.test(index.createSql) || !index.createSql.includes(`${index.schema}.${index.table}`)) {
    throw new BridgeError('invalid_catalog')
  }
  for (const [column, type] of Object.entries(index.requiredColumns ?? {})) {
    if (!identifier(column) || typeof type !== 'string') throw new BridgeError('invalid_catalog')
  }
  for (const [column, value] of Object.entries(index.requiredColumnDefaults ?? {})) {
    if (!identifier(column) || typeof value !== 'string') throw new BridgeError('invalid_catalog')
  }
  if (!Array.isArray(index.requiredDefinitionTokens) || index.requiredDefinitionTokens.length === 0 || index.requiredDefinitionTokens.some((token) => typeof token !== 'string' || token.trim() === '')) {
    throw new BridgeError('invalid_catalog')
  }
  if (!Array.isArray(index.requiredKeyExpressions) || index.requiredKeyExpressions.length === 0 || index.requiredKeyExpressions.some((expression) => typeof expression !== 'string' || expression.trim() === '')) {
    throw new BridgeError('invalid_catalog')
  }
}

async function configureSession(client) {
  await client.query("SET lock_timeout = '30s'")
  await client.query("SET statement_timeout = '15min'")
}

async function verifyIdentity(client, expectedDatabase) {
  const result = await client.query(`
    SELECT current_database() AS database,
           current_user AS current_user,
           current_setting('server_version_num') AS server_version_num
  `)
  const identity = result.rows[0]
  if (identity.database !== expectedDatabase || !/^\d+$/u.test(identity.server_version_num) || Number(identity.server_version_num) < 120000) {
    throw new BridgeError('database_identity_mismatch')
  }
  return { database: identity.database, currentUser: identity.current_user }
}

async function assertNoGooseLedger(client) {
  const result = await client.query(`
    SELECT COUNT(*)::text AS count
    FROM pg_class
    WHERE relname = 'goose_db_version'
  `)
  if (result.rows[0]?.count !== '0') throw new BridgeError('goose_ledger_present')
}

async function inspectCatalog(client, catalog, currentUser) {
  const indexes = []
  for (const target of catalog.indexes) indexes.push(await inspectIndex(client, target, currentUser))
  return { indexes }
}

async function inspectIndex(client, target, currentUser) {
  let tableOid
  try {
    tableOid = await assertTableContract(client, target, currentUser)
  } catch (error) {
    if (error instanceof BridgeError && error.code === 'missing_required_columns' && target.skipWhenRequiredColumnsMissing === true) {
      return { name: target.name, state: 'not_applicable', reason: error.code }
    }
    throw error
  }
  const existing = await readIndex(client, target, tableOid)
  if (!existing) return { name: target.name, state: 'missing' }
  if (!existing.indisvalid || !existing.indisready || !existing.indislive) return { name: target.name, state: 'invalid' }
  if (existing.owner !== currentUser || !definitionMatches(existing, target)) return { name: target.name, state: 'mismatched' }
  return { name: target.name, state: 'valid' }
}

async function assertTableContract(client, target, currentUser) {
  const tableResult = await client.query(`
    SELECT c.oid::text AS oid, c.relkind, pg_get_userbyid(c.relowner) AS owner
    FROM pg_class c
    INNER JOIN pg_namespace n ON n.oid = c.relnamespace
    WHERE n.nspname = $1 AND c.relname = $2
  `, [target.schema, target.table])
  const table = tableResult.rows[0]
  if (!table || table.relkind !== 'r' || table.owner !== currentUser) throw new BridgeError('table_precondition_failed')

  const columnsResult = await client.query(`
    SELECT a.attname, a.attnotnull, pg_get_expr(d.adbin, d.adrelid) AS column_default,
           format_type(a.atttypid, a.atttypmod) AS type
    FROM pg_attribute a
    INNER JOIN pg_class c ON c.oid = a.attrelid
    INNER JOIN pg_namespace n ON n.oid = c.relnamespace
    LEFT JOIN pg_attrdef d ON d.adrelid = a.attrelid AND d.adnum = a.attnum
    WHERE n.nspname = $1 AND c.relname = $2 AND a.attnum > 0 AND NOT a.attisdropped
  `, [target.schema, target.table])
  const columns = new Map(columnsResult.rows.map((row) => [row.attname, row]))
  for (const [column, type] of Object.entries(target.requiredColumns)) {
    const actual = columns.get(column)
    if (!actual) throw new BridgeError('missing_required_columns')
    if (actual.type !== type) throw new BridgeError('column_type_mismatch')
  }
  for (const [column, expectedDefault] of Object.entries(target.requiredColumnDefaults ?? {})) {
    if (normalizeSql(columns.get(column)?.column_default ?? '') !== normalizeSql(expectedDefault)) {
      throw new BridgeError('column_default_precondition_failed')
    }
  }

  for (const [column, allowed] of Object.entries(target.requiredIntegerValues ?? {})) {
    if (columns.get(column)?.attnotnull !== true) throw new BridgeError('integer_nullability_precondition_failed')
    const result = await client.query(`SELECT COUNT(*)::text AS count FROM ${qualified(target.schema, target.table)} WHERE ${quoted(column)} IS NULL OR ${quoted(column)} <> ALL($1::integer[])`, [allowed])
    if (result.rows[0].count !== '0') throw new BridgeError('integer_value_precondition_failed')
  }
  return table.oid
}

async function readIndex(client, target, tableOid) {
  const result = await client.query(`
    SELECT i.indisvalid, i.indisready, i.indislive, i.indnkeyatts, am.amname AS access_method,
           i.indoption::text AS key_options,
           pg_get_expr(i.indpred, i.indrelid, false) AS predicate,
           ARRAY_AGG(pg_get_indexdef(i.indexrelid, key_position, false) ORDER BY key_position)
             FILTER (WHERE key_position IS NOT NULL) AS key_expressions,
           pg_get_userbyid(c.relowner) AS owner
    FROM pg_class c
    INNER JOIN pg_namespace n ON n.oid = c.relnamespace
    INNER JOIN pg_index i ON i.indexrelid = c.oid
    INNER JOIN pg_am am ON am.oid = c.relam
    LEFT JOIN LATERAL generate_series(1, i.indnkeyatts) AS key_position ON TRUE
    WHERE n.nspname = $1 AND c.relname = $2 AND i.indrelid = $3::oid
    GROUP BY c.oid, i.indexrelid, i.indisvalid, i.indisready, i.indislive, i.indnkeyatts, i.indoption, am.amname
  `, [target.schema, target.name, tableOid])
  return result.rows[0]
}

function definitionMatches(existing, target) {
  const expectedPredicate = target.createSql.match(/\sWHERE\s(.+)$/u)?.[1]
  const actualKeys = Array.isArray(existing.key_expressions) ? existing.key_expressions : []
  const keyOptions = String(existing.key_options ?? '').match(/-?\d+/gu)?.map(Number) ?? []
  if (!expectedPredicate || existing.access_method !== target.accessMethod || Number(existing.indnkeyatts) !== target.requiredKeyExpressions.length || actualKeys.length !== target.requiredKeyExpressions.length) return false
  if (keyOptions.length !== target.requiredKeyExpressions.length || keyOptions.some((option) => option !== 0)) return false
  if (!actualKeys.every((expression, index) => canonicalKeyExpression(expression) === canonicalKeyExpression(target.requiredKeyExpressions[index]))) return false
  return canonicalBooleanExpression(existing.predicate) === canonicalBooleanExpression(expectedPredicate)
}

async function applyCatalog(client, catalog, currentUser) {
  const report = await inspectCatalog(client, catalog, currentUser)
  for (const index of report.indexes) {
    if (index.state === 'invalid' || index.state === 'mismatched') throw new BridgeError('index_precondition_failed')
  }
  for (const target of catalog.indexes) {
    if (report.indexes.find((index) => index.name === target.name)?.state === 'missing') await client.query(target.createSql)
  }
  const verified = await inspectCatalog(client, catalog, currentUser)
  if (verified.indexes.some((index) => !isVerifiedState(index.state))) throw new BridgeError('verification_failed')
  return verified
}

function isVerifiedState(state) {
  return state === 'valid' || state === 'not_applicable'
}

async function cleanupInvalidIndex(client, catalog, currentUser, requestedIndex) {
  const target = catalog.indexes.find((index) => index.name === requestedIndex)
  if (!target) throw new BridgeError('cleanup_index_not_allowlisted')
  const tableOid = await assertTableContract(client, target, currentUser)
  const existing = await readIndex(client, target, tableOid)
  if (!existing || (existing.indisvalid && existing.indisready && existing.indislive) || existing.owner !== currentUser) throw new BridgeError('cleanup_precondition_failed')
  await client.query(`DROP INDEX CONCURRENTLY ${qualified(target.schema, target.name)}`)
  if (await readIndex(client, target, tableOid)) throw new BridgeError('cleanup_verification_failed')
  return { indexes: [{ name: target.name, state: 'removed_invalid' }] }
}

async function acquireAdvisoryLock(client) {
  const result = await client.query('SELECT pg_try_advisory_lock(hashtextextended($1, 0)) AS acquired', [advisoryLockKey])
  if (result.rows[0]?.acquired !== true) throw new BridgeError('advisory_lock_busy')
}

async function releaseAdvisoryLock(client) {
  await client.query('SELECT pg_advisory_unlock(hashtextextended($1, 0))', [advisoryLockKey])
}

function identifier(value) {
  return /^[A-Za-z_][A-Za-z0-9_]{0,62}$/u.test(value)
}

function quoted(value) {
  return `"${value}"`
}

function qualified(schema, name) {
  return `${quoted(schema)}.${quoted(name)}`
}

function normalizeSql(value) {
  return value.toLowerCase().replace(/\s+/gu, ' ').replace(/"/gu, '').trim()
}

function normalizeIndexDefinition(value) {
  return normalizeSql(value)
    .replace(/\bconcurrently\b/gu, '')
    .replace(/\busing btree\b/gu, '')
    .replace(/\s*;\s*$/gu, '')
    .replace(/\s+/gu, ' ')
    .trim()
}

function canonicalKeyExpression(value) {
  return String(value)
    .replace(/::[A-Za-z_][A-Za-z0-9_]*/gu, '')
    .replace(/"/gu, '')
    .replace(/\s+ASC\b/gu, '')
    .replace(/\s+/gu, ' ')
    .replace(/\s*([(),])\s*/gu, '$1')
    .trim()
}

function canonicalBooleanExpression(value) {
  const tokens = tokenizePredicate(value)
  let position = 0
  const parsePrimary = () => {
    if (tokens[position] === '(') {
      position += 1
      const expression = parseOr()
      if (tokens[position] !== ')') throw new BridgeError('index_definition_unparseable')
      position += 1
      return expression
    }
    const atom = []
    while (position < tokens.length && tokens[position] !== ')' && upper(tokens[position]) !== 'AND' && upper(tokens[position]) !== 'OR') atom.push(tokens[position++])
    if (atom.length === 0) throw new BridgeError('index_definition_unparseable')
    return { kind: 'atom', value: atom.map(canonicalAtomToken).join(' ') }
  }
  const parseAnd = () => {
    const expressions = [parsePrimary()]
    while (upper(tokens[position]) === 'AND') {
      position += 1
      expressions.push(parsePrimary())
    }
    return expressions.length === 1 ? expressions[0] : { kind: 'and', expressions }
  }
  const parseOr = () => {
    const expressions = [parseAnd()]
    while (upper(tokens[position]) === 'OR') {
      position += 1
      expressions.push(parseAnd())
    }
    return expressions.length === 1 ? expressions[0] : { kind: 'or', expressions }
  }
  const expression = parseOr()
  if (position !== tokens.length) throw new BridgeError('index_definition_unparseable')
  return JSON.stringify(canonicalBooleanTree(expression))
}

function tokenizePredicate(value) {
  const source = String(value).replace(/::[A-Za-z_][A-Za-z0-9_]*/gu, '').replace(/"/gu, '').replace(/\s+/gu, ' ').trim()
  const pattern = /'(?:''|[^'])*'|!~|<=|>=|<>|=|~|<|>|\(|\)|[A-Za-z_][A-Za-z0-9_]*|[0-9]+/gu
  const tokens = []
  let match
  while ((match = pattern.exec(source)) !== null) tokens.push(match[0])
  if (tokens.join('') !== source.replace(/\s+/gu, '')) throw new BridgeError('index_definition_unparseable')
  return tokens
}

function canonicalBooleanTree(expression) {
  if (expression.kind === 'atom') return expression
  return {
    kind: expression.kind,
    expressions: expression.expressions.map(canonicalBooleanTree).sort((left, right) => JSON.stringify(left).localeCompare(JSON.stringify(right)))
  }
}

function canonicalAtomToken(token) {
  if (token.startsWith("'")) return token
  const keyword = upper(token)
  return new Set(['IS', 'NOT', 'NULL', 'DISTINCT', 'FROM']).has(keyword) ? keyword : token
}

function upper(value) {
  return typeof value === 'string' ? value.toUpperCase() : ''
}

function print(value) {
  process.stdout.write(`${JSON.stringify(value)}\n`)
}
