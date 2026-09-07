import { encryptJson } from '../../storage/crypto.js'
import type { AccountBalanceRefreshCandidate } from '../../storage/account-balance.repository.js'
import type { AccountBalanceQueryConfig, AccountBalanceSnapshot } from '../accounts/account-balance.types.js'
import { effectiveAccountApiKeys, normalizeAccountBalanceConfig } from '../accounts/account-balance-config.js'
import { requiredRfc3339Instant } from '../../shared/rfc3339.js'
import { resolveProxyUrlForProfileAsync } from '../../storage/proxy.repository.js'

/**
 * J2's Node boundary is fail-closed by default. In explicit Go-owner mode it
 * is the sole manual command bridge and result fence; it never falls back to
 * the legacy Node query path.
 */
export const accountBalanceHandoverSchemaVersion = 1 as const
export const accountBalanceHandoverJobName = 'account-balance-refresh' as const

/** Explicit owner switch used by every legacy Node J2 entry point. */
export function accountBalanceGoOwnerEnabled(env: NodeJS.ProcessEnv = process.env): boolean {
  return env.JUHE_AI_ACCOUNT_BALANCE_JOBS_OWNER?.trim().toLowerCase() === 'go'
}

export function accountBalanceNodeOwnerEnabled(env: NodeJS.ProcessEnv = process.env): boolean {
  return !accountBalanceGoOwnerEnabled(env)
}

/** Both processes may use different credentials, but a Go-owner handover
 * must point the jobs writer and the Node outcome reader at the same PG DB. */
export function sameAccountBalanceJobsPostgresStore(left: string | undefined, right: string | undefined): boolean {
  try {
    const a = new URL(left?.trim() ?? '')
    const b = new URL(right?.trim() ?? '')
    if (!['postgres:', 'postgresql:'].includes(a.protocol) || !['postgres:', 'postgresql:'].includes(b.protocol)) return false
    const identity = (value: URL): string | undefined => {
      // Driver URL parsers permit query parameters that override the parsed
      // host, port or database. Reject every target override rather than
      // accepting a pair that looks like one DB here but splits at runtime.
      const targetOverrides = new Set(['host', 'hostaddr', 'port', 'dbname', 'database', 'service', 'servicefile'])
      if ([...value.searchParams.keys()].some((key) => targetOverrides.has(key.toLowerCase()))) return undefined
      const path = value.pathname
      if (!path.startsWith('/') || path === '/') return undefined
      let database: string
      try {
        database = decodeURIComponent(path.slice(1))
      } catch {
        return undefined
      }
      if (!database || database.includes('/')) return undefined
      return `${value.hostname.toLowerCase()}:${value.port || '5432'}/${database}`
    }
    const leftIdentity = identity(a)
    const rightIdentity = identity(b)
    return leftIdentity !== undefined && leftIdentity === rightIdentity
  } catch {
    return false
  }
}

export type AccountBalanceHandoverMode = 'automatic' | 'recovery' | 'manual'

export interface AccountBalanceHandoverGateInput {
  /** Explicit operator/owner decision.  The default is false. */
  enabled?: boolean
  /** Set only after a Go jobs command is wired and independently verified. */
  goCommandWiringReady?: boolean
  /** Set only after the Go input/result contract is available end to end. */
  goInputResultReady?: boolean
  /** Set only after the Go projection/readback owner is available. */
  goProjectionReady?: boolean
  /** Set only after the current Node scheduler/writer has been drained. */
  nodeOwnerDrained?: boolean
}

export type AccountBalanceHandoverGateReason =
  | 'disabled_by_default'
  | 'go_command_wiring_missing'
  | 'go_input_result_missing'
  | 'go_projection_missing'
  | 'node_owner_not_drained'

export interface AccountBalanceHandoverGate {
  enabled: boolean
  reason?: AccountBalanceHandoverGateReason
}

export const defaultAccountBalanceHandoverGate: AccountBalanceHandoverGate = Object.freeze({
  enabled: false,
  reason: 'disabled_by_default'
})

/**
 * Resolve the explicit J2 handover gate. Callers must pass every independently
 * verified fact; any missing fact remains fail-closed.
 */
