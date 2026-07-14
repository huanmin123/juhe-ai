import { ACCOUNT_HEALTH_CHECK_ENDPOINT_MODES } from '../../domain/account-health-check-endpoint-mode.js'
import { decryptJson, encryptJson } from '../../storage/crypto.js'
import { getPostgresPool, type PostgresPoolClient } from '../../storage/postgres-client.js'

const legacyModeMap = {
  chat_completions: 'chat_json',
  responses: 'responses_json',
  messages: 'messages_json',
  generate_content: 'generate_content_json'
} as const

const exactModeSet = new Set<string>(ACCOUNT_HEALTH_CHECK_ENDPOINT_MODES)

export interface AccountHealthCheckEndpointModeMigrationOptions {
  mode: 'dry-run' | 'execute' | 'verify'
  batchSize: number
}

export interface AccountHealthCheckEndpointModeMigrationStats {
  scanned: number
  gptAccounts: number
  authorizationInstances: number
  credentialUpdates: number
  clientCompatibilityUpdates: number
  modeCounts: Record<string, number>
}

interface LegacyAccountRow extends Record<string, unknown> {
  id: string
  type: string
  provider_code: string
  client_compatibility: string
  credentials_encrypted: string
  health_check_endpoint_family: keyof typeof legacyModeMap
  authorization_instance_authorization_id: string | null
}

interface ExactAccountRow extends Record<string, unknown> {
  id: string
  provider_code: string
  client_compatibility: string
  credentials_encrypted: string
  health_check_endpoint_mode: string
  authorization_instance_authorization_id: string | null
}

export function normalizeGptHealthCheckCredentials(
  credentials: Record<string, unknown>,
  accountType: string
): { credentials: Record<string, unknown>; changed: boolean } {
  const rawModes = credentials.supported_endpoint_modes
  let modes: string[]
  if (rawModes === undefined) {
    modes = accountType.trim() === 'oauth'
      ? ['responses_json', 'responses_sse']
      : ['chat_json', 'chat_sse', 'responses_json', 'responses_sse']
  } else {
    if (!Array.isArray(rawModes)) {
      throw new Error('supported_endpoint_modes 必须是字符串数组')
    }
    modes = []
    for (const value of rawModes) {
      if (typeof value !== 'string' || !value.trim()) {
        throw new Error('supported_endpoint_modes 必须是非空字符串数组')
      }
      const mode = value.trim()
      if (!modes.includes(mode)) modes.push(mode)
    }
    if (!modes.includes('responses_sse')) modes.push('responses_sse')
  }
  const currentModes = Array.isArray(rawModes) ? rawModes : undefined
  const changed = !currentModes
    || currentModes.length !== modes.length
    || currentModes.some((value, index) => value !== modes[index])
  return {
    credentials: changed ? { ...credentials, supported_endpoint_modes: modes } : credentials,
    changed
  }
}

export async function runAccountHealthCheckEndpointModeMigration(
  options: AccountHealthCheckEndpointModeMigrationOptions
): Promise<AccountHealthCheckEndpointModeMigrationStats> {
  const pool = await getPostgresPool()
  const client = await pool.connect()
  try {
    if (options.mode === 'verify') return await verifyMigration(client, options.batchSize)
    if (options.mode === 'execute') return await executeMigration(client, options.batchSize)
    return await dryRunMigration(client, options.batchSize)
  } finally {
    client.release()
  }
}

async function dryRunMigration(client: PostgresPoolClient, batchSize: number): Promise<AccountHealthCheckEndpointModeMigrationStats> {
  await client.query('BEGIN ISOLATION LEVEL REPEATABLE READ READ ONLY')
  try {
    await assertColumnState(client, 'legacy')
    const stats = await scanLegacyAccounts(client, batchSize, false)
    await client.query('ROLLBACK')
    return stats
  } catch (error) {
    await client.query('ROLLBACK').catch(() => undefined)
    throw error
  }
}

async function executeMigration(client: PostgresPoolClient, batchSize: number): Promise<AccountHealthCheckEndpointModeMigrationStats> {
  await client.query('BEGIN')
  try {
    await client.query('LOCK TABLE juhe_business.accounts IN ACCESS EXCLUSIVE MODE')
    await assertColumnState(client, 'legacy')
    const stats = await scanLegacyAccounts(client, batchSize, true)
    await replaceLegacyColumn(client)
    await verifyExactRows(client, batchSize)
    await client.query('COMMIT')
    return stats
  } catch (error) {
    await client.query('ROLLBACK').catch(() => undefined)
    throw error
  }
}

