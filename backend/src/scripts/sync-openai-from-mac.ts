import { spawn } from 'node:child_process'
import { existsSync } from 'node:fs'
import { homedir } from 'node:os'
import { resolve } from 'node:path'

import { getDatabase } from '../storage/database.js'
import { importOpenAIApiKeyAccounts, type MigrationAccountInput, type MigrationOAuthAccountInput } from '../storage/repositories.js'

interface SyncOptions {
  host: string
  user: string
  identityFile?: string
  container: string
  databaseUser: string
  databaseName: string
  groupName: string
  createGatewayKey: boolean
  gatewayKeyName: string
  dryRun: boolean
}

interface SourceApiKeyAccountRow {
  sourceId?: unknown
  name?: unknown
  description?: unknown
  baseUrl?: unknown
  apiKey?: unknown
}

interface SourceOAuthAccountRow {
  sourceId?: unknown
  name?: unknown
  description?: unknown
  accessToken?: unknown
  refreshToken?: unknown
  idToken?: unknown
  expiresAt?: unknown
  clientId?: unknown
  email?: unknown
  chatgptAccountId?: unknown
  chatgptUserId?: unknown
  organizationId?: unknown
  planType?: unknown
}

interface SourcePayload {
  apiKeyAccounts: SourceApiKeyAccountRow[]
  oauthAccounts: SourceOAuthAccountRow[]
}

const options = parseArgs(process.argv.slice(2))

getDatabase()

const sourcePayload = await loadOpenAIAccounts(options)
const accounts = normalizeSourceApiKeyAccounts(sourcePayload.apiKeyAccounts)
const oauthAccounts = normalizeSourceOAuthAccounts(sourcePayload.oauthAccounts)

const result = importOpenAIApiKeyAccounts({
  accounts,
  oauthAccounts,
  groupName: options.groupName,
  createGatewayApiKey: options.createGatewayKey,
  gatewayApiKeyName: options.gatewayKeyName,
  dryRun: options.dryRun
})

console.log(JSON.stringify({
  source: {
    host: options.host,
    user: options.user,
    container: options.container,
    database: options.databaseName,
    openaiApiKeyAccounts: accounts.length,
    openaiOAuthAccounts: oauthAccounts.length,
    apiKeyNames: accounts.map((account) => account.name),
    oauthNames: oauthAccounts.map((account) => account.name)
  },
  sync: result,
  dryRun: options.dryRun,
  note: result.apiKey
    ? 'apiKey 是 sub2api-lite 本地网关 Key，前端 API 密钥列表也会显示完整值。'
    : '没有新建网关 Key；如果是旧数据且没有回填明文，请在前端新建一个 Key。'
}, null, 2))

async function loadOpenAIAccounts(options: SyncOptions): Promise<SourcePayload> {
  const sql = `
WITH openai_accounts AS (
  SELECT
    id,
    name,
    lower(type) AS account_type,
    COALESCE(notes, '') AS description,
    credentials
  FROM accounts
  WHERE deleted_at IS NULL
    AND lower(platform) = 'openai'
), api_key_accounts AS (
  SELECT
    id,
    name,
    description,
    COALESCE(NULLIF(credentials->>'base_url', ''), 'https://api.openai.com/v1') AS base_url,
    credentials->>'api_key' AS api_key
  FROM openai_accounts
  WHERE account_type IN ('apikey', 'api_key')
    AND NULLIF(credentials->>'api_key', '') IS NOT NULL
), oauth_accounts AS (
  SELECT
    id,
    name,
    description,
    credentials->>'access_token' AS access_token,
    credentials->>'refresh_token' AS refresh_token,
    credentials->>'id_token' AS id_token,
    credentials->>'expires_at' AS expires_at,
    credentials->>'client_id' AS client_id,
    credentials->>'email' AS email,
    credentials->>'chatgpt_account_id' AS chatgpt_account_id,
    credentials->>'chatgpt_user_id' AS chatgpt_user_id,
    credentials->>'organization_id' AS organization_id,
    credentials->>'plan_type' AS plan_type
  FROM openai_accounts
  WHERE account_type = 'oauth'
    AND NULLIF(credentials->>'access_token', '') IS NOT NULL
    AND NULLIF(credentials->>'refresh_token', '') IS NOT NULL
)
SELECT jsonb_build_object(
  'apiKeyAccounts', COALESCE((
    SELECT jsonb_agg(jsonb_build_object(
      'sourceId', id,
      'name', name,
      'description', description,
      'baseUrl', base_url,
      'apiKey', api_key
    ) ORDER BY id ASC)
    FROM api_key_accounts
  ), '[]'::jsonb),
  'oauthAccounts', COALESCE((
    SELECT jsonb_agg(jsonb_build_object(
      'sourceId', id,
      'name', name,
      'description', description,
      'accessToken', access_token,
      'refreshToken', refresh_token,
      'idToken', id_token,
      'expiresAt', expires_at,
      'clientId', client_id,
      'email', email,
      'chatgptAccountId', chatgpt_account_id,
      'chatgptUserId', chatgpt_user_id,
      'organizationId', organization_id,
      'planType', plan_type
    ) ORDER BY id ASC)
    FROM oauth_accounts
  ), '[]'::jsonb)
)::text;
`

  const command = [
    'docker',
    'exec',
    '-i',
    options.container,
    'psql',
    '-U',
    options.databaseUser,
    '-d',
    options.databaseName,
    '-tA',
    '-f',
    '-'
  ].map(shellQuote).join(' ')
  const output = await runSsh(options, command, sql)
  const text = output.trim()
  if (!text) {
    return { apiKeyAccounts: [], oauthAccounts: [] }
  }
  const parsed = JSON.parse(text) as unknown
  if (!isSourcePayload(parsed)) {
    throw new Error('Mac Postgres returned an unexpected payload')
  }
  return parsed
}