export function resolveAccountBalanceHandoverGate(
  input: AccountBalanceHandoverGateInput = {}
): AccountBalanceHandoverGate {
  if (input.enabled !== true) return { ...defaultAccountBalanceHandoverGate }
  if (input.goCommandWiringReady !== true) return { enabled: false, reason: 'go_command_wiring_missing' }
  if (input.goInputResultReady !== true) return { enabled: false, reason: 'go_input_result_missing' }
  if (input.goProjectionReady !== true) return { enabled: false, reason: 'go_projection_missing' }
  if (input.nodeOwnerDrained !== true) return { enabled: false, reason: 'node_owner_not_drained' }
  return { enabled: true }
}

interface AccountBalanceHandoverCredential {
  kind: 'api_key'
  ciphertext: string
}

export interface AccountBalanceHandoverInput {
  schemaVersion: typeof accountBalanceHandoverSchemaVersion
  job: typeof accountBalanceHandoverJobName
  mode: AccountBalanceHandoverMode
  accountId: string
  systemAccountId: string
  inputVersion: number
  configRevision: number
  provider: string
  type: 'api_key'
  status: string
  schedulable: boolean
  config: AccountBalanceQueryConfig
  baseUrl: string
  credential: AccountBalanceHandoverCredential
  nextRefreshAt: string | null
  proxyProfileId?: string
  issuedAt: string
  deadlineAt: string
  expiresAt: string
}

export interface PreparedAccountBalanceHandoverInput {
  input: AccountBalanceHandoverInput
  body: Buffer
}

export type AccountBalanceHandoverInputBuildResult =
  | { enabled: false; reason: AccountBalanceHandoverGateReason }
  | { enabled: true; prepared: PreparedAccountBalanceHandoverInput }

export interface AccountBalanceHandoverInputOptions {
  gate?: AccountBalanceHandoverGateInput
  mode?: AccountBalanceHandoverMode
  now?: Date
  deadlineAt?: Date
  expiresAt?: Date
}

/** Synchronous manual bridge used only after the explicit Go owner switch. */
export async function runAccountBalanceManualViaGo(
  candidate: AccountBalanceRefreshCandidate,
  options: { signal?: AbortSignal; timeoutMs?: number } = {}
): Promise<AccountBalanceHandoverResult> {
  if (!accountBalanceGoOwnerEnabled()) throw new Error('J2 Go owner 模式未启用')
  const endpoint = process.env.JUHE_AI_ACCOUNT_BALANCE_JOBS_HTTP_URL?.trim()
  if (!endpoint) throw new Error('J2 Go manual bridge 缺少 JUHE_AI_ACCOUNT_BALANCE_JOBS_HTTP_URL')
  const bridgeSecret = process.env.JUHE_AI_ACCOUNT_BALANCE_JOBS_HTTP_SECRET?.trim()
  if (!bridgeSecret || bridgeSecret.length < 32) throw new Error('J2 Go manual bridge 缺少有效 HTTP secret')
  const prepared = prepareAccountBalanceHandoverInput(candidate, {
    gate: {
      enabled: true,
      goCommandWiringReady: Boolean(endpoint) && envTrue('JUHE_AI_ACCOUNT_BALANCE_JOBS_COMMAND_WIRING_READY'),
      goInputResultReady: envTrue('JUHE_AI_ACCOUNT_BALANCE_JOBS_INPUT_RESULT_READY'),
      goProjectionReady: envTrue('JUHE_AI_ACCOUNT_BALANCE_JOBS_PROJECTION_ENABLED')
        && Boolean(process.env.JUHE_AI_ACCOUNT_BALANCE_JOBS_OUTCOME_POSTGRES_URL?.trim())
        && envTrue('JUHE_AI_ACCOUNT_BALANCE_JOBS_PROJECTION_READY'),
      nodeOwnerDrained: envTrue('JUHE_AI_ACCOUNT_BALANCE_JOBS_NODE_OWNER_DRAINED')
    },
    mode: 'manual'
  })
  if (!prepared.enabled) throw new Error(`J2 Go manual bridge 被 gate 拒绝: ${prepared.reason}`)
  const input = prepared.prepared.input
  const proxyUrl = candidate.proxyProfileId ? await resolveProxyUrlForProfileAsync(candidate.proxyProfileId) : undefined
  const proxy = proxyUrl ? { kind: 'proxy_url', ciphertext: encryptJson({ url: proxyUrl }) } : undefined
  const goInput = {
    account_id: input.accountId,
    system_account_id: input.systemAccountId,
    input_version: input.inputVersion,
    config_revision: input.configRevision,
    provider: input.provider,
    type: input.type,
    status: input.status,
    schedulable: input.schedulable,
    base_url: input.baseUrl,
    config: input.config,
    api_key: input.credential,
    ...(proxy ? { proxy } : {}),
    trigger: 'manual',
    issued_at: input.issuedAt,
    expires_at: input.expiresAt,
    next_refresh_at: input.nextRefreshAt
  }
  const timeoutMs = Math.max(1_000, Math.min(options.timeoutMs ?? 20_000, 30_000))
  const signal = options.signal ? AbortSignal.any([options.signal, AbortSignal.timeout(timeoutMs)]) : AbortSignal.timeout(timeoutMs)
  const response = await fetch(`${endpoint.replace(/\/$/u, '')}/account-balance/manual`, {
    method: 'POST',
    headers: { 'content-type': 'application/json', accept: 'application/json', authorization: `Bearer ${bridgeSecret}` },
    body: JSON.stringify({ input: goInput }),
    signal
  })
  const body = await response.text()
  if (!response.ok) {
    if (response.status === 409) {
      try { return parseAccountBalanceHandoverResult(body) } catch { /* preserve the original transport error below */ }
    }
    throw new Error(`J2 Go manual bridge HTTP ${response.status}: ${body.slice(0, 512)}`)
  }
  return parseAccountBalanceHandoverResult(body)
}

