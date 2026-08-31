import assert from 'node:assert/strict'

import { runtimeConfig } from '../../config/runtime.js'
import {
  acquireAccountLockRetryLeaseAsync,
  completeAccountLockSuccessAsync,
  consumeAccountLockRetryLeaseAsync,
  findAccountLockStateAsync,
  recordAccountLockFailureAsync,
  releaseAccountLockRetryLeaseAsync,
  setAccountLockAsync
} from '../../storage/account-lock.repository.js'
import { closePostgresPool, getPostgresPool } from '../../storage/postgres-client.js'

if (runtimeConfig.databaseDriver !== 'postgres') {
  console.log('SKIP: account-lock PostgreSQL smoke 需要 JUHE_AI_DATABASE_DRIVER=postgres')
  process.exit(0)
}

const marker = `account_lock_pg_${Date.now()}_${Math.random().toString(16).slice(2)}`
const pool = await getPostgresPool()

try {
  const fixture = await pool.query(`
    SELECT provider_code, provider_protocol_profile_id, protocol_code, protocol_version
    FROM juhe_business.accounts
    WHERE deleted_at IS NULL
    UNION ALL
    SELECT profile.provider_code, profile.id, profile.protocol_code, profile.protocol_version
    FROM juhe_business.provider_protocol_profiles profile
    WHERE NOT EXISTS (SELECT 1 FROM juhe_business.accounts WHERE deleted_at IS NULL)
    ORDER BY provider_code, provider_protocol_profile_id
    LIMIT 1
  `)
  const source = fixture.rows[0] as {
    provider_code?: string
    provider_protocol_profile_id?: string
    protocol_code?: string
    protocol_version?: string
  } | undefined
  assert.ok(source?.provider_code && source.provider_protocol_profile_id && source.protocol_code && source.protocol_version, 'PG smoke 需要 provider/profile/protocol fixture')
  const nowIso = new Date().toISOString()
  await pool.query(`
    INSERT INTO juhe_business.system_accounts (
      id, username, display_name, role, status, password_hash, created_at, updated_at
    ) VALUES ($1, $1, $1, 'user', 'active', 'test-only', $2, $2)
  `, [marker, nowIso])
  await pool.query(`
    INSERT INTO juhe_business.accounts (
      id, system_account_id, provider_code, provider_protocol_profile_id,
      protocol_code, protocol_version, name, type, status, credentials_encrypted,
      health_check_model, health_check_endpoint_mode, created_at, updated_at
    ) VALUES ($1, $1, $2, $3, $4, $5, $6, 'api_key', 'active', '{}', 'gpt-5.6-sol', 'responses_sse', $7, $7)
  `, [marker, source.provider_code, source.provider_protocol_profile_id, source.protocol_code, source.protocol_version, marker, nowIso])

  const access = { systemAccountId: marker, role: 'user' as const }
  const idle = await setAccountLockAsync({ accountId: marker, enabled: true, access })
  assert.equal(idle?.lockState, 'LOCKED_IDLE')
  const failure = await recordAccountLockFailureAsync(marker, 'pg_smoke', {
    generation: idle!.generation,
    incidentId: idle!.incidentId
  })
  assert.equal(failure?.lockState, 'ENGAGED')
  assert.ok(failure?.incidentId)

  const firstLease = await acquireAccountLockRetryLeaseAsync(marker, 0)
  assert.equal(firstLease.allowed, true)
  assert.ok(firstLease.leaseId)
  await pool.query(`UPDATE juhe_business.account_lock_states SET next_retry_at_ms = $2 WHERE account_id = $1 AND lease_id = $3`, [marker, Date.now() - 1, firstLease.leaseId])
  assert.equal(await consumeAccountLockRetryLeaseAsync(marker, firstLease.leaseId), true)
  const oldObservation = {
    generation: failure!.generation,
    incidentId: failure!.incidentId,
    leaseId: firstLease.leaseId
  }

  const expiredAt = Date.now() - 1
  await pool.query(`
    UPDATE juhe_business.account_lock_states
    SET next_retry_at_ms = $2, lease_until_ms = $2
    WHERE account_id = $1
  `, [marker, expiredAt])
  const secondLease = await acquireAccountLockRetryLeaseAsync(marker, 0)
  assert.equal(secondLease.allowed, true)
  assert.ok(secondLease.leaseId)
  assert.notEqual(secondLease.leaseId, firstLease.leaseId)
  const staleSuccess = await completeAccountLockSuccessAsync(marker, oldObservation)
  assert.equal(staleSuccess?.lockState, 'ENGAGED', '旧 lease 的迟到成功不得关闭当前事故')
  assert.equal((await findAccountLockStateAsync(marker))?.leaseId, secondLease.leaseId)

  assert.equal(await consumeAccountLockRetryLeaseAsync(marker, secondLease.leaseId), true)
  const currentObservation = {
    generation: failure!.generation,
    incidentId: failure!.incidentId,
    leaseId: secondLease.leaseId
  }
  const completed = await completeAccountLockSuccessAsync(marker, currentObservation)
  assert.equal(completed?.lockState, 'LOCKED_IDLE')
  assert.equal((await findAccountLockStateAsync(marker))?.leaseId, undefined)

  const secondFailure = await recordAccountLockFailureAsync(marker, 'pg_smoke_again', {
    generation: completed!.generation,
    incidentId: completed!.incidentId
  })
  const thirdLease = await acquireAccountLockRetryLeaseAsync(marker, 0)
  assert.equal(thirdLease.allowed, true)
  await pool.query(`UPDATE juhe_business.account_lock_states SET next_retry_at_ms = $2 WHERE account_id = $1 AND lease_id = $3`, [marker, Date.now() - 1, thirdLease.leaseId])
  assert.equal(await consumeAccountLockRetryLeaseAsync(marker, thirdLease.leaseId), true)
  assert.equal(await releaseAccountLockRetryLeaseAsync({
    accountId: marker,
    leaseId: thirdLease.leaseId,
    globalDelayMs: 2_000,
    scheduleNextRetry: true
  }), true)
  const released = await findAccountLockStateAsync(marker)
  assert.equal(released?.lockState, 'ENGAGED')
  assert.equal(released?.leaseId, undefined)
  assert.ok(released?.nextRetryAtMs && released.nextRetryAtMs > Date.now(), '可重试终态必须保留共享到期时间但清除在途 lease')
  await pool.query(`
    UPDATE juhe_business.account_lock_states
    SET next_retry_at_ms = $2, lease_id = NULL, lease_until_ms = NULL
    WHERE account_id = $1
  `, [marker, Date.now() - 1])
  const concurrentLeases = await Promise.all([
    acquireAccountLockRetryLeaseAsync(marker, 0),
    acquireAccountLockRetryLeaseAsync(marker, 0)
  ])
  assert.equal(concurrentLeases.filter((item) => item.allowed).length, 1, '同一到期点并发 acquire 必须只有一个请求赢得 CAS lease')
  const winner = concurrentLeases.find((item) => item.allowed)
  assert.ok(winner?.leaseId)
  assert.equal(await consumeAccountLockRetryLeaseAsync(marker, winner?.leaseId), true)
  await pool.query(`
    UPDATE juhe_business.account_lock_states
    SET lease_until_ms = $2
    WHERE account_id = $1 AND lease_id = $3
  `, [marker, Date.now() - 1, winner?.leaseId])
  const expiredObservation = {
    generation: secondFailure!.generation,
    incidentId: secondFailure!.incidentId,
    leaseId: winner!.leaseId
  }
  const expiredSuccess = await completeAccountLockSuccessAsync(marker, expiredObservation)
  assert.equal(expiredSuccess?.lockState, 'ENGAGED', '过期在途 lease 的迟到成功不得清除事故')
  assert.equal((await findAccountLockStateAsync(marker))?.leaseId, winner?.leaseId)
  await pool.query(`
    UPDATE juhe_business.account_lock_states
    SET lease_until_ms = $2
    WHERE account_id = $1 AND lease_id = $3
  `, [marker, Date.now() + 60_000, winner?.leaseId])
  assert.equal(await releaseAccountLockRetryLeaseAsync({
    accountId: marker,
    leaseId: winner?.leaseId,
    scheduleNextRetry: false
  }), true)
  const terminalReleased = await findAccountLockStateAsync(marker)
  assert.equal(terminalReleased?.lockState, 'ENGAGED')
  assert.equal(terminalReleased?.leaseId, undefined)
  assert.ok(secondFailure?.incidentId)
  console.log('account-lock PostgreSQL smoke passed: generation/incident/lease CAS、过期 lease、防重复发送与释放生命周期通过')
} finally {
  await pool.query('DELETE FROM juhe_business.accounts WHERE id = $1', [marker])
  await pool.query('DELETE FROM juhe_business.system_accounts WHERE id = $1', [marker])
  await closePostgresPool()
}