async function verifyMigration(client: PostgresPoolClient, batchSize: number): Promise<AccountHealthCheckEndpointModeMigrationStats> {
  await client.query('BEGIN ISOLATION LEVEL REPEATABLE READ READ ONLY')
  try {
    await assertColumnState(client, 'exact')
    const stats = await verifyExactRows(client, batchSize)
    await client.query('ROLLBACK')
    return stats
  } catch (error) {
    await client.query('ROLLBACK').catch(() => undefined)
    throw error
  }
}

async function scanLegacyAccounts(
  client: PostgresPoolClient,
  batchSize: number,
  apply: boolean
): Promise<AccountHealthCheckEndpointModeMigrationStats> {
  const stats = emptyStats()
  let afterId = ''
  while (true) {
    const result = await client.query(`
      SELECT id, type, provider_code, client_compatibility, credentials_encrypted,
        health_check_endpoint_family, authorization_instance_authorization_id
      FROM juhe_business.accounts
      WHERE id > $1
      ORDER BY id ASC
      LIMIT $2
    `, [afterId, batchSize])
    const rows = result.rows as unknown as LegacyAccountRow[]
    if (rows.length === 0) break
    for (const row of rows) {
      stats.scanned += 1
      if (!(row.health_check_endpoint_family in legacyModeMap)) {
        throw new Error(`账户 ${row.id} 的历史健康检查协议族无效`)
      }
      const nextMode = row.provider_code === 'gpt'
        ? 'responses_sse'
        : legacyModeMap[row.health_check_endpoint_family]
      stats.modeCounts[nextMode] = (stats.modeCounts[nextMode] ?? 0) + 1
      if (row.authorization_instance_authorization_id) stats.authorizationInstances += 1
      if (row.provider_code !== 'gpt') continue
      stats.gptAccounts += 1
      const credentials = decryptCredentialRecord(row.id, row.credentials_encrypted)
      const normalized = normalizeGptHealthCheckCredentials(credentials, row.type)
      const compatibilityChanged = row.client_compatibility !== 'codex_responses'
      if (normalized.changed) stats.credentialUpdates += 1
      if (compatibilityChanged) stats.clientCompatibilityUpdates += 1
      if (!apply || (!normalized.changed && !compatibilityChanged)) continue
      if (normalized.changed) {
        await client.query(`
          UPDATE juhe_business.accounts
          SET credentials_encrypted = $1,
              client_compatibility = 'codex_responses'
          WHERE id = $2
        `, [encryptJson(normalized.credentials), row.id])
      } else {
        await client.query(`
          UPDATE juhe_business.accounts
          SET client_compatibility = 'codex_responses'
          WHERE id = $1
        `, [row.id])
      }
    }
    afterId = rows.at(-1)?.id ?? afterId
    writeProgress(apply ? 'execute' : 'dry-run', afterId, stats)
  }
  return stats
}

async function replaceLegacyColumn(client: PostgresPoolClient): Promise<void> {
  await client.query(`
    DO $$
    DECLARE
      constraint_name text;
    BEGIN
      FOR constraint_name IN
        SELECT conname
        FROM pg_constraint
        WHERE conrelid = 'juhe_business.accounts'::regclass
          AND contype = 'c'
          AND pg_get_constraintdef(oid) LIKE '%health_check_endpoint_family%'
      LOOP
        EXECUTE format('ALTER TABLE juhe_business.accounts DROP CONSTRAINT %I', constraint_name);
      END LOOP;
    END $$
  `)
  await client.query(`
    ALTER TABLE juhe_business.accounts
    RENAME COLUMN health_check_endpoint_family TO health_check_endpoint_mode
  `)
  await client.query(`
    UPDATE juhe_business.accounts
    SET health_check_endpoint_mode = CASE
      WHEN provider_code = 'gpt' THEN 'responses_sse'
      WHEN health_check_endpoint_mode = 'chat_completions' THEN 'chat_json'
      WHEN health_check_endpoint_mode = 'responses' THEN 'responses_json'
      WHEN health_check_endpoint_mode = 'messages' THEN 'messages_json'
      WHEN health_check_endpoint_mode = 'generate_content' THEN 'generate_content_json'
      ELSE health_check_endpoint_mode
    END,
    client_compatibility = CASE
      WHEN provider_code = 'gpt' THEN 'codex_responses'
      ELSE client_compatibility
    END
  `)
  await client.query(`
    ALTER TABLE juhe_business.accounts
    ADD CONSTRAINT accounts_health_check_endpoint_mode_check
    CHECK (health_check_endpoint_mode IN (
      'chat_json', 'chat_sse',
      'responses_json', 'responses_sse',
      'messages_json', 'messages_sse',
      'generate_content_json', 'generate_content_sse'
    ))
  `)
}

