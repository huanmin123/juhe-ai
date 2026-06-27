import { createHash } from 'node:crypto'
import type { DatabaseSync } from 'node:sqlite'

import { newId } from './database.js'
import type { DatabaseClient } from './database-client.js'
import { stableJsonStringify } from './audit-log-stable-json.js'
import type {
  AuditLogInput,
  AuditPayloadPartType,
  AuditTrafficSource
} from './audit-log-types.js'
import { optionalString } from './value-utils.js'

type AuditErrorGroupStatement = ReturnType<DatabaseSync['prepare']>

export interface AuditErrorGroupStatements {
  selectExisting: AuditErrorGroupStatement
  updateExisting: AuditErrorGroupStatement
  insertGroup: AuditErrorGroupStatement
}

export interface AuditErrorGroupPayloadInput {
  partType: AuditPayloadPartType
  bodySha256?: string
}

const auditErrorGroupWindowMs = 5 * 60 * 1000

export function prepareAuditErrorGroupStatements(database: DatabaseSync): AuditErrorGroupStatements {
  return {
    selectExisting: database.prepare('SELECT id FROM audit_error_groups WHERE fingerprint = ? AND window_started_at = ?'),
    updateExisting: database.prepare(`
      UPDATE audit_error_groups
      SET count = count + 1,
          window_ended_at = ?,
          last_event_id = ?,
          sample_event_id = COALESCE(sample_event_id, ?),
          last_message = ?,
          updated_at = ?
      WHERE id = ?
    `),
    insertGroup: database.prepare(`
      INSERT INTO audit_error_groups (
        id, fingerprint, window_started_at, window_ended_at, system_account_id, api_key_id, group_id, account_id,
        provider_code, path, model, status_code, error_phase, error_code, error_type, request_fingerprint,
        error_fingerprint, count, first_event_id, last_event_id, sample_event_id, last_message, created_at, updated_at
      ) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, 1, ?, ?, ?, ?, ?, ?)
    `)
  }
}

export function upsertAuditErrorGroup(
  input: AuditLogInput,
  auditLogId: string,
  payloads: AuditErrorGroupPayloadInput[],
  timestamp: string,
  trafficSource: AuditTrafficSource,
  statements: AuditErrorGroupStatements
): string | null {
  if (input.auditOutcome === 'success') {
    return null
  }
  const requestFingerprint = auditRequestFingerprint(input, payloads)
  const errorFingerprint = auditErrorFingerprint(input)
  const windowStartedAt = auditErrorWindowStart(timestamp)
  const windowEndedAt = new Date(Date.parse(windowStartedAt) + auditErrorGroupWindowMs).toISOString()
  const fingerprint = sha256Text(stableJsonStringify({
    systemAccountId: input.systemAccountId ?? '',
    apiKeyId: input.apiKeyId ?? '',
    groupId: input.groupId ?? '',
    accountId: input.accountId ?? '',
    providerCode: input.providerCode ?? '',
    trafficSource,
    path: input.path,
    model: input.model ?? '',
    statusCode: input.finalStatusCode ?? '',
    errorPhase: input.errorPhase ?? '',
    errorCode: input.errorCode ?? '',
    requestFingerprint,
    errorFingerprint
  }))
  const existing = statements.selectExisting.get(fingerprint, windowStartedAt) as { id?: unknown } | undefined
  const existingId = optionalString(existing?.id)
  if (existingId) {
    statements.updateExisting.run(windowEndedAt, auditLogId, auditLogId, input.errorMessage ?? null, timestamp, existingId)
    return existingId
  }

  const id = newId('audgrp')
  statements.insertGroup.run(
    id,
    fingerprint,
    windowStartedAt,
    windowEndedAt,
    input.systemAccountId ?? null,
    input.apiKeyId ?? null,
    input.groupId ?? null,
    input.accountId ?? null,
    input.providerCode ?? null,
    input.path,
    input.model ?? null,
    input.finalStatusCode ?? null,
    input.errorPhase ?? null,
    input.errorCode ?? null,
    input.auditOutcome,
    requestFingerprint,
    errorFingerprint,
    auditLogId,
    auditLogId,
    auditLogId,
    input.errorMessage ?? null,
    timestamp,
    timestamp
  )
  return id
}