function envTrue(name: string, env: NodeJS.ProcessEnv = process.env): boolean {
  return env[name]?.trim().toLowerCase() === 'true'
}

/**
 * Build the immutable input envelope for a future Go jobs owner.  Disabled
 * by default, this returns before reading or encrypting credentials.  Once
 * all gate facts are explicit, malformed candidates throw instead of being
 * silently skipped or sent down a Node fallback path.
 */
export function prepareAccountBalanceHandoverInput(
  candidate: AccountBalanceRefreshCandidate,
  options: AccountBalanceHandoverInputOptions = {}
): AccountBalanceHandoverInputBuildResult {
  const gate = resolveAccountBalanceHandoverGate(options.gate)
  if (!gate.enabled) return { enabled: false, reason: gate.reason ?? 'disabled_by_default' }

  const now = options.now ?? new Date()
  const deadlineAt = options.deadlineAt ?? new Date(now.getTime() + 20_000)
  const expiresAt = options.expiresAt ?? deadlineAt
  if (!isValidDate(now) || !isValidDate(deadlineAt) || !isValidDate(expiresAt)) {
    throw new Error('J2 余额输入时间无效')
  }
  if (deadlineAt <= now || expiresAt <= now || expiresAt < deadlineAt) {
    throw new Error('J2 余额输入 deadline/expiresAt 必须在当前时间之后且 expiresAt 不早于 deadlineAt')
  }

  const accountId = requiredText(candidate?.id, 'J2 accountId')
  const systemAccountId = requiredText(candidate?.systemAccountId, 'J2 systemAccountId')
  if (!Number.isSafeInteger(candidate.configRevision) || candidate.configRevision < 1) {
    throw new Error('J2 configRevision 必须是正整数')
  }
  const config = normalizeAccountBalanceConfig(candidate.config)
  const baseUrl = normalizeBaseUrl(candidate.credentials?.base_url)
  const apiKeys = effectiveAccountApiKeys(candidate.credentials)
  if (apiKeys.length !== 1) {
    throw new Error('J2 输入必须包含一个有效的 API Key')
  }

  const nextRefreshAt = candidate.nextRefreshAt === null
    ? null
    : requiredRfc3339Instant(candidate.nextRefreshAt, 'J2 nextRefreshAt')
  const proxyProfileId = optionalText(candidate.proxyProfileId)
  const input: AccountBalanceHandoverInput = {
    schemaVersion: accountBalanceHandoverSchemaVersion,
    job: accountBalanceHandoverJobName,
    mode: options.mode ?? 'automatic',
    accountId,
    systemAccountId,
    inputVersion: Number.isSafeInteger(candidate.inputVersion) && (candidate.inputVersion ?? 0) > 0 ? candidate.inputVersion as number : 1,
    configRevision: candidate.configRevision,
    provider: candidate.provider ?? 'openai',
    type: 'api_key',
    status: candidate.status ?? 'active',
    schedulable: candidate.schedulable ?? true,
    config,
    baseUrl,
    credential: {
      kind: 'api_key',
      ciphertext: encryptJson({ api_key: apiKeys[0] })
    },
    nextRefreshAt,
    ...(proxyProfileId ? { proxyProfileId } : {}),
    issuedAt: now.toISOString(),
    deadlineAt: deadlineAt.toISOString(),
    expiresAt: expiresAt.toISOString()
  }
  return {
    enabled: true,
    prepared: {
      input,
      body: Buffer.from(JSON.stringify({
        schemaVersion: accountBalanceHandoverSchemaVersion,
        job: accountBalanceHandoverJobName,
        input
      }), 'utf8')
    }
  }
}