function isSourcePayload(value: unknown): value is SourcePayload {
  return typeof value === 'object'
    && value !== null
    && Array.isArray((value as SourcePayload).apiKeyAccounts)
    && Array.isArray((value as SourcePayload).oauthAccounts)
}

function normalizeSourceApiKeyAccounts(rows: SourceApiKeyAccountRow[]): MigrationAccountInput[] {
  return rows.flatMap((row) => {
    const apiKey = asNonEmptyString(row.apiKey)
    if (!apiKey) {
      return []
    }
    return [{
      name: asNonEmptyString(row.name) ?? `openai-api-key-${String(row.sourceId ?? 'unknown')}`,
      description: asNonEmptyString(row.description),
      baseUrl: asNonEmptyString(row.baseUrl) ?? 'https://api.openai.com/v1',
      apiKey
    }]
  })
}

function normalizeSourceOAuthAccounts(rows: SourceOAuthAccountRow[]): MigrationOAuthAccountInput[] {
  return rows.flatMap((row) => {
    const accessToken = asNonEmptyString(row.accessToken)
    const refreshToken = asNonEmptyString(row.refreshToken)
    if (!accessToken || !refreshToken) {
      return []
    }
    return [{
      name: asNonEmptyString(row.name) ?? asNonEmptyString(row.email) ?? `openai-oauth-${String(row.sourceId ?? 'unknown')}`,
      description: asNonEmptyString(row.description),
      accessToken,
      refreshToken,
      idToken: asNonEmptyString(row.idToken),
      expiresAt: asNonEmptyString(row.expiresAt),
      clientId: asNonEmptyString(row.clientId),
      email: asNonEmptyString(row.email),
      chatgptAccountId: asNonEmptyString(row.chatgptAccountId),
      chatgptUserId: asNonEmptyString(row.chatgptUserId),
      organizationId: asNonEmptyString(row.organizationId),
      planType: asNonEmptyString(row.planType)
    }]
  })
}

function asNonEmptyString(value: unknown): string | undefined {
  if (typeof value !== 'string') {
    return undefined
  }
  const trimmed = value.trim()
  return trimmed.length > 0 ? trimmed : undefined
}

function runSsh(options: SyncOptions, remoteCommand: string, stdin: string): Promise<string> {
  return new Promise((resolveOutput, reject) => {
    const args = ['-o', 'BatchMode=yes', '-o', 'StrictHostKeyChecking=no']
    if (options.identityFile) {
      args.push('-i', options.identityFile)
    }
    args.push(`${options.user}@${options.host}`, remoteCommand)

    const child = spawn('ssh', args, { stdio: ['pipe', 'pipe', 'pipe'] })
    const stdout: Buffer[] = []
    const stderr: Buffer[] = []

    child.stdout.on('data', (chunk: Buffer) => stdout.push(chunk))
    child.stderr.on('data', (chunk: Buffer) => stderr.push(chunk))
    child.on('error', reject)
    child.on('close', (code) => {
      const stdoutText = Buffer.concat(stdout).toString('utf8')
      const stderrText = Buffer.concat(stderr).toString('utf8')
      if (code !== 0) {
        reject(new Error(`SSH command failed with code ${code}: ${stderrText || stdoutText}`))
        return
      }
      resolveOutput(stdoutText)
    })

    child.stdin.end(stdin)
  })
}