export async function upsertAuditErrorGroupAsync(
  client: DatabaseClient,
  input: AuditLogInput,
  auditLogId: string,
  payloads: AuditErrorGroupPayloadInput[],
  timestamp: string,
  trafficSource: AuditTrafficSource
): Promise<string | null> {
  if (input.auditOutcome === 'success') {
    return null
  }
  const requestFingerprint = auditRequestFingerprint(input, payloads)
  const errorFingerprint = auditErrorFingerprint(input)
  const windowStartedAt = auditErrorWindowStart(timestamp)
  const windowEndedAt = new Date(Date.parse(windowStartedAt) + auditErrorGroupWindowMs).toISOString()
  const fingerprint = sha256Text(stableJsonStringify({
    systemAccountId: input.systemAccountId ?? '',
    apiKeyId: input.apiKeyId ?? '',
    groupId: input.groupId ?? '',
    accountId: input.accountId ?? '',
    providerCode: input.providerCode ?? '',
    trafficSource,
    path: input.path,
    model: input.model ?? '',
    statusCode: input.finalStatusCode ?? '',
    errorPhase: input.errorPhase ?? '',
    errorCode: input.errorCode ?? '',
    requestFingerprint,
    errorFingerprint
  }))
  const row = await client.one<{ id?: string }>(`
    INSERT INTO juhe_dataset.audit_error_groups (
      id, fingerprint, window_started_at, window_ended_at, system_account_id, api_key_id, group_id, account_id,
      provider_code, path, model, status_code, error_phase, error_code, error_type, request_fingerprint,
      error_fingerprint, count, first_event_id, last_event_id, sample_event_id, last_message, created_at, updated_at
    ) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, 1, ?, ?, ?, ?, ?, ?)
    ON CONFLICT(fingerprint, window_started_at) DO UPDATE SET
      count = audit_error_groups.count + 1,
      window_ended_at = EXCLUDED.window_ended_at,
      last_event_id = EXCLUDED.last_event_id,
      sample_event_id = COALESCE(audit_error_groups.sample_event_id, EXCLUDED.sample_event_id),
      last_message = EXCLUDED.last_message,
      updated_at = EXCLUDED.updated_at
    RETURNING id
  `, [
    newId('audgrp'),
    fingerprint,
    windowStartedAt,
    windowEndedAt,
    input.systemAccountId ?? null,
    input.apiKeyId ?? null,
    input.groupId ?? null,
    input.accountId ?? null,
    input.providerCode ?? null,
    input.path,
    input.model ?? null,
    input.finalStatusCode ?? null,
    input.errorPhase ?? null,
    input.errorCode ?? null,
    input.auditOutcome,
    requestFingerprint,
    errorFingerprint,
    auditLogId,
    auditLogId,
    auditLogId,
    input.errorMessage ?? null,
    timestamp,
    timestamp
  ])
  return optionalString(row?.id) ?? null
}

function auditRequestFingerprint(input: AuditLogInput, payloads: AuditErrorGroupPayloadInput[]): string {
  const clientRequest = payloads.find((payload) => payload.partType === 'client_request')
  return sha256Text(stableJsonStringify({
    method: input.method,
    path: input.path,
    model: input.model ?? '',
    stream: input.stream === true,
    bodySha256: clientRequest?.bodySha256 ?? ''
  }))
}

function auditErrorFingerprint(input: AuditLogInput): string {
  const failedAttempt = input.attempts.find((attempt) => attempt.success === false)
  return sha256Text(stableJsonStringify({
    outcome: input.auditOutcome,
    statusCode: input.finalStatusCode ?? failedAttempt?.upstreamStatusCode ?? '',
    phase: input.errorPhase ?? failedAttempt?.errorPhase ?? '',
    code: input.errorCode ?? failedAttempt?.errorCode ?? '',
    message: normalizeErrorMessage(input.errorMessage ?? failedAttempt?.errorMessage ?? '')
  }))
}

function normalizeErrorMessage(value: string): string {
  return value
    .slice(0, 500)
    .replace(/[0-9a-f]{16,}/gi, '{hex}')
    .replace(/\d{3,}/g, '{num}')
}

function auditErrorWindowStart(timestamp: string): string {
  const time = Date.parse(timestamp)
  const safeTime = Number.isFinite(time) ? time : Date.now()
  return new Date(Math.floor(safeTime / auditErrorGroupWindowMs) * auditErrorGroupWindowMs).toISOString()
}

function sha256Text(value: string): string {
  return createHash('sha256').update(value).digest('hex')
}