export type AccountBalanceHandoverOutcome = 'refreshed' | 'lease_busy' | 'stale' | 'failed' | 'unsupported'

export interface AccountBalanceHandoverResult {
  schemaVersion: typeof accountBalanceHandoverSchemaVersion
  job: typeof accountBalanceHandoverJobName
  accountId: string
  systemAccountId: string
  configRevision: number
  expectedNextRefreshAt?: string | null
  nextRefreshAfter: string | null
  outcome: AccountBalanceHandoverOutcome
  committed: boolean
  snapshot: AccountBalanceSnapshot
}

export interface AccountBalanceHandoverProjectionFence {
  accountId: string
  systemAccountId: string
  configRevision: number
  nextRefreshAt: string | null
}

export interface AccountBalanceHandoverProjection {
  accountId: string
  systemAccountId: string
  configRevision: number
  nextRefreshAfter: string | null
  snapshot: AccountBalanceSnapshot
}

export type AccountBalanceHandoverProjectionResult =
  | { projected: true; projection: AccountBalanceHandoverProjection }
  | {
    projected: false
    reason:
      | AccountBalanceHandoverGateReason
      | 'account_fence_mismatch'
      | 'config_revision_mismatch'
      | 'next_refresh_fence_mismatch'
      | 'result_not_committed'
      | 'outcome_snapshot_mismatch'
  }

/**
 * Decode a Go result envelope without trusting its object shape.  Unknown
 * fields, bad timestamps, invalid snapshots and secret-bearing fields are
 * rejected; no result is converted into a Node write operation here.
 */
export function parseAccountBalanceHandoverResult(value: unknown): AccountBalanceHandoverResult {
  const envelope = typeof value === 'string' ? parseJson(value) : value
  const record = requiredRecord(envelope, 'J2 结果')
  exactFields(record, ['schemaVersion', 'job', 'result'], [], 'J2 结果 envelope')
  if (record.schemaVersion !== accountBalanceHandoverSchemaVersion) throw new Error('J2 结果 schemaVersion 不受支持')
  if (record.job !== accountBalanceHandoverJobName) throw new Error('J2 结果 job 不匹配')
  return parseResultRecord(requiredRecord(record.result, 'J2 result'))
}

/**
 * Apply only a committed result whose account/config/next-refresh fence still
 * matches the current Node read.  This is a detached projection value, not a
 * repository write and not a fallback when the fence is stale.
 */
export function projectAccountBalanceHandoverResult(
  result: AccountBalanceHandoverResult,
  current: AccountBalanceHandoverProjectionFence,
  gateInput: AccountBalanceHandoverGateInput = {}
): AccountBalanceHandoverProjectionResult {
  const gate = resolveAccountBalanceHandoverGate(gateInput)
  if (!gate.enabled) return { projected: false, reason: gate.reason ?? 'disabled_by_default' }
  if (result.accountId !== current.accountId || result.systemAccountId !== current.systemAccountId) {
    return { projected: false, reason: 'account_fence_mismatch' }
  }
  if (result.configRevision !== current.configRevision) {
    return { projected: false, reason: 'config_revision_mismatch' }
  }
  if (result.expectedNextRefreshAt !== undefined
    && canonicalOptionalInstant(result.expectedNextRefreshAt) !== canonicalOptionalInstant(current.nextRefreshAt)) {
    return { projected: false, reason: 'next_refresh_fence_mismatch' }
  }
  if (!result.committed || result.outcome === 'lease_busy' || result.outcome === 'stale') {
    return { projected: false, reason: 'result_not_committed' }
  }
  if ((result.outcome === 'refreshed' && !isSuccessfulSnapshot(result.snapshot))
    || (result.outcome === 'unsupported' && result.snapshot.status !== 'unsupported')) {
    return { projected: false, reason: 'outcome_snapshot_mismatch' }
  }
  return {
    projected: true,
    projection: {
      accountId: result.accountId,
      systemAccountId: result.systemAccountId,
      configRevision: result.configRevision,
      nextRefreshAfter: result.nextRefreshAfter,
      snapshot: result.snapshot
    }
  }
}