function parseArgs(argv: string[]): SyncOptions {
  const options: SyncOptions = {
    host: process.env.SUB2API_MAC_HOST ?? '192.168.1.156',
    user: process.env.SUB2API_MAC_USER ?? 'huanmin',
    identityFile: defaultIdentityFile(),
    container: process.env.SUB2API_MAC_POSTGRES_CONTAINER ?? 'sub2api-postgres',
    databaseUser: process.env.SUB2API_MAC_POSTGRES_USER ?? 'sub2api',
    databaseName: process.env.SUB2API_MAC_POSTGRES_DB ?? 'sub2api',
    groupName: process.env.SUB2API_LITE_SYNC_GROUP_NAME ?? 'Mac 同步 OpenAI API Key 分组',
    createGatewayKey: process.env.SUB2API_LITE_CREATE_GATEWAY_KEY !== '0',
    gatewayKeyName: process.env.SUB2API_LITE_SYNC_GATEWAY_KEY_NAME ?? 'Mac 同步 OpenAI 网关 Key',
    dryRun: false
  }

  for (let index = 0; index < argv.length; index += 1) {
    const item = argv[index]
    switch (item) {
      case '--host':
        options.host = requiredValue(argv, index, item)
        index += 1
        break
      case '--user':
        options.user = requiredValue(argv, index, item)
        index += 1
        break
      case '--identity-file':
        options.identityFile = resolvePath(requiredValue(argv, index, item))
        index += 1
        break
      case '--no-identity-file':
        options.identityFile = undefined
        break
      case '--container':
        options.container = requiredValue(argv, index, item)
        index += 1
        break
      case '--db-user':
        options.databaseUser = requiredValue(argv, index, item)
        index += 1
        break
      case '--db-name':
        options.databaseName = requiredValue(argv, index, item)
        index += 1
        break
      case '--group-name':
        options.groupName = requiredValue(argv, index, item)
        index += 1
        break
      case '--gateway-key-name':
        options.gatewayKeyName = requiredValue(argv, index, item)
        index += 1
        break
      case '--no-create-gateway-key':
        options.createGatewayKey = false
        break
      case '--dry-run':
        options.dryRun = true
        break
      case '--help':
      case '-h':
        printUsage()
        process.exit(0)
      default:
        throw new Error(`Unknown argument: ${item}`)
    }
  }

  return options
}

function requiredValue(argv: string[], index: number, flag: string): string {
  const value = argv[index + 1]
  if (!value || value.startsWith('--')) {
    throw new Error(`${flag} requires a value`)
  }
  return value
}

function defaultIdentityFile(): string | undefined {
  const candidates = [
    process.env.SUB2API_MAC_IDENTITY_FILE,
    resolve(homedir(), '.ssh', 'id_ed25519'),
    resolve(homedir(), '.ssh', 'id_rsa')
  ].filter(Boolean) as string[]
  return candidates.map(resolvePath).find((candidate) => existsSync(candidate))
}

function resolvePath(value: string): string {
  if (value === '~') {
    return homedir()
  }
  if (value.startsWith('~/') || value.startsWith('~\\')) {
    return resolve(homedir(), value.slice(2))
  }
  return resolve(value)
}

function shellQuote(value: string): string {
  return `'${value.replace(/'/g, `'"'"'`)}'`
}

function printUsage(): void {
  console.log(`Usage:
  pnpm --filter sub2api-lite-backend sync:openai-from-mac

Options:
  --host <host>                 Mac host, default: 192.168.1.156
  --user <user>                 SSH user, default: huanmin
  --identity-file <path>        SSH private key, default: ~/.ssh/id_ed25519 then ~/.ssh/id_rsa
  --no-identity-file            Do not pass -i to ssh
  --container <name>            Postgres container, default: sub2api-postgres
  --db-user <user>              Postgres user, default: sub2api
  --db-name <name>              Postgres database, default: sub2api
  --group-name <name>           Target group, default: Mac 同步 OpenAI API Key 分组
  --gateway-key-name <name>     Local gateway API Key name
  --no-create-gateway-key       Do not create a local gateway API Key
  --dry-run                     Read source and report only, do not write SQLite
`)
}