async function verifyExactRows(
  client: PostgresPoolClient,
  batchSize: number
): Promise<AccountHealthCheckEndpointModeMigrationStats> {
  const stats = emptyStats()
  let afterId = ''
  while (true) {
    const result = await client.query(`
      SELECT id, provider_code, client_compatibility, credentials_encrypted,
        health_check_endpoint_mode, authorization_instance_authorization_id
      FROM juhe_business.accounts
      WHERE id > $1
      ORDER BY id ASC
      LIMIT $2
    `, [afterId, batchSize])
    const rows = result.rows as unknown as ExactAccountRow[]
    if (rows.length === 0) break
    for (const row of rows) {
      stats.scanned += 1
      if (!exactModeSet.has(row.health_check_endpoint_mode)) {
        throw new Error(`账户 ${row.id} 的健康检查请求形态无效`)
      }
      stats.modeCounts[row.health_check_endpoint_mode] = (stats.modeCounts[row.health_check_endpoint_mode] ?? 0) + 1
      if (row.authorization_instance_authorization_id) stats.authorizationInstances += 1
      if (row.provider_code !== 'gpt') continue
      stats.gptAccounts += 1
      if (row.health_check_endpoint_mode !== 'responses_sse') {
        throw new Error(`GPT 账户 ${row.id} 未切换到 responses_sse`)
      }
      if (row.client_compatibility !== 'codex_responses') {
        throw new Error(`GPT 账户 ${row.id} 未切换到 codex_responses`)
      }
      const credentials = decryptCredentialRecord(row.id, row.credentials_encrypted)
      const modes = credentials.supported_endpoint_modes
      if (!Array.isArray(modes) || !modes.includes('responses_sse')) {
        throw new Error(`GPT 账户 ${row.id} 的加密 supported_endpoint_modes 缺少 responses_sse`)
      }
    }
    afterId = rows.at(-1)?.id ?? afterId
    writeProgress('verify', afterId, stats)
  }
  return stats
}

async function assertColumnState(client: PostgresPoolClient, expected: 'legacy' | 'exact'): Promise<void> {
  const result = await client.query(`
    SELECT column_name
    FROM information_schema.columns
    WHERE table_schema = 'juhe_business'
      AND table_name = 'accounts'
      AND column_name IN ('health_check_endpoint_family', 'health_check_endpoint_mode')
    ORDER BY column_name
  `)
  const columns = result.rows.map((row) => String(row.column_name))
  const expectedColumns = expected === 'legacy'
    ? ['health_check_endpoint_family']
    : ['health_check_endpoint_mode']
  if (columns.length !== 1 || columns[0] !== expectedColumns[0]) {
    throw new Error(`账户健康检查字段状态不符合 ${expected} 迁移阶段：${columns.join(',') || 'missing'}`)
  }
}

function decryptCredentialRecord(accountId: string, encrypted: string): Record<string, unknown> {
  let value: unknown
  try {
    value = decryptJson(encrypted)
  } catch (error) {
    throw new Error(`账户 ${accountId} 凭据解密失败：${error instanceof Error ? error.message : String(error)}`)
  }
  if (!value || typeof value !== 'object' || Array.isArray(value)) {
    throw new Error(`账户 ${accountId} 的解密凭据不是对象`)
  }
  return value as Record<string, unknown>
}

function emptyStats(): AccountHealthCheckEndpointModeMigrationStats {
  return {
    scanned: 0,
    gptAccounts: 0,
    authorizationInstances: 0,
    credentialUpdates: 0,
    clientCompatibilityUpdates: 0,
    modeCounts: {}
  }
}

function writeProgress(
  phase: AccountHealthCheckEndpointModeMigrationOptions['mode'],
  afterId: string,
  stats: AccountHealthCheckEndpointModeMigrationStats
): void {
  process.stdout.write(`${JSON.stringify({ event: 'account_health_check_endpoint_mode_migration_progress', phase, afterId, ...stats })}\n`)
}