function parseResultRecord(record: Record<string, unknown>): AccountBalanceHandoverResult {
  exactFields(record, [
    'schemaVersion',
    'job',
    'accountId',
    'systemAccountId',
    'configRevision',
    'nextRefreshAfter',
    'outcome',
    'committed',
    'snapshot'
  ], ['expectedNextRefreshAt'], 'J2 result')
  if (record.schemaVersion !== accountBalanceHandoverSchemaVersion) throw new Error('J2 result schemaVersion 不受支持')
  if (record.job !== accountBalanceHandoverJobName) throw new Error('J2 result job 不匹配')
  const outcome = record.outcome
  if (outcome !== 'refreshed' && outcome !== 'lease_busy' && outcome !== 'stale' && outcome !== 'failed' && outcome !== 'unsupported') {
    throw new Error('J2 result outcome 无效')
  }
  if (typeof record.committed !== 'boolean') throw new Error('J2 result committed 无效')
  const configRevision = positiveInteger(record.configRevision, 'J2 result configRevision')
  const expectedNextRefreshAt = record.expectedNextRefreshAt === undefined
    ? undefined
    : record.expectedNextRefreshAt === null
      ? null
      : requiredRfc3339Instant(record.expectedNextRefreshAt, 'J2 result expectedNextRefreshAt')
  const nextRefreshAfter = record.nextRefreshAfter === null
    ? null
    : requiredRfc3339Instant(record.nextRefreshAfter, 'J2 result nextRefreshAfter')
  return {
    schemaVersion: accountBalanceHandoverSchemaVersion,
    job: accountBalanceHandoverJobName,
    accountId: requiredText(record.accountId, 'J2 result accountId'),
    systemAccountId: requiredText(record.systemAccountId, 'J2 result systemAccountId'),
    configRevision,
    ...(expectedNextRefreshAt === undefined ? {} : { expectedNextRefreshAt }),
    nextRefreshAfter,
    outcome,
    committed: record.committed,
    snapshot: parseSnapshot(record.snapshot)
  }
}

function parseSnapshot(value: unknown): AccountBalanceSnapshot {
  const record = requiredRecord(value, 'J2 result snapshot')
  const allowedFields = [
    'status',
    'remainingUsd',
    'rawRemaining',
    'rawUnit',
    'basis',
    'errorMessage',
    'lastAttemptAt',
    'lastSuccessAt',
    'consecutiveTransientFailures',
    'lastTransientErrorMessage',
    'lastTransientFailureAt'
  ]
  exactFields(record, ['status'], allowedFields.filter((field) => field !== 'status'), 'J2 result snapshot')
  const status = record.status
  if (status !== 'pending' && status !== 'refreshing' && status !== 'fresh' && status !== 'unlimited' && status !== 'unsupported' && status !== 'failed') {
    throw new Error('J2 result snapshot status 无效')
  }
  const rawUnit = record.rawUnit
  if (rawUnit !== undefined && rawUnit !== 'usd' && rawUnit !== 'cny' && rawUnit !== 'quota') throw new Error('J2 result snapshot rawUnit 无效')
  const basis = record.basis
  if (basis !== undefined && basis !== 'api_key_quota' && basis !== 'budget' && basis !== 'subscription' && basis !== 'wallet' && basis !== 'custom') {
    throw new Error('J2 result snapshot basis 无效')
  }
  const output: AccountBalanceSnapshot = {
    status,
    ...(decimalText(record.remainingUsd, 'remainingUsd') === undefined ? {} : { remainingUsd: decimalText(record.remainingUsd, 'remainingUsd') }),
    ...(decimalText(record.rawRemaining, 'rawRemaining') === undefined ? {} : { rawRemaining: decimalText(record.rawRemaining, 'rawRemaining') }),
    ...(rawUnit === undefined ? {} : { rawUnit }),
    ...(basis === undefined ? {} : { basis }),
    ...(boundedText(record.errorMessage, 'errorMessage') === undefined ? {} : { errorMessage: boundedText(record.errorMessage, 'errorMessage') }),
    ...(optionalInstant(record.lastAttemptAt, 'lastAttemptAt') === undefined ? {} : { lastAttemptAt: optionalInstant(record.lastAttemptAt, 'lastAttemptAt') }),
    ...(optionalInstant(record.lastSuccessAt, 'lastSuccessAt') === undefined ? {} : { lastSuccessAt: optionalInstant(record.lastSuccessAt, 'lastSuccessAt') }),
    ...(record.consecutiveTransientFailures === undefined ? {} : { consecutiveTransientFailures: boundedFailureCount(record.consecutiveTransientFailures) }),
    ...(boundedText(record.lastTransientErrorMessage, 'lastTransientErrorMessage') === undefined ? {} : { lastTransientErrorMessage: boundedText(record.lastTransientErrorMessage, 'lastTransientErrorMessage') }),
    ...(optionalInstant(record.lastTransientFailureAt, 'lastTransientFailureAt') === undefined ? {} : { lastTransientFailureAt: optionalInstant(record.lastTransientFailureAt, 'lastTransientFailureAt') })
  }
  return output
}

function normalizeBaseUrl(value: unknown): string {
  const text = requiredText(value, 'J2 baseUrl')
  let url: URL
  try {
    url = new URL(text)
  } catch {
    throw new Error('J2 baseUrl 无效')
  }
  if ((url.protocol !== 'http:' && url.protocol !== 'https:') || url.username || url.password || url.search || url.hash) {
    throw new Error('J2 baseUrl 必须是无凭据、无查询和无片段的 HTTP(S) URL')
  }
  return url.origin
}

function parseJson(value: string): unknown {
  try {
    return JSON.parse(value)
  } catch {
    throw new Error('J2 结果 JSON 无效')
  }
}

function requiredRecord(value: unknown, label: string): Record<string, unknown> {
  if (!value || typeof value !== 'object' || Array.isArray(value)) throw new Error(`${label}必须是对象`)
  return value as Record<string, unknown>
}

function exactFields(value: Record<string, unknown>, required: string[], optional: string[] = [], label: string): void {
  const actual = Object.keys(value).sort()
  const allowed = new Set([...required, ...optional])
  if (required.some((field) => !Object.prototype.hasOwnProperty.call(value, field))
    || actual.some((field) => !allowed.has(field))) {
    throw new Error(`${label}包含未知或缺失字段`)
  }
}

function requiredText(value: unknown, label: string): string {
  const text = optionalText(value)
  if (!text) throw new Error(`${label}不能为空`)
  return text
}

function optionalText(value: unknown): string | undefined {
  return typeof value === 'string' && value.trim() ? value.trim() : undefined
}

function positiveInteger(value: unknown, label: string): number {
  if (!Number.isSafeInteger(value) || (value as number) < 1) throw new Error(`${label}必须是正整数`)
  return value as number
}

function isValidDate(value: Date): boolean {
  return value instanceof Date && Number.isFinite(value.getTime())
}

function canonicalOptionalInstant(value: string | null | undefined): string | null | undefined {
  if (value === undefined) return undefined
  return value === null ? null : requiredRfc3339Instant(value)
}

function optionalInstant(value: unknown, label: string): string | undefined {
  return value === undefined ? undefined : requiredRfc3339Instant(value, `J2 result snapshot ${label}`)
}

function boundedText(value: unknown, label: string): string | undefined {
  if (value === undefined) return undefined
  if (typeof value !== 'string' || value.length > 4096) throw new Error(`J2 result snapshot ${label} 无效`)
  return value
}

function decimalText(value: unknown, label: string): string | undefined {
  if (value === undefined) return undefined
  if (typeof value !== 'string' || !/^-?(?:0|[1-9]\d*)(?:\.\d+)?$/u.test(value)) throw new Error(`J2 result snapshot ${label} 无效`)
  return value
}

function boundedFailureCount(value: unknown): number {
  if (!Number.isSafeInteger(value) || (value as number) < 0 || (value as number) > 3) throw new Error('J2 result snapshot consecutiveTransientFailures 无效')
  return value as number
}

function isSuccessfulSnapshot(snapshot: AccountBalanceSnapshot): boolean {
  return snapshot.status === 'fresh' || snapshot.status === 'unlimited'
}
